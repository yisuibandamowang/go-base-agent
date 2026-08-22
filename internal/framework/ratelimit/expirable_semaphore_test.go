package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go-base-agent/internal/framework/ratelimit"
)

func TestExpirableSemaphoreLeaseAllowsRecoveryAfterOwnerExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	sem := ratelimit.NewExpirableSemaphore("test:mineru", rdb, 1)
	defer sem.Shutdown()

	token, err := sem.Acquire(context.Background(), 30*time.Millisecond, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire first permit: %v", err)
	}
	if token == "" {
		t.Fatal("expected permit token")
	}
	time.Sleep(50 * time.Millisecond)

	second, err := sem.Acquire(context.Background(), time.Second, time.Second)
	if err != nil {
		t.Fatalf("acquire after lease expiry: %v", err)
	}
	if second == "" || second == token {
		t.Fatalf("expected a fresh permit token, first=%q second=%q", token, second)
	}
}

func TestExpirableSemaphoreSharesLimitAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	first := ratelimit.NewExpirableSemaphore("test:mineru:shared", rdb, 1)
	second := ratelimit.NewExpirableSemaphore("test:mineru:shared", rdb, 1)
	defer first.Shutdown()
	defer second.Shutdown()

	token, err := first.Acquire(context.Background(), time.Second, time.Second)
	if err != nil {
		t.Fatalf("acquire first instance permit: %v", err)
	}
	if _, err := second.Acquire(context.Background(), 80*time.Millisecond, time.Second); err == nil {
		t.Fatal("expected second instance to observe the shared limit")
	}
	if err := first.Release(context.Background(), token); err != nil {
		t.Fatalf("release first instance permit: %v", err)
	}
	if _, err := second.Acquire(context.Background(), time.Second, time.Second); err != nil {
		t.Fatalf("expected second instance to acquire after release: %v", err)
	}
}
