package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	tests := []struct {
		input    string
		req      int
		duration time.Duration
		err      bool
	}{
		{"100/m", 100, time.Minute, false},
		{"10/s", 10, time.Second, false},
		{"5/h", 5, time.Hour, false},
		{"invalid", 0, 0, true},
		{"10/x", 0, 0, true},
	}

	for _, test := range tests {
		req, dur, err := ParseRate(test.input)
		if test.err {
			if err == nil {
				t.Errorf("Expected error for input %s", test.input)
			}
		} else {
			if err != nil {
				t.Errorf("Unexpected error for input %s: %v", test.input, err)
			}
			if req != test.req {
				t.Errorf("Expected requests %d for input %s, got %d", test.req, test.input, req)
			}
			if dur != test.duration {
				t.Errorf("Expected duration %v for input %s, got %v", test.duration, test.input, dur)
			}
		}
	}
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		remote   string
		expected string
	}{
		{
			name: "X-Forwarded-For",
			headers: map[string]string{
				"X-Forwarded-For": "192.168.1.1, 10.0.0.1",
			},
			remote:   "127.0.0.1:12345",
			expected: "192.168.1.1",
		},
		{
			name: "X-Real-IP",
			headers: map[string]string{
				"X-Real-IP": "192.168.1.2",
			},
			remote:   "127.0.0.1:12345",
			expected: "192.168.1.2",
		},
		{
			name:     "RemoteAddr",
			headers:  map[string]string{},
			remote:   "192.168.1.3:12345",
			expected: "192.168.1.3",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = test.remote
			for k, v := range test.headers {
				req.Header.Set(k, v)
			}

			ip := GetClientIP(req)
			if ip != test.expected {
				t.Errorf("Expected IP %q, got %q", test.expected, ip)
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	config := RateLimitConfig{
		Enabled: true,
		PerIPRate: map[string]string{
			"/test": "2/s",
		},
	}

	rl := NewRateLimiter(config)

	// Create test requests
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.1:12345"

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.2:12345"

	// First request from each IP should be allowed
	if !rl.IsAllowed(req1, "/test") {
		t.Error("First request from IP1 should be allowed")
	}
	if !rl.IsAllowed(req2, "/test") {
		t.Error("First request from IP2 should be allowed")
	}

	// Second requests should be allowed
	if !rl.IsAllowed(req1, "/test") {
		t.Error("Second request from IP1 should be allowed")
	}
	if !rl.IsAllowed(req2, "/test") {
		t.Error("Second request from IP2 should be allowed")
	}

	// Third requests should be denied
	if rl.IsAllowed(req1, "/test") {
		t.Error("Third request from IP1 should be denied")
	}
	if rl.IsAllowed(req2, "/test") {
		t.Error("Third request from IP2 should be denied")
	}
}

func TestBucket(t *testing.T) {
	bucket := &Bucket{
		capacity:   5,
		tokens:     5,
		refillRate: 5, // 5 tokens per second
		lastRefill: time.Now(),
	}

	// Use all tokens
	for i := 0; i < 5; i++ {
		if !bucket.AllowNext() {
			t.Errorf("Request %d should be allowed", i+1)
		}
	}

	// Next should be denied
	if bucket.AllowNext() {
		t.Error("Request should be denied when no tokens")
	}

	// Wait for refill
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again
	if !bucket.AllowNext() {
		t.Error("Request should be allowed after refill")
	}
}

func TestRateLimiterWhitelist(t *testing.T) {
	config := RateLimitConfig{
		Enabled: true,
		PerIPRate: map[string]string{
			"/test": "1/s",
		},
		Whitelist: []string{"192.168.1.100"},
	}

	rl := NewRateLimiter(config)

	// Whitelisted IP
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"

	for i := 0; i < 10; i++ {
		if !rl.IsAllowed(req, "/test") {
			t.Errorf("Whitelisted IP request %d should always be allowed", i+1)
		}
	}

	// Non-whitelisted IP
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.200:12345"

	// First should be allowed
	if !rl.IsAllowed(req2, "/test") {
		t.Error("First request from non-whitelisted IP should be allowed")
	}

	// Second should be denied
	if rl.IsAllowed(req2, "/test") {
		t.Error("Second request from non-whitelisted IP should be denied")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	config := RateLimitConfig{
		Enabled: true,
		PerIPRate: map[string]string{
			"/test": "1/s",
		},
	}

	rl := NewRateLimiter(config)

	// Test handler
	handlerCalled := false
	next := func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}

	// Create middleware
	middleware := RateLimitMiddleware(rl, nil, next)

	// First request
	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.1:12345"

	middleware(w1, req1)

	if !handlerCalled {
		t.Error("Handler should be called for first request")
	}
	if w1.Code != http.StatusOK {
		t.Errorf("Expected status 200 for first request, got %d", w1.Code)
	}

	// Reset
	handlerCalled = false

	// Second request
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.1:12345"

	middleware(w2, req2)

	if handlerCalled {
		t.Error("Handler should not be called for second request")
	}
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("Expected status 429 for second request, got %d", w2.Code)
	}

	// Check response body
	if !strings.Contains(w2.Body.String(), "rate_limit_exceeded") {
		t.Error("Response should contain rate_limit_exceeded error")
	}
}