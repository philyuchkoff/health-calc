> [Русская версия](ru/01-quickstart.md)

# Quickstart

Run Health Calculator in 5 minutes.

---

## 1. Download

```bash
git clone https://github.com/philyuchkoff/health-calc
cd health-calc
```

Or install via `go install` (requires Go 1.23+):

```bash
go install github.com/philyuchkoff/health-calc@latest
```

## 2. Configure

Copy and edit the config:

```bash
cp health-config.yaml health-config.yaml
```

Minimal working config:

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

All fields and their descriptions are in the [Configuration](03-configuration.md) section.

## 3. Run

```bash
go run .
```

The service will start on `:8080`.

## 4. Verify

```bash
# Health check
curl http://localhost:8080/health

# Prometheus metrics
curl http://localhost:8080/metrics | grep platform_health_score

# Circuit breaker status
curl http://localhost:8080/circuit-breaker
```

## 5. Docker (alternative)

```bash
docker build -t health-calculator .
docker run -p 8080:8080 \
  -v $(pwd)/health-config.yaml:/root/health-config.yaml \
  health-calculator
```

For more details on installation, see the [Installation](02-installation.md) section.

---

| Back | Next |
|------|------|
| [index](index.md) | [Installation](02-installation.md) |
