package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Config structures
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

// Prometheus response structures
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

type HealthCalculator struct {
	config              *Config
	healthScore         prometheus.Gauge
	metricValues        map[string]float64
	mutex               sync.RWMutex
	prometheusDownCount int
	httpClient          *http.Client
}

func NewHealthCalculator() *HealthCalculator {
	healthScore := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "platform_health_score",
		Help: "Overall platform health score (0.0 - 1.0)",
	})
	prometheus.MustRegister(healthScore)

	return &HealthCalculator{
		healthScore:  healthScore,
		metricValues: make(map[string]float64),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (hc *HealthCalculator) loadConfig(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %v", err)
	}

	// Replace environment variables
	expanded := os.ExpandEnv(string(data))

	var config Config
	if err := yaml.Unmarshal([]byte(expanded), &config); err != nil {
		return fmt.Errorf("failed to parse config: %v", err)
	}

	hc.config = &config
	timeout, _ := time.ParseDuration(config.Prometheus.Timeout)
	hc.httpClient.Timeout = timeout

	// Validate weights sum to 1.0
	totalWeight := 0.0
	for _, metric := range config.Metrics {
		totalWeight += metric.Weight
	}

	if math.Abs(totalWeight-1.0) > 0.001 {
		return fmt.Errorf("metric weights must sum to 1.0, got: %f", totalWeight)
	}

	return nil
}

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

	// Extract the value (timestamp, value)
	value := result.Data.Result[0].Value[1]
	switch v := value.(type) {
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, fmt.Errorf("unexpected value type: %T", value)
	}
}

func (hc *HealthCalculator) queryPrometheusWithRetry(query string, metricName string) (float64, error) {
	var lastErr error
	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		value, err := hc.queryPrometheus(query)
		if err == nil {
			hc.prometheusDownCount = 0 // Reset counter on success
			return value, nil
		}

		lastErr = err
		log.Printf("Retry %d/%d for metric %s failed: %v", i+1, maxRetries, metricName, err)
		time.Sleep(time.Duration(i+1) * time.Second) // Exponential backoff
	}

	// All retries failed
	hc.prometheusDownCount++
	if hc.prometheusDownCount >= hc.config.Alerting.PrometheusUnavailableThreshold {
		hc.sendAlert(fmt.Sprintf("🚨 Prometheus unavailable after %d attempts. Last error: %v",
			hc.prometheusDownCount, lastErr))
	}

	return 0, lastErr
}

func (hc *HealthCalculator) sendAlert(message string) {
	if hc.config.Alerting.Telegram.BotToken == "" || hc.config.Alerting.Telegram.ChatID == "" {
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

	resp, err := hc.httpClient.Post(url, "application/json",
		bytes.NewReader(jsonData))
	if err != nil {
		log.Printf("Failed to send Telegram alert: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Telegram API returned non-200 status: %d", resp.StatusCode)
	}
}

func (hc *HealthCalculator) normalizeValue(value float64, metric Metric) float64 {
	// Clamp value to valid range
	if value < metric.MinValue {
		return metric.MinValue
	}
	if value > metric.MaxValue {
		return metric.MaxValue
	}

	// Normalize to 0-1 range based on min/max
	rangeSize := metric.MaxValue - metric.MinValue
	if rangeSize == 0 {
		return 1.0
	}

	return (value - metric.MinValue) / rangeSize
}

func (hc *HealthCalculator) calculateHealthScore() {
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

	// If some metrics are missing, adjust score proportionally
	if validMetrics < len(hc.config.Metrics) {
		adjustment := float64(validMetrics) / float64(len(hc.config.Metrics))
		totalScore *= adjustment
		log.Printf("Only %d/%d metrics available, adjusted score by factor %.2f",
			validMetrics, len(hc.config.Metrics), adjustment)
	}

	hc.healthScore.Set(totalScore)
	log.Printf("Health score updated: %.4f (from %d metrics)", totalScore, validMetrics)
}

func (hc *HealthCalculator) Start() error {
	// Initial config load
	if err := hc.loadConfig("health-config.yaml"); err != nil {
		return fmt.Errorf("failed to load config: %v", err)
	}

	// Start background config reloader
	go hc.watchConfig()

	// Start metric calculator
	interval, err := time.ParseDuration(hc.config.UpdateInterval)
	if err != nil {
		interval = 5 * time.Minutes // Default fallback
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hc.calculateHealthScore()
		}
	}
}

func (hc *HealthCalculator) watchConfig() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if err := hc.loadConfig("health-config.yaml"); err != nil {
			log.Printf("Failed to reload config: %v", err)
		}
	}
}

func main() {
	calculator := NewHealthCalculator()

	// Start metrics HTTP server
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	go func() {
		log.Printf("Starting metrics server on :8080")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Fatal(err)
		}
	}()

	if err := calculator.Start(); err != nil {
		log.Fatal(err)
	}
}
