# Решение проблем

## Содержание

- [Сервис не стартует](#сервис-не-стартует)
- [Health score не обновляется](#health-score-не-обновляется)
- [Rate limit срабатывает слишком часто](#rate-limit-срабатывает-слишком-часто)
- [Circuit breaker постоянно открыт](#circuit-breaker-постоянно-открыт)
- [Алерты не приходят в Telegram](#алерты-не-приходят-в-telegram)
- [Метрики не появляются в Prometheus](#метрики-не-появляются-в-prometheus)
- [Kubernetes probe fails](#kubernetes-probe-fails)
- [Паника в сервисе](#паника-в-сервисе)
- [Config hot-reload не работает](#config-hot-reload-не-работает)
- [Диагностические команды](#диагностические-команды)

---

## Сервис не стартует

**Симптом:** `go run .` или `./health-calculator` завершается с ошибкой.

**Шаг 1. Проверьте, что порт не занят**

```bash
lsof -i :8080
# или
ss -tlnp | grep 8080
```

Если порт занят — либо остановите процесс, либо измените порт в коде (`server.Addr` в `calc.go`).

**Шаг 2. Проверьте health-config.yaml**

```bash
./health-calculator
# Expected output:
# Failed to load initial config: failed to parse config: ...
```

Типичные ошибки в конфиге:

| Ошибка | Причина |
|--------|---------|
| `failed to read config` | Файл `health-config.yaml` не найден |
| `failed to parse config` | Ошибка YAML-синтаксиса |
| `metric weights must sum to 1.0` | Сумма весов метрик != 1.0 |
| `invalid cache TTL` | Формат времени в `cache_ttl` неверный |

**Шаг 3. Проверьте Go-зависимости**

```bash
go mod tidy
go run .
```

## Health score не обновляется

**Симптом:** метрика `platform_health_score` не меняется или отсутствует.

**Шаг 1. Проверьте логи**

```bash
# Поднять уровень логирования в конфиге
logging:
  level: "debug"

# В логах должно быть:
# "Health score updated: 0.8943 (from 4 metrics, 0 degraded...)"
```

**Шаг 2. Проверьте доступность Prometheus**

```bash
# Прямой запрос к Prometheus API
curl "http://prometheus:9090/api/v1/query?query=up"

# Если не отвечает — проблема в сети или Prometheus
# Если отвечает, но health-calc не видит — проверьте URL в конфиге
```

**Шаг 3. Проверьте PromQL-запросы**

Выполните PromQL-запрос из конфига вручную:

```bash
curl "http://prometheus:9090/api/v1/query?query=avg(up)"
```

Если запрос возвращает ошибку — исправьте PromQL в конфиге.

**Шаг 4. Проверьте circuit breaker**

```bash
curl http://localhost:8080/circuit-breaker
```

Если `state: "open"` — Prometheus недоступен и CB блокирует запросы.

## Rate limit срабатывает слишком часто

**Симптом:** curl возвращает `429 Too Many Requests` при нормальной нагрузке.

**Шаг 1. Проверьте whitelist**

```bash
# Убедитесь, что ваш IP в whitelist
# Например, для localhost:
rate_limit:
  whitelist:
    - "127.0.0.1"
```

**Шаг 2. Проверьте, какой лимит срабатывает**

В логах пишется IP и endpoint:

```
Rate limit exceeded for IP 192.168.1.100 on endpoint /health
```

**Шаг 3. Увеличьте лимиты**

```yaml
rate_limit:
  per_ip_rate:
    "/health": "60/m"   # Было 10/m
```

**Шаг 4. Проверьте X-RateLimit headers**

```bash
curl -v http://localhost:8080/health 2>&1 | grep -i rate
# < X-RateLimit-Limit: 60
# < X-RateLimit-Remaining: 59
```

`X-RateLimit-Remaining: 0` — значит лимит исчерпан.

## Circuit breaker постоянно открыт

**Симптом:** `curl /circuit-breaker` показывает `"state": "open"`.

**Шаг 1. Проверьте Prometheus**

```bash
curl http://prometheus:9090/-/ready
```

Если не отвечает — проблема в Prometheus.

**Шаг 2. Проверьте timeout**

Увеличьте timeout в конфиге, если Prometheus отвечает медленно:

```yaml
prometheus:
  timeout: "30s"
```

**Шаг 3. Проверьте настройки CB**

Для нестабильного Prometheus:

```yaml
circuit_breaker:
  max_failures: 10     # Больше терпимости
  reset_timeout: "60s" # Дольше ждать перед пробой
```

**Шаг 4. Сбросьте circuit breaker**

CB не имеет API для ручного сброса (защита от каскадных сбоев). Дождитесь `reset_timeout` или перезапустите сервис.

## Алерты не приходят в Telegram

**Симптом:** Prometheus недоступен, но Telegram-уведомлений нет.

**Шаг 1. Проверьте credentials**

```bash
# В логах должно быть:
# ALERT would be sent - no Telegram credentials configured
```

Если это сообщение есть — `bot_token` или `chat_id` пустые.

**Шаг 2. Проверьте переменные окружения**

```yaml
alerting:
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"
```

Убедитесь, что переменные `TELEGRAM_BOT_TOKEN` и `TELEGRAM_CHAT_ID` заданы:

```bash
echo $TELEGRAM_BOT_TOKEN
echo $TELEGRAM_CHAT_ID
```

**Шаг 3. Проверьте порог алертов**

```yaml
prometheus_unavailable_alert_threshold: 3
```

Алерт отправляется только после 3 последовательных неудач. Если Prometheus переодически доступен — порог не будет превышен.

**Шаг 4. Проверьте бота**

Напишите боту любое сообщение в Telegram. Если бот не отвечает — его могли заблокировать или отозвать токен.

## Метрики не появляются в Prometheus

**Симптом:** Prometheus не видит `platform_health_score`.

**Шаг 1. Проверьте, отвечает ли `/metrics`**

```bash
curl http://localhost:8080/metrics | grep platform_health_score
```

Если метрика есть — проблема в сборе Prometheus (scrape config).

**Шаг 2. Проверьте target в Prometheus**

```bash
# В веб-интерфейсе Prometheus:
# Status → Targets → ищите health-calculator
# Или через API:
curl http://prometheus:9090/api/v1/targets
```

Target должен быть `UP`.

**Шаг 3. Добавьте job в prometheus.yml**

```yaml
scrape_configs:
  - job_name: 'health-calculator'
    scrape_interval: 30s
    static_configs:
      - targets: ['health-calculator:8080']
```

## Kubernetes probe fails

**Симптом:** Pod перезапускается, в events `Liveness probe failed`.

**Шаг 1. Проверьте логи перед failure**

```bash
kubectl logs -l app=health-calculator --tail=20
```

**Шаг 2. Проверьте readiness**

```bash
kubectl exec -it <pod> -- curl http://localhost:8080/ready
```

Если `not_ready` — вероятно, сервис не успел выполнить первый расчёт.

**Шаг 3. Увеличьте `initialDelaySeconds`**

```yaml
livenessProbe:
  initialDelaySeconds: 60  # Было 30 — дать больше времени на первый расчёт
```

**Шаг 4. Проверьте ресурсы**

```bash
kubectl describe pod <pod> | grep -A5 "Events"
```

Если `OOMKilled` — увеличьте `memory.limits`.

## Паника в сервисе

**Симптом:** сервис упал, в логах `panic:`.

**Шаг 1. Найдите панику в логах**

```bash
kubectl logs --previous -l app=health-calculator | grep -A10 panic
```

**Шаг 2. Восстановите сервис**

```bash
kubectl rollout restart deployment/health-calculator
```

**Шаг 3. Предотвратите повторение**

Сервис имеет `recoveryMiddleware`, который ловит паники в HTTP-handler'ах и возвращает 500. Если паника происходит вне HTTP (например, в горутине расчёта), сервис упадёт. Проверьте кэш, конфиг и запросы на nil.

## Config hot-reload не работает

**Симптом:** меняете `health-config.yaml`, но сервис продолжает работать по-старому.

**Шаг 1. Проверьте логи**

```bash
# Ищите сообщение:
# Config loaded successfully: 4 metrics, update interval: 5m0s
# или
# Failed to reload config: ...
```

**Шаг 2. Проверьте, что конфиг валиден**

```bash
# Скопируйте конфиг в онлайн-валидатор YAML
# или проверьте вручную:
python3 -c "import yaml; yaml.safe_load(open('health-config.yaml'))"
```

**Шаг 3. Проверьте сумму весов**

```bash
# Рассчитайте сумму weight всех метрик
python3 -c "
import yaml
cfg = yaml.safe_load(open('health-config.yaml'))
total = sum(m['weight'] for m in cfg['metrics'])
print(f'Total weight: {total}')
assert abs(total - 1.0) < 0.001, 'Weights must sum to 1.0'
"
```

## Диагностические команды

```bash
# Полный health-check
curl http://localhost:8080/health | jq .

# Readiness probe
curl http://localhost:8080/ready | jq .

# Circuit breaker status
curl http://localhost:8080/circuit-breaker | jq .

# Prometheus metrics
curl -s http://localhost:8080/metrics | grep -E "^(platform_health|health_calculator|service_uptime)"

# Проверка rate limit headers
curl -v http://localhost:8080/health 2>&1 | grep -i rate-limit

# Логи сервиса (Docker)
docker logs health-calculator --tail 50

# Логи сервиса (Kubernetes)
kubectl logs -l app=health-calculator --tail 50

# Проверка доступности Prometheus
curl -s http://prometheus:9090/-/healthy

# Go pprof (если включён)
curl http://localhost:8080/debug/pprof/heap -o heap.prof
go tool pprof heap.prof
```

---

| Назад |
|-------|
| [Production-эксплуатация](05-operations.md) |
