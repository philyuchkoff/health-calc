#!/bin/bash

# Test script for rate limiting functionality

echo "=== Testing Rate Limiting ==="

# Start the health calculator in background
echo "Starting health calculator..."
./health-calc &
SERVER_PID=$!

# Wait for server to start
sleep 5

# Test normal requests (should work)
echo -e "\n1. Testing normal requests..."
for i in {1..3}; do
  curl -s -w "HTTP %{http_code}\n" http://localhost:8080/health | tail -1
done

# Test rate limit exceeded (should be 429 after 10 requests)
echo -e "\n2. Testing rate limit exceeding (max 10 req/min per IP)..."
for i in {1..15}; do
  status=$(curl -s -w "%{http_code}" http://localhost:8080/health -o /dev/null)
  echo "Request $i: HTTP $status"
  if [ "$status" = "429" ]; then
    echo "Rate limit triggered at request $i"
    break
  fi
  # Small delay between requests
  sleep 0.1
done

# Test metrics endpoint (no rate limit)
echo -e "\n3. Testing /metrics endpoint (no rate limit)..."
for i in {1..5}; do
  status=$(curl -s -w "%{http_code}" http://localhost:8080/metrics -o /dev/null)
  echo "Request $i: HTTP $status"
done

# Check rate limit metrics
echo -e "\n4. Checking rate limit metrics..."
curl -s http://localhost:8080/metrics | grep "health_calculator_rate_limit"

# Kill the server
echo -e "\n5. Stopping server..."
kill $SERVER_PID
wait $SERVER_PID 2>/dev/null

echo -e "\n=== Test completed ==="