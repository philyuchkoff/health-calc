# Health Calculator — документация

**Health Calculator** — это сервис на Go, который вычисляет агрегированный показатель «здоровья» платформы (`platform_health_score`) на основе метрик из Prometheus. Используется для SLO-мониторинга, синтетического алертинга и визуализации в Grafana.

---

## Навигация

### Для начинающих

| Раздел | О чём |
|--------|-------|
| [Быстрый старт](01-quickstart.md) | Запустить сервис за 5 минут |
| [Установка](02-installation.md) | Скачать, собрать, установить (локально / Docker) |
| [Конфигурация](03-configuration.md) | Полный разбор health-config.yaml |

### Для ежедневной работы

| Раздел | О чём |
|--------|-------|
| [Запуск и API](04-usage.md) | Запуск, endpoints, примеры curl |
| [Production-эксплуатация](05-operations.md) | Мониторинг, алерты, K8s, типовые сценарии |
| [Решение проблем](06-troubleshooting.md) | FAQ, частые ошибки, диагностика |

### Глубокое погружение в механизмы

| Раздел | О чём |
|--------|-------|
| [Circuit Breaker](circuit-breaker.md) | Защита от каскадных сбоев |
| [Graceful Degradation](graceful-degradation.md) | Работа при недоступности метрик |
| [Rate Limiting](rate-limiting.md) | Защита от перегрузок |

---

## Быстрая карта

```
┌──────────────────────────────────────────────────┐
│                  health-calc                      │
├──────────────────────────────────────────────────┤
│  GET /metrics       → Prometheus metrics          │
│  GET /health        → Liveness probe (K8s)        │
│  GET /ready         → Readiness probe (K8s)       │
│  GET /circuit-breaker → Circuit breaker status    │
│  GET /              → Status page                 │
├──────────────────────────────────────────────────┤
│  periodic → query Prometheus → calculate score    │
│          → cache values → expose metrics          │
└──────────────────────────────────────────────────┘
```

## Требования

- Go 1.23+ (для сборки из исходников)
- Docker (опционально, для контейнеризации)
- Prometheus (источник метрик)
- Telegram bot token (опционально, для алертов)

---

К началу: [index](index.md)
