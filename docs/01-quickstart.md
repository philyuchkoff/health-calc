# Быстрый старт

Запустить Health Calculator за 5 минут.

---

## 1. Скачать

```bash
git clone https://github.com/your-org/health-calc.git
cd health-calc
```

Или установить через `go install` (если есть Go 1.23+):

```bash
go install github.com/your-org/health-calc@latest
```

## 2. Настроить

Скопировать и отредактировать конфиг:

```bash
cp health-config.yaml health-config.yaml
```

Минимальный рабочий конфиг:

```yaml
update_interval: "5m"

prometheus:
  url: "http://localhost:9090"
  timeout: "10s"

metrics:
  - name: "example_uptime"
    prometheus_query: "avg(up)"
    weight: 1.0
    min_valid_value: 0.0
    max_valid_value: 1.0
```

Все поля и их описание — в разделе [Конфигурация](03-configuration.md).

## 3. Запустить

```bash
go run .
```

Сервис запустится на `:8080`.

## 4. Проверить

```bash
# Health check
curl http://localhost:8080/health

# Prometheus metrics
curl http://localhost:8080/metrics | grep platform_health_score

# Circuit breaker status
curl http://localhost:8080/circuit-breaker
```

## 5. Docker (альтернативный способ)

```bash
docker build -t health-calculator .
docker run -p 8080:8080 \
  -v $(pwd)/health-config.yaml:/root/health-config.yaml \
  health-calculator
```

Подробнее про установку — в разделе [Установка](02-installation.md).

---

| Назад | Дальше |
|-------|--------|
| [index](index.md) | [Установка](02-installation.md) |
