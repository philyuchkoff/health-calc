> [Русская версия](ru/05-operations.md)

# Production Operations

## Table of Contents

- [Kubernetes deployment](#kubernetes-deployment)
  - [Deployment manifest](#deployment-manifest)
  - [Secrets](#secrets)
  - [Probes](#probes)
- [Monitoring](#monitoring)
  - [Prometheus rules](#prometheus-rules)
  - [Grafana dashboard](#grafana-dashboard)
- [Common scenarios](#common-scenarios)
  - [Prometheus is unreachable](#prometheus-is-unreachable)
  - [Partial metric degradation](#partial-metric-degradation)
  - [Config reload](#config-reload)
  - [Rollback](#rollback)
- [Capacity planning](#capacity-planning)
- [Security](#security)

---

## Kubernetes deployment

Ready-to-use manifests are in the [`k8s/`](../k8s/) directory:

```bash
kubectl apply -f k8s/
```

### Deployment

See [k8s/deployment.yaml](../k8s/deployment.yaml). Key details:
- 2 replicas with liveness and readiness probes
- Config mounted from ConfigMap (read-only)
- Telegram credentials via Kubernetes Secrets
- Resource requests: 100m CPU / 64Mi memory

### Service

See [k8s/service.yaml](../k8s/service.yaml). Exposes port 80 → 8080.

### Secrets

Telegram credentials are stored in Kubernetes Secrets:

```bash
kubectl create secret generic telegram-secrets \
  --from-literal=bot-token='123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11' \
  --from-literal=chat-id='-1001234567890'
```

### Probes

| Probe | Path | `initialDelaySeconds` | `periodSeconds` | When it returns failure |
|-------|------|-----------------------|-----------------|--------------------------|
| Liveness | `/health` | 30 | 10 | If no calculation has been performed for > 10 minutes |
| Readiness | `/ready` | 5 | 5 | Until the first calculation is complete |

**Important:** Readiness probe has a smaller `initialDelaySeconds` than Liveness. This ensures the pod does not receive traffic until it is ready. Liveness probe starts checking later to allow time for the first metric calculation.

### ConfigMap

Configuration is stored in a ConfigMap. See [k8s/configmap.yaml](../k8s/configmap.yaml):

```bash
kubectl apply -f k8s/configmap.yaml
```

When the ConfigMap changes, pods need to be restarted (rolling restart):

```bash
kubectl rollout restart deployment/health-calculator
```

---

## Monitoring

### Prometheus rules

Recommended set of alerts for the health-calculator itself:

```yaml
groups:
  - name: health-calculator
    interval: 30s

    rules:
      - alert: HealthCalculatorDown
        expr: up{job="health-calculator"} == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Health Calculator is down"

      - alert: HealthScoreLow
        expr: platform_health_score < 0.7
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Platform health score is below 0.7"

      - alert: HealthScoreWarn
        expr: platform_health_score < 0.9
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Platform health score is below 0.9"

      - alert: ServiceDegraded
        expr: health_calculator_degraded_mode == 1
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Health Calculator is running in degraded mode"

      - alert: CircuitBreakerOpen
        expr: health_calculator_circuit_breaker_tripped_total > 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Circuit breaker is open — Prometheus unreachable"

      - alert: PrometheusConnectionErrors
        expr: rate(health_calculator_prometheus_connection_errors_total[5m]) > 1
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "High rate of Prometheus connection errors"

      - alert: ConfigReloadFailing
        expr: rate(health_calculator_config_reload_total[5m]) < 1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Config reload may be failing"
```

### Grafana dashboard

Main dashboard variables:

- `${service}` — `health-calculator`
- `${interval}` — `5m`

Dashboard panels:

**Row 1: Overview**
- `platform_health_score` — singlestat (current value) + graph (7 days)
- `health_calculator_degraded_mode` — states timeline

**Row 2: Performance**
- `rate(health_calculator_calculation_duration_seconds_sum[5m]) / rate(health_calculator_calculation_duration_seconds_count[5m])` — average calculation time
- `health_calculator_metrics_fetched_total - health_calculator_metrics_failed_total` — successful vs failed

**Row 3: Resilience**
- `health_calculator_circuit_breaker_tripped_total` — how many times CB tripped
- `health_calculator_fallback_used_total` — fallback counter

**Row 4: Rate Limiting**
- `rate(health_calculator_rate_limit_exceeded_total[5m])` — blocked RPS
- `health_calculator_active_rate_limit_clients` — active clients

**Row 5: Service Health**
- `service_uptime_seconds` — uptime
- `rate(health_calculator_prometheus_connection_errors_total[5m])` — connection errors

---

## Common scenarios

### Prometheus is unreachable

**What happens:**
1. Requests to Prometheus start failing with errors
2. Circuit breaker counts the errors
3. After `max_failures` (default 3) the CB opens
4. Metrics start using fallback values
5. The service enters degraded mode
6. If `prometheus_unavailable_alert_threshold` is exceeded — a Telegram alert is sent
7. After Prometheus recovers, the CB transitions to Half-Open, verifies, and closes

**How to distinguish a temporary outage from a failure:**
- Temporary outage: `metrics_failed` grows, `fallback_used_total` does not grow (CB is still closed)
- Failure: `circuit_breaker_tripped_total` increased, `degraded_mode = 1`

**What to do:**
1. Check Prometheus availability: `curl http://prometheus:9090/-/healthy`
2. Check logs: `kubectl logs -l app=health-calculator | grep prometheus`
3. If Prometheus is restarting — wait for recovery
4. If the issue is at the network level — check NetworkPolicy / DNS

### Partial metric degradation

**Scenario:** 2 out of 4 metrics are unavailable

```
degradation_factor = 1.0 - (2/4 * 0.3) = 0.85
```

The final score is multiplied by `0.85`. The service continues to operate.

**When this is normal:**
- Individual metrics are temporarily unavailable
- Score remains within SLO

**When action is needed:**
- `degraded_mode = 1` for more than 10 minutes
- More than 50% of metrics are in fallback
- Score has dropped below SLO

### Config reload

The config is re-read every minute. Upon change:

1. The file `health-config.yaml` is read
2. YAML is parsed
3. Validated (metric weights, time format, strategies)
4. Under `mutex` lock, the following are updated: `config`, `circuit_breaker`, `logger`, `rate_limiter`
5. Success or error is logged

If the new config is invalid — the service continues working with the old one.

### Rollback

```bash
# Kubernetes — rollback the deployment
kubectl rollout undo deployment/health-calculator

# Docker — run the previous image
docker run -d --name health-calculator-old \
  your-registry/health-calculator:previous-tag

# Locally — run the previous binary
./health-calculator.bak
```

---

## Capacity planning

**Resource usage (reference):**

| Metric | Value |
|--------|-------|
| CPU (idle) | < 1% |
| CPU (calculation) | ~50ms per cycle |
| Memory (baseline) | ~10 MB |
| Memory (1000 rate limit buckets) | ~15 MB |
| Startup time | < 1s |
| Image size | ~16 MB |

**When to scale up resources:**
- > 20 metrics in config
- > 10000 unique IPs per minute (rate limiting)
- Prometheus latency > 5s (increase timeout)

---

## Security

- Config is mounted read-only in the container
- Telegram credentials via Kubernetes Secrets (not in config)
- Rate limiting protects against DDoS
- Timeout on all external HTTP requests
- Circuit breaker prevents cascading failures

---

| Previous | Next |
|----------|------|
| [Startup and API](04-usage.md) | [Troubleshooting](06-troubleshooting.md) |
