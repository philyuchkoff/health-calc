package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// RateLimiter реализует rate limiting с помощью leaky bucket алгоритма
type RateLimiter struct {
	clients map[string]*Bucket
	mutex   sync.RWMutex
	config  RateLimitConfig
}

// Bucket представляет leaky bucket для одного клиента
type Bucket struct {
	capacity   int
	tokens     float64
	refillRate float64
	lastRefill time.Time
	mutex      sync.Mutex
}

// RateLimitConfig конфигурация rate limiting
type RateLimitConfig struct {
	Enabled    bool              `yaml:"enabled"`
	GlobalRate map[string]string `yaml:"global_rate"` // endpoint -> rate
	PerIPRate  map[string]string `yaml:"per_ip_rate"` // endpoint -> rate per IP
	Whitelist  []string          `yaml:"whitelist"`   // IP whitelist
}

// NewRateLimiter создает новый rate limiter
func NewRateLimiter(config RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		clients: make(map[string]*Bucket),
		config:  config,
	}
}

// ParseRate парсит строку Rate в формате "requests/period"
func ParseRate(rateStr string) (requests int, period time.Duration, err error) {
	parts := strings.Split(rateStr, "/")
	if len(parts) != 2 {
		return 0, 0, ErrInvalidRateFormat
	}

	requests, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}

	switch parts[1] {
	case "s", "sec", "second":
		period = time.Second
	case "m", "min", "minute":
		period = time.Minute
	case "h", "hour":
		period = time.Hour
	default:
		return 0, 0, ErrInvalidRateFormat
	}

	return requests, period, nil
}

// RateLimitDecision описывает результат проверки rate limiter
type RateLimitDecision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	ResetAfter time.Duration
}

// AllowNext проверяет, разрешен ли следующий запрос
func (b *Bucket) AllowNext() RateLimitDecision {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	now := time.Now()

	elapsed := now.Sub(b.lastRefill)
	tokensToAdd := float64(elapsed.Milliseconds()) * b.refillRate / 1000

	if tokensToAdd > 0 {
		b.tokens = min(float64(b.capacity), b.tokens+tokensToAdd)
		b.lastRefill = now
	}

	if b.tokens >= 1 {
		b.tokens--
		tokensNeeded := 1.0 - b.tokens
		if tokensNeeded < 0 {
			tokensNeeded = 0
		}
		resetAfter := time.Duration(tokensNeeded / b.refillRate * float64(time.Second))
		return RateLimitDecision{
			Allowed:    true,
			Remaining:  int(b.tokens),
			ResetAfter: resetAfter,
		}
	}

	tokensNeeded := 1.0 - b.tokens
	if tokensNeeded < 0 {
		tokensNeeded = 0
	}
	resetAfter := time.Duration(tokensNeeded / b.refillRate * float64(time.Second))
	return RateLimitDecision{
		Allowed:    false,
		Remaining:  0,
		ResetAfter: resetAfter,
	}
}

// Capacity возвращает ёмкость bucket
func (b *Bucket) Capacity() int {
	return b.capacity
}

// GetOrCreateBucket получает или создает bucket для клиента
func (rl *RateLimiter) GetOrCreateBucket(clientKey string, requests int, period time.Duration) *Bucket {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	bucket, exists := rl.clients[clientKey]
	if !exists {
		bucket = &Bucket{
			capacity:   requests,
			tokens:     float64(requests),
			refillRate: float64(requests) / period.Seconds(),
			lastRefill: time.Now(),
		}
		rl.clients[clientKey] = bucket
	}

	return bucket
}

// IsAllowed проверяет, разрешен ли запрос. Возвращает (разрешено, лимит, осталось).
func (rl *RateLimiter) IsAllowed(r *http.Request, endpoint string) RateLimitDecision {
	if !rl.config.Enabled {
		return RateLimitDecision{Allowed: true}
	}

	if endpoint == "/metrics" {
		return RateLimitDecision{Allowed: true}
	}

	// Check whitelist
	clientIP := GetClientIP(r)
	for _, ip := range rl.config.Whitelist {
		if ip == clientIP {
			return RateLimitDecision{Allowed: true}
		}
	}

	finalDecision := RateLimitDecision{Allowed: true}

	// Check per-IP rate limit first
	if rateStr, exists := rl.config.PerIPRate[endpoint]; exists {
		requests, period, err := ParseRate(rateStr)
		if err != nil {
			return RateLimitDecision{Allowed: true}
		}

		clientKey := clientIP + ":" + endpoint
		bucket := rl.GetOrCreateBucket(clientKey, requests, period)
		decision := bucket.AllowNext()
		decision.Limit = requests
		finalDecision = decision
		if !decision.Allowed {
			return decision
		}
	}

	// Check global rate limit
	if rateStr, exists := rl.config.GlobalRate[endpoint]; exists {
		requests, period, err := ParseRate(rateStr)
		if err != nil {
			return finalDecision
		}

		clientKey := "global:" + endpoint
		bucket := rl.GetOrCreateBucket(clientKey, requests, period)
		decision := bucket.AllowNext()
		decision.Limit = requests
		finalDecision = decision
		if !decision.Allowed {
			return decision
		}
	}

	return finalDecision
}

// CleanupExpiredBuckets удаляет неиспользуемые bucket'ы
func (rl *RateLimiter) CleanupExpiredBuckets() {
	rl.mutex.Lock()
	defer rl.mutex.Unlock()

	now := time.Now()
	for key, bucket := range rl.clients {
		bucket.mutex.Lock()
		// Удаляем bucket'ы, не используемые более 5 минут
		if now.Sub(bucket.lastRefill) > 5*time.Minute {
			delete(rl.clients, key)
		}
		bucket.mutex.Unlock()
	}
}

// GetClientIP получает IP адрес клиента из запроса
func GetClientIP(r *http.Request) string {
	// Проверяем X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Берем первый IP из списка
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Проверяем X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Используем RemoteAddr
	if idx := strings.LastIndex(r.RemoteAddr, ":"); idx != -1 {
		return r.RemoteAddr[:idx]
	}

	return r.RemoteAddr
}

// Errors
var (
	ErrInvalidRateFormat = &RateLimitError{
		Type:    "invalid_format",
		Message: "Invalid rate format. Use format like '100/m' or '10/s'",
	}
)

type RateLimitError struct {
	Type    string
	Message string
}

func (e *RateLimitError) Error() string {
	return e.Message
}

// RateLimitMiddleware создает middleware для rate limiting с метриками
func RateLimitMiddleware(rl *RateLimiter, metrics *RateLimitMetrics, logger *Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		endpoint := r.URL.Path

		// Update active clients metric if available
		if metrics != nil {
			rl.mutex.RLock()
			activeCount := len(rl.clients)
			rl.mutex.RUnlock()
			metrics.activeClients.Set(float64(activeCount))
		}

		decision := rl.IsAllowed(r, endpoint)

		if !decision.Allowed {
			// Increment rate limit exceeded metric if available
			if metrics != nil {
				metrics.rateLimitExceeded.Inc()
			}

			// Log rate limit violation
			if logger != nil {
				logger.WithContextFields(context.Background(), SourceRateLimit).
					RateLimitViolation(GetClientIP(r), endpoint)
			}

			// Return 429 Too Many Requests
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(decision.Limit))
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(decision.ResetAfter).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)

			response := map[string]interface{}{
				"error":    "rate_limit_exceeded",
				"message":  "Too many requests. Please try again later.",
				"endpoint": endpoint,
			}
			_ = json.NewEncoder(w).Encode(response)
			return
		}

		// Add rate limit headers
		if decision.Limit > 0 {
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(decision.Limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
			if decision.ResetAfter > 0 {
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(decision.ResetAfter).Unix(), 10))
			}
		}

		next(w, r)
	}
}

// RateLimitMetrics хранит метрики для rate limiting
type RateLimitMetrics struct {
	rateLimitExceeded prometheus.Counter
	activeClients     prometheus.Gauge
}
