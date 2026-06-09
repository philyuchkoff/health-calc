> [Русская версия](ru/index.md)

# Health Calculator — Documentation

**Health Calculator** is a Go service that computes an aggregated platform health score (`platform_health_score`) based on Prometheus metrics. It is used for SLO monitoring, synthetic alerting, and visualization in Grafana.

---

## Navigation

### For beginners

| Section | Description |
|---------|-------------|
| [Quick Start](01-quickstart.md) | Run the service in 5 minutes |
| [Installation](02-installation.md) | Download, build, install (local / Docker) |
| [Configuration](03-configuration.md) | Full breakdown of health-config.yaml |

### For day-to-day operations

| Section | Description |
|---------|-------------|
| [Running and API](04-usage.md) | Startup, endpoints, curl examples |
| [Production Operations](05-operations.md) | Monitoring, alerts, K8s, common scenarios |
| [Troubleshooting](06-troubleshooting.md) | FAQ, common errors, diagnostics |

### Deep dive into internals

| Section | Description |
|---------|-------------|
| [Circuit Breaker](circuit-breaker.md) | Protection against cascading failures |
| [Graceful Degradation](graceful-degradation.md) | Operating when metrics are unavailable |
| [Rate Limiting](rate-limiting.md) | Overload protection |

---

## Quick map

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

## Requirements

- Go 1.23+ (for building from source)
- Docker (optional, for containerization)
- Prometheus (metric source)
- Telegram bot token (optional, for alerts)

---

Back to top: [index](index.md)
