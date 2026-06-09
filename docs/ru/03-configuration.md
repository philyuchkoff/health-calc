> [English version](../03-configuration.md)

# Конфигурация

Сервис читает YAML-файл `health-config.yaml`. Путь к файлу жёстко задан в коде (см. `calc.go:Start`).

Конфиг перечитывается каждую минуту (hot-reload) — можно менять настройки без перезапуска.

---

## Содержание

- [Полный пример](#полный-пример)
- [Секции конфига](#секции-конфига)
  - [`update_interval`](#update_interval)
  - [`prometheus`](#prometheus)
  - [`circuit_breaker`](#circuit_breaker)
  - [`graceful_degradation`](#graceful_degradation)
  - [`rate_limit`](#rate_limit)
  - [`metrics`](#metrics)
  - [`alerting`](#alerting)
  - [`logging`](#logging)
- [Переменные окружения](#переменные-окружения)
- [Валидация](#валидация)
- [Hot-reload](#hot-reload)

---

## Полный пример

```yaml
# --- Основное ---
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

# --- Метрики ---
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

# --- Алерты ---
alerting:
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"
  prometheus_unavailable_alert_threshold: 3

# --- Логирование ---
logging:
  level: "info"      # debug | info | warn | error
  format: "json"     # json | text
  service: "health-calculator"
```

---

## Секции конфига

### `update_interval`

Интервал между расчётами health score.

```yaml
update_interval: "5m"
```

Формат — строка, парсится через `time.ParseDuration`. Допустимые суффиксы: `s` (секунды), `m` (минуты), `h` (часы).

Если значение некорректное — используется `5m`.

### `prometheus`

Настройки подключения к Prometheus:

```yaml
prometheus:
  url: "http://prometheus:9090"     # Базовый URL Prometheus API
  timeout: "10s"                     # Таймаут запроса к Prometheus
```

Поле `url` — обязательное. Используется для формирования запросов вида `{url}/api/v1/query?query=...`.

Поле `timeout` — задаёт таймаут HTTP-запроса. Если не указано или некорректно — 10 секунд.

### `circuit_breaker`

Защита от каскадных сбоев. Подробнее — [Circuit Breaker](circuit-breaker.md).

```yaml
circuit_breaker:
  max_failures: 3        # После скольких ошибок открыть цепь
  reset_timeout: "30s"   # Через сколько попробовать снова (Half-Open)
```

**Состояния:**

```
Closed ──(max_failures errors)──→ Open ──(reset_timeout)──→ Half-Open
  ↑                                                            │
  └──────────────────────(success)─────────────────────────────┘
```

**Рекомендации:**
- Для стабильного Prometheus: `max_failures: 5`, `reset_timeout: "60s"`
- Для high-load: уменьшить `reset_timeout` для быстрого восстановления

### `graceful_degradation`

Обеспечивает работу при недоступности метрик. Подробнее — [Graceful Degradation](graceful-degradation.md).

```yaml
graceful_degradation:
  enable_cache: true        # Включить кэширование значений метрик
  cache_ttl: "5m"           # Время жизни кэша
  max_age: "10m"            # Максимальный возраст для last_known fallback
  fallback_strategy: "neutral"  # zero | average | last_known | neutral
```

**Стратегии fallback:**

| Стратегия | Возвращает | Когда использовать |
|-----------|------------|-------------------|
| `zero` | `0.0` | Критичные метрики — 0 = проблема |
| `neutral` | `0.5` | Универсальный вариант |
| `average` | Средняя точка диапазона | Метрики с известным нормальным диапазоном |
| `last_known` | Последнее кэшированное значение | Максимальное сохранение данных |

**Фактор деградации:**

Когда часть метрик использует fallback, итоговый score умножается на коэффициент:

```
degradation_factor = 1.0 - (degraded_metrics / total_metrics × 0.3)
```

Максимальное снижение — 30% (когда все метрики в fallback).

### `rate_limit`

Защита от перегрузок. Подробнее — [Rate Limiting](rate-limiting.md).

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

**Формат rate:** `{число}/{период}`, где период — `s` (секунда), `m` (минута), `h` (час).

Примеры: `10/s`, `100/m`, `5/h`.

**Порядок проверки:**
1. Проверка whitelist — если IP в списке, пропускаем
2. Per-IP rate limit — проверяем индивидуальный лимит
3. Global rate limit — проверяем общий лимит

Если `rate_limit` не указан или `enabled: false` — лимитирование отключено.

### `metrics`

Список метрик для расчёта health score. **Сумма weight всех метрик должна быть 1.0** (проверяется при загрузке конфига).

```yaml
metrics:
  - name: "synthetic_uptime"           # Уникальное имя метрики
    prometheus_query: 'avg(up)'        # PromQL запрос
    weight: 0.4                        # Вес в итоговом score (0.0 – 1.0)
    description: "Описание"            # Необязательное описание
    min_valid_value: 0.0               # Минимальное допустимое значение
    max_valid_value: 1.0               # Максимальное допустимое значение
```

**Расчёт health score:**

Для каждой метрики:
1. Выполняется PromQL-запрос к Prometheus
2. Полученное значение нормализуется в диапазон `[0, 1]`
3. Умножается на `weight`
4. Все взвешенные значения суммируются

**Нормализация:**

```
normalized = (value - min_valid_value) / (max_valid_value - min_valid_value)
```

Если значение меньше `min_valid_value` → `0.0`
Если значение больше `max_valid_value` → `1.0`

**Пример:** метрика `api_success_rate` с диапазоном `0.0 – 1.0` и `weight: 0.6`.
Если запрос вернул `0.95`, то вклад в score: `0.95 × 0.6 = 0.57`.

### `alerting`

Настройки алертов в Telegram.

```yaml
alerting:
  telegram:
    bot_token: "${TELEGRAM_BOT_TOKEN}"    # Токен бота (или переменная окружения)
    chat_id: "${TELEGRAM_CHAT_ID}"        # ID чата (или переменная окружения)
  prometheus_unavailable_alert_threshold: 3  # Сколько циклов подряд Prometheus недоступен
```

Алерт отправляется, когда количество последовательных неудачных запросов к Prometheus превышает `prometheus_unavailable_alert_threshold`.

Если `bot_token` или `chat_id` не указаны — алерты пишутся в лог (WARN).

**Создание бота:**
1. Написать [@BotFather](https://t.me/BotFather) в Telegram
2. Отправить `/newbot` и следовать инструкциям
3. Получить токен вида `123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11`
4. Добавить бота в группу и узнать `chat_id` (можно через `@getidsbot`)

### `logging`

```yaml
logging:
  level: "info"     # debug | info | warn | error
  format: "json"    # json | text
  service: "health-calculator"
  output: "stdout"  # stdout | stderr | file
  output_file: "/var/log/health-calc.log"  # только если output: file
```

Формат `json` — для production (легко парсить через Loki/ELK).
Формат `text` — для локальной разработки (читаемый вывод в консоль).

---

## Переменные окружения

В конфиге можно использовать переменные окружения через синтаксис `${VAR_NAME}`:

```yaml
telegram:
  bot_token: "${TELEGRAM_BOT_TOKEN}"
  chat_id: "${TELEGRAM_CHAT_ID}"
```

При загрузке конфига `${TELEGRAM_BOT_TOKEN}` заменяется на значение переменной окружения. Если переменная не задана — подставляется пустая строка.

---

## Валидация

При загрузке конфига проверяется:

1. **YAML синтаксис** — если файл некорректный, возвращается ошибка
2. **Сумма весов** — `sum(weight)` должна быть `1.0` (погрешность `±0.001`)
3. **Формат времени** — `cache_ttl`, `max_age`, `reset_timeout` и т.д. должны быть валидными duration
4. **Fallback стратегия** — только `zero`, `average`, `last_known`, `neutral`
5. **Rate limit формат** — `число/период`

Если валидация не пройдена — сервис не применяет новый конфиг и продолжает работать со старым.

---

## Hot-reload

Конфиг перечитывается каждую минуту в фоновой горутине (`watchConfig`):

```
main goroutine:    Start() → loadConfig() → ticker loop → calculateHealthScore()
watchConfig:       every 1 minute → loadConfig() → swap config under mutex
```

Логирование при hot-reload:
- Успех: `Config loaded successfully: 4 metrics, update interval: 5m0s`
- Ошибка: `Errorf("Failed to reload config: %v", err)`

---

| Назад | Дальше |
|-------|--------|
| [Установка](02-installation.md) | [Запуск и API](04-usage.md) |
