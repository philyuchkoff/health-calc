package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root YAML configuration structure.
type Config struct {
	UpdateInterval     string               `yaml:"update_interval"`
	Metrics            []Metric             `yaml:"metrics"`
	Alerting           Alerting             `yaml:"alerting"`
	Prometheus         PrometheusConfig     `yaml:"prometheus"`
	CircuitBreaker     CircuitBreakerConfig `yaml:"circuit_breaker"`
	GracefulDeg        GracefulDegConfig    `yaml:"graceful_degradation"`
	RateLimit          RateLimitConfig      `yaml:"rate_limit"`
	Logging            LoggingConfig        `yaml:"logging"`
	UnhealthyThreshold string               `yaml:"unhealthy_threshold"`
}

type CircuitBreakerConfig struct {
	MaxFailures  int    `yaml:"max_failures"`
	ResetTimeout string `yaml:"reset_timeout"`
}

type GracefulDegConfig struct {
	EnableCache      bool   `yaml:"enable_cache"`
	CacheTTL         string `yaml:"cache_ttl"`
	MaxAge           string `yaml:"max_age"`
	FallbackStrategy string `yaml:"fallback_strategy"`
}

const (
	FallbackStrategyZero    = "zero"
	FallbackStrategyAverage = "average"
	FallbackStrategyLast    = "last_known"
	FallbackStrategyNeutral = "neutral"
)

type Metric struct {
	Name        string  `yaml:"name"`
	Query       string  `yaml:"prometheus_query"`
	Weight      float64 `yaml:"weight"`
	Description string  `yaml:"description"`
	MinValue    float64 `yaml:"min_valid_value"`
	MaxValue    float64 `yaml:"max_valid_value"`
}

type Alerting struct {
	Telegram                       TelegramConfig `yaml:"telegram"`
	PrometheusUnavailableThreshold int            `yaml:"prometheus_unavailable_alert_threshold"`
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
}

type PrometheusConfig struct {
	URL     string `yaml:"url"`
	Timeout string `yaml:"timeout"`
}

// loadConfig reads, expands environment variables in, validates, and applies the
// YAML configuration at configPath. Hot-reload safe — increments the reload
// counter on every call (including failures).
func (hc *HealthCalculator) loadConfig(configPath string) (err error) {
	defer hc.metrics.configReloadTotal.Inc()

	ctx := context.Background()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %v", err)
	}

	if hc.logger == nil {
		hc.logger = NewLogger(LoggingConfig{
			Level:   "info",
			Format:  "json",
			Service: "health-calculator",
		})
		hc.logger.Info("Logger initialized (default config)")
	}

	expanded := os.ExpandEnv(string(data))

	var config Config
	if err := yaml.Unmarshal([]byte(expanded), &config); err != nil {
		return fmt.Errorf("failed to parse config: %v", err)
	}

	totalWeight := 0.0
	for _, metric := range config.Metrics {
		totalWeight += metric.Weight
	}

	if math.Abs(totalWeight-1.0) > 0.001 {
		return fmt.Errorf("metric weights must sum to 1.0, got: %f", totalWeight)
	}

	hc.mutex.Lock()
	hc.config = &config

	if config.CircuitBreaker.MaxFailures > 0 {
		resetTimeout, err := time.ParseDuration(config.CircuitBreaker.ResetTimeout)
		if err != nil {
			hc.logger.WithContextFields(ctx, SourceConfig).Warnf(
				"Invalid circuit breaker reset timeout, using default 30s: %v", err)
			resetTimeout = 30 * time.Second
		}

		cb := NewCircuitBreaker("prometheus", config.CircuitBreaker.MaxFailures, resetTimeout)
		cb.SetStateChangeCallback(func(name string, from, to CircuitBreakerState) {
			hc.logger.WithContextFields(context.Background(), SourceConfig).
				Infof("Circuit breaker '%s' changed state from %v to %v", name, from, to)
			if to == StateOpen {
				hc.metrics.circuitBreakerTripped.Inc()
			}
		})
		hc.circuitBreaker = cb
	}

	if config.Logging.Service != "" {
		hc.logger = NewLogger(config.Logging)
	}

	if config.GracefulDeg.CacheTTL != "" {
		hc.parseGracefulDegConfig(&config.GracefulDeg)
	}

	if config.UnhealthyThreshold != "" {
		dur, err := time.ParseDuration(config.UnhealthyThreshold)
		if err != nil {
			hc.logger.WithContextFields(ctx, SourceConfig).
				Warnf("Invalid unhealthy threshold, using default 10m: %v", err)
		} else {
			hc.unhealthyThreshold = dur
		}
	}

	hc.rateLimiter = NewRateLimiter(config.RateLimit)
	hc.mutex.Unlock()

	if config.CircuitBreaker.MaxFailures > 0 {
		hc.logger.WithContextFields(ctx, SourceConfig).Infof(
			"Circuit breaker updated: max_failures=%d, reset_timeout=%s",
			config.CircuitBreaker.MaxFailures, config.CircuitBreaker.ResetTimeout)
	}
	if config.Logging.Service != "" {
		hc.logger.WithModule(context.Background(), SourceConfig, "config_load").Info(
			"Logging configuration updated",
		)
	}
	if config.RateLimit.Enabled {
		hc.logger.WithModule(context.Background(), SourceConfig, "config_load").Infof(
			"Rate limiting enabled with %d global rules and %d per-IP rules",
			len(config.RateLimit.GlobalRate), len(config.RateLimit.PerIPRate),
		)
	}

	hc.logger.WithContextFields(ctx, SourceConfig).Infof(
		"Config loaded successfully: %d metrics, update interval: %s",
		len(config.Metrics), config.UpdateInterval)
	return nil
}

// parseGracefulDegConfig validates and applies the graceful-degradation section.
// Mutates the passed config in place to fill defaults.
func (hc *HealthCalculator) parseGracefulDegConfig(config *GracefulDegConfig) {
	if config.CacheTTL != "" {
		if _, err := time.ParseDuration(config.CacheTTL); err != nil {
			hc.logger.WithContextFields(context.Background(), SourceConfig).
				Warnf("Invalid cache TTL in config, using default 5m: %v", err)
			config.CacheTTL = "5m"
		}
	}

	if config.MaxAge != "" {
		maxAge, err := time.ParseDuration(config.MaxAge)
		if err != nil {
			hc.logger.WithContextFields(context.Background(), SourceConfig).
				Warnf("Invalid max age in config, using default 10m: %v", err)
			maxAge = 10 * time.Minute
		}
		hc.degradation.maxAgeDuration = maxAge
	}

	validStrategies := map[string]bool{
		FallbackStrategyZero:    true,
		FallbackStrategyAverage: true,
		FallbackStrategyLast:    true,
		FallbackStrategyNeutral: true,
	}

	if !validStrategies[config.FallbackStrategy] {
		hc.logger.WithContextFields(context.Background(), SourceConfig).
			Warnf("Invalid fallback strategy '%s', using 'neutral'", config.FallbackStrategy)
		config.FallbackStrategy = FallbackStrategyNeutral
	} else if config.FallbackStrategy == "" {
		config.FallbackStrategy = FallbackStrategyNeutral
	}

	hc.logger.WithContextFields(context.Background(), SourceConfig).Infof(
		"Graceful degradation configured: cache=%v, ttl=%s, maxAge=%s, strategy=%s",
		config.EnableCache, config.CacheTTL, config.MaxAge, config.FallbackStrategy)
}
