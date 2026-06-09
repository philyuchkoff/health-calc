> [Русская версия](ru/rate-limiting.md)

# Rate Limiting Implementation

> See also: [Configuration → rate_limit](03-configuration.md#rate_limit)
> Back to navigation: [index](index.md)



## Overview

Rate limiting protects the service from overload and abuse. It uses the leaky bucket algorithm with configurable limits.

## How It Works

### 1. Leaky Bucket Algorithm

- Each IP and/or endpoint has its own bucket with tokens
- A request consumes one token
- Tokens replenish at a constant rate
- When all tokens are exhausted, requests are blocked

### 2. Limit Levels

**Global Rate Limiter**
- Applied to all requests on an endpoint regardless of IP
- Useful for DDoS attack protection

**Per-IP Rate Limiter**
- Applied individually to each IP address
- Allows different clients to have their own limits

### 3. Whitelist

IP addresses in the whitelist bypass rate limiting:
- Local addresses (127.0.0.1, ::1) by default
- Any IP can be added in the config

## Configuration

```yaml
rate_limit:
  enabled: true                    # Enable/disable rate limiting
  global_rate:                     # Global rate limits
    "/metrics": "100/m"            # 100 requests per minute
    "/health": "60/m"              # 60 requests per minute
  per_ip_rate:                     # Per-IP rate limits
    "/health": "10/m"              # 10 requests per minute per IP
    "/circuit-breaker": "20/m"     # 20 requests per minute per IP
  whitelist:                        # Excluded IPs
    - "127.0.0.1"
    - "::1"
    - "10.0.0.0/8"
```

### Rate Format

Format: `requests/period`

- `10/s` - 10 requests per second
- `100/m` - 100 requests per minute
- `5/h` - 5 requests per hour

## Middleware Integration

Rate limiting is applied via middleware to HTTP handlers:

```go
mux.HandleFunc("/health", calculator.wrapWithRateLimit(calculator.healthHandler))
```

- `/metrics` endpoint has no rate limiting
- All other endpoints are protected

## When Limit Is Exceeded

Returns HTTP 429 Too Many Requests:
```json
{
  "error": "rate_limit_exceeded",
  "message": "Too many requests. Please try again later.",
  "endpoint": "/health"
}
```

Headers:
- `X-RateLimit-Limit` — allowed request count
- `X-RateLimit-Remaining` — remaining requests
- `X-RateLimit-Reset` — limit reset time

## Monitoring

### Prometheus Metrics

- `health_calculator_rate_limit_exceeded_total` — number of blocked requests
- `health_calculator_active_rate_limit_clients` — active clients (buckets)

### Logging

Every blocked request is logged:
```
Rate limit exceeded for IP 192.168.1.1 on endpoint /health
```

## Optimization

### Automatic Cleanup

Inactive buckets are removed after 5 minutes to save memory.

### Proxy Support

The middleware correctly handles:
- `X-Forwarded-For` header
- `X-Real-IP` header
- `RemoteAddr` as fallback

## Usage Examples

### 1. Protecting an API Endpoint

```yaml
per_ip_rate:
  "/api/v1/data": "10/m"  # Clients cannot spam the API
```

### 2. Limiting Health Checks

```yamlper_ip_rate:
  "/health": "1/m"         # 1 health check per minute per service
```

### 3. Excluding Monitoring

```yaml
whitelist:
  - "10.0.0.0/8"          # Internal network without limits
```

## Production Configuration

Recommended values:

**For public API:**
```yaml
global_rate:
  "/": "1000/m"             # Global protection
per_ip_rate:
  "/api/": "100/m"          # Reasonable per-client limits
```

**For internal services:**
```yaml
whitelist:
  - "10.0.0.0/8"           # Entire internal network in whitelist
```

**For metrics endpoint:**
- No rate limiting (authentication only)
- Rate limiting at the proxy/ingress level
