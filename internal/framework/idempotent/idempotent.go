package idempotent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Guard prevents duplicate execution of operations using Redis as a dedup store.
// Aligns with Java IdempotentGuard.
type Guard struct {
	client *redis.Client
	prefix string
}

// New creates an idempotency Guard.
func New(client *redis.Client) *Guard {
	return &Guard{client: client, prefix: "idempotent:"}
}

// Check returns true if this key has not been processed yet.
// Uses SET NX to atomically check and mark.
func (g *Guard) Check(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	fullKey := g.prefix + key
	ok, err := g.client.SetNX(ctx, fullKey, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("idempotent check: %w", err)
	}
	return ok, nil
}

// Mark marks a key as processed.
func (g *Guard) Mark(ctx context.Context, key string, ttl time.Duration) error {
	fullKey := g.prefix + key
	return g.client.Set(ctx, fullKey, "1", ttl).Err()
}

// IsProcessed checks if a key has been processed.
func (g *Guard) IsProcessed(ctx context.Context, key string) (bool, error) {
	fullKey := g.prefix + key
	n, err := g.client.Exists(ctx, fullKey).Result()
	return n > 0, err
}

// Clear removes the processed marker for a key.
func (g *Guard) Clear(ctx context.Context, key string) error {
	fullKey := g.prefix + key
	return g.client.Del(ctx, fullKey).Err()
}

// Run executes fn only once per key within ttl window.
func (g *Guard) Run(ctx context.Context, key string, ttl time.Duration, fn func() error) error {
	ok, err := g.Check(ctx, key, ttl)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return fn()
}

// Key generates an idempotency key from a string.
func Key(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:16])
}
