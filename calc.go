package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Config structures - определяют структуру конфигурационного файла
type Config struct {
	UpdateInterval string               `yaml:"update_interval"`
	Metrics        []Metric             `yaml:"metrics"`
	Alerting       Alerting             `yaml:"alerting"`
	Prometheus     PrometheusConfig     `yaml:"prometheus"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	GracefulDeg    GracefulDegConfig    `yaml:"graceful_degradation"`
	RateLimit      RateLimitConfig      `yaml:"rate_limit"`
	Logging        LoggingConfig        `yaml:"logging"`
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

const maxPrometheusResponseSize = 10 * 1024 * 1024 // 10 MB

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

// Prometheus response structures - для парсинга JSON ответов от Prometheus API
type PrometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// HealthCalculator - основной сервисный объект
type HealthCalculator struct {
	config                    *Config
	healthScore               prometheus.Gauge
	metricValues              map[string]float64
	metricsFetched            prometheus.Counter
	metricsFailed             prometheus.Counter
	calculationTime           prometheus.Histogram
	lastSuccessfulCalculation time.Time
	prometheusDownCount       int
	httpClient                *http.Client
	mutex                     sync.RWMutex
	circuitBreaker            *CircuitBreaker
	circuitBreakerTripped     prometheus.Counter
	// Graceful degradation fields
	cachedValues   map[string]*CachedValue
	degradedMode   prometheus.Gauge
	fallbackUsed   prometheus.Counter
	maxAgeDuration time.Duration
	isDegraded     bool // Track degraded state separately
	// Rate limiting fields
	rateLimiter       *RateLimiter
	rateLimitExceeded prometheus.Counter
	activeClients     prometheus.Gauge
	// Config reload tracking
	configReloadTotal prometheus.Counter
	// Uptime tracking
	startTime            time.Time
	serviceUptime        prometheus.Gauge
	prometheusConnErrors prometheus.Counter
	// Logging
	logger *Logger
	// HTTP metrics
	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
}

// CachedValue хранит кэшированное значение метрики с метаданными
type CachedValue struct {
	Value     float64
	Timestamp time.Time
	Expires   time.Time
}

// registerOrLog registers Prometheus collectors, ignoring AlreadyRegistered
// errors (which happen on subsequent NewHealthCalculator calls in tests).
// Other errors are logged but do not abort startup — a missing metric is
// better than a crashed service.
func registerOrLog(cs ...prometheus.Collector) {
	for _, c := range cs {
		if err := prometheus.Register(c); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				fmt.Printf("prometheus register failed: %v\n", err)
			}
		}
	}
}

// NewHealthCalculator создает и инициализирует новый экземпляр калькулятора
func NewHealthCalculator() *HealthCalculator {
	// Регистрируем Prometheus метрики
	healthScore := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "platform_health_score",
		Help: "Overall platform health score (0.0 - 1.0)",
	})

	metricsFetched := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_calculator_metrics_fetched_total",
		Help: "Total number of metrics successfully fetched from Prometheus",
	})

	metricsFailed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_calculator_metrics_failed_total",
		Help: "Total number of failed metric fetches from Prometheus",
	})

	calculationTime := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "health_calculator_calculation_duration_seconds",
		Help:    "Time taken to calculate health score",
		Buckets: []float64{0.1, 0.5, 1.0, 2.0, 5.0},
	})

	circuitBreakerTripped := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_calculator_circuit_breaker_tripped_total",
		Help: "Total number of times the circuit breaker has opened",
	})

	degradedMode := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "health_calculator_degraded_mode",
		Help: "Indicates if service is running in degraded mode (1 = degraded, 0 = normal)",
	})

	fallbackUsed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_calculator_fallback_used_total",
		Help: "Total number of times fallback values were used for metrics",
	})

	rateLimitExceeded := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_calculator_rate_limit_exceeded_total",
		Help: "Total number of requests blocked by rate limiting",
	})

	activeClients := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "health_calculator_active_rate_limit_clients",
		Help: "Number of active clients tracked by rate limiter",
	})

	configReloadTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_calculator_config_reload_total",
		Help: "Total number of config reload attempts",
	})

	httpRequestsTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests by method, path, and status",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests by method and path",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1.0, 5.0},
		},
		[]string{"method", "path"},
	)

	serviceUptime := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "service_uptime_seconds",
		Help: "Time since the service started",
	})

	prometheusConnErrors := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "health_calculator_prometheus_connection_errors_total",
		Help: "Total number of connection errors to Prometheus",
	})

	registerOrLog(healthScore, metricsFetched, metricsFailed, calculationTime, circuitBreakerTripped, degradedMode, fallbackUsed, rateLimitExceeded, activeClients, configReloadTotal, serviceUptime, prometheusConnErrors, httpRequestsTotal, httpRequestDuration)

	// Создаем circuit breaker с настройками по умолчанию
	// Они будут обновлены при загрузке конфига
	cb := NewCircuitBreaker("prometheus", 3, 30*time.Second)

	return &HealthCalculator{
		healthScore:           healthScore,
		metricValues:          make(map[string]float64),
		metricsFetched:        metricsFetched,
		metricsFailed:         metricsFailed,
		calculationTime:       calculationTime,
		circuitBreakerTripped: circuitBreakerTripped,
		degradedMode:          degradedMode,
		fallbackUsed:          fallbackUsed,
		cachedValues:          make(map[string]*CachedValue),
		maxAgeDuration:        10 * time.Minute, // по умолчанию
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		circuitBreaker: cb,
		logger: func() *Logger {
			logger := NewLogger(LoggingConfig{
				Level:   "info",
				Format:  "json",
				Service: "health-calculator",
			})
			logger.Info("Health calculator service initialized")
			return logger
		}(), rateLimiter: NewRateLimiter(RateLimitConfig{}), // Will be updated in loadConfig
		rateLimitExceeded:    rateLimitExceeded,
		activeClients:        activeClients,
		configReloadTotal:    configReloadTotal,
		startTime:            time.Now(),
		serviceUptime:        serviceUptime,
		prometheusConnErrors: prometheusConnErrors,
		httpRequestsTotal:    httpRequestsTotal,
		httpRequestDuration:  httpRequestDuration,
	}
}

// loadConfig загружает и парсит конфигурационный файл
func (hc *HealthCalculator) loadConfig(configPath string) (err error) {
	defer hc.configReloadTotal.Inc()

	ctx := context.Background()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %v", err)
	}

	// Initialize logger if not set
	if hc.logger == nil {
		hc.logger = NewLogger(LoggingConfig{
			Level:   "info",
			Format:  "json",
			Service: "health-calculator",
		})
		hc.logger.Info("Logger initialized (default config)")
	}

	// Заменяем переменные окружения в конфиге (например ${TELEGRAM_BOT_TOKEN})
	expanded := os.ExpandEnv(string(data))

	var config Config
	if err := yaml.Unmarshal([]byte(expanded), &config); err != nil {
		return fmt.Errorf("failed to parse config: %v", err)
	}

	// Валидируем что сумма весов метрик = 1.0
	totalWeight := 0.0
	for _, metric := range config.Metrics {
		totalWeight += metric.Weight
	}

	if math.Abs(totalWeight-1.0) > 0.001 {
		return fmt.Errorf("metric weights must sum to 1.0, got: %f", totalWeight)
	}

	hc.mutex.Lock()
	hc.config = &config

	// Обновляем настройки circuit breaker
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
				hc.circuitBreakerTripped.Inc()
			}
		})
		hc.circuitBreaker = cb
	}

	// Обновляем настройки логирования
	if config.Logging.Service != "" {
		hc.logger = NewLogger(config.Logging)
	}

	// Обновляем настройки graceful degradation
	if config.GracefulDeg.CacheTTL != "" {
		hc.parseGracefulDegConfig(&config.GracefulDeg)
	}

	// Обновляем настройки rate limiting
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

// parseGracefulDegConfig парсит конфигурацию graceful degradation
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
		hc.maxAgeDuration = maxAge
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

// queryPrometheus выполняет запрос к Prometheus API
func (hc *HealthCalculator) queryPrometheus(query string, promURL string, timeout time.Duration) (float64, error) {
	url := fmt.Sprintf("%s/api/v1/query", promURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Add("query", query)
	req.URL.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := hc.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("prometheus returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPrometheusResponseSize))
	if err != nil {
		return 0, err
	}

	var result PrometheusResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	if result.Status != "success" {
		return 0, fmt.Errorf("prometheus query failed: %s", result.Status)
	}

	if len(result.Data.Result) == 0 {
		return 0, fmt.Errorf("no data returned from query")
	}

	// Prometheus возвращает значения в формате [timestamp, value]
	value := result.Data.Result[0].Value[1]
	switch v := value.(type) {
	case string:
		floatValue, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse value: %v", err)
		}
		return floatValue, nil
	default:
		return 0, fmt.Errorf("unexpected value type: %T", value)
	}
}

// cacheValue сохраняет значение в кэше
func (hc *HealthCalculator) cacheValue(metricName string, value float64, ttl time.Duration) {
	hc.mutex.Lock()
	defer hc.mutex.Unlock()
	hc.cacheValueLocked(metricName, value, ttl)
}

func (hc *HealthCalculator) cacheValueLocked(metricName string, value float64, ttl time.Duration) {
	hc.cachedValues[metricName] = &CachedValue{
		Value:     value,
		Timestamp: time.Now(),
		Expires:   time.Now().Add(ttl),
	}
}

// getCachedValue получает значение из кэша
func (hc *HealthCalculator) getCachedValue(metricName string) (float64, bool) {
	hc.mutex.RLock()
	defer hc.mutex.RUnlock()
	return hc.getCachedValueLocked(metricName)
}

func (hc *HealthCalculator) getCachedValueLocked(metricName string) (float64, bool) {
	cached, exists := hc.cachedValues[metricName]
	if !exists {
		return 0, false
	}

	if time.Now().After(cached.Expires) {
		return 0, false
	}

	return cached.Value, true
}

// getFallbackValue возвращает fallback значение на основе стратегии
func (hc *HealthCalculator) getFallbackValue(metricName string, metric Metric) float64 {
	hc.mutex.RLock()
	defer hc.mutex.RUnlock()
	return hc.getFallbackValueLocked(metricName, metric)
}

func (hc *HealthCalculator) getFallbackValueLocked(metricName string, metric Metric) float64 {
	if hc.fallbackUsed != nil {
		hc.fallbackUsed.Inc()
	}

	logger := hc.logger
	if logger == nil {
		logger = NewLogger(LoggingConfig{Level: "error", Format: "text", Service: "health-calculator"})
	}

	switch hc.config.GracefulDeg.FallbackStrategy {
	case FallbackStrategyZero:
		logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Using zero fallback for metric %s", metricName)
		return 0
	case FallbackStrategyNeutral:
		logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Using neutral fallback (0.5) for metric %s", metricName)
		return 0.5
	case FallbackStrategyLast:
		if cachedValue, exists := hc.getCachedValueLocked(metricName); exists {
			logger.WithContextFields(context.Background(), SourceCalculator).
				Warnf("Using last known value %.4f for metric %s", cachedValue, metricName)
			return cachedValue
		}
		logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("No valid cached value for metric %s, using neutral fallback", metricName)
		return 0.5
	case FallbackStrategyAverage:
		avg := (metric.MinValue + metric.MaxValue) / 2
		logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Using average fallback %.4f for metric %s", avg, metricName)
		rangeSize := metric.MaxValue - metric.MinValue
		if rangeSize == 0 {
			return 1.0
		}
		return (avg - metric.MinValue) / rangeSize
	default:
		logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Unknown fallback strategy, using neutral for metric %s", metricName)
		return 0.5
	}
}

// cleanupExpiredCache удаляет просроченные значения из кэша
func (hc *HealthCalculator) cleanupExpiredCache() {
	hc.mutex.Lock()
	defer hc.mutex.Unlock()
	hc.cleanupExpiredCacheLocked()
}

func (hc *HealthCalculator) cleanupExpiredCacheLocked() {
	now := time.Now()
	maxAge := hc.maxAgeDuration
	for name, cached := range hc.cachedValues {
		if now.After(cached.Expires) || now.After(cached.Timestamp.Add(maxAge)) {
			delete(hc.cachedValues, name)
		}
	}
}

// queryPrometheusWithRetry выполняет запрос через circuit breaker и с ретраями
func (hc *HealthCalculator) queryPrometheusWithRetry(query string, metricName string, promURL string, timeout time.Duration, alertThreshold int, botToken string, chatID string) (float64, error) {
	var result float64
	var err error

	cbErr := hc.circuitBreaker.Execute(func() error {
		var lastErr error
		maxRetries := 3

		for i := 0; i < maxRetries; i++ {
			value, queryErr := hc.queryPrometheus(query, promURL, timeout)
			if queryErr == nil {
				result = value
				hc.prometheusDownCount = 0
				hc.metricsFetched.Inc()
				return nil
			}

			lastErr = queryErr
			hc.metricsFailed.Inc()
			hc.prometheusConnErrors.Inc()
			hc.logger.WithContextFields(context.Background(), SourcePrometheus).
				Warnf("Retry %d/%d for metric %s failed: %v", i+1, maxRetries, metricName, queryErr)

			time.Sleep(time.Duration(i+1) * time.Second)
		}

		err = lastErr

		hc.prometheusDownCount++
		if hc.prometheusDownCount >= alertThreshold {
			hc.sendAlert(context.Background(),
				fmt.Sprintf("Prometheus unavailable after %d attempts. Last error: %v",
					hc.prometheusDownCount, lastErr),
				botToken, chatID)
		}

		return fmt.Errorf("all retries failed: %v", lastErr)
	})

	if cbErr == ErrCircuitBreakerOpen {
		hc.logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Circuit breaker is open, using fallback value for metric %s", metricName)
		return 0.5, nil
	}

	if err != nil {
		return 0, err
	}

	return result, nil
}

// sendAlert отправляет уведомление в Telegram
func (hc *HealthCalculator) sendAlert(ctx context.Context, message string, botToken string, chatID string) {
	logger := hc.logger.WithContextFields(ctx, SourceAlerting)

	if botToken == "" || chatID == "" {
		logger.WithField("message", message).Warn("ALERT would be sent - no Telegram credentials configured")
		return
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	payload := map[string]string{
		"chat_id": chatID,
		"text":    message,
	}

	jsonData, _ := json.Marshal(payload)

	resp, err := hc.httpClient.Post(url, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		hc.logger.WithError(err, SourceAlerting).Error("Failed to send Telegram alert")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.WithField("status_code", resp.StatusCode).Error("Telegram API returned non-200 status")
	} else {
		logger.Info("Telegram alert sent successfully")
	}
}

// normalizeValue нормализует значение метрики в диапазон 0-1
func (hc *HealthCalculator) normalizeValue(value float64, metric Metric) float64 {
	// Ограничиваем значение минимальным и максимальным диапазоном
	if value < metric.MinValue {
		return 0.0
	}
	if value > metric.MaxValue {
		return 1.0
	}

	// Нормализуем к диапазону 0-1
	rangeSize := metric.MaxValue - metric.MinValue
	if rangeSize == 0 {
		return 1.0
	}

	return (value - metric.MinValue) / rangeSize
}

// calculateHealthScore - основная функция расчета health score с graceful degradation
func (hc *HealthCalculator) calculateHealthScore() {
	startTime := time.Now()

	ctx := ContextWithRequestID(context.Background(), GenerateRequestID())

	hc.mutex.Lock()
	defer hc.mutex.Unlock()

	// Очищаем просроченные кэши
	hc.cleanupExpiredCacheLocked()

	promURL := ""
	timeout := 10 * time.Second
	alertThreshold := 3
	botToken := ""
	chatID := ""
	if hc.config != nil {
		promURL = hc.config.Prometheus.URL
		if hc.config.Prometheus.Timeout != "" {
			if t, err := time.ParseDuration(hc.config.Prometheus.Timeout); err == nil {
				timeout = t
			}
		}
		alertThreshold = hc.config.Alerting.PrometheusUnavailableThreshold
		botToken = hc.config.Alerting.Telegram.BotToken
		chatID = hc.config.Alerting.Telegram.ChatID
	}

	totalScore := 0.0
	validMetrics := 0
	degradedMetrics := 0
	var cacheTTL time.Duration

	if hc.config != nil && hc.config.GracefulDeg.EnableCache {
		var err error
		cacheTTL, err = time.ParseDuration(hc.config.GracefulDeg.CacheTTL)
		if err != nil {
			hc.logger.WithContextFields(context.Background(), SourceCalculator).
				Warnf("Invalid cache TTL, using default 5m: %v", err)
			cacheTTL = 5 * time.Minute
		}
	}

	for _, metric := range hc.config.Metrics {
		var normalizedValue float64
		var value float64
		var err error
		var usedFallback bool

		if cachedValue, exists := hc.getCachedValueLocked(metric.Name); exists && hc.config.GracefulDeg.EnableCache {
			value = cachedValue
			hc.logger.WithContextFields(ctx, SourceCalculator).
				Debugf("Using cached value for metric %s: %.4f", metric.Name, cachedValue)
		} else {
			value, err = hc.queryPrometheusWithRetry(metric.Query, metric.Name, promURL, timeout, alertThreshold, botToken, chatID)

			if err != nil {
				hc.logger.WithContextFields(ctx, SourceCalculator).
					Warnf("Failed to get metric %s, using fallback: %v", metric.Name, err)
				value = hc.getFallbackValueLocked(metric.Name, metric)
				usedFallback = true
				degradedMetrics++
			} else {
				if hc.config.GracefulDeg.EnableCache {
					hc.cacheValueLocked(metric.Name, value, cacheTTL)
				}
			}
		}

		normalizedValue = hc.normalizeValue(value, metric)
		hc.metricValues[metric.Name] = normalizedValue

		totalScore += normalizedValue * metric.Weight
		validMetrics++

		if usedFallback {
			hc.logger.WithContextFields(ctx, SourceCalculator).
				Infof("Metric %s used fallback value: %.4f (normalized: %.4f)",
					metric.Name, value, normalizedValue)
		}
	}

	degradationFactor := 1.0
	if degradedMetrics > 0 {
		degradationFactor = 1.0 - (float64(degradedMetrics) / float64(len(hc.config.Metrics)) * 0.3)
		hc.logger.WithModule(ctx, SourceCalculator, "score_calc").Infof(
			"Degradation: %d/%d metrics using fallback, factor: %.2f",
			degradedMetrics, len(hc.config.Metrics), degradationFactor,
		)
	}

	finalScore := totalScore * degradationFactor

	if degradedMetrics > 0 {
		hc.degradedMode.Set(1)
		hc.isDegraded = true
	} else {
		hc.degradedMode.Set(0)
		hc.isDegraded = false
	}

	hc.healthScore.Set(finalScore)
	hc.lastSuccessfulCalculation = time.Now()
	hc.calculationTime.Observe(time.Since(startTime).Seconds())
	hc.serviceUptime.Set(time.Since(hc.startTime).Seconds())

	hc.logger.WithContextFields(ctx, SourceCalculator).Infof(
		"Health score updated: %.4f (from %d metrics, %d degraded, factor %.2f, took %v)",
		finalScore, validMetrics, degradedMetrics, degradationFactor, time.Since(startTime))
}

// circuitBreakerHandler - HTTP handler для отображения состояния circuit breaker
func (hc *HealthCalculator) circuitBreakerHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	state := hc.circuitBreaker.State()
	stateName := "unknown"
	switch state {
	case StateClosed:
		stateName = "closed"
	case StateOpen:
		stateName = "open"
	case StateHalfOpen:
		stateName = "half-open"
	}

	response := map[string]interface{}{
		"name":     hc.circuitBreaker.name,
		"state":    stateName,
		"failures": hc.circuitBreaker.Failures(),
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

// recoveryMiddleware восстанавливает панику в HTTP handler и логирует её
func (hc *HealthCalculator) recoveryMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				hc.logger.WithContextFields(r.Context(), SourceHTTP).
					Errorf("panic recovered: %v", rec)
				http.Error(w, `{"error":"internal server error"}`,
					http.StatusInternalServerError)
			}
		}()
		next(w, r)
	}
}

// wrapWithRateLimit оборачивает handler в rate limiting middleware
func (hc *HealthCalculator) wrapWithRateLimit(handler http.HandlerFunc) http.HandlerFunc {
	metrics := &RateLimitMetrics{
		rateLimitExceeded: hc.rateLimitExceeded,
		activeClients:     hc.activeClients,
	}
	return RateLimitMiddleware(hc.rateLimiter, metrics, hc.logger, handler)
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// knownEndpoints is a whitelist of paths used as Prometheus labels to
// prevent unbounded label cardinality. Any unknown path is bucketed as
// "/other" to keep time-series count bounded.
var knownEndpoints = map[string]bool{
	"/metrics":         true,
	"/health":          true,
	"/ready":           true,
	"/circuit-breaker": true,
	"/":                true,
}

func normalizePath(p string) string {
	if knownEndpoints[p] {
		return p
	}
	return "/other"
}

// httpMetricsMiddleware записывает RED-метрики для HTTP запросов
func (hc *HealthCalculator) httpMetricsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next(rec, r)
		duration := time.Since(start).Seconds()
		path := normalizePath(r.URL.Path)
		hc.httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rec.statusCode)).Inc()
		hc.httpRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	}
}

// healthHandler - HTTP handler для health checks
func (hc *HealthCalculator) healthHandler(w http.ResponseWriter, r *http.Request) {
	hc.mutex.RLock()
	lastUpdate := time.Since(hc.lastSuccessfulCalculation)
	lastCalcTime := hc.lastSuccessfulCalculation
	isDegraded := hc.isDegraded
	cbState := hc.circuitBreaker.State()
	circuitOpen := cbState == StateOpen
	hc.mutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	status := "healthy"
	statusCode := http.StatusOK
	stateName := "unknown"
	switch cbState {
	case StateClosed:
		stateName = "closed"
	case StateOpen:
		stateName = "open"
	case StateHalfOpen:
		stateName = "half-open"
	}
	response := map[string]interface{}{
		"status":                      status,
		"last_successful_calculation": lastCalcTime.Format(time.RFC3339),
		"age":                         lastUpdate.String(),
		"degraded":                    isDegraded,
		"circuit_breaker": map[string]interface{}{
			"state": stateName,
		},
	}

	// Определяем общий статус
	if lastUpdate > 10*time.Minute {
		status = "unhealthy"
		statusCode = http.StatusServiceUnavailable
		response["status"] = status
		response["reason"] = fmt.Sprintf("last calculation too old: %v", lastUpdate)
	} else if isDegraded {
		status = "degraded"
		response["status"] = status
		response["reason"] = "some metrics are using fallback values"
	} else if circuitOpen {
		status = "degraded"
		response["status"] = status
		response["reason"] = "circuit breaker is open"
	}

	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(response)
}

// readyHandler - HTTP handler для Kubernetes readiness probe
func (hc *HealthCalculator) readyHandler(w http.ResponseWriter, r *http.Request) {
	hc.mutex.RLock()
	configLoaded := hc.config != nil
	hasCalculation := !hc.lastSuccessfulCalculation.IsZero()
	lastUpdate := time.Since(hc.lastSuccessfulCalculation)
	isDegraded := hc.isDegraded
	hc.mutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	if !configLoaded || !hasCalculation {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "not_ready",
			"reason": "service has not completed initial startup",
		})
		return
	}

	status := "ready"
	statusCode := http.StatusOK
	if lastUpdate > 10*time.Minute {
		status = "not_ready"
		statusCode = http.StatusServiceUnavailable
	} else if isDegraded {
		status = "ready_degraded"
	}

	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": status,
	})
}

// Start запускает основной цикл работы сервиса
func (hc *HealthCalculator) Start(ctx context.Context) error {
	// Загружаем конфиг при старте
	if err := hc.loadConfig(configPath()); err != nil {
		return fmt.Errorf("failed to load initial config: %v", err)
	}

	// Запускаем фоновое обновление конфига
	go hc.watchConfig(ctx)

	// Запускаем очистку rate limiter buckets
	go hc.cleanupRateLimitBuckets(ctx)

	interval, err := time.ParseDuration(hc.config.UpdateInterval)
	if err != nil {
		hc.logger.WithContextFields(ctx, SourceCalculator).
			Warnf("Invalid update interval, using default 5m: %v", err)
		interval = 5 * time.Minute
	}

	hc.logger.WithContextFields(ctx, SourceCalculator).
		Infof("Starting health calculation loop with interval: %v", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	hc.calculateHealthScore()

	for {
		select {
		case <-ctx.Done():
			hc.logger.WithContextFields(ctx, SourceCalculator).
				Info("Shutting down health calculator gracefully")
			return nil
		case <-ticker.C:
			hc.calculateHealthScore()
		}
	}
}

// cleanupRateLimitBuckets периодически очищает неиспользуемые bucket'ы
func (hc *HealthCalculator) cleanupRateLimitBuckets(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hc.rateLimiter.CleanupExpiredBuckets()
		}
	}
}

// watchConfig периодически перезагружает конфиг
func (hc *HealthCalculator) watchConfig(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := hc.loadConfig(configPath()); err != nil {
				hc.logger.WithContextFields(context.Background(), SourceConfig).
					Errorf("Failed to reload config: %v", err)
			}
		}
	}
}

func configPath() string {
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		return p
	}
	return "health-config.yaml"
}

func main() {
	calculator := NewHealthCalculator()
	calculator.logger.Info("Starting Health Calculator Service...")

	// Настраиваем HTTP сервер
	mux := http.NewServeMux()
	mux.Handle("/metrics", calculator.httpMetricsMiddleware(promhttp.Handler().ServeHTTP))
	mux.HandleFunc("/health", calculator.recoveryMiddleware(calculator.httpMetricsMiddleware(calculator.wrapWithRateLimit(calculator.healthHandler))))
	mux.HandleFunc("/ready", calculator.recoveryMiddleware(calculator.httpMetricsMiddleware(calculator.readyHandler)))
	mux.HandleFunc("/circuit-breaker", calculator.recoveryMiddleware(calculator.httpMetricsMiddleware(calculator.wrapWithRateLimit(calculator.circuitBreakerHandler))))
	mux.HandleFunc("/", calculator.recoveryMiddleware(calculator.httpMetricsMiddleware(calculator.wrapWithRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Health Calculator Service"))
	}))))

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		calculator.logger.Infof("Starting HTTP server on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			calculator.logger.Fatalf("HTTP server error: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	serviceErr := make(chan error, 1)
	go func() {
		serviceErr <- calculator.Start(ctx)
	}()

	select {
	case sig := <-sigChan:
		calculator.logger.Infof("Received signal: %v, shutting down...", sig)
		cancel()
	case err := <-serviceErr:
		calculator.logger.Infof("Service error: %v, shutting down...", err)
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		calculator.logger.Errorf("HTTP server shutdown error: %v", err)
	}

	calculator.logger.Info("Health Calculator Service stopped gracefully")
}
