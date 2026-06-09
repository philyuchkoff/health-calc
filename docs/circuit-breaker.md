> [Русская версия](ru/circuit-breaker.md)

# Circuit Breaker Implementation

> More about configuration: [Configuration → circuit_breaker](03-configuration.md#circuit_breaker)
> Back to navigation: [index](index.md)

The Circuit Breaker pattern is implemented to protect the service from cascading failures when Prometheus is unavailable. It prevents infinite connection attempts to a non-working service.

## How It Works

### Circuit Breaker States

1. **Closed** - Normal state
   - All requests pass to Prometheus
   - Errors are counted
   - When the error threshold is reached, transitions to Open

2. **Open** - Failure protection
   - Requests to Prometheus are blocked
   - Falls back immediately with value 0.5
   - After reset_timeout, transitions to Half-Open

3. **Half-Open** - Recovery check
   - One request is allowed
   - On success, transitions to Closed
   - On failure, returns to Open

### Configuration

```yaml
circuit_breaker:
  max_failures: 3          # Number of failures before opening
  reset_timeout: "30s"     # How long to stay open
```

### Fallback Strategy

When the circuit breaker is open:
- Returns neutral value `0.5` for all metrics
- This allows the service to continue with degraded functionality

## Monitoring

### Metrics

- `health_calculator_circuit_breaker_tripped_total` - how many times the circuit breaker was opened

### Endpoints

- `GET /circuit-breaker` - current state:
```json
{
  "name": "prometheus",
  "state": "closed",
  "failures": 0
}
```

### Logging

State changes are logged automatically:
```
Circuit breaker 'prometheus' changed state from closed to open
```

## Usage in Code

```go
// Wrap a call in circuit breaker
err := hc.circuitBreaker.Execute(func() error {
    value, err := hc.queryPrometheus(query)
    if err != nil {
        return err
    }
    // process result
    return nil
})

if err == ErrCircuitBreakerOpen {
    // Use fallback value
    return 0.5, nil
}
```

## Advantages

1. **Protection from cascading failures** - does not exhaust resources when Prometheus is unavailable
2. **Fast response** - does not wait for timeouts in the open state
3. **Automatic recovery** - periodically checks service availability
4. **Observability** - full visibility through metrics and endpoints

## Production Configuration

Recommended values:
- `max_failures: 5` - more tolerance for temporary failures
- `reset_timeout: "60s"` - longer wait for recovery

For high-load systems:
- Reduce `reset_timeout` for faster recovery
- Increase `max_failures` to protect against flapping
