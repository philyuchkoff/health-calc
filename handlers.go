package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// circuitBreakerHandler reports the current state of the circuit breaker.
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

// healthHandler is the Kubernetes liveness probe endpoint.
// Returns 200 if the last successful calculation is within
// unhealthyThreshold, 503 otherwise. Reports degraded state when
// fallback values are in use or the circuit breaker is open.
func (hc *HealthCalculator) healthHandler(w http.ResponseWriter, r *http.Request) {
	hc.mutex.RLock()
	lastUpdate := time.Since(hc.calc.lastSuccessfulCalculation)
	lastCalcTime := hc.calc.lastSuccessfulCalculation
	isDegraded := hc.degradation.isDegraded
	unhealthyThreshold := hc.unhealthyThreshold
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

	if lastUpdate > unhealthyThreshold {
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

// readyHandler is the Kubernetes readiness probe endpoint.
// Returns 200 when the service has completed initial startup, 503
// otherwise. Reports ready_degraded when fallback values are in use.
func (hc *HealthCalculator) readyHandler(w http.ResponseWriter, r *http.Request) {
	hc.mutex.RLock()
	configLoaded := hc.config != nil
	hasCalculation := !hc.calc.lastSuccessfulCalculation.IsZero()
	lastUpdate := time.Since(hc.calc.lastSuccessfulCalculation)
	isDegraded := hc.degradation.isDegraded
	unhealthyThreshold := hc.unhealthyThreshold
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
	if lastUpdate > unhealthyThreshold {
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
