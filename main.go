package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// configPath returns the path to the YAML configuration file. Honors the
// CONFIG_PATH environment variable; falls back to "health-config.yaml".
func configPath() string {
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		return p
	}
	return "health-config.yaml"
}

func main() {
	calculator := NewHealthCalculator()
	calculator.logger.Info("Starting Health Calculator Service...")

	mux := buildMux(calculator)

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

// buildMux registers all HTTP routes. Returns the assembled ServeMux.
func buildMux(hc *HealthCalculator) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", hc.httpMetricsMiddleware(promhttp.Handler().ServeHTTP))
	mux.HandleFunc("/health", hc.recoveryMiddleware(hc.httpMetricsMiddleware(hc.wrapWithRateLimit(hc.healthHandler))))
	mux.HandleFunc("/ready", hc.recoveryMiddleware(hc.httpMetricsMiddleware(hc.readyHandler)))
	mux.HandleFunc("/circuit-breaker", hc.recoveryMiddleware(hc.httpMetricsMiddleware(hc.wrapWithRateLimit(hc.circuitBreakerHandler))))
	mux.HandleFunc("/", hc.recoveryMiddleware(hc.httpMetricsMiddleware(hc.wrapWithRateLimit(rootHandler))))
	return mux
}

// rootHandler is the trivial "/" handler returning a status string.
func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Health Calculator Service"))
}
