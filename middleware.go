package main

import (
	"net/http"
	"strconv"
	"time"
)

// statusRecorder wraps http.ResponseWriter to capture the actual status
// code written by handlers, used by httpMetricsMiddleware.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// recoveryMiddleware catches panics in HTTP handlers and returns a 500
// response, logging the panic for diagnostics. Prevents one bad request
// from killing the process.
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

// wrapWithRateLimit applies rate limiting to a handler using the configured
// RateLimiter. Records the rate_limit_exceeded counter and active client
// gauge.
func (hc *HealthCalculator) wrapWithRateLimit(handler http.HandlerFunc) http.HandlerFunc {
	metrics := &RateLimitMetrics{
		rateLimitExceeded: hc.rateLimitExceeded,
		activeClients:     hc.activeClients,
	}
	return RateLimitMiddleware(hc.rateLimiter, metrics, hc.logger, handler)
}

// httpMetricsMiddleware records RED metrics (request count + duration) for
// every HTTP request. Normalizes the URL path to a known endpoint label to
// prevent label cardinality explosion.
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
