> [Русская версия](ru/03-configuration.md)

# Configuration

The service reads a YAML file `health-config.yaml`. The file path is hardcoded in the code (see `calc.go:Start`).

The config is re-read every minute (hot-reload) — settings can be changed without restart.

---

## Contents

- [Full example](#full-example)
- [Config sections](#config-sections)
  - [`update_interval`](#update_interval)
  - [`prometheus`](#prometheus)
  - [`circuit_breaker`](#circuit_breaker)
  - [`graceful_degradation`](#graceful_degradation)
  - [`rate_limit`](#rate_limit)
  - [`metrics`](#metrics)
  - [`alerting`](#alerting)
  - [`logging`](#logging)
- [Environment variables](#environment-variables)
- [Validation](#validation)
- [Hot-reload](#hot-reload)

---

## Full example

```yaml
# --- Core ---
update_interval: "5m"

# --- Prometheus ---
prometheus:
  url: "http://prometheus:9090"
  timeout: "10s"

# --- Circuit Breaker ---
circuit_breaker:
  max_failures: 3
  reset_timeout: "30s"

# --- Graceful Degradation ---
graceful_degradation:
  enable_cache: true
  cache_ttl: "5m"
  max_age: "10m"
  fallback_strategy: "neutral"   # zero | average | last_known | neutral

# --- Rate Limiting ---
rate_limit:
  enabled: true
  global_rate:
    "/metrics": "100/m"
    "/health": "60/m"
  per_ip_rate:
    "/health": "10/m"
    "/circuit-breaker": "20/m"
  whitelist:
    - "127.0.0.1"
    - "::1"

# --- Metrics ---
metrics:
  - name: "synthetic_uptime"
    prometheus_query: 'avg(synthetic_check_success{environment="production"})'
    weight: 0.4
    description: "Uptime from external synthetic monitoring"
    min_valid_value: 0.0
    max_valid_value: 1.0

  - name: "api_success_rate"
    prometheus_query: 'rate(http_requests_total{status=~"2.."}[5m]) / rate(http_requests_total[5m])'
    weight: 0.6
    description: "API success rate"
    min_valid_value: 0.0
    max_valid_value: 1.0

# --- Alerts ---
alerting:
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"
  prometheus_unavailable_alert_threshold: 3

# --- Logging ---
logging:
  level: "info"      # debug | info | warn | error
  format: "json"     # json | text
  service: "health-calculator"
```

---

## Config sections

### `update_interval`

Interval between health score calculations.

```yaml
update_interval: "5m"
```

Format is a string, parsed via `time.ParseDuration`. Valid suffixes: `s` (seconds), `m` (minutes), `h` (hours).

If the value is invalid — `5m` is used.

### `prometheus`

Prometheus connection settings:

```yaml
prometheus:
  url: "http://prometheus:9090"     # Prometheus API base URL
  timeout: "10s"                     # Prometheus request timeout
```

The `url` field is required. It is used to form requests like `{url}/api/v1/query?query=...`.

The `timeout` field sets the HTTP request timeout. If not specified or invalid — 10 seconds.

### `circuit_breaker`

Protection against cascading failures. Details — [Circuit Breaker](circuit-breaker.md).

```yaml
circuit_breaker:
  max_failures: 3        # How many errors before opening the circuit
  reset_timeout: "30s"   # How long before retrying (Half-Open)
```

**States:**

```
Closed ──(max_failures errors)──→ Open ──(reset_timeout)──→ Half-Open
  ↑                                                            │
  └──────────────────────(success)─────────────────────────────┘
```

**Recommendations:**
- For stable Prometheus: `max_failures: 5`, `reset_timeout: "60s"`
- For high-load: decrease `reset_timeout` for faster recovery

### `graceful_degradation`

Ensures operation when metrics are unavailable. Details — [Graceful Degradation](graceful-degradation.md).

```yaml
graceful_degradation:
  enable_cache: true        # Enable metric value caching
  cache_ttl: "5m"           # Cache TTL
  max_age: "10m"            # Maximum age for last_known fallback
  fallback_strategy: "neutral"  # zero | average | last_known | neutral
```

**Fallback strategies:**

| Strategy | Returns | When to use |
|----------|---------|-------------|
| `zero` | `0.0` | Critical metrics — 0 = problem |
| `neutral` | `0.5` | Universal option |
| `average` | Midpoint of range | Metrics with known normal range |
| `last_known` | Last cached value | Maximum data preservation |

**Degradation factor:**

When some metrics use fallback, the final score is multiplied by a coefficient:

```
degradation_factor = 1.0 - (degraded_metrics / total_metrics × 0.3)
```

Maximum reduction — 30% (when all metrics are in fallback).

### `rate_limit`

Protection against overloads. Details — [Rate Limiting](rate-limiting.md).

```yaml
rate_limit:
  enabled: true
  global_rate:
    "/metrics": "100/m"
  per_ip_rate:
    "/health": "10/m"
  whitelist:
    - "127.0.0.1"
```

**Rate format:** `{number}/{period}`, where period is `s` (second), `m` (minute), `h` (hour).

Examples: `10/s`, `100/m`, `5/h`.

**Check order:**
1. Whitelist check — if IP is in the list, skip
2. Per-IP rate limit — check individual limit
3. Global rate limit — check overall limit

If `rate_limit` is not specified or `enabled: false` — rate limiting is disabled.

### `metrics`

List of metrics for health score calculation. **The sum of all metric weights must be 1.0** (checked on config load).

```yaml
metrics:
  - name: "synthetic_uptime"           # Unique metric name
    prometheus_query: 'avg(up)'        # PromQL query
    weight: 0.4                        # Weight in final score (0.0 – 1.0)
    description: "Description"         # Optional description
    min_valid_value: 0.0               # Minimum valid value
    max_valid_value: 1.0               # Maximum valid value
```

**Health score calculation:**

For each metric:
1. A PromQL query is executed against Prometheus
2. The obtained value is normalized to the `[0, 1]` range
3. Multiplied by `weight`
4. All weighted values are summed

**Normalization:**

```
normalized = (value - min_valid_value) / (max_valid_value - min_valid_value)
```

If value is less than `min_valid_value` → `0.0`
If value is greater than `max_valid_value` → `1.0`

**Example:** metric `api_success_rate` with range `0.0 – 1.0` and `weight: 0.6`.
If the query returned `0.95`, contribution to score: `0.95 × 0.6 = 0.57`.

### `alerting`

Telegram alert settings.

```yaml
alerting:
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"    # Bot token (or environment variable)
    chat_id: "${TELEGRAM_CHAT_ID}"        # Chat ID (or environment variable)
  prometheus_unavailable_alert_threshold: 3  # How many consecutive cycles Prometheus is unavailable
```

An alert is sent when the number of consecutive failed requests to Prometheus exceeds `prometheus_unavailable_alert_threshold`.

If `bot_token` or `chat_id` are not specified — alerts are written to the log (WARN).

**Creating a bot:**
1. Message [@BotFather](https://t.me/BotFather) in Telegram
2. Send `/newbot` and follow the instructions
3. Get a token like `123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`
4. Add the bot to a group and find out `chat_id` (can use `@getidsbot`)

### `logging`

```yaml
logging:
  level: "info"     # debug | info | warn | error
  format: "json"    # json | text
  service: "health-calculator"
  output: "stdout"  # stdout | stderr | file
  output_file: "/var/log/health-calc.log"  # only if output: file
```

`json` format — for production (easily parsed by Loki/ELK).
`text` format — for local development (human-readable console output).

---

## Environment variables

Environment variables can be used in config via `${VAR_NAME}` syntax:

```yaml
telegram:
  bot_token: "${TELEGRAM_BOT_TOKEN}"
  chat_id: "${TELEGRAM_CHAT_ID}"
```

When loading the config, `${TELEGRAM_BOT_TOKEN}` is replaced with the environment variable value. If the variable is not set — an empty string is substituted.

---

## Validation

When loading the config, the following is checked:

1. **YAML syntax** — if the file is invalid, an error is returned
2. **Sum of weights** — `sum(weight)` must be `1.0` (tolerance `±0.001`)
3. **Time format** — `cache_ttl`, `max_age`, `reset_timeout` etc. must be valid durations
4. **Fallback strategy** — only `zero`, `average`, `last_known`, `neutral`
5. **Rate limit format** — `number/period`

If validation fails — the service does not apply the new config and continues working with the old one.

---

## Hot-reload

The config is re-read every minute in a background goroutine (`watchConfig`):

```
main goroutine:    Start() → loadConfig() → ticker loop → calculateHealthScore()
watchConfig:       every 1 minute → loadConfig() → swap config under mutex
```

Logging during hot-reload:
- Success: `Config loaded successfully: 4 metrics, update interval: 5m0s`
- Error: `Errorf("Failed to reload config: %v", err)`

---

| Previous | Next |
|----------|------|
| [Installation](02-installation.md) | [Usage and API](04-usage.md) |
