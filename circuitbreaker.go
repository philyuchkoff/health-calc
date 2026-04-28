package main

import (
	"sync"
	"time"
)

// CircuitBreaker определяет состояния circuit breaker
type CircuitBreakerState int

const (
	StateClosed CircuitBreakerState = iota
	StateOpen
	StateHalfOpen
)

// CircuitBreaker реализует pattern circuit breaker для защиты от каскадных сбоев
type CircuitBreaker struct {
	name           string
	maxFailures    int
	resetTimeout   time.Duration
	mutex          sync.RWMutex
	state          CircuitBreakerState
	failures       int
	lastFailTime   time.Time
	onStateChange  func(name string, from, to CircuitBreakerState)
}

// NewCircuitBreaker создает новый circuit breaker
func NewCircuitBreaker(name string, maxFailures int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		name:         name,
		maxFailures:  maxFailures,
		resetTimeout: resetTimeout,
		state:        StateClosed,
	}
}

// SetStateChangeCallback устанавливает callback для уведомления об изменениях состояния
func (cb *CircuitBreaker) SetStateChangeCallback(callback func(name string, from, to CircuitBreakerState)) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	cb.onStateChange = callback
}

// Execute выполняет функцию через circuit breaker
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if !cb.allowRequest() {
		return ErrCircuitBreakerOpen
	}

	err := fn()
	cb.recordResult(err == nil)
	return err
}

// allowRequest проверяет, разрешен ли запрос
func (cb *CircuitBreaker) allowRequest() bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		// Проверяем, не прошло ли достаточно времени для перехода в half-open
		if time.Since(cb.lastFailTime) > cb.resetTimeout {
			cb.setState(StateHalfOpen)
			return true
		}
		return false
	case StateHalfOpen:
		return true
	default:
		return false
	}
}

// recordResult записывает результат выполнения
func (cb *CircuitBreaker) recordResult(success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	if success {
		cb.onSuccess()
	} else {
		cb.onFailure()
	}
}

// onSuccess обрабатывает успешное выполнение
func (cb *CircuitBreaker) onSuccess() {
	cb.failures = 0

	if cb.state == StateHalfOpen {
		cb.setState(StateClosed)
	}
}

// onFailure обрабатывает неуспешное выполнение
func (cb *CircuitBreaker) onFailure() {
	cb.failures++
	cb.lastFailTime = time.Now()

	if cb.failures >= cb.maxFailures {
		if cb.state != StateOpen {
			cb.setState(StateOpen)
		}
	}
}

// setState изменяет состояние и вызывает callback
func (cb *CircuitBreaker) setState(newState CircuitBreakerState) {
	oldState := cb.state
	cb.state = newState

	if cb.onStateChange != nil {
		go cb.onStateChange(cb.name, oldState, newState)
	}
}

// State возвращает текущее состояние circuit breaker
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.state
}

// Failures возвращает текущее количество ошибок
func (cb *CircuitBreaker) Failures() int {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.failures
}

// Reset сбрасывает circuit breaker в начальное состояние
func (cb *CircuitBreaker) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.state = StateClosed
	cb.failures = 0
	cb.lastFailTime = time.Time{}
}

// ErrCircuitBreakerOpen ошибка, возвращаемая когда circuit breaker открыт
var ErrCircuitBreakerOpen = &circuitBreakerError{
	message: "circuit breaker is open",
}

func (e *circuitBreakerError) Error() string {
	return e.message
}

type circuitBreakerError struct {
	message string
}