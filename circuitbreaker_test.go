package main

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker("test", 3, 100*time.Millisecond)

	// Initially closed
	if cb.State() != StateClosed {
		t.Errorf("Expected initial state to be Closed, got %v", cb.State())
	}

	// Simulate failures
	failFunc := func() error {
		return errors.New("test error")
	}

	// First failure - should not trip
	for i := 0; i < 2; i++ {
		err := cb.Execute(failFunc)
		if err == nil {
			t.Errorf("Expected error for failure %d", i+1)
		}
		if cb.State() != StateClosed {
			t.Errorf("Expected state to remain Closed after %d failures", i+1)
		}
	}

	// Third failure - should trip
	err := cb.Execute(failFunc)
	if err == nil {
		t.Error("Expected error for third failure")
	}
	if cb.State() != StateOpen {
		t.Error("Expected circuit breaker to be Open after 3 failures")
	}

	// Try to execute while open - should fail fast
	err = cb.Execute(failFunc)
	if err != ErrCircuitBreakerOpen {
		t.Errorf("Expected ErrCircuitBreakerOpen, got %v", err)
	}

	// Wait for reset timeout
	time.Sleep(150 * time.Millisecond)

	// Successful execution should close the circuit
	successFunc := func() error {
		return nil
	}
	err = cb.Execute(successFunc)
	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}

	// After successful execution in half-open, should be closed
	if cb.State() != StateClosed {
		t.Error("Expected circuit breaker to be Closed after successful execution")
	}

	// Reset the circuit breaker
	cb.Execute(failFunc)
	cb.Execute(failFunc)
	cb.Execute(failFunc)
	if cb.State() != StateOpen {
		t.Error("Expected circuit breaker to be Open before reset")
	}

	cb.Reset()
	if cb.State() != StateClosed {
		t.Errorf("Expected state to be Closed after reset, got %v", cb.State())
	}
	if cb.Failures() != 0 {
		t.Errorf("Expected 0 failures after reset, got %d", cb.Failures())
	}
}

func TestCircuitBreakerStateChangeCallback(t *testing.T) {
	cb := NewCircuitBreaker("test", 2, 50*time.Millisecond)

	callbackCalled := false
	var fromState, toState CircuitBreakerState

	cb.SetStateChangeCallback(func(name string, from, to CircuitBreakerState) {
		callbackCalled = true
		fromState = from
		toState = to
	})

	// Trip the circuit breaker
	failFunc := func() error {
		return errors.New("test error")
	}

	cb.Execute(failFunc)
	cb.Execute(failFunc) // This should trip it

	// Wait a bit for async callback
	time.Sleep(10 * time.Millisecond)

	if !callbackCalled {
		t.Error("State change callback was not called")
	}
	if fromState != StateHalfOpen && fromState != StateClosed {
		t.Errorf("Expected from state to be Closed or HalfOpen, got %v", fromState)
	}
	if toState != StateOpen {
		t.Errorf("Expected to state to be Open, got %v", toState)
	}
}