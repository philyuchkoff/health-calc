# Rate Limiting Implementation

## Обзор

Rate limiting реализован для защиты сервиса от перегрузки и злоупотреблений. Использует leaky bucket алгоритм с настраиваемыми лимитами.

## Как это работает

### 1. Leaky Bucket Algorithm

- Каждый IP и/или endpoint имеет свой bucket с токенами
- Запрос потребляет токен
- Токены восполняются с постоянной скоростью
- Когда все токены израсходованы - запросы блокируются

### 2. Уровни лимитов

**Global Rate Limiter**
- Применяется ко всем запросам на endpoint независимо от IP
- Полезно для защиты от DDoS атак

**Per-IP Rate Limiter**
- Применяется индивидуально к каждому IP адресу
- Позволяет различным клиентам иметь свои лимиты

### 3. Whitelist

IP адреса в whitelist игнорируют rate limiting:
- Локальные адреса (127.0.0.1, ::1) по умолчанию
- Можно добавить любые IP в конфиге

## Configuration

```yaml
rate_limit:
  enabled: true                    # Включить/выключить rate limiting
  global_rate:                     # Глобальные лимиты
    "/metrics": "100/m"            # 100 запросов в минуту
    "/health": "60/m"              # 60 запросов в минуту
  per_ip_rate:                     # Лимиты на IP
    "/health": "10/m"              # 10 запросов в минуту на IP
    "/circuit-breaker": "20/m"     # 20 запросов в минуту на IP
  whitelist:                        # Исключенные IP
    - "127.0.0.1"
    - "::1"
    - "10.0.0.0/8"
```

### Формат Rate

Формат: `requests/period`

- `10/s` - 10 запросов в секунду
- `100/m` - 100 запросов в минуту
- `5/h` - 5 запросов в час

## Middleware Integration

Rate limiting применяется через middleware к HTTP handlers:

```go
mux.HandleFunc("/health", calculator.wrapWithRateLimit(calculator.healthHandler))
```

- `/metrics` endpoint не имеет rate limiting
- Все остальные endpoints защищены

## При превышении лимита

Возвращается HTTP 429 Too Many Requests:
```json
{
  "error": "rate_limit_exceeded",
  "message": "Too many requests. Please try again later.",
  "endpoint": "/health"
}
```

Headers:
- `X-RateLimit-Limit` - допустимое количество
- `X-RateLimit-Remaining` - оставшиеся запросы
- `X-RateLimit-Reset` - время сброса лимита

## Мониторинг

### Prometheus метрики

- `health_calculator_rate_limit_exceeded_total` - количество заблокированных запросов
- `health_calculator_active_rate_limit_clients` - активных клиентов (bucket'ов)

### Логирование

Каждый заблокированный запрос логируется:
```
Rate limit exceeded for IP 192.168.1.1 on endpoint /health
```

## Оптимизация

### Автоматическая очистка

Неактивные bucket'ы удаляются через 5 минут для экономии памяти.

### Поддержка прокси

Middleware корректно обрабатывает:
- `X-Forwarded-For` header
- `X-Real-IP` header
- `RemoteAddr` как fallback

## Примеры использования

### 1. Защита API endpoint

```yaml
per_ip_rate:
  "/api/v1/data": "10/m"  # Клиенты не могут спамить API
```

### 2. Ограничение health checks

```yamlper_ip_rate:
  "/health": "1/m"         # 1 health check в минуту на сервис
```

### 3. Исключение мониторинга

```yaml
whitelist:
  - "10.0.0.0/8"          # Внутренняя сеть без лимитов
```

## Настройка для production

Рекомендуемые значения:

**Для public API:**
```yaml
global_rate:
  "/": "1000/m"             # Глобальная защита
per_ip_rate:
  "/api/": "100/m"          # Разумные лимиты на клиента
```

**Для internal сервисов:**
```yaml
whitelist:
  - "10.0.0.0/8"           # Вся внутренняя сеть в whitelist
```

**Для metrics endpoint:**
- Без rate limiting (только аутентификация)
- Rate limiting на уровне прокси/ingress