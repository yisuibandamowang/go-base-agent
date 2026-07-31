package ratelimit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go-base-agent/internal/framework/ratelimit"
)

func newTestLimiterT(t *testing.T, maxConcurrent int) (*ratelimit.FairQueueLimiter, func()) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return ratelimit.NewFairQueueLimiter("test", rdb, ratelimit.LimiterConfig{
		MaxConcurrent:  maxConcurrent,
		MaxWaitSeconds: 3,
		LeaseSeconds:   30,
		PollIntervalMs: 50,
	}), func() { mr.Close() }
}

func TestLimiter_SingleAcquire(t *testing.T) {
	l, cleanup := newTestLimiterT(t, 5)
	defer cleanup()

	acquired := make(chan struct{}, 1)
	err := l.Acquire(context.Background(), ratelimit.AcquireRequest{
		MaxWait: 2 * time.Second,
		OnAcquire: func() {
			acquired <- struct{}{}
		},
		OnTimeout: func() {
			t.Fatal("unexpected timeout")
		},
	})
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("onAcquire not called")
	}
}

func TestLimiter_AcquireWaitsForOnAcquire(t *testing.T) {
	l, cleanup := newTestLimiterT(t, 1)
	defer cleanup()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Acquire(context.Background(), ratelimit.AcquireRequest{
			MaxWait: 2 * time.Second,
			OnAcquire: func() {
				started <- struct{}{}
				<-release
			},
			OnTimeout: func() {
				t.Fatal("unexpected timeout")
			},
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("onAcquire did not start")
	}

	select {
	case <-done:
		t.Fatal("acquire returned before onAcquire completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("acquire did not return after onAcquire completed")
	}
}

func TestLimiter_ConcurrentWithinLimit(t *testing.T) {
	l, cleanup := newTestLimiterT(t, 3)
	defer cleanup()

	var acquired atomic.Int32
	var wg sync.WaitGroup
	ctx := context.Background()

	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Acquire(ctx, ratelimit.AcquireRequest{
				MaxWait: 5 * time.Second,
				OnAcquire: func() {
					acquired.Add(1)
					time.Sleep(100 * time.Millisecond)
				},
				OnTimeout: func() {},
			})
		}()
	}
	wg.Wait()

	count := acquired.Load()
	if count < 3 {
		t.Fatalf("expected at least 3 acquired, got %d", count)
	}
}

func TestLimiter_TimeoutWhenFull(t *testing.T) {
	l, cleanup := newTestLimiterT(t, 1)
	defer cleanup()

	// 占满唯一 permit
	block := make(chan struct{})
	go l.Acquire(context.Background(), ratelimit.AcquireRequest{
		MaxWait: 10 * time.Second,
		OnAcquire: func() {
			<-block
		},
		OnTimeout: func() {},
	})
	time.Sleep(100 * time.Millisecond)

	// 第二个请求应该超时
	timeoutHit := make(chan struct{}, 1)
	err := l.Acquire(context.Background(), ratelimit.AcquireRequest{
		MaxWait: 500 * time.Millisecond,
		OnAcquire: func() {
			t.Fatal("should not acquire when full")
		},
		OnTimeout: func() {
			timeoutHit <- struct{}{}
		},
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	select {
	case <-timeoutHit:
	case <-time.After(2 * time.Second):
		t.Fatal("onTimeout not called")
	}

	close(block)
}

func TestLimiter_DisabledAcquiresDirectly(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	l := ratelimit.NewFairQueueLimiter("test", rdb, ratelimit.LimiterConfig{
		MaxConcurrent:  0,
		MaxWaitSeconds: 0,
		LeaseSeconds:   0,
		PollIntervalMs: 0,
	})
	defer l.Shutdown()

	acquired := make(chan struct{}, 1)
	err := l.Acquire(context.Background(), ratelimit.AcquireRequest{
		MaxWait: 1 * time.Second,
		OnAcquire: func() {
			acquired <- struct{}{}
		},
		OnTimeout: func() {
			t.Fatal("should not timeout when disabled")
		},
	})
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(1 * time.Second):
		t.Fatal("onAcquire not called")
	}
}
