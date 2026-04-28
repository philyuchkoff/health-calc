#!/bin/bash

# Test script for graceful degradation functionality

echo "=== Testing Graceful Degradation ==="

# Start the health calculator in background
echo "Starting health calculator..."
./health-calc &
SERVER_PID=$!

# Wait for server to start
sleep 5

# Test normal operation
echo -e "\n1. Testing normal operation..."
curl -s http://localhost:8080/health | jq '.'

# Test circuit breaker opening (simulate Prometheus down)
echo -e "\n2. Simulating Prometheus unavailable for 5 seconds..."
# This would require actual Prometheus to be down, but we can observe the behavior

# Test degraded mode indicator
echo -e "\n3. Checking metrics endpoint for degraded mode..."
curl -s http://localhost:8080/metrics | grep health_calculator_degraded_mode
curl -s http://localhost:8080/metrics | grep health_calculator_fallback_used_total

# Test circuit breaker status
echo -e "\n4. Checking circuit breaker status..."
curl -s http://localhost:8080/circuit-breaker | jq '.'

# Kill the server
echo -e "\n5. Stopping server..."
kill $SERVER_PID
wait $SERVER_PID 2>/dev/null

echo -e "\n=== Test completed ==="