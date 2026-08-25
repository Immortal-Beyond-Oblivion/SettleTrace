// Package ingestion implements validation and safe ingestion primitives.
package ingestion

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrInvalidSignature is returned when a webhook HMAC does not match the raw body.
var ErrInvalidSignature = errors.New("invalid webhook signature")

// DuplicateCache is a best-effort duplicate fast-path. MySQL uniqueness remains authoritative.
type DuplicateCache interface {
	Seen(ctx context.Context, key string) (bool, error)
	Mark(ctx context.Context, key string) error
}

// MemoryCache is a process-local duplicate fast-path for tests and degraded local runs.
type MemoryCache struct {
	mu    sync.Mutex
	items map[string]struct{}
}

// NewMemoryCache constructs an empty in-process duplicate cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{items: map[string]struct{}{}}
}

// Seen reports whether the key was marked after a previous successful commit.
func (cache *MemoryCache) Seen(_ context.Context, key string) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	_, exists := cache.items[key]
	return exists, nil
}

// Mark records a key only after the caller has committed the ingest transaction.
func (cache *MemoryCache) Mark(_ context.Context, key string) error {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.items[key] = struct{}{}
	return nil
}

// NoopCache always misses so tests can prove MySQL uniqueness without a Redis hit.
type NoopCache struct{}

// Seen always reports that the key is unseen.
func (NoopCache) Seen(context.Context, string) (bool, error) { return false, nil }

// Mark ignores cache writes.
func (NoopCache) Mark(context.Context, string) error { return nil }

// RedisCache stores committed idempotency keys as a duplicate fast-path.
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisCache wraps a Redis client with a bounded idempotency TTL.
func NewRedisCache(client *redis.Client, ttl time.Duration) *RedisCache {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &RedisCache{client: client, ttl: ttl}
}

// Seen returns false when Redis is unavailable so ingestion can fall back to MySQL.
func (cache *RedisCache) Seen(ctx context.Context, key string) (bool, error) {
	value, err := cache.client.Exists(ctx, cacheName(key)).Result()
	if err != nil {
		return false, nil
	}
	return value == 1, nil
}

// Mark writes the key after commit and ignores Redis outages.
func (cache *RedisCache) Mark(ctx context.Context, key string) error {
	_ = cache.client.Set(ctx, cacheName(key), "1", cache.ttl).Err()
	return nil
}

// cacheName namespaces ingest idempotency keys away from lock keys.
func cacheName(key string) string {
	return "ingest:idempotency:" + key
}
