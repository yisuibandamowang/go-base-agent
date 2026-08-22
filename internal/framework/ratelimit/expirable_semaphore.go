package ratelimit

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

var expirableSemaphoreAcquireLua = redis.NewScript(`
local now = tonumber(ARGV[1])
local lease = tonumber(ARGV[2])
local max = tonumber(ARGV[3])
local key = KEYS[1]

redis.call('ZREMRANGEBYSCORE', key, '-inf', now)
if redis.call('ZCARD', key) < max then
  redis.call('ZADD', key, now + lease, ARGV[4])
  return ARGV[4]
end
return ''
`)

var expirableSemaphoreTokenSeq atomic.Uint64

// ExpirableSemaphore 是基于 Redis 有租约许可的跨实例信号量。
type ExpirableSemaphore struct {
	name string
	rdb  *redis.Client
	max  int
}

// NewExpirableSemaphore 创建一个跨实例共享的有租约信号量。
func NewExpirableSemaphore(name string, rdb *redis.Client, max int) *ExpirableSemaphore {
	return &ExpirableSemaphore{name: name, rdb: rdb, max: max}
}

// Acquire 获取一个许可；许可在 lease 到期后自动失效。
func (s *ExpirableSemaphore) Acquire(ctx context.Context, maxWait, lease time.Duration) (string, error) {
	if s == nil || s.rdb == nil {
		return "", fmt.Errorf("expirable semaphore is not configured")
	}
	if s.max <= 0 {
		return "", fmt.Errorf("expirable semaphore max permits must be positive")
	}
	if lease <= 0 {
		return "", fmt.Errorf("expirable semaphore lease must be positive")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	deadline := time.Now().Add(maxWait)
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), expirableSemaphoreTokenSeq.Add(1))
	for {
		permit, err := expirableSemaphoreAcquireLua.Run(ctx, s.rdb, []string{s.name},
			time.Now().UnixMilli(), lease.Milliseconds(), s.max, token,
		).Text()
		if err != nil {
			return "", fmt.Errorf("acquire expirable semaphore %s: %w", s.name, err)
		}
		if permit != "" {
			return permit, nil
		}
		if maxWait <= 0 || !time.Now().Before(deadline) {
			return "", fmt.Errorf("expirable semaphore %s wait timeout after %s", s.name, maxWait)
		}

		wait := 25 * time.Millisecond
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

// Release 释放一个许可；租约已过期时释放操作是幂等的。
func (s *ExpirableSemaphore) Release(ctx context.Context, token string) error {
	if s == nil || s.rdb == nil || token == "" {
		return nil
	}
	if err := s.rdb.ZRem(ctx, s.name, token).Err(); err != nil {
		return fmt.Errorf("release expirable semaphore %s: %w", s.name, err)
	}
	return nil
}

// Run 获取许可后执行函数，并在函数返回后释放许可。
func (s *ExpirableSemaphore) Run(ctx context.Context, maxWait, lease time.Duration, fn func() error) error {
	token, err := s.Acquire(ctx, maxWait, lease)
	if err != nil {
		return err
	}
	fnErr := error(nil)
	if fn != nil {
		fnErr = fn()
	}
	if releaseErr := s.Release(context.Background(), token); fnErr == nil && releaseErr != nil {
		return releaseErr
	}
	return fnErr
}

// Shutdown 保留与其他限流器一致的生命周期接口。
func (s *ExpirableSemaphore) Shutdown() {}
