package matcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrLockHeld indicates another matching-engine run currently holds the window lock.
var ErrLockHeld = errors.New("matching window lock is held by another run")

// Locker prevents two matching-engine invocations from processing the same window at once.
// This guards one whole run, not a per-batch claim: the schema has no batch linkage yet
// for payments/settlement_lines/ledger_lines (see state.md section 5.4), so this is coarser
// mutual exclusion, not the parallel per-batch coordination implementation.md describes.
type Locker interface {
	// Acquire takes the lock or returns ErrLockHeld, and returns a function that releases it.
	Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, error)
}

// RedisLocker is a SETNX-plus-TTL lock backed by the already-configured Redis client.
type RedisLocker struct {
	Client *redis.Client
}

// Acquire sets the lock key if absent and returns a release function tied to this token.
func (locker RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context) error, error) {
	token := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	acquired, err := locker.Client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	if !acquired {
		return nil, ErrLockHeld
	}
	release := func(releaseCtx context.Context) error {
		current, getErr := locker.Client.Get(releaseCtx, key).Result()
		if getErr != nil {
			return nil
		}
		if current != token {
			return nil
		}
		return locker.Client.Del(releaseCtx, key).Err()
	}
	return release, nil
}

// NoopLocker never blocks. It exists for single-instance local runs and tests where no
// concurrent second matching-engine process can race the current one.
type NoopLocker struct{}

// Acquire always succeeds immediately and returns a no-op release function.
func (NoopLocker) Acquire(context.Context, string, time.Duration) (func(context.Context) error, error) {
	return func(context.Context) error { return nil }, nil
}
