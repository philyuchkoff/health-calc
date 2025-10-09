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
	UpdateInterval string           `yaml:"update_interval"`
	Metrics        []Metric         `yaml:"metrics"`
	Alerting       Alerting         `yaml:"alerting"`
	Prometheus     PrometheusConfig `yaml:"prometheus"`
}

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

	// Регистрируем все метрики в Prometheus registry
	prometheus.MustRegister(healthScore, metricsFetched, metricsFailed, calculationTime)

	return &HealthCalculator{
		healthScore:     healthScore,
		metricValues:    make(map[string]float64),
		metricsFetched:  metricsFetched,
		metricsFailed:   metricsFailed,
		calculationTime: calculationTime,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// loadConfig загружает и парсит конфигурационный файл
func (hc *HealthCalculator) loadConfig(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %v", err)
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
		log.Printf("Invalid timeout format, using default 30s: %v", err)
		timeout = 30 * time.Second
	}
	hc.httpClient.Timeout = timeout

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

// queryPrometheusWithRetry выполняет запрос с ретраями
func (hc *HealthCalculator) queryPrometheusWithRetry(query string, metricName string) (float64, error) {
	var lastErr error
	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		value, err := hc.queryPrometheus(query)
		if err == nil {
			hc.prometheusDownCount = 0 // Сбрасываем счетчик при успехе
			hc.metricsFetched.Inc()
			return value, nil
		}

		lastErr = err
		hc.metricsFailed.Inc()
		log.Printf("Retry %d/%d for metric %s failed: %v", i+1, maxRetries, metricName, err)

		// Exponential backoff: 1s, 2s, 3s
		time.Sleep(time.Duration(i+1) * time.Second)
	}

	// Все ретраи провалились
	hc.prometheusDownCount++
	if hc.config != nil && hc.prometheusDownCount >= hc.config.Alerting.PrometheusUnavailableThreshold {
		hc.sendAlert(fmt.Sprintf("🚨 Prometheus unavailable after %d attempts. Last error: %v",
			hc.prometheusDownCount, lastErr))
	}

	return 0, fmt.Errorf("all retries failed: %v", lastErr)
}

// sendAlert отправляет уведомление в Telegram
func (hc *HealthCalculator) sendAlert(message string) {
	if hc.config == nil || hc.config.Alerting.Telegram.BotToken == "" || hc.config.Alerting.Telegram.ChatID == "" {
		log.Printf("ALERT would be sent: %s", message)
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
		log.Printf("Failed to send Telegram alert: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Telegram API returned non-200 status: %d", resp.StatusCode)
	} else {
		log.Printf("Telegram alert sent successfully")
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

// calculateHealthScore - основная функция расчета health score
func (hc *HealthCalculator) calculateHealthScore() {
	startTime := time.Now()

	hc.mutex.Lock()
	defer hc.mutex.Unlock()

	totalScore := 0.0
	validMetrics := 0

	for _, metric := range hc.config.Metrics {
		value, err := hc.queryPrometheusWithRetry(metric.Query, metric.Name)
		if err != nil {
			log.Printf("Failed to get metric %s: %v", metric.Name, err)
			continue
		}

		normalizedValue := hc.normalizeValue(value, metric)
		hc.metricValues[metric.Name] = normalizedValue

		totalScore += normalizedValue * metric.Weight
		validMetrics++
	}

	if validMetrics == 0 {
		log.Printf("No valid metrics available, setting health score to 0")
		hc.healthScore.Set(0)
		return
	}

	// Если некоторые метрики недоступны, корректируем score пропорционально
	if validMetrics < len(hc.config.Metrics) {
		adjustment := float64(validMetrics) / float64(len(hc.config.Metrics))
		totalScore *= adjustment
		log.Printf("Only %d/%d metrics available, adjusted score by factor %.2f",
			validMetrics, len(hc.config.Metrics), adjustment)
	}

	hc.healthScore.Set(totalScore)
	hc.lastSuccessfulCalculation = time.Now()
	hc.calculationTime.Observe(time.Since(startTime).Seconds())

	log.Printf("Health score updated: %.4f (from %d/%d metrics, calculation took %v)",
		totalScore, validMetrics, len(hc.config.Metrics), time.Since(startTime))
}

// healthHandler - HTTP handler для health checks
func (hc *HealthCalculator) healthHandler(w http.ResponseWriter, r *http.Request) {
	hc.mutex.RLock()
	lastUpdate := time.Since(hc.lastSuccessfulCalculation)
	hc.mutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	// Если последний расчет был более 10 минут назад - считаем сервис unhealthy
	if lastUpdate > 10*time.Minute {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status":    "unhealthy",
			"reason":    fmt.Sprintf("last calculation too old: %v", lastUpdate),
			"timestamp": hc.lastSuccessfulCalculation.Format(time.RFC3339),
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":                      "healthy",
		"last_successful_calculation": hc.lastSuccessfulCalculation.Format(time.RFC3339),
		"age":                         lastUpdate.String(),
	})
}

// Start запускает основной цикл работы сервиса
func (hc *HealthCalculator) Start(ctx context.Context) error {
	// Загружаем конфиг при старте
	if err := hc.loadConfig("health-config.yaml"); err != nil {
		return fmt.Errorf("failed to load initial config: %v", err)
	}

	// Запускаем фоновое обновление конфига
	go hc.watchConfig(ctx)

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
	log.Println("Starting Health Calculator Service...")

	calculator := NewHealthCalculator()

	// Настраиваем HTTP сервер
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())          // Prometheus metrics endpoint
	mux.HandleFunc("/health", calculator.healthHandler) // Health check endpoint
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
