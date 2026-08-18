package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const maxPrometheusResponseSize = 10 * 1024 * 1024 // 10 MB

// PrometheusResponse mirrors the Prometheus /api/v1/query JSON response shape.
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

// queryPrometheus issues an instant query against the Prometheus API.
// Caller is responsible for retries and circuit-breaking (see queryPrometheusWithRetry).
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

	resp, err := hc.http.prometheus.Do(req)
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

// queryPrometheusWithRetry runs queryPrometheus with retries through the
// circuit breaker. On consecutive failures beyond alertThreshold, fires a
// Telegram alert.
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
				hc.calc.prometheusDownCount = 0
				hc.metrics.metricsFetched.Inc()
				return nil
			}

			lastErr = queryErr
			hc.metrics.metricsFailed.Inc()
			hc.metrics.prometheusConnErrors.Inc()
			hc.logger.WithContextFields(context.Background(), SourcePrometheus).
				Warnf("Retry %d/%d for metric %s failed: %v", i+1, maxRetries, metricName, queryErr)

			time.Sleep(time.Duration(i+1) * time.Second)
		}

		err = lastErr

		hc.calc.prometheusDownCount++
		if hc.calc.prometheusDownCount >= alertThreshold {
			hc.sendAlert(context.Background(),
				fmt.Sprintf("Prometheus unavailable after %d attempts. Last error: %v",
					hc.calc.prometheusDownCount, lastErr),
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

// sendAlert posts an alert message to the configured Telegram chat.
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

	resp, err := hc.http.telegram.Post(url, "application/json", bytes.NewReader(jsonData))
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

// normalizeValue maps a raw metric value to the [0, 1] range using the
// metric's configured MinValue/MaxValue bounds.
func (hc *HealthCalculator) normalizeValue(value float64, metric Metric) float64 {
	if value < metric.MinValue {
		return 0.0
	}
	if value > metric.MaxValue {
		return 1.0
	}

	rangeSize := metric.MaxValue - metric.MinValue
	if rangeSize == 0 {
		return 1.0
	}

	return (value - metric.MinValue) / rangeSize
}

// knownEndpoints is a whitelist of HTTP paths used as Prometheus labels to
// prevent unbounded label cardinality. Any unknown path is bucketed as
// "/other" to keep time-series count bounded.
var knownEndpoints = map[string]bool{
	"/metrics":         true,
	"/health":          true,
	"/ready":           true,
	"/circuit-breaker": true,
	"/":                true,
}

// normalizePath maps an arbitrary request path onto a known endpoint label
// or "/other" if the path is not in the whitelist.
func normalizePath(p string) string {
	if knownEndpoints[p] {
		return p
	}
	return "/other"
}
