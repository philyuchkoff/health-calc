package main

import (
	"testing"
	"time"
)

func TestCacheValue(t *testing.T) {
	hc := &HealthCalculator{
		cachedValues: make(map[string]*CachedValue),
	}

	// Test caching
	metricName := "test_metric"
	value := 0.75
	ttl := 1 * time.Second

	hc.cacheValue(metricName, value, ttl)

	// Test retrieval
	cached, exists := hc.getCachedValue(metricName)
	if !exists {
		t.Error("Expected cached value to exist")
	}
	if cached != value {
		t.Errorf("Expected cached value %f, got %f", value, cached)
	}

	// Wait for expiration
	time.Sleep(1100 * time.Millisecond)

	// Should be expired now
	_, exists = hc.getCachedValue(metricName)
	if exists {
		t.Error("Expected cached value to be expired")
	}
}

func TestGetFallbackValue(t *testing.T) {
	hc := &HealthCalculator{
		cachedValues: make(map[string]*CachedValue),
		config: &Config{
			GracefulDeg: GracefulDegConfig{
				FallbackStrategy: FallbackStrategyNeutral,
			},
		},
		maxAgeDuration: 10 * time.Minute,
	}

	metric := Metric{
		Name:      "test_metric",
		MinValue:  0,
		MaxValue:  100,
	}

	// Test neutral strategy
	value := hc.getFallbackValue("test_metric", metric)
	if value != 0.5 {
		t.Errorf("Expected neutral fallback 0.5, got %f", value)
	}

	// Test zero strategy
	hc.config.GracefulDeg.FallbackStrategy = FallbackStrategyZero
	value = hc.getFallbackValue("test_metric", metric)
	if value != 0 {
		t.Errorf("Expected zero fallback 0, got %f", value)
	}

	// Test average strategy
	hc.config.GracefulDeg.FallbackStrategy = FallbackStrategyAverage
	value = hc.getFallbackValue("test_metric", metric)
	if value != 0.5 {
		t.Errorf("Expected average fallback 0.5, got %f", value)
	}

	// Test last known strategy with cached value
	hc.config.GracefulDeg.FallbackStrategy = FallbackStrategyLast
	hc.cacheValue("test_metric", 0.8, 5*time.Minute)
	value = hc.getFallbackValue("test_metric", metric)
	if value != 0.8 {
		t.Errorf("Expected last known fallback 0.8, got %f", value)
	}

	// Test last known strategy with expired cached value
	hc.cacheValue("test_metric2", 0.8, 1*time.Second)
	time.Sleep(1100 * time.Millisecond)
	value = hc.getFallbackValue("test_metric2", metric)
	if value != 0.5 {
		t.Errorf("Expected neutral fallback for expired cached value, got %f", value)
	}
}

func TestCleanupExpiredCache(t *testing.T) {
	hc := &HealthCalculator{
		cachedValues: make(map[string]*CachedValue),
	}

	// Add some values
	hc.cacheValue("metric1", 0.5, 1*time.Second)
	hc.cacheValue("metric2", 0.7, 5*time.Second)
	hc.cacheValue("metric3", 0.3, 1*time.Second)

	// Should have 3 values
	if len(hc.cachedValues) != 3 {
		t.Errorf("Expected 3 cached values, got %d", len(hc.cachedValues))
	}

	// Wait for some to expire
	time.Sleep(1100 * time.Millisecond)

	// Cleanup
	hc.cleanupExpiredCache()

	// Should have only 1 value left
	if len(hc.cachedValues) != 1 {
		t.Errorf("Expected 1 cached value after cleanup, got %d", len(hc.cachedValues))
	}

	// metric2 should still exist
	if _, exists := hc.cachedValues["metric2"]; !exists {
		t.Error("Expected metric2 to still exist in cache")
	}
}

func TestParseGracefulDegConfig(t *testing.T) {
	hc := &HealthCalculator{
		maxAgeDuration: 10 * time.Minute,
	}

	// Test valid config
	config := &GracefulDegConfig{
		EnableCache:      true,
		CacheTTL:         "5m",
		MaxAge:           "15m",
		FallbackStrategy: FallbackStrategyLast,
	}

	hc.parseGracefulDegConfig(config)

	// Check max age was updated
	if hc.maxAgeDuration != 15*time.Minute {
		t.Errorf("Expected max age 15m, got %v", hc.maxAgeDuration)
	}

	// Test invalid strategy
	config.FallbackStrategy = "invalid"
	hc.parseGracefulDegConfig(config)

	if config.FallbackStrategy != FallbackStrategyNeutral {
		t.Errorf("Expected strategy to be reset to neutral, got %s", config.FallbackStrategy)
	}

	// Test empty TTL config
	config2 := &GracefulDegConfig{
		EnableCache:      true,
		CacheTTL:         "",
		MaxAge:           "invalid",
		FallbackStrategy: FallbackStrategyZero,
	}

	hc.parseGracefulDegConfig(config2)
	// Should not panic and max age should reset to default (10m)
	if hc.maxAgeDuration != 10*time.Minute {
		t.Errorf("Expected max age to be reset to 10m for invalid config, got %v", hc.maxAgeDuration)
	}
}