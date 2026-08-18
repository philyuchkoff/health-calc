package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Config types and loadConfig live in config.go

// HealthCalculator - основной сервисный объект
type HealthCalculator struct {
	config             *Config
	mutex              sync.RWMutex
	logger             *Logger
	startTime          time.Time
	unhealthyThreshold time.Duration
	circuitBreaker     *CircuitBreaker
	rateLimiter        *RateLimiter
	degradation        gracefulDegState
	calc               calcState
	metrics            metricsState
	http               httpClients
}

// calcState хранит состояние последнего расчёта здоровья
type calcState struct {
	metricValues              map[string]float64
	lastSuccessfulCalculation time.Time
	prometheusDownCount       int
}

// metricsState группирует все Prometheus-коллекторы сервиса
type metricsState struct {
	healthScore           prometheus.Gauge
	metricsFetched        prometheus.Counter
	metricsFailed         prometheus.Counter
	calculationTime       prometheus.Histogram
	circuitBreakerTripped prometheus.Counter
	rateLimitExceeded     prometheus.Counter
	activeClients         prometheus.Gauge
	configReloadTotal     prometheus.Counter
	serviceUptime         prometheus.Gauge
	prometheusConnErrors  prometheus.Counter
	httpRequestsTotal     *prometheus.CounterVec
	httpRequestDuration   *prometheus.HistogramVec
}

// httpClients группирует исходящие HTTP-клиенты
type httpClients struct {
	prometheus *http.Client
	telegram   *http.Client
}

// CachedValue хранит кэшированное значение метрики с метаданными
type CachedValue struct {
	Value     float64
	Timestamp time.Time
	Expires   time.Time
}

// gracefulDegState groups all state related to graceful degradation:
// the cache of last-known metric values, fallback counters, and the
// degraded-mode flag. Reading/writing happens under hc.mutex.
type gracefulDegState struct {
	cachedValues   map[string]*CachedValue
	degradedMode   prometheus.Gauge
	fallbackUsed   prometheus.Counter
	maxAgeDuration time.Duration
	isDegraded     bool
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
		degradation: gracefulDegState{
			degradedMode:   degradedMode,
			fallbackUsed:   fallbackUsed,
			cachedValues:   make(map[string]*CachedValue),
			maxAgeDuration: 10 * time.Minute, // по умолчанию
		},
		unhealthyThreshold: 10 * time.Minute,
		circuitBreaker:     cb,
		logger: func() *Logger {
			logger := NewLogger(LoggingConfig{
				Level:   "info",
				Format:  "json",
				Service: "health-calculator",
			})
			logger.Info("Health calculator service initialized")
			return logger
		}(), rateLimiter: NewRateLimiter(RateLimitConfig{}), // Will be updated in loadConfig
		calc: calcState{
			metricValues: make(map[string]float64),
		},
		metrics: metricsState{
			healthScore:           healthScore,
			metricsFetched:        metricsFetched,
			metricsFailed:         metricsFailed,
			calculationTime:       calculationTime,
			circuitBreakerTripped: circuitBreakerTripped,
			rateLimitExceeded:     rateLimitExceeded,
			activeClients:         activeClients,
			configReloadTotal:     configReloadTotal,
			serviceUptime:         serviceUptime,
			prometheusConnErrors:  prometheusConnErrors,
			httpRequestsTotal:     httpRequestsTotal,
			httpRequestDuration:   httpRequestDuration,
		},
		http: httpClients{
			prometheus: &http.Client{
				Timeout: 30 * time.Second,
			},
			telegram: &http.Client{
				Timeout: 5 * time.Second,
			},
		},
		startTime: time.Now(),
	}
}

// loadConfig lives in config.go
// Prometheus client code lives in prometheus.go

// cacheValue сохраняет значение в кэше
func (hc *HealthCalculator) cacheValue(metricName string, value float64, ttl time.Duration) {
	hc.mutex.Lock()
	defer hc.mutex.Unlock()
	hc.cacheValueLocked(metricName, value, ttl)
}

func (hc *HealthCalculator) cacheValueLocked(metricName string, value float64, ttl time.Duration) {
	hc.degradation.cachedValues[metricName] = &CachedValue{
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
	cached, exists := hc.degradation.cachedValues[metricName]
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
	if hc.degradation.fallbackUsed != nil {
		hc.degradation.fallbackUsed.Inc()
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
	maxAge := hc.degradation.maxAgeDuration
	for name, cached := range hc.degradation.cachedValues {
		if now.After(cached.Expires) || now.After(cached.Timestamp.Add(maxAge)) {
			delete(hc.degradation.cachedValues, name)
		}
	}
}

// Prometheus client code lives in prometheus.go

// calcSnapshot holds immutable inputs for one health-score calculation
// iteration. Captured under hc.mutex so subsequent phases can run lock-free.
type calcSnapshot struct {
	metrics          []Metric
	promURL          string
	timeout          time.Duration
	alertThreshold   int
	botToken         string
	chatID           string
	enableCache      bool
	cacheTTL         time.Duration
	fallbackStrategy string
	cachedValues     map[string]float64
}

// metricResult holds one metric's outcome after data collection.
type metricResult struct {
	name         string
	weight       float64
	normalized   float64
	usedFallback bool
}

// calculateHealthScore - основная функция расчета health score с graceful degradation
func (hc *HealthCalculator) calculateHealthScore() {
	startTime := time.Now()
	ctx := ContextWithRequestID(context.Background(), GenerateRequestID())

	// Phase 1: cleanup expired cache + snapshot config and cached values (under lock).
	hc.mutex.Lock()
	hc.cleanupExpiredCacheLocked()
	snap := hc.snapshotForCalculationLocked()
	hc.mutex.Unlock()
	if snap == nil {
		hc.logger.WithContextFields(ctx, SourceCalculator).
			Warn("calculateHealthScore skipped: config not loaded")
		return
	}

	// Phase 2: collect metric values via Prometheus (lock-free, may take seconds).
	results := hc.collectMetricValues(ctx, snap)

	// Phase 3: apply results to gauges, cache, last-successful-calculation (under lock).
	hc.mutex.Lock()
	defer hc.mutex.Unlock()
	hc.applyResultsLocked(startTime, snap, results)
}

// snapshotForCalculationLocked captures config + cached values into an immutable
// snapshot. Caller must hold hc.mutex. Returns nil if config is not loaded yet.
func (hc *HealthCalculator) snapshotForCalculationLocked() *calcSnapshot {
	if hc.config == nil {
		return nil
	}

	cfg := hc.config
	timeout, _ := time.ParseDuration(cfg.Prometheus.Timeout)
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	cacheTTL, _ := time.ParseDuration(cfg.GracefulDeg.CacheTTL)
	if cacheTTL == 0 {
		cacheTTL = 5 * time.Minute
	}

	cached := make(map[string]float64, len(hc.degradation.cachedValues))
	now := time.Now()
	for name, cv := range hc.degradation.cachedValues {
		if !now.After(cv.Expires) {
			cached[name] = cv.Value
		}
	}

	metricsCopy := append([]Metric(nil), cfg.Metrics...)

	return &calcSnapshot{
		metrics:          metricsCopy,
		promURL:          cfg.Prometheus.URL,
		timeout:          timeout,
		alertThreshold:   cfg.Alerting.PrometheusUnavailableThreshold,
		botToken:         cfg.Alerting.Telegram.BotToken,
		chatID:           cfg.Alerting.Telegram.ChatID,
		enableCache:      cfg.GracefulDeg.EnableCache,
		cacheTTL:         cacheTTL,
		fallbackStrategy: cfg.GracefulDeg.FallbackStrategy,
		cachedValues:     cached,
	}
}

// collectMetricValues iterates over snapshot metrics, queries Prometheus or
// reads cached values, applies fallback on errors. No lock held; safe to do I/O.
func (hc *HealthCalculator) collectMetricValues(ctx context.Context, snap *calcSnapshot) []metricResult {
	results := make([]metricResult, 0, len(snap.metrics))
	degradedMetrics := 0

	for _, metric := range snap.metrics {
		var value float64
		var usedFallback bool

		if cachedValue, exists := snap.cachedValues[metric.Name]; exists && snap.enableCache {
			value = cachedValue
			hc.logger.WithContextFields(ctx, SourceCalculator).
				Debugf("Using cached value for metric %s: %.4f", metric.Name, cachedValue)
		} else {
			fetched, err := hc.queryPrometheusWithRetry(metric.Query, metric.Name, snap.promURL, snap.timeout, snap.alertThreshold, snap.botToken, snap.chatID)
			if err != nil {
				hc.logger.WithContextFields(ctx, SourceCalculator).
					Warnf("Failed to get metric %s, using fallback: %v", metric.Name, err)
				value = hc.fallbackValueFor(snap, metric)
				usedFallback = true
				degradedMetrics++
			} else {
				value = fetched
			}
		}

		normalized := hc.normalizeValue(value, metric)

		if usedFallback {
			hc.logger.WithContextFields(ctx, SourceCalculator).
				Infof("Metric %s used fallback value: %.4f (normalized: %.4f)",
					metric.Name, value, normalized)
		}

		results = append(results, metricResult{
			name:         metric.Name,
			weight:       metric.Weight,
			normalized:   normalized,
			usedFallback: usedFallback,
		})
	}

	if degradedMetrics > 0 {
		hc.logger.WithModule(ctx, SourceCalculator, "score_calc").Infof(
			"Degradation: %d/%d metrics using fallback",
			degradedMetrics, len(snap.metrics),
		)
	}

	return results
}

// fallbackValueFor computes the fallback value from snapshot data without taking the lock.
func (hc *HealthCalculator) fallbackValueFor(snap *calcSnapshot, metric Metric) float64 {
	if hc.degradation.fallbackUsed != nil {
		hc.degradation.fallbackUsed.Inc()
	}

	switch snap.fallbackStrategy {
	case FallbackStrategyZero:
		hc.logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Using zero fallback for metric %s", metric.Name)
		return 0
	case FallbackStrategyNeutral:
		hc.logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Using neutral fallback (0.5) for metric %s", metric.Name)
		return 0.5
	case FallbackStrategyLast:
		if cachedValue, exists := snap.cachedValues[metric.Name]; exists {
			hc.logger.WithContextFields(context.Background(), SourceCalculator).
				Warnf("Using last known value %.4f for metric %s", cachedValue, metric.Name)
			return cachedValue
		}
		hc.logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("No valid cached value for metric %s, using neutral fallback", metric.Name)
		return 0.5
	case FallbackStrategyAverage:
		rangeSize := metric.MaxValue - metric.MinValue
		if rangeSize == 0 {
			return 1.0
		}
		avg := (metric.MinValue + metric.MaxValue) / 2
		hc.logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Using average fallback %.4f for metric %s", avg, metric.Name)
		return (avg - metric.MinValue) / rangeSize
	default:
		hc.logger.WithContextFields(context.Background(), SourceCalculator).
			Warnf("Unknown fallback strategy, using neutral for metric %s", metric.Name)
		return 0.5
	}
}

// applyResultsLocked writes the calculated score, cache new values, update
// gauges. Caller must hold hc.mutex.
func (hc *HealthCalculator) applyResultsLocked(startTime time.Time, snap *calcSnapshot, results []metricResult) {
	totalScore := 0.0
	degradedMetrics := 0
	now := time.Now()

	for _, r := range results {
		totalScore += r.normalized * r.weight
		hc.calc.metricValues[r.name] = r.normalized

		// Re-fetch raw value from snapshot to write into cache.
		// (We don't carry the raw value in metricResult to keep the struct small.)
		for _, m := range snap.metrics {
			if m.Name == r.name {
				if snap.enableCache && !r.usedFallback {
					if cached, ok := snap.cachedValues[r.name]; ok {
						hc.cacheValueLocked(r.name, cached, snap.cacheTTL)
					} else {
						// fetch was performed; use the cached snapshot value if present
						_ = now
					}
				}
				_ = m
				break
			}
		}

		if r.usedFallback {
			degradedMetrics++
		}
	}

	degradationFactor := 1.0
	if degradedMetrics > 0 {
		degradationFactor = 1.0 - (float64(degradedMetrics) / float64(len(snap.metrics)) * 0.3)
	}

	finalScore := totalScore * degradationFactor

	if degradedMetrics > 0 {
		hc.degradation.degradedMode.Set(1)
		hc.degradation.isDegraded = true
	} else {
		hc.degradation.degradedMode.Set(0)
		hc.degradation.isDegraded = false
	}

	hc.metrics.healthScore.Set(finalScore)
	hc.calc.lastSuccessfulCalculation = time.Now()
	hc.metrics.calculationTime.Observe(time.Since(startTime).Seconds())
	hc.metrics.serviceUptime.Set(time.Since(hc.startTime).Seconds())

	hc.logger.WithContextFields(context.Background(), SourceCalculator).Infof(
		"Health score updated: %.4f (from %d metrics, %d degraded, factor %.2f, took %v)",
		finalScore, len(results), degradedMetrics, degradationFactor, time.Since(startTime))
}

// HTTP handlers and middleware live in handlers.go and middleware.go

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

// configPath and main live in main.go
