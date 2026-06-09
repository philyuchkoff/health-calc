# Запуск и API

## Содержание

- [Запуск](#запуск)
- [Endpoints](#endpoints)
  - [`GET /metrics` — Prometheus metrics](#get-metrics--prometheus-metrics)
  - [`GET /health` — Liveness probe](#get-health--liveness-probe)
  - [`GET /ready` — Readiness probe](#get-ready--readiness-probe)
  - [`GET /circuit-breaker` — статус CB](#get-circuit-breaker--статус-cb)
  - [`GET /` — Status page](#get----status-page)
- [Примеры использования](#примеры-использования)
- [Graceful shutdown](#graceful-shutdown)
- [Логирование](#логирование)

---

## Запуск

```bash
# Локально (dev)
go run .

# Собранный бинарник
./health-calculator

# Docker
docker run -p 8080:8080 -v $(pwd)/health-config.yaml:/root/health-config.yaml:ro health-calculator
```

Проверить, что сервис запущен:

```bash
curl -s http://localhost:8080/health | jq .
```

Ожидаемый ответ:

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

Стандартный Prometheus endpoint. Без rate limiting.

```bash
curl -s http://localhost:8080/metrics | grep platform_health_score
```

Основные метрики сервиса:

| Метрика | Тип | Описание |
|---------|-----|----------|
| `platform_health_score` | Gauge | Итоговый health score (`0.0 – 1.0`) |
| `health_calculator_metrics_fetched_total` | Counter | Успешные запросы к Prometheus |
| `health_calculator_metrics_failed_total` | Counter | Неудачные запросы к Prometheus |
| `health_calculator_calculation_duration_seconds` | Histogram | Время расчёта |
| `health_calculator_circuit_breaker_tripped_total` | Counter | Сколько раз CB открывался |
| `health_calculator_degraded_mode` | Gauge | `1` если сервис в degraded-режиме |
| `health_calculator_fallback_used_total` | Counter | Сколько раз использован fallback |
| `health_calculator_rate_limit_exceeded_total` | Counter | Заблокировано запросов |
| `health_calculator_active_rate_limit_clients` | Gauge | Активных клиентов rate limiter |
| `health_calculator_config_reload_total` | Counter | Попыток перезагрузить конфиг |
| `service_uptime_seconds` | Gauge | Время работы сервиса |
| `health_calculator_prometheus_connection_errors_total` | Counter | Ошибок соединения с Prometheus |

### `GET /health` — Liveness probe

Используется Kubernetes для проверки, жив ли сервис (liveness probe).

```bash
curl http://localhost:8080/health
```

**Ответы:**

```json
// ✅ Сервис здоров — HTTP 200
{
  "status": "healthy",
  "last_successful_calculation": "2026-06-09T12:00:00Z",
  "age": "2.5s",
  "degraded": false,
  "circuit_breaker": { "state": "closed" }
}

// ⚠️ Часть метрик в fallback — HTTP 200
{
  "status": "degraded",
  "reason": "some metrics are using fallback values",
  "degraded": true,
  ...
}

// 🔴 Circuit breaker открыт — HTTP 200
{
  "status": "degraded",
  "reason": "circuit breaker is open",
  ...
}

// 🔴 Давно не было расчёта — HTTP 503
{
  "status": "unhealthy",
  "reason": "last calculation too old: 15m0s",
  ...
}
```

**Логика определения статуса:**

```
если last_calculation > 10 минут назад → status = "unhealthy" (503)
иначе если degraded → status = "degraded"
иначе если circuit breaker open → status = "degraded"
иначе → status = "healthy" (200)
```

### `GET /ready` — Readiness probe

Используется Kubernetes для проверки, готов ли сервис принимать трафик (readiness probe).

```bash
curl http://localhost:8080/ready
```

Отличия от `/health`:
- Возвращает `503` пока не загружен конфиг и не выполнен первый расчёт
- Возвращает `503` если последний расчёт был более 10 минут назад
- Возвращает `200` даже в degraded-режиме (сервис работает, пусть и не в полную силу)

```json
// ✅ Готов — HTTP 200
{ "status": "ready" }

// ⚠️ Готов, но degraded — HTTP 200
{ "status": "ready_degraded" }

// 🔴 Не готов — HTTP 503
{ "status": "not_ready", "reason": "service has not completed initial startup" }
```

### `GET /circuit-breaker` — статус CB

Показывает текущее состояние circuit breaker:

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

Поле `state` может быть: `closed`, `open`, `half-open`.

Полезно для мониторинга и отладки — можно алертнуть по `state == "open"`.

### `GET /` — Status page

Возвращает простой текст:

```
Health Calculator Service
```

Используется для базовой проверки, что сервер отвечает.

---

## Примеры использования

### Мониторинг в Grafana

PromQL-запросы для дашборда:

```promql
# Текущий health score
platform_health_score

# Динамика за последние 7 дней
avg_over_time(platform_health_score[7d])

# Сервис в degraded режиме
health_calculator_degraded_mode

# Частота ошибок Prometheus
rate(health_calculator_metrics_failed_total[5m])
```

### Алерты в Prometheus

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

Сервис корректно обрабатывает сигналы `SIGINT` и `SIGTERM`:

```
1. Получен сигнал (SIGINT / SIGTERM)
2. Отменяется контекст (cancel)
3. Останавливается ticker
4. Сервер HTTP завершает текущие запросы (таймаут 10 секунд)
5. Сервис завершает работу
```

Логи при shutdown:

```
Received signal: interrupt, shutting down...
Shutting down health calculator gracefully
Health Calculator Service stopped gracefully
```

---

## Логирование

Логи выводятся в JSON (в production) или текст (в dev):

```json
{"level":"info","service":"health-calculator","source":"calculator","message":"Health score updated: 0.8943 (from 4 metrics, 0 degraded, factor 1.00, took 450ms)","time":"2026-06-09T12:00:00Z"}

{"level":"warn","service":"health-calculator","source":"prometheus","message":"Retry 1/3 for metric api_success_rate failed: prometheus returned status: 503","time":"2026-06-09T12:00:01Z"}
```

Все логи содержат поля:
- `time` — временная метка (RFC3339)
- `level` — уровень: `debug`, `info`, `warn`, `error`
- `service` — имя сервиса
- `source` — модуль: `prometheus`, `calculator`, `config`, `alerting`, `rate_limit`, `http`
- `message` — сообщение
- `request_id` — (опционально) ID запроса для трейсинга

---

| Назад | Дальше |
|-------|--------|
| [Конфигурация](03-configuration.md) | [Production-эксплуатация](05-operations.md) |
