> [English version](../05-operations.md)

# Production-эксплуатация

## Содержание

- [Kubernetes deployment](#kubernetes-deployment)
  - [Deployment manifest](#deployment-manifest)
  - [Secrets](#secrets)
  - [Probes](#probes)
- [Мониторинг](#мониторинг)
  - [Prometheus rules](#prometheus-rules)
  - [Grafana dashboard](#grafana-dashboard)
- [Типовые сценарии](#типовые-сценарии)
  - [Prometheus недоступен](#prometheus-недоступен)
  - [Частичная деградация метрик](#частичная-деградация-метрик)
  - [Перезагрузка конфига](#перезагрузка-конфига)
  - [Роллбэк](#роллбэк)
- [Capacity planning](#capacity-planning)
- [Security](#security)

---

## Kubernetes deployment

Готовые манифесты в директории [`k8s/`](../../k8s/):

```bash
kubectl apply -f k8s/
```

### Deployment

См. [k8s/deployment.yaml](../../k8s/deployment.yaml). Ключевые детали:
- 2 реплики с liveness и readiness probes
- Конфиг монтируется из ConfigMap (read-only)
- Telegram credentials через Kubernetes Secrets
- Resource requests: 100m CPU / 64Mi memory

### Service

См. [k8s/service.yaml](../../k8s/service.yaml). Проброс порта 80 → 8080.

### Secrets

Telegram credentials хранятся в Kubernetes Secrets:

```bash
kubectl create secret generic telegram-secrets \
  --from-literal=bot-token='123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11' \
  --from-literal=chat-id='-1001234567890'
```

### Probes

| Probe | Path | `initialDelaySeconds` | `periodSeconds` | Когда возвращает failure |
|-------|------|-----------------------|-----------------|--------------------------|
| Liveness | `/health` | 30 | 10 | Если расчёта не было > 10 минут |
| Readiness | `/ready` | 5 | 5 | Пока не выполнен первый расчёт |

**Важно:** Readiness probe имеет меньший `initialDelaySeconds`, чем Liveness. Это гарантирует, что под не получит трафик, пока не будет готов. Liveness probe начинает проверяться позже, чтобы дать время на первый расчёт метрик.

### ConfigMap

Конфигурация хранится в ConfigMap. См. [k8s/configmap.yaml](../../k8s/configmap.yaml):

```bash
kubectl apply -f k8s/configmap.yaml
```

При изменении ConfigMap нужно перезапустить поды (rolling restart):

```bash
kubectl rollout restart deployment/health-calculator
```

---

## Мониторинг

### Prometheus rules

Рекомендуемый набор алертов для самого health-calculator:

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

Основные переменные дашборда:

- `${service}` — `health-calculator`
- `${interval}` — `5m`

Панели для дашборда:

**Row 1: Overview**
- `platform_health_score` — singlestat (текущее значение) + graph (7 дней)
- `health_calculator_degraded_mode` — states timeline

**Row 2: Performance**
- `rate(health_calculator_calculation_duration_seconds_sum[5m]) / rate(health_calculator_calculation_duration_seconds_count[5m])` — среднее время расчёта
- `health_calculator_metrics_fetched_total - health_calculator_metrics_failed_total` — успешные vs неудачные

**Row 3: Resilience**
- `health_calculator_circuit_breaker_tripped_total` — сколько раз CB открывался
- `health_calculator_fallback_used_total` — fallback счётчик

**Row 4: Rate Limiting**
- `rate(health_calculator_rate_limit_exceeded_total[5m])` — RPS заблокированных
- `health_calculator_active_rate_limit_clients` — активных клиентов

**Row 5: Service Health**
- `service_uptime_seconds` — аптайм
- `rate(health_calculator_prometheus_connection_errors_total[5m])` — ошибки соединения

---

## Типовые сценарии

### Prometheus недоступен

**Что происходит:**
1. Запросы к Prometheus начинают падать с ошибкой
2. Circuit breaker считает ошибки
3. После `max_failures` (по умолчанию 3) CB открывается
4. Метрики начинают использовать fallback-значения
5. Сервис переходит в degraded-режим
6. Если `prometheus_unavailable_alert_threshold` превышен — отправляется Telegram-алерт
7. После восстановления Prometheus CB переходит в Half-Open, проверяет, и закрывается

**Как отличить временный сбой от отказа:**
- Временный сбой: `metrics_failed` растёт, `fallback_used_total` не растёт (CB ещё закрыт)
- Отказ: `circuit_breaker_tripped_total` увеличился, `degraded_mode = 1`

**Что делать:**
1. Проверить доступность Prometheus: `curl http://prometheus:9090/-/healthy`
2. Проверить логи: `kubectl logs -l app=health-calculator | grep prometheus`
3. Если Prometheus перезапускается — дождаться восстановления
4. Если проблема на сетевом уровне — проверить NetworkPolicy / DNS

### Частичная деградация метрик

**Сценарий:** 2 из 4 метрик недоступны

```
degradation_factor = 1.0 - (2/4 * 0.3) = 0.85
```

Итоговый score умножается на `0.85`. Сервис продолжает работать.

**Когда это нормально:**
- Единичные метрики временно недоступны
- Score остаётся в пределах SLO

**Когда нужна реакция:**
- `degraded_mode = 1` более 10 минут
- Более 50% метрик в fallback
- Score упал ниже SLO

### Перезагрузка конфига

Конфиг перечитывается каждую минуту. При изменении:

1. Читается файл `health-config.yaml`
2. Парсится YAML
3. Валидируется (веса метрик, формат времени, стратегии)
4. Под блокировкой `mutex` обновляются: `config`, `circuit_breaker`, `logger`, `rate_limiter`
5. Логируется успех или ошибка

Если новый конфиг некорректен — сервис продолжает работать со старым.

### Роллбэк

```bash
# Kubernetes — откатить deployment
kubectl rollout undo deployment/health-calculator

# Docker — запустить предыдущий образ
docker run -d --name health-calculator-old \
  your-registry/health-calculator:previous-tag

# Локально — запустить предыдущий бинарник
./health-calculator.bak
```

---

## Capacity planning

**Resource usage (reference):**

| Метрика | Значение |
|---------|----------|
| CPU (idle) | < 1% |
| CPU (расчёт) | ~50ms per cycle |
| Memory (базово) | ~10 MB |
| Memory (1000 rate limit buckets) | ~15 MB |
| Startup time | < 1s |
| Image size | ~16 MB |

**Когда нужно увеличивать ресурсы:**
- > 20 метрик в конфиге
- > 10000 уникальных IP за минуту (rate limiting)
- Prometheus latency > 5s (увеличить timeout)

---

## Security

- Конфиг монтируется read-only в контейнер
- Telegram credentials через Kubernetes Secrets (не в config)
- Rate limiting защищает от DDoS
- Timeout на все внешние HTTP-запросы
- Circuit breaker предотвращает каскадные сбои

---

| Назад | Дальше |
|-------|--------|
| [Запуск и API](04-usage.md) | [Решение проблем](06-troubleshooting.md) |
