package lock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newLockTestServer(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: server.Addr()}), server
}

func waitUntilLocked(t *testing.T, client *redis.Client, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		held, err := client.Exists(context.Background(), key).Result()
		if err != nil {
			t.Fatalf("exists %s: %v", key, err)
		}
		if held > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("lock %s was not acquired in time", key)
}

// TestRunWithLockExpiresWithoutDeletingForeignLock 长任务超过 TTL、锁被他人接管后，
// 原持有者结束时不得误删别人的锁（对齐 Java Redisson isHeldByCurrentThread）。
func TestRunWithLockExpiresWithoutDeletingForeignLock(t *testing.T) {
	client, server := newLockTestServer(t)
	l := New(client)
	ctx := context.Background()

	firstDone := make(chan struct{})
	go func() {
		if err := l.RunWithLock(ctx, "test:lock", 10*time.Millisecond, func() error {
			<-firstDone // 模拟任务耗时超过锁 TTL
			return nil
		}); err != nil {
			t.Errorf("first holder: %v", err)
		}
	}()
	waitUntilLocked(t, client, "test:lock")

	// 推进 miniredis 时钟让锁 TTL 到期，第二个持有者接管
	server.FastForward(11 * time.Millisecond)
	ok, err := l.Acquire(ctx, "test:lock", time.Minute)
	if err != nil || !ok {
		t.Fatalf("expected second holder to acquire after TTL, ok=%v err=%v", ok, err)
	}

	// 第一个持有者的 defer 释放不应删除第二个持有者的锁
	close(firstDone)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		held, err := l.IsLocked(ctx, "test:lock")
		if err != nil {
			t.Fatalf("is locked: %v", err)
		}
		if !held {
			t.Fatal("second holder's lock was deleted by the expired first holder")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRunWithLockRejectsConcurrentSecondHolder 互斥语义保持：持锁期间第二个 RunWithLock 失败。
func TestRunWithLockRejectsConcurrentSecondHolder(t *testing.T) {
	client, _ := newLockTestServer(t)
	l := New(client)
	ctx := context.Background()

	release := make(chan struct{})
	go func() {
		if err := l.RunWithLock(ctx, "test:mutex", time.Minute, func() error {
			<-release
			return nil
		}); err != nil {
			t.Errorf("first holder: %v", err)
		}
	}()
	waitUntilLocked(t, client, "test:mutex")

	err := l.RunWithLock(ctx, "test:mutex", time.Minute, func() error { return nil })
	if err == nil {
		t.Fatal("expected second RunWithLock to fail while lock is held")
	}
	close(release)
}

// TestRunWithLockReleasesOwnLock 正常路径：任务结束后释放自己的锁。
func TestRunWithLockReleasesOwnLock(t *testing.T) {
	client, _ := newLockTestServer(t)
	l := New(client)
	ctx := context.Background()

	if err := l.RunWithLock(ctx, "test:own", time.Minute, func() error { return nil }); err != nil {
		t.Fatalf("run with lock: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	held, err := l.IsLocked(ctx, "test:own")
	if err != nil {
		t.Fatalf("is locked: %v", err)
	}
	if held {
		t.Fatal("expected lock to be released after fn completed")
	}
}

// TestRunWithLockStillReturnsFnError 释放路径不吞业务错误。
func TestRunWithLockStillReturnsFnError(t *testing.T) {
	client, _ := newLockTestServer(t)
	l := New(client)

	wantErr := errors.New("business failure")
	err := l.RunWithLock(context.Background(), "test:err", time.Minute, func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected fn error to propagate, got %v", err)
	}
}
