# Circuit Breaker Implementation

## Overview

Circuit breaker паттерн внедрен для защиты сервиса от каскадных сбоев при недоступности Prometheus. Он предотвращает бесконечные попытки подключения к неработающему сервису.

## Как это работает

### Состояния Circuit Breaker

1. **Closed** (Закрыт) - Нормальное состояние
   - Все запросы проходят к Prometheus
   - Ошибки подсчитываются
   - При достижении порога ошибок переходит в Open

2. **Open** (Открыт) - Защита от сбоев
   - Запросы к Prometheus блокируются
   - Сразу возвращается fallback значение (0.5)
   - Через reset_timeout переходит в Half-Open

3. **Half-Open** (Полуоткрыт) - Проверка восстановления
   - Один запрос разрешен
   - При успехе переходит в Closed
   - При ошибке возвращается в Open

### Конфигурация

```yaml
circuit_breaker:
  max_failures: 3          # Сколько ошибок перед открытием
  reset_timeout: "30s"     # Как долго оставаться открытым
```

### Fallback стратегия

При открытом circuit breaker:
- Возвращается нейтральное значение `0.5` для всех метрик
- Это позволяет сервису продолжать работу с деградированным функционалом

## Мониторинг

### Метрики

- `health_calculator_circuit_breaker_tripped_total` - сколько раз circuit breaker открывался

### Endpoints

- `GET /circuit-breaker` - текущее состояние:
```json
{
  "name": "prometheus",
  "state": "closed",
  "failures": 0
}
```

### Логирование

При изменении состояния автоматически логируется:
```
Circuit breaker 'prometheus' changed state from closed to open
```

## Использование в коде

```go
// Обернуть вызов в circuit breaker
err := hc.circuitBreaker.Execute(func() error {
    value, err := hc.queryPrometheus(query)
    if err != nil {
        return err
    }
    // обработка результата
    return nil
})

if err == ErrCircuitBreakerOpen {
    // Использовать fallback значение
    return 0.5, nil
}
```

## Преимущества

1. **Защита от каскадных сбоев** - не истощает ресурсы при недоступности Prometheus
2. **Быстрое реагирование** - не ждет таймаутов при открытом состоянии
3. **Автоматическое восстановление** - периодически проверяет доступность сервиса
4. **Наблюдаемость** - полное visibility через метрики и эндпоинты

## Настройка для production

Рекомендуемые значения:
- `max_failures: 5` - больше терпимости к временным сбоям
- `reset_timeout: "60s"` - дольше ждать восстановления

Для high-load систем:
- Уменьшить `reset_timeout` для быстрого восстановления
- Увеличить `max_failures` для защиты от флапов