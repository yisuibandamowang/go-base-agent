package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// releaseIfStillHeld deletes the lock only when it still carries our owner
// token: after TTL expiry another holder may have taken over and releasing
// theirs would break mutual exclusion（对齐 Java Redisson isHeldByCurrentThread
// 的持有者校验，仅对异步长任务的 RunWithLock 生效）。
func (l *RedisLock) releaseIfStillHeld(ctx context.Context, key, token string) {
	script := redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if value == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)
	if _, err := script.Run(ctx, l.client, []string{key}, token).Result(); err != nil && err != redis.Nil {
		// 释放失败不影响业务结果：锁最终会因 TTL 到期自动释放
		_ = err
	}
}

// newLockToken 生成随机的锁持有者标识，值域避开 Acquire 的 "locked" 字面量。
func newLockToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("owner-%d", time.Now().UnixNano())
	}
	return "owner:" + hex.EncodeToString(buf)
}

// RunWithLock executes fn while holding the lock, releasing on completion.
// 锁值携带随机持有者标识，释放前经 Lua 脚本比对：任务超过 TTL 后锁被他人
// 接管时不会误删别人的锁。
func (l *RedisLock) RunWithLock(ctx context.Context, key string, ttl time.Duration, fn func() error) error {
	token := newLockToken()
	ok, err := l.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return fmt.Errorf("lock acquire: %w", err)
	}
	if !ok {
		return fmt.Errorf("failed to acquire lock: %s", key)
	}
	defer l.releaseIfStillHeld(context.Background(), key, token)
	return fn()
}
