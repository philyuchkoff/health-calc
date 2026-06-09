> [Русская версия](ru/graceful-degradation.md)

# Graceful Degradation Implementation

> More about configuration: [Configuration → graceful_degradation](03-configuration.md#graceful_degradation)
> Back to navigation: [index](index.md)



Graceful degradation allows the service to continue operating even when some metrics are unavailable, using various fallback strategies.

## How It Works

### 1. Value Caching

- **TTL-based cache**: Metric values are cached for the duration of `cache_ttl`
- **Automatic cleanup**: Expired values are removed on each calculation
- **Maximum age**: Values older than `max_age` are not used in fallback

### 2. Fallback Strategies

When a metric is unavailable, one of the following strategies is used:

#### `zero`
- Returns 0 (minimum value)
- Suitable for critical metrics where 0 indicates a problem

#### `neutral`
- Returns 0.5 (average value)
- A neutral option that does not heavily affect the overall score

#### `average`
- Returns the midpoint of the metric's range
- Example: for a range of 0-100 returns 50

#### `last_known`
- Uses the last cached value
- If the cache is empty or expired, falls back to `neutral`

### 3. Degradation Factor

When metrics use fallback:
- A degradation factor is calculated: `1 - (degraded_metrics / total_metrics * 0.3)`
- The final score is multiplied by this factor
- Maximum 30% reduction when all metrics are in fallback

## Configuration

```yaml
graceful_degradation:
  enable_cache: true          # Enable caching
  cache_ttl: "5m"            # Value cache TTL
  max_age: "10m"             # Maximum age for last_known
  fallback_strategy: "neutral" # zero|average|last_known|neutral
```

## Monitoring

### Prometheus Metrics

- `health_calculator_degraded_mode` - 1 if the service is in degraded mode
- `health_calculator_fallback_used_total` - number of times fallback was used

### Health Endpoint

`GET /health` returns additional information:
```json
{
  "status": "degraded",
  "degraded": true,
  "reason": "some metrics are using fallback values",
  "circuit_breaker": {
    "state": "closed"
  }
}
```

## Usage Examples

### Scenario 1: Temporary Prometheus Unavailability
1. Circuit breaker opens after 3 failed requests
2. Metrics start using fallback values
3. The service continues operating with a degraded score
4. When Prometheus recovers, the service switches back to real data

### Scenario 2: Partial Metric Unavailability
1. 2 out of 4 metrics are unavailable
2. Degradation factor: `1 - (2/4 * 0.3) = 0.85`
3. Score is multiplied by 0.85
4. The service is marked as degraded but continues operating

### Scenario 3: Cache Usage
1. New requests to Prometheus succeed
2. Values are cached for 5 minutes
3. Subsequent requests use cached values
4. Fewer requests are made to Prometheus

## Advantages

1. **Fault tolerance**: The service continues operating during failures
2. **Graceful degradation**: Score decreases proportionally to issues
3. **Flexibility**: Different strategies for different metric types
4. **Transparency**: Clear indicators of degraded state
5. **Efficiency**: Cache reduces load on Prometheus

## Production Configuration

Recommended settings:
- `cache_ttl: "10m"` — longer caching for stability
- `max_age: "30m"` — use last_known values for longer
- `fallback_strategy: "last_known"` — maximize use of historical data

For critical systems:
- `fallback_strategy: "zero"` — clear indication of problems
- Smaller TTLs for faster response to changes
