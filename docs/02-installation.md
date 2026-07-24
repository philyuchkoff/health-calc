> [Русская версия](ru/02-installation.md)

# Installation

## Contents

- [Requirements](#requirements)
- [Downloading the Repository](#downloading-the-repository)
- [Building from Source](#building-from-source)
- [Running via Go (dev mode)](#running-via-go-dev-mode)
- [Docker](#docker)
- [Cross-compilation](#cross-compilation)
- [Verifying Installation](#verifying-installation)

---

## Requirements

| Component | Version | Purpose |
|-----------|---------|---------|
| Go | 1.23+ | Building from source |
| Docker | 20.x+ | Containerization (optional) |
| Prometheus | any | Data source |
| Telegram Bot | — | Alerts (optional) |

## Downloading the Repository

```bash
git clone https://github.com/philyuchkoff/health-calc
cd health-calc
```

After cloning, the directory structure:

```
health-calc/
├── calc.go                  # Core service
├── circuitbreaker.go        # Circuit breaker
├── ratelimit.go             # Rate limiting
├── logging.go               # Structured logging
├── health-config.yaml       # Configuration file
├── Dockerfile               # For building the image
├── docker-compose.yml       # Docker Compose for local dev
├── go.mod / go.sum          # Go dependencies
├── docs/                    # Documentation
└── .github/workflows/       # CI
```

## Building from Source

```bash
# Install dependencies
go mod download

# Build the binary
go build -ldflags="-w -s" -o health-calculator

# Verify the binary is built
./health-calculator &
```

Build flags:

| Flag | What it does |
|------|--------------|
| `-ldflags="-w -s"` | Strips DWARF info and symbols — binary ~30% smaller |
| `-o health-calculator` | Output file name |

## Running via Go (dev mode)

Convenient for development and debugging:

```bash
go run .
```

On every code change you need to restart — `Ctrl+C` and `go run .` again.

## Docker

### Building the Image

```bash
docker build -t health-calculator .
```

Uses multi-stage build:

```
Stage 1 (builder):        golang:1.23-alpine → compilation
Stage 2 (runtime):        alpine:latest → binary + config only
```

Final image size — ~16 MB.

### Running the Container

```bash
docker run -p 8080:8080 \
  -v $(pwd)/health-config.yaml:/root/health-config.yaml:ro \
  -e TELEGRAM_BOT_TOKEN="your_token" \
  -e TELEGRAM_CHAT_ID="your_chat_id" \
  health-calculator
```

Options:

| Parameter | Purpose |
|-----------|---------|
| `-p 8080:8080` | Port forwarding |
| `-v ...:ro` | Mount config read-only (security) |
| `-e TELEGRAM_*` | Environment variables for Telegram |

### Docker Compose

Spin up the service alongside VictoriaMetrics and Grafana for a test environment:

```bash
docker compose up -d
```

The full [docker-compose.yml](../docker-compose.yml) includes:
- **health-calculator** — the service itself (built from local Dockerfile)
- **victoriametrics** — Prometheus-compatible metrics storage
- **grafana** — pre-configured dashboards

The config file is mounted read-only from `./health-config.yaml`.

## Cross-compilation

Build a binary for another platform:

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o health-calculator-linux

# macOS arm64 (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -ldflags="-w -s" -o health-calculator-darwin-arm64

# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-w -s" -o health-calculator.exe
```

## Verifying Installation

```bash
# Ensure the service responds
curl -s http://localhost:8080/health | jq .

# Expected response:
# {
#   "status": "healthy",
#   "last_successful_calculation": "2026-06-09T12:00:00Z",
#   "age": "30.5s",
#   "degraded": false,
#   "circuit_breaker": { "state": "closed" }
# }
```

If `jq` is not installed — just `curl` without `| jq .`.

---

| Back | Next |
|------|------|
| [Quick Start](01-quickstart.md) | [Configuration](03-configuration.md) |
