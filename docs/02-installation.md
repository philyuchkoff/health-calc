# Установка

## Содержание

- [Требования](#требования)
- [Скачивание репозитория](#скачивание-репозитория)
- [Сборка из исходников](#сборка-из-исходников)
- [Запуск через Go (dev-режим)](#запуск-через-go-dev-режим)
- [Docker](#docker)
- [Cross-compilation](#cross-compilation)
- [Проверка установки](#проверка-установки)

---

## Требования

| Компонент | Версия | Зачем |
|-----------|--------|-------|
| Go | 1.23+ | Сборка из исходников |
| Docker | 20.x+ | Контейнеризация (опционально) |
| Prometheus | любая | Источник данных |
| Telegram Bot | — | Алерты (опционально) |

## Скачивание репозитория

```bash
git clone https://github.com/your-org/health-calc.git
cd health-calc
```

После клонирования структура директории:

```
health-calc/
├── calc.go                  # Основной сервис
├── circuitbreaker.go        # Circuit breaker
├── ratelimit.go             # Rate limiting
├── logging.go               # Структурированное логирование
├── health-config.yaml       # Конфигурационный файл
├── Dockerfile               # Для сборки образа
├── go.mod / go.sum          # Go-зависимости
├── docs/                    # Документация
└── .github/workflows/       # CI
```

## Сборка из исходников

```bash
# Установить зависимости
go mod download

# Собрать бинарник
go build -ldflags="-w -s" -o health-calculator

# Проверить, что бинарник собран
./health-calculator &
```

Флаги сборки:

| Флаг | Что делает |
|------|------------|
| `-ldflags="-w -s"` | Убирает DWARF-информацию и символы — бинарник меньше на ~30% |
| `-o health-calculator` | Имя выходного файла |

## Запуск через Go (dev-режим)

Удобно для разработки и отладки:

```bash
go run .
```

При каждом изменении кода нужно перезапускать — `Ctrl+C` и снова `go run .`.

## Docker

### Сборка образа

```bash
docker build -t health-calculator .
```

Используется multi-stage сборка:

```
Stage 1 (builder):        golang:1.23-alpine → компиляция
Stage 2 (runtime):        alpine:latest → только бинарник + config
```

Итоговый образ — ~16 MB.

### Запуск контейнера

```bash
docker run -p 8080:8080 \
  -v $(pwd)/health-config.yaml:/root/health-config.yaml:ro \
  -e TELEGRAM_BOT_TOKEN="your_token" \
  -e TELEGRAM_CHAT_ID="your_chat_id" \
  health-calculator
```

Ключи:

| Параметр | Зачем |
|----------|-------|
| `-p 8080:8080` | Проброс порта |
| `-v ...:ro` | Монтируем конфиг read-only (безопасность) |
| `-e TELEGRAM_*` | Переменные окружения для Telegram |

### Docker Compose

Поднять сервис вместе с VictoriaMetrics и Grafana для тестового окружения:

```yaml
version: "3"
services:
  victoriametrics:
    image: victoriametrics/victoria-metrics:latest
    ports:
      - "8428:8428"

  health-calculator:
    build: .
    ports:
      - "8080:8080"
    environment:
      - PROMETHEUS_URL=http://victoriametrics:8428
    depends_on:
      - victoriametrics
```

## Cross-compilation

Собрать бинарник для другой платформы:

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o health-calculator-linux

# macOS arm64 (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -ldflags="-w -s" -o health-calculator-darwin-arm64

# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-w -s" -o health-calculator.exe
```

## Проверка установки

```bash
# Убедиться, что сервис отвечает
curl -s http://localhost:8080/health | jq .

# Ожидаемый ответ:
# {
#   "status": "healthy",
#   "last_successful_calculation": "2026-06-09T12:00:00Z",
#   "age": "30.5s",
#   "degraded": false,
#   "circuit_breaker": { "state": "closed" }
# }
```

Если `jq` не установлен — просто `curl` без `| jq .`.

---

| Назад | Дальше |
|-------|--------|
| [Быстрый старт](01-quickstart.md) | [Конфигурация](03-configuration.md) |
