package ai

import (
	"context"
	"testing"
	"time"
)

// fakeBudgetClient is a hand-rolled in-memory budgetClient, mirroring the fakeReconStore
// convention in internal/matcher/dispatcher_test.go: no mocking library, just a small struct
// that records what was called.
type fakeBudgetClient struct {
	totals       map[string]float64
	expireCalls  []string
	incrErr      error
	expireErr    error
}

func newFakeBudgetClient() *fakeBudgetClient {
	return &fakeBudgetClient{totals: map[string]float64{}}
}

func (fake *fakeBudgetClient) IncrByFloat(_ context.Context, key string, value float64) (float64, error) {
	if fake.incrErr != nil {
		return 0, fake.incrErr
	}
	fake.totals[key] += value
	return fake.totals[key], nil
}

func (fake *fakeBudgetClient) Expire(_ context.Context, key string, _ time.Duration) error {
	fake.expireCalls = append(fake.expireCalls, key)
	return fake.expireErr
}

// newTestBudgetTracker builds a BudgetTracker around a fake client, bypassing
// NewBudgetTracker's nil-on-no-real-Redis-client behavior so the cap/rollback logic itself can
// be tested in isolation.
func newTestBudgetTracker(client budgetClient, capUSD float64) *BudgetTracker {
	return &BudgetTracker{client: client, capUSD: capUSD, keyPrefix: "ai_budget:", ttl: time.Hour}
}

func TestBudgetTracker_CheckAndReserve_AllowsWithinCap(t *testing.T) {
	fake := newFakeBudgetClient()
	tracker := newTestBudgetTracker(fake, 2.00)

	ok, err := tracker.CheckAndReserve(context.Background(), "batch_1", 0.50)
	if err != nil {
		t.Fatalf("CheckAndReserve returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected the first 0.50 reservation against a 2.00 cap to be allowed")
	}
	if fake.totals["ai_budget:batch_1"] != 0.50 {
		t.Fatalf("expected running total 0.50, got %v", fake.totals["ai_budget:batch_1"])
	}
	if len(fake.expireCalls) != 1 {
		t.Fatalf("expected exactly one Expire call to set the counter's TTL, got %d", len(fake.expireCalls))
	}
}

func TestBudgetTracker_CheckAndReserve_RejectsAndRollsBackOverCap(t *testing.T) {
	fake := newFakeBudgetClient()
	tracker := newTestBudgetTracker(fake, 1.00)

	// Spend right up to the cap first.
	ok, err := tracker.CheckAndReserve(context.Background(), "batch_2", 1.00)
	if err != nil || !ok {
		t.Fatalf("expected the first call to exactly fill the cap to succeed, got ok=%v err=%v", ok, err)
	}

	// The next call must be rejected, and must not leave the over-the-cap reservation
	// standing -- a rejected call must be a true no-op for the batch's remaining spend.
	ok, err = tracker.CheckAndReserve(context.Background(), "batch_2", 0.30)
	if err != nil {
		t.Fatalf("CheckAndReserve returned error: %v", err)
	}
	if ok {
		t.Fatalf("expected a call that would exceed the cap to be rejected")
	}
	if fake.totals["ai_budget:batch_2"] != 1.00 {
		t.Fatalf("expected the rejected reservation to be rolled back to 1.00, got %v", fake.totals["ai_budget:batch_2"])
	}
}

func TestBudgetTracker_CheckAndReserve_BatchesAreIndependent(t *testing.T) {
	fake := newFakeBudgetClient()
	tracker := newTestBudgetTracker(fake, 1.00)

	if ok, _ := tracker.CheckAndReserve(context.Background(), "batch_A", 1.00); !ok {
		t.Fatalf("expected batch_A to fill its own cap")
	}
	ok, err := tracker.CheckAndReserve(context.Background(), "batch_B", 1.00)
	if err != nil {
		t.Fatalf("CheckAndReserve returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected batch_B's spend to be tracked independently of batch_A's")
	}
}

func TestBudgetTracker_NilTracker_AlwaysDegradesToSkip(t *testing.T) {
	var tracker *BudgetTracker
	ok, err := tracker.CheckAndReserve(context.Background(), "batch_1", 0.01)
	if err != nil {
		t.Fatalf("expected a nil tracker to never error, got %v", err)
	}
	if ok {
		t.Fatalf("expected a nil (unconfigured) tracker to always report not-ok, so the caller degrades safely")
	}
}

func TestNewBudgetTracker_ReturnsNilWithoutClientOrCap(t *testing.T) {
	if tracker := NewBudgetTracker(nil, 2.00); tracker != nil {
		t.Fatalf("expected NewBudgetTracker(nil client, ...) to return nil")
	}
	if tracker := NewBudgetTracker(nil, 0); tracker != nil {
		t.Fatalf("expected NewBudgetTracker with a zero cap to return nil")
	}
}
