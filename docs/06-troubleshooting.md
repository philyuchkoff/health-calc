> [Русская версия](ru/06-troubleshooting.md)

# Troubleshooting

## Table of Contents

- [Service won't start](#service-wont-start)
- [Health score not updating](#health-score-not-updating)
- [Rate limit triggers too often](#rate-limit-triggers-too-often)
- [Circuit breaker constantly open](#circuit-breaker-constantly-open)
- [Alerts not arriving in Telegram](#alerts-not-arriving-in-telegram)
- [Metrics not showing up in Prometheus](#metrics-not-showing-up-in-prometheus)
- [Kubernetes probe fails](#kubernetes-probe-fails)
- [Panic in the service](#panic-in-the-service)
- [Config hot-reload not working](#config-hot-reload-not-working)
- [Diagnostic commands](#diagnostic-commands)

---

## Service won't start

**Symptom:** `go run .` or `./health-calculator` exits with an error.

**Step 1. Check that the port is not in use**

```bash
lsof -i :8080
# or
ss -tlnp | grep 8080
```

If the port is in use — either stop the process or change the port in the code (`server.Addr` in `calc.go`).

**Step 2. Check health-config.yaml**

```bash
./health-calculator
# Expected output:
# Failed to load initial config: failed to parse config: ...
```

Typical config errors:

| Error | Cause |
|-------|-------|
| `failed to read config` | File `health-config.yaml` not found |
| `failed to parse config` | YAML syntax error |
| `metric weights must sum to 1.0` | Sum of metric weights != 1.0 |
| `invalid cache TTL` | Time format in `cache_ttl` is invalid |

**Step 3. Check Go dependencies**

```bash
go mod tidy
go run .
```

## Health score not updating

**Symptom:** metric `platform_health_score` does not change or is absent.

**Step 1. Check logs**

```bash
# Raise log level in config
logging:
  level: "debug"

# Logs should show:
# "Health score updated: 0.8943 (from 4 metrics, 0 degraded...)"
```

**Step 2. Check Prometheus availability**

```bash
# Direct request to Prometheus API
curl "http://prometheus:9090/api/v1/query?query=up"

# If no response — network or Prometheus issue
# If it responds but health-calc doesn't see it — check URL in config
```

**Step 3. Check PromQL queries**

Execute the PromQL query from the config manually:

```bash
curl "http://prometheus:9090/api/v1/query?query=avg(up)"
```

If the query returns an error — fix the PromQL in the config.

**Step 4. Check the circuit breaker**

```bash
curl http://localhost:8080/circuit-breaker
```

If `state: "open"` — Prometheus is unreachable and the CB is blocking requests.

## Rate limit triggers too often

**Symptom:** curl returns `429 Too Many Requests` under normal load.

**Step 1. Check the whitelist**

```bash
# Make sure your IP is in the whitelist
# For example, for localhost:
rate_limit:
  whitelist:
    - "127.0.0.1"
```

**Step 2. Check which limit is triggered**

The logs show the IP and endpoint:

```
Rate limit exceeded for IP 192.168.1.100 on endpoint /health
```

**Step 3. Increase limits**

```yaml
rate_limit:
  per_ip_rate:
    "/health": "60/m"   # Was 10/m
```

**Step 4. Check X-RateLimit headers**

```bash
curl -v http://localhost:8080/health 2>&1 | grep -i rate
# < X-RateLimit-Limit: 60
# < X-RateLimit-Remaining: 59
```

`X-RateLimit-Remaining: 0` — means the limit is exhausted.

## Circuit breaker constantly open

**Symptom:** `curl /circuit-breaker` shows `"state": "open"`.

**Step 1. Check Prometheus**

```bash
curl http://prometheus:9090/-/ready
```

If no response — Prometheus is the issue.

**Step 2. Check timeout**

Increase the timeout in the config if Prometheus is slow to respond:

```yaml
prometheus:
  timeout: "30s"
```

**Step 3. Check CB settings**

For an unstable Prometheus:

```yaml
circuit_breaker:
  max_failures: 10     # More tolerance
  reset_timeout: "60s" # Longer wait before retry
```

**Step 4. Reset the circuit breaker**

The CB has no API for manual reset (protection against cascading failures). Wait for `reset_timeout` or restart the service.

## Alerts not arriving in Telegram

**Symptom:** Prometheus is unreachable, but no Telegram notification.

**Step 1. Check credentials**

```bash
# Logs should show:
# ALERT would be sent - no Telegram credentials configured
```

If this message appears — `bot_token` or `chat_id` are empty.

**Step 2. Check environment variables**

```yaml
alerting:
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"
```

Make sure the `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` variables are set:

```bash
echo $TELEGRAM_BOT_TOKEN
echo $TELEGRAM_CHAT_ID
```

**Step 3. Check alert threshold**

```yaml
prometheus_unavailable_alert_threshold: 3
```

The alert is only sent after 3 consecutive failures. If Prometheus is intermittently available — the threshold won't be exceeded.

**Step 4. Check the bot**

Send any message to the bot in Telegram. If the bot doesn't respond — it may have been blocked or the token revoked.

## Metrics not showing up in Prometheus

**Symptom:** Prometheus does not see `platform_health_score`.

**Step 1. Check if `/metrics` responds**

```bash
curl http://localhost:8080/metrics | grep platform_health_score
```

If the metric exists — the issue is in Prometheus scrape config.

**Step 2. Check targets in Prometheus**

```bash
# In the Prometheus web interface:
# Status → Targets → look for health-calculator
# Or via API:
curl http://prometheus:9090/api/v1/targets
```

Target should be `UP`.

**Step 3. Add the job in prometheus.yml**

```yaml
scrape_configs:
  - job_name: 'health-calculator'
    scrape_interval: 30s
    static_configs:
      - targets: ['health-calculator:8080']
```

## Kubernetes probe fails

**Symptom:** Pod restarts, events show `Liveness probe failed`.

**Step 1. Check logs before failure**

```bash
kubectl logs -l app=health-calculator --tail=20
```

**Step 2. Check readiness**

```bash
kubectl exec -it <pod> -- curl http://localhost:8080/ready
```

If `not_ready` — the service likely hasn't completed its first calculation.

**Step 3. Increase `initialDelaySeconds`**

```yaml
livenessProbe:
  initialDelaySeconds: 60  # Was 30 — give more time for first calculation
```

**Step 4. Check resources**

```bash
kubectl describe pod <pod> | grep -A5 "Events"
```

If `OOMKilled` — increase `memory.limits`.

## Panic in the service

**Symptom:** service crashed, logs show `panic:`.

**Step 1. Find the panic in logs**

```bash
kubectl logs --previous -l app=health-calculator | grep -A10 panic
```

**Step 2. Restore the service**

```bash
kubectl rollout restart deployment/health-calculator
```

**Step 3. Prevent recurrence**

The service has `recoveryMiddleware` that catches panics in HTTP handlers and returns 500. If a panic occurs outside HTTP (e.g., in a calculation goroutine), the service will crash. Check cache, config, and nil references.

## Config hot-reload not working

**Symptom:** you change `health-config.yaml` but the service continues using the old config.

**Step 1. Check logs**

```bash
# Look for the message:
# Config loaded successfully: 4 metrics, update interval: 5m0s
# or
# Failed to reload config: ...
```

**Step 2. Check that the config is valid**

```bash
# Copy the config into an online YAML validator
# or check manually:
python3 -c "import yaml; yaml.safe_load(open('health-config.yaml'))"
```

**Step 3. Check the sum of weights**

```bash
# Calculate the sum of all metric weights
python3 -c "
import yaml
cfg = yaml.safe_load(open('health-config.yaml'))
total = sum(m['weight'] for m in cfg['metrics'])
print(f'Total weight: {total}')
assert abs(total - 1.0) < 0.001, 'Weights must sum to 1.0'
"
```

## Diagnostic commands

```bash
# Full health-check
curl http://localhost:8080/health | jq .

# Readiness probe
curl http://localhost:8080/ready | jq .

# Circuit breaker status
curl http://localhost:8080/circuit-breaker | jq .

# Prometheus metrics
curl -s http://localhost:8080/metrics | grep -E "^(platform_health|health_calculator|service_uptime)"

# Check rate limit headers
curl -v http://localhost:8080/health 2>&1 | grep -i rate-limit

# Service logs (Docker)
docker logs health-calculator --tail 50

# Service logs (Kubernetes)
kubectl logs -l app=health-calculator --tail 50

# Check Prometheus availability
curl -s http://prometheus:9090/-/healthy

# Go pprof (if enabled)
curl http://localhost:8080/debug/pprof/heap -o heap.prof
go tool pprof heap.prof
```

---

| Back |
|------|
| [Production operations](05-operations.md) |
