package ai

import (
	"testing"
	"time"
)

func TestCircuitBreaker_OpensAfterConsecutiveFailures(t *testing.T) {
	breaker := NewCircuitBreaker(3, 30*time.Second)

	for i := 0; i < 3; i++ {
		if !breaker.Allow() {
			t.Fatalf("expected the breaker to allow call %d before it has tripped", i+1)
		}
		breaker.RecordFailure()
	}

	if breaker.Allow() {
		t.Fatalf("expected the breaker to be open after 3 consecutive failures")
	}
}

func TestCircuitBreaker_SuccessResetsFailureCount(t *testing.T) {
	breaker := NewCircuitBreaker(3, 30*time.Second)
	breaker.RecordFailure()
	breaker.RecordFailure()
	breaker.RecordSuccess()
	breaker.RecordFailure()
	breaker.RecordFailure()

	if !breaker.Allow() {
		t.Fatalf("expected the breaker to still be closed: only 2 consecutive failures since the reset")
	}
}

func TestCircuitBreaker_HalfOpenAfterCooldown(t *testing.T) {
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	breaker := NewCircuitBreaker(2, 10*time.Second)
	breaker.now = func() time.Time { return current }

	breaker.RecordFailure()
	breaker.RecordFailure()
	if breaker.Allow() {
		t.Fatalf("expected the breaker to be open immediately after tripping")
	}

	current = current.Add(5 * time.Second)
	if breaker.Allow() {
		t.Fatalf("expected the breaker to still be open before the cooldown elapses")
	}

	current = current.Add(10 * time.Second)
	if !breaker.Allow() {
		t.Fatalf("expected a half-open trial call to be allowed once the cooldown has elapsed")
	}
}

func TestCircuitBreaker_FailedHalfOpenTrialReopensCooldown(t *testing.T) {
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	breaker := NewCircuitBreaker(1, 10*time.Second)
	breaker.now = func() time.Time { return current }

	breaker.RecordFailure() // trips immediately, threshold=1
	current = current.Add(11 * time.Second)
	if !breaker.Allow() {
		t.Fatalf("expected the half-open trial to be allowed after the cooldown")
	}
	breaker.RecordFailure() // the trial call itself fails
	if breaker.Allow() {
		t.Fatalf("expected a failed half-open trial to reopen the breaker immediately")
	}

	current = current.Add(11 * time.Second)
	if !breaker.Allow() {
		t.Fatalf("expected the breaker to allow another trial after the reopened cooldown elapses")
	}
}

func TestCircuitBreaker_NilBreakerAlwaysAllows(t *testing.T) {
	var breaker *CircuitBreaker
	if !breaker.Allow() {
		t.Fatalf("expected a nil (unconfigured) breaker to always allow calls")
	}
	// Must not panic even though these are no-ops on a nil receiver.
	breaker.RecordSuccess()
	breaker.RecordFailure()
}
