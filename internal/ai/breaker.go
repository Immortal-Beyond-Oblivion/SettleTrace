package ai

import (
	"sync"
	"time"
)

// CircuitBreaker opens after a configurable number of consecutive LLM failures and stays open
// for a cooldown period, per implementation.md section 24's "3 consecutive failures" threshold
// (the exact behavior that document's manual smoke test exercises by forcing four calls
// against an invalid API key and expecting the fourth to skip the LLM call entirely).
type CircuitBreaker struct {
	failureThreshold int
	cooldown         time.Duration
	now              func() time.Time

	mu              sync.Mutex
	consecutiveFail int
	openedAt        time.Time
}

// NewCircuitBreaker returns a breaker with the given threshold and cooldown, defaulting to
// implementation.md's 3-failure threshold and a conservative 30s cooldown when zero values are
// passed.
func NewCircuitBreaker(failureThreshold int, cooldown time.Duration) *CircuitBreaker {
	if failureThreshold <= 0 {
		failureThreshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		cooldown:         cooldown,
		now:              func() time.Time { return time.Now().UTC() },
	}
}

// Allow reports whether a call should be attempted right now. A nil breaker always allows the
// call, matching every other guardrail's "not configured degrades to no guardrail, never a
// block" convention in this package. Once the breaker has tripped, it only allows a call again
// after the cooldown has elapsed -- that next call is a half-open trial, and its own outcome
// (recorded via RecordSuccess/RecordFailure) decides whether the breaker closes again or
// reopens.
func (breaker *CircuitBreaker) Allow() bool {
	if breaker == nil {
		return true
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.consecutiveFail < breaker.failureThreshold {
		return true
	}
	return breaker.now().Sub(breaker.openedAt) >= breaker.cooldown
}

// RecordSuccess resets the failure count, closing the breaker.
func (breaker *CircuitBreaker) RecordSuccess() {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.consecutiveFail = 0
}

// RecordFailure increments the consecutive-failure count and, once it reaches the threshold
// (including a failed half-open trial), (re)starts the cooldown window from now.
func (breaker *CircuitBreaker) RecordFailure() {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	breaker.consecutiveFail++
	if breaker.consecutiveFail >= breaker.failureThreshold {
		breaker.openedAt = breaker.now()
	}
}
