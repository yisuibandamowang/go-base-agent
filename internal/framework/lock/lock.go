package lock

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLock is a distributed lock backed by Redis SET NX with TTL.
type RedisLock struct {
	client *redis.Client
}

// New creates a RedisLock.
func New(client *redis.Client) *RedisLock {
	return &RedisLock{client: client}
}

// Acquire attempts to acquire a distributed lock.
// Returns true if the lock was acquired, false if already held.
func (l *RedisLock) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ok, err := l.client.SetNX(ctx, key, "locked", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("lock acquire: %w", err)
	}
	return ok, nil
}

// Release releases a distributed lock.
func (l *RedisLock) Release(ctx context.Context, key string) error {
	return l.client.Del(ctx, key).Err()
}

// Extend extends the TTL of a held lock.
func (l *RedisLock) Extend(ctx context.Context, key string, ttl time.Duration) error {
	return l.client.Expire(ctx, key, ttl).Err()
}

// IsLocked checks if a lock is currently held.
func (l *RedisLock) IsLocked(ctx context.Context, key string) (bool, error) {
	n, err := l.client.Exists(ctx, key).Result()
	return n > 0, err
}

// RunWithLock executes fn while holding the lock, releasing on completion.
func (l *RedisLock) RunWithLock(ctx context.Context, key string, ttl time.Duration, fn func() error) error {
	ok, err := l.Acquire(ctx, key, ttl)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("failed to acquire lock: %s", key)
	}
	defer l.Release(context.Background(), key)
	return fn()
}
