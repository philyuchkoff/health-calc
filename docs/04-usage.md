> [Русская версия](ru/04-usage.md)

# Running and API

## Contents

- [Running](#running)
- [Endpoints](#endpoints)
  - [`GET /metrics` — Prometheus metrics](#get-metrics--prometheus-metrics)
  - [`GET /health` — Liveness probe](#get-health--liveness-probe)
  - [`GET /ready` — Readiness probe](#get-ready--readiness-probe)
  - [`GET /circuit-breaker` — CB status](#get-circuit-breaker--cb-status)
  - [`GET /` — Status page](#get----status-page)
- [Usage examples](#usage-examples)
- [Graceful shutdown](#graceful-shutdown)
- [Logging](#logging)

---

## Running

```bash
# Locally (dev)
go run .

# Built binary
./health-calculator

# Docker
docker run -p 8080:8080 -v $(pwd)/health-config.yaml:/root/health-config.yaml:ro health-calculator
```

Verify the service is running:

```bash
curl -s http://localhost:8080/health | jq .
```

Expected response:

```json
{
  "status": "healthy",
  "last_successful_calculation": "2026-06-09T12:00:00Z",
  "age": "2.5s",
  "degraded": false,
  "circuit_breaker": {
    "state": "closed"
  }
}
```

## Endpoints

### `GET /metrics` — Prometheus metrics

Standard Prometheus endpoint. No rate limiting.

```bash
curl -s http://localhost:8080/metrics | grep platform_health_score
```

Main service metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `platform_health_score` | Gauge | Final health score (`0.0 – 1.0`) |
| `health_calculator_metrics_fetched_total` | Counter | Successful Prometheus requests |
| `health_calculator_metrics_failed_total` | Counter | Failed Prometheus requests |
| `health_calculator_calculation_duration_seconds` | Histogram | Calculation time |
| `health_calculator_circuit_breaker_tripped_total` | Counter | Number of times CB opened |
| `health_calculator_degraded_mode` | Gauge | `1` if service is in degraded mode |
| `health_calculator_fallback_used_total` | Counter | Number of times fallback was used |
| `health_calculator_rate_limit_exceeded_total` | Counter | Blocked requests |
| `health_calculator_active_rate_limit_clients` | Gauge | Active rate limiter clients |
| `health_calculator_config_reload_total` | Counter | Config reload attempts |
| `service_uptime_seconds` | Gauge | Service uptime |
| `health_calculator_prometheus_connection_errors_total` | Counter | Prometheus connection errors |

### `GET /health` — Liveness probe

Used by Kubernetes to check if the service is alive (liveness probe).

```bash
curl http://localhost:8080/health
```

**Responses:**

```json
// ✅ Service healthy — HTTP 200
{
  "status": "healthy",
  "last_successful_calculation": "2026-06-09T12:00:00Z",
  "age": "2.5s",
  "degraded": false,
  "circuit_breaker": { "state": "closed" }
}

// ⚠️ Some metrics are using fallback — HTTP 200
{
  "status": "degraded",
  "reason": "some metrics are using fallback values",
  "degraded": true,
  ...
}

// 🔴 Circuit breaker is open — HTTP 200
{
  "status": "degraded",
  "reason": "circuit breaker is open",
  ...
}

// 🔴 No recent calculation — HTTP 503
{
  "status": "unhealthy",
  "reason": "last calculation too old: 15m0s",
  ...
}
```

**Status determination logic:**

```
if last_calculation > 10 minutes ago → status = "unhealthy" (503)
else if degraded → status = "degraded"
else if circuit breaker open → status = "degraded"
else → status = "healthy" (200)
```

### `GET /ready` — Readiness probe

Used by Kubernetes to check if the service is ready to accept traffic (readiness probe).

```bash
curl http://localhost:8080/ready
```

Differences from `/health`:
- Returns `503` until config is loaded and the first calculation is performed
- Returns `503` if the last calculation was more than 10 minutes ago
- Returns `200` even in degraded mode (service is working, though not at full capacity)

```json
// ✅ Ready — HTTP 200
{ "status": "ready" }

// ⚠️ Ready but degraded — HTTP 200
{ "status": "ready_degraded" }

// 🔴 Not ready — HTTP 503
{ "status": "not_ready", "reason": "service has not completed initial startup" }
```

### `GET /circuit-breaker` — CB status

Shows the current state of the circuit breaker:

```bash
curl http://localhost:8080/circuit-breaker
```

```json
{
  "name": "prometheus",
  "state": "closed",
  "failures": 0
}
```

The `state` field can be: `closed`, `open`, `half-open`.

Useful for monitoring and debugging — you can alert on `state == "open"`.

### `GET /` — Status page

Returns simple text:

```
Health Calculator Service
```

Used for basic health check that the server is responding.

---

## Usage examples

### Monitoring in Grafana

PromQL queries for a dashboard:

```promql
# Current health score
platform_health_score

# Trend over the last 7 days
avg_over_time(platform_health_score[7d])

# Service in degraded mode
health_calculator_degraded_mode

# Prometheus error rate
rate(health_calculator_metrics_failed_total[5m])
```

### Alerts in Prometheus

```yaml
groups:
  - name: health-calc
    rules:
      - alert: HealthScoreDegraded
        expr: platform_health_score < 0.8
        for: 5m
        labels:
          severity: warning

      - alert: ServiceDegraded
        expr: health_calculator_degraded_mode == 1
        for: 2m
        labels:
          severity: warning

      - alert: CircuitBreakerOpen
        expr: health_calculator_circuit_breaker_tripped_total > 0
        for: 1m
        labels:
          severity: critical
```

---

## Graceful shutdown

The service handles `SIGINT` and `SIGTERM` signals correctly:

```
1. Signal received (SIGINT / SIGTERM)
2. Context cancelled
3. Ticker stopped
4. HTTP server finishes pending requests (10 second timeout)
5. Service exits
```

Logs on shutdown:

```
Received signal: interrupt, shutting down...
Shutting down health calculator gracefully
Health Calculator Service stopped gracefully
```

---

## Logging

Logs are output as JSON (in production) or text (in dev):

```json
{"level":"info","service":"health-calculator","source":"calculator","message":"Health score updated: 0.8943 (from 4 metrics, 0 degraded, factor 1.00, took 450ms)","time":"2026-06-09T12:00:00Z"}

{"level":"warn","service":"health-calculator","source":"prometheus","message":"Retry 1/3 for metric api_success_rate failed: prometheus returned status: 503","time":"2026-06-09T12:00:01Z"}
```

All logs contain the following fields:
- `time` — timestamp (RFC3339)
- `level` — level: `debug`, `info`, `warn`, `error`
- `service` — service name
- `source` — module: `prometheus`, `calculator`, `config`, `alerting`, `rate_limit`, `http`
- `message` — log message
- `request_id` — (optional) request ID for tracing

---

| Previous | Next |
|----------|------|
| [Configuration](03-configuration.md) | [Production operations](05-operations.md) |
