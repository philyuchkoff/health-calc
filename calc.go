package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	UpdateInterval  string               `yaml:"update_interval"`
	Metrics         []Metric             `yaml:"metrics"`
	Alerting        Alerting             `yaml:"alerting"`
	Prometheus      PrometheusConfig     `yaml:"prometheus"`
	CircuitBreaker  CircuitBreakerConfig `yaml:"circuit_breaker"`
	GracefulDeg    GracefulDegConfig    `yaml:"graceful_degradation"`
	RateLimit       RateLimitConfig      `yaml:"rate_limit"`
	Logging         LoggingConfig        `yaml:"logging"`
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
	cachedValues              map[string]*CachedValue
	degradedMode              prometheus.Gauge
	fallbackUsed              prometheus.Counter
	maxAgeDuration            time.Duration
	isDegraded                bool // Track degraded state separately
	// Rate limiting fields
	rateLimiter               *RateLimiter
	rateLimitExceeded         prometheus.Counter
	activeClients             prometheus.Gauge
	// Logging
	logger                   *Logger
}

// CachedValue хранит кэшированное значение метрики с метаданными
type CachedValue struct {
	Value     float64
	Timestamp time.Time
	Expires   time.Time
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

	// Регистрируем все метрики в Prometheus registry
	prometheus.MustRegister(healthScore, metricsFetched, metricsFailed, calculationTime, circuitBreakerTripped, degradedMode, fallbackUsed, rateLimitExceeded, activeClients)

	// Создаем circuit breaker с настройками по умолчанию
	// Они будут обновлены при загрузке конфига
	cb := NewCircuitBreaker("prometheus", 3, 30*time.Second)

	return &HealthCalculator{
		healthScore:          healthScore,
		metricValues:         make(map[string]float64),
		metricsFetched:       metricsFetched,
		metricsFailed:        metricsFailed,
		calculationTime:      calculationTime,
		circuitBreakerTripped: circuitBreakerTripped,
			degradedMode:         degradedMode,
			fallbackUsed:         fallbackUsed,
			cachedValues:         make(map[string]*CachedValue),
			maxAgeDuration:       10 * time.Minute, // по умолчанию
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
	},
	circuitBreaker: cb,
	logger: func() *Logger {
		logger := NewLogger(LoggingConfig{
			Level: "info",
			Format: "json",
			Service: "health-calculator",
		})
		logger.Info("Health calculator service initialized")
		return logger
	}(),	rateLimiter: NewRateLimiter(RateLimitConfig{}), // Will be updated in loadConfig
	rateLimitExceeded: rateLimitExceeded,
	activeClients: activeClients,
}
}

// loadConfig загружает и парсит конфигурационный файл
func (hc *HealthCalculator) loadConfig(configPath string) error {
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

	hc.config = &config

	// Парсим timeout из конфига
	timeout, err := time.ParseDuration(config.Prometheus.Timeout)
	if err != nil {
		hc.logger.WithContextFields(ctx, SourceConfig).Warnf(
			"Invalid timeout format, using default 30s: %v", err)
		timeout = 30 * time.Second
	}
	hc.httpClient.Timeout = timeout

	// Обновляем настройки circuit breaker
	if config.CircuitBreaker.MaxFailures > 0 {
		resetTimeout, err := time.ParseDuration(config.CircuitBreaker.ResetTimeout)
		if err != nil {
			log.Printf("Invalid circuit breaker reset timeout, using default 30s: %v", err)
			resetTimeout = 30 * time.Second
		}

		// Создаем новый circuit breaker с обновленными настройками
		cb := NewCircuitBreaker("prometheus", config.CircuitBreaker.MaxFailures, resetTimeout)

		// Устанавливаем callback для логирования изменений состояния
		cb.SetStateChangeCallback(func(name string, from, to CircuitBreakerState) {
			log.Printf("Circuit breaker '%s' changed state from %v to %v", name, from, to)
			hc.circuitBreakerTripped.Inc()
		})

		hc.circuitBreaker = cb
		log.Printf("Circuit breaker updated: max_failures=%d, reset_timeout=%s",
			config.CircuitBreaker.MaxFailures, config.CircuitBreaker.ResetTimeout)
	}

		// Обновляем настройки логирования
		if config.Logging.Service != "" {
			hc.logger = NewLogger(config.Logging)
			hc.logger.WithModule(context.Background(), SourceConfig, "config_load").Info(
				"Logging configuration updated",
			)
		}

		// Обновляем настройки graceful degradation
		if config.GracefulDeg.CacheTTL != "" {
			hc.parseGracefulDegConfig(&config.GracefulDeg)
		}

		// Обновляем настройки rate limiting
		hc.rateLimiter = NewRateLimiter(config.RateLimit)
		if config.RateLimit.Enabled {
			hc.logger.WithModule(context.Background(), SourceConfig, "config_load").Infof(
				"Rate limiting enabled with %d global rules and %d per-IP rules",
				len(config.RateLimit.GlobalRate), len(config.RateLimit.PerIPRate),
			)
		}

		// Валидируем что сумма весов метрик = 1.0
	totalWeight := 0.0
	for _, metric := range config.Metrics {
		totalWeight += metric.Weight
	}

	if math.Abs(totalWeight-1.0) > 0.001 {
		return fmt.Errorf("metric weights must sum to 1.0, got: %f", totalWeight)
	}

	log.Printf("Config loaded successfully: %d metrics, update interval: %s",
		len(config.Metrics), config.UpdateInterval)
	return nil
}

// parseGracefulDegConfig парсит конфигурацию graceful degradation
func (hc *HealthCalculator) parseGracefulDegConfig(config *GracefulDegConfig) {
	if config.CacheTTL != "" {
		if _, err := time.ParseDuration(config.CacheTTL); err != nil {
			log.Printf("Invalid cache TTL in config, using default 5m: %v", err)
			config.CacheTTL = "5m"
		}
	}

	if config.MaxAge != "" {
		maxAge, err := time.ParseDuration(config.MaxAge)
		if err != nil {
			log.Printf("Invalid max age in config, using default 10m: %v", err)
			maxAge = 10 * time.Minute
		}
		hc.maxAgeDuration = maxAge
	}

	// Валидация fallback стратегии
	validStrategies := map[string]bool{
		FallbackStrategyZero:    true,
		FallbackStrategyAverage: true,
		FallbackStrategyLast:    true,
		FallbackStrategyNeutral: true,
	}

	if !validStrategies[config.FallbackStrategy] {
		log.Printf("Invalid fallback strategy '%s', using 'neutral'", config.FallbackStrategy)
		config.FallbackStrategy = FallbackStrategyNeutral
	} else if config.FallbackStrategy == "" {
		config.FallbackStrategy = FallbackStrategyNeutral
	}

	log.Printf("Graceful degradation configured: cache=%v, ttl=%s, maxAge=%s, strategy=%s",
		config.EnableCache, config.CacheTTL, config.MaxAge, config.FallbackStrategy)
}

// queryPrometheus выполняет запрос к Prometheus API
func (hc *HealthCalculator) queryPrometheus(query string) (float64, error) {
	url := fmt.Sprintf("%s/api/v1/query", hc.config.Prometheus.URL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Add("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := hc.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("prometheus returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
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

	cached, exists := hc.cachedValues[metricName]
	if !exists {
		return 0, false
	}

	// Проверяем TTL
	if time.Now().After(cached.Expires) {
		return 0, false
	}

	return cached.Value, true
}

// getFallbackValue возвращает fallback значение на основе стратегии
func (hc *HealthCalculator) getFallbackValue(metricName string, metric Metric) float64 {
	hc.mutex.RLock()
	defer hc.mutex.RUnlock()

	// Защита от nil в тестах
	if hc.fallbackUsed != nil {
		hc.fallbackUsed.Inc()
	}

	switch hc.config.GracefulDeg.FallbackStrategy {
	case FallbackStrategyZero:
		log.Printf("Using zero fallback for metric %s", metricName)
		return 0
	case FallbackStrategyNeutral:
		log.Printf("Using neutral fallback (0.5) for metric %s", metricName)
		return 0.5
	case FallbackStrategyLast:
		if cachedValue, exists := hc.getCachedValue(metricName); exists {
			// getCachedValue уже проверил TTL
			log.Printf("Using last known value %.4f for metric %s",
				cachedValue, metricName)
			return cachedValue
		}
		log.Printf("No valid cached value for metric %s, using neutral fallback", metricName)
		return 0.5
	case FallbackStrategyAverage:
		// Возвращаем среднюю точку диапазона
		avg := (metric.MinValue + metric.MaxValue) / 2
		log.Printf("Using average fallback %.4f for metric %s", avg, metricName)
		// Нормализуем к диапазону 0-1
		rangeSize := metric.MaxValue - metric.MinValue
		if rangeSize == 0 {
			return 1.0
		}
		return (avg - metric.MinValue) / rangeSize
	default:
		log.Printf("Unknown fallback strategy, using neutral for metric %s", metricName)
		return 0.5
	}
}

// cleanupExpiredCache удаляет просроченные значения из кэша
func (hc *HealthCalculator) cleanupExpiredCache() {
	hc.mutex.Lock()
	defer hc.mutex.Unlock()

	now := time.Now()
	for name, cached := range hc.cachedValues {
		if now.After(cached.Expires) {
			delete(hc.cachedValues, name)
		}
	}
}

// queryPrometheusWithRetry выполняет запрос через circuit breaker и с ретраями
func (hc *HealthCalculator) queryPrometheusWithRetry(query string, metricName string) (float64, error) {
	// Используем circuit breaker для защиты от каскадных сбоев
	var result float64
	var err error

	cbErr := hc.circuitBreaker.Execute(func() error {
		var lastErr error
		maxRetries := 3

		for i := 0; i < maxRetries; i++ {
			value, queryErr := hc.queryPrometheus(query)
			if queryErr == nil {
				result = value
				hc.prometheusDownCount = 0 // Сбрасываем счетчик при успехе
				hc.metricsFetched.Inc()
				return nil // Успех
			}

			lastErr = queryErr
			hc.metricsFailed.Inc()
			log.Printf("Retry %d/%d for metric %s failed: %v", i+1, maxRetries, metricName, queryErr)

			// Exponential backoff: 1s, 2s, 3s
			time.Sleep(time.Duration(i+1) * time.Second)
		}

		err = lastErr

		// Все ретраи провалились
		hc.prometheusDownCount++
		if hc.config != nil && hc.prometheusDownCount >= hc.config.Alerting.PrometheusUnavailableThreshold {
			hc.sendAlert(context.Background(), fmt.Sprintf("🚨 Prometheus unavailable after %d attempts. Last error: %v",
				hc.prometheusDownCount, lastErr))
		}

		return fmt.Errorf("all retries failed: %v", lastErr)
	})

	if cbErr == ErrCircuitBreakerOpen {
		// Circuit breaker открыт - возвращаем дефолтное значение
		log.Printf("Circuit breaker is open, using fallback value for metric %s", metricName)
		return 0.5, nil // Возвращаем нейтральное значение 0.5
	}

	if err != nil {
		return 0, err
	}

	return result, nil
}

// sendAlert отправляет уведомление в Telegram
func (hc *HealthCalculator) sendAlert(ctx context.Context, message string) {
	logger := hc.logger.WithContextFields(ctx, SourceAlerting)

	if hc.config == nil || hc.config.Alerting.Telegram.BotToken == "" || hc.config.Alerting.Telegram.ChatID == "" {
		logger.WithField("message", message).Warn("ALERT would be sent - no Telegram credentials configured")
		return
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage",
		hc.config.Alerting.Telegram.BotToken)

	payload := map[string]string{
		"chat_id": hc.config.Alerting.Telegram.ChatID,
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
		return metric.MinValue
	}
	if value > metric.MaxValue {
		return metric.MaxValue
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
	hc.cleanupExpiredCache()

	totalScore := 0.0
	validMetrics := 0
	degradedMetrics := 0
	var cacheTTL time.Duration

	// Определяем TTL кэша из конфига
	if hc.config != nil && hc.config.GracefulDeg.EnableCache {
		var err error
		cacheTTL, err = time.ParseDuration(hc.config.GracefulDeg.CacheTTL)
		if err != nil {
			log.Printf("Invalid cache TTL, using default 5m: %v", err)
			cacheTTL = 5 * time.Minute
		}
	}

	for _, metric := range hc.config.Metrics {
		var normalizedValue float64
		var value float64
		var err error
		var usedFallback bool

		// Пытаемся получить значение из кэша
		if cachedValue, exists := hc.getCachedValue(metric.Name); exists && hc.config.GracefulDeg.EnableCache {
			value = cachedValue
			log.Printf("Using cached value for metric %s: %.4f", metric.Name, cachedValue)
		} else {
			// Запрашиваем свежее значение
			value, err = hc.queryPrometheusWithRetry(metric.Query, metric.Name)

			if err != nil {
				log.Printf("Failed to get metric %s, using fallback: %v", metric.Name, err)
				value = hc.getFallbackValue(metric.Name, metric)
				usedFallback = true
				degradedMetrics++
			} else {
				// Кэшируем успешное значение
				if hc.config.GracefulDeg.EnableCache {
					hc.cacheValue(metric.Name, value, cacheTTL)
				}
			}
		}

		normalizedValue = hc.normalizeValue(value, metric)
		hc.metricValues[metric.Name] = normalizedValue

		totalScore += normalizedValue * metric.Weight
		validMetrics++

		if usedFallback {
			log.Printf("Metric %s used fallback value: %.4f (normalized: %.4f)",
				metric.Name, value, normalizedValue)
		}
	}

	// Calculate degradation factor
	degradationFactor := 1.0
	if degradedMetrics > 0 {
		// Чем больше метрик используют fallback, тем больше снижение
		degradationFactor = 1.0 - (float64(degradedMetrics) / float64(len(hc.config.Metrics)) * 0.3)
		hc.logger.WithModule(ctx, SourceCalculator, "score_calc").Infof(
			"Degradation: %d/%d metrics using fallback, factor: %.2f",
			degradedMetrics, len(hc.config.Metrics), degradationFactor,
		)
	}

	// Применяем фактор деградации
	finalScore := totalScore * degradationFactor

	// Обновляем метрику degraded mode и флаг
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

	log.Printf("Health score updated: %.4f (from %d metrics, %d degraded, factor %.2f, took %v)",
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
	json.NewEncoder(w).Encode(response)
}

// wrapWithRateLimit оборачивает handler в rate limiting middleware
func (hc *HealthCalculator) wrapWithRateLimit(handler http.HandlerFunc) http.HandlerFunc {
	metrics := &RateLimitMetrics{
		rateLimitExceeded: hc.rateLimitExceeded,
		activeClients:     hc.activeClients,
	}
	return RateLimitMiddleware(hc.rateLimiter, metrics, handler)
}

// healthHandler - HTTP handler для health checks
func (hc *HealthCalculator) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx := ContextWithRequestID(context.Background(), GenerateRequestID())

	hc.mutex.RLock()
	lastUpdate := time.Since(hc.lastSuccessfulCalculation)
	isDegraded := hc.isDegraded
	circuitOpen := hc.circuitBreaker.State() == StateOpen
	hc.mutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	status := "healthy"
	statusCode := http.StatusOK
	response := map[string]interface{}{
		"status":                      status,
		"last_successful_calculation": hc.lastSuccessfulCalculation.Format(time.RFC3339),
		"age":                         lastUpdate.String(),
		"degraded":                    isDegraded,
		"circuit_breaker": map[string]interface{}{
			"state": func() string {
				switch hc.circuitBreaker.State() {
				case StateClosed:
					return "closed"
				case StateOpen:
					return "open"
				case StateHalfOpen:
					return "half-open"
				default:
					return "unknown"
				}
			}(),
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
	json.NewEncoder(w).Encode(response)
}

// Start запускает основной цикл работы сервиса
func (hc *HealthCalculator) Start(ctx context.Context) error {
	// Загружаем конфиг при старте
	if err := hc.loadConfig("health-config.yaml"); err != nil {
		return fmt.Errorf("failed to load initial config: %v", err)
	}

	// Запускаем фоновое обновление конфига
	go hc.watchConfig(ctx)

	// Запускаем очистку rate limiter buckets
	go hc.cleanupRateLimitBuckets(ctx)

	// Парсим интервал обновления из конфига
	interval, err := time.ParseDuration(hc.config.UpdateInterval)
	if err != nil {
		log.Printf("Invalid update interval, using default 5m: %v", err)
		interval = 5 * time.Minute
	}

	log.Printf("Starting health calculation loop with interval: %v", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Выполняем первый расчет сразу при старте
	hc.calculateHealthScore()

	// Основной цикл
	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down health calculator gracefully")
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
			if err := hc.loadConfig("health-config.yaml"); err != nil {
				log.Printf("Failed to reload config: %v", err)
			}
		}
	}
}

func main() {
	calculator := NewHealthCalculator()
	calculator.logger.Info("Starting Health Calculator Service...")

	// Настраиваем HTTP сервер
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler()) // Prometheus metrics endpoint (no rate limit)
	mux.HandleFunc("/health", calculator.wrapWithRateLimit(calculator.healthHandler))
	mux.HandleFunc("/circuit-breaker", calculator.wrapWithRateLimit(calculator.circuitBreakerHandler))
	mux.HandleFunc("/", calculator.wrapWithRateLimit(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Health Calculator Service"))
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Health Calculator Service"))
	})

	server := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Запускаем HTTP сервер в горутине
	go func() {
		log.Printf("Starting HTTP server on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Настраиваем graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Обработка сигналов OS для graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Запускаем основной сервис в горутине
	serviceErr := make(chan error, 1)
	go func() {
		serviceErr <- calculator.Start(ctx)
	}()

	// Ожидаем сигнал завершения или ошибку сервиса
	select {
	case sig := <-sigChan:
		log.Printf("Received signal: %v, shutting down...", sig)
		cancel()
	case err := <-serviceErr:
		log.Printf("Service error: %v, shutting down...", err)
		cancel()
	}

	// Graceful shutdown HTTP сервера
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("Health Calculator Service stopped gracefully")
}
