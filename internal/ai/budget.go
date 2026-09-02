// Package ai implements the tightly-guardrailed AI explainer described in architecture.md
// section 9-11 and implementation.md section 8/24: a read-only, evidence-bound layer that
// explains reconciliation exceptions in plain language, never diagnoses from scratch, and is
// wrapped in a hard per-batch spend cap and a circuit breaker so an LLM outage or a runaway
// prompt can never block or bankrupt a batch run.
package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// budgetClient is the minimal Redis surface BudgetTracker needs. It is kept as an interface,
// separate from *redis.Client, so the cap/rollback arithmetic below can be unit-tested
// against a hand-rolled fake (see budget_test.go) rather than a real Redis server -- the same
// "test the logic separately from the transport" discipline internal/store/guard.go and
// internal/audit/hash.go already follow.
type budgetClient interface {
	IncrByFloat(ctx context.Context, key string, value float64) (float64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
}

// redisBudgetClient adapts a real *redis.Client to budgetClient.
type redisBudgetClient struct{ client *redis.Client }

func (adapter redisBudgetClient) IncrByFloat(ctx context.Context, key string, value float64) (float64, error) {
	return adapter.client.IncrByFloat(ctx, key, value).Result()
}

func (adapter redisBudgetClient) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return adapter.client.Expire(ctx, key, ttl).Err()
}

// BudgetTracker enforces a per-batch AI spend cap using Redis INCRBYFLOAT, exactly as
// implementation.md section 8 and section 11 describe: the cap is on total spend per
// batch_run_id, not per call, so it must be shared and atomic across however many exception
// explanations one batch run triggers, including concurrently.
type BudgetTracker struct {
	client    budgetClient
	capUSD    float64
	keyPrefix string
	ttl       time.Duration
}

// NewBudgetTracker builds a Redis-backed tracker, or returns nil when no Redis client or no
// positive cap is configured. Returning nil here (rather than a tracker that always errors)
// lets every caller treat "not configured" exactly like Tier3Client's nil-client convention
// (internal/matcher/tier3.go): CheckAndReserve on a nil *BudgetTracker degrades to "skip AI for
// this call," never a fatal error, matching the "AI budget/circuit issues must never fail the
// batch" requirement.
func NewBudgetTracker(client *redis.Client, capUSD float64) *BudgetTracker {
	if client == nil || capUSD <= 0 {
		return nil
	}
	return &BudgetTracker{
		client:    redisBudgetClient{client: client},
		capUSD:    capUSD,
		keyPrefix: "ai_budget:",
		ttl:       24 * time.Hour,
	}
}

// CheckAndReserve atomically adds estCostUSD to the named batch's running total and reports
// whether the batch is still within its cap. A nil tracker always reports "not ok, no error" --
// the caller (Explainer.Explain) treats that identically to an explicit over-budget result: the
// explanation is skipped, the exception keeps its reason code, and nothing about the batch run
// fails.
func (tracker *BudgetTracker) CheckAndReserve(ctx context.Context, batchRunID string, estCostUSD float64) (bool, error) {
	if tracker == nil {
		return false, nil
	}
	if batchRunID == "" {
		return false, fmt.Errorf("budget check requires a non-empty batch run id")
	}
	key := tracker.keyPrefix + batchRunID
	total, err := tracker.client.IncrByFloat(ctx, key, estCostUSD)
	if err != nil {
		return false, fmt.Errorf("incr ai budget: %w", err)
	}
	if tracker.ttl > 0 {
		// Best-effort: a failed Expire call must not turn an otherwise-successful budget
		// check into an error, since the counter is a soft spend guard, not a source of
		// truth (that role belongs to ai_explanation_log, which is written unconditionally).
		_ = tracker.client.Expire(ctx, key, tracker.ttl)
	}
	if total > tracker.capUSD {
		// Roll back the reservation this call just made -- an over-budget check must be a
		// true no-op for the batch's remaining spend, never a silent charge for an
		// explanation that was never actually produced.
		_, _ = tracker.client.IncrByFloat(ctx, key, -estCostUSD)
		return false, nil
	}
	return true, nil
}
