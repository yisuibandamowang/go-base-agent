package idempotent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newConsumeGuard(t *testing.T) (*ConsumeGuard, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	return NewConsumeGuard(client), server
}

// TestConsumeGuardThreeStates 三态语义（对齐 Java IdempotentConsumeAspect）：
// 首次进入获得执行权；消费中报延迟重试；完成后再进直接跳过；失败后允许重投。
func TestConsumeGuardThreeStates(t *testing.T) {
	guard, _ := newConsumeGuard(t)
	ctx := context.Background()
	ttl := time.Hour

	if err := guard.TryBegin(ctx, "msg-1", ttl); err != nil {
		t.Fatalf("first begin should succeed, got %v", err)
	}
	// 消费中：第二个消费者进入被拒（应触发延迟重试）
	if err := guard.TryBegin(ctx, "msg-1", ttl); !errors.Is(err, ErrConsuming) {
		t.Fatalf("expected ErrConsuming while consuming, got %v", err)
	}
	// 完成后：直接跳过
	if err := guard.Complete(ctx, "msg-1", ttl); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := guard.TryBegin(ctx, "msg-1", ttl); !errors.Is(err, ErrConsumed) {
		t.Fatalf("expected ErrConsumed after completion, got %v", err)
	}

	// 失败路径：删除标记后允许重投
	if err := guard.TryBegin(ctx, "msg-2", ttl); err != nil {
		t.Fatalf("begin msg-2: %v", err)
	}
	if err := guard.Fail(ctx, "msg-2"); err != nil {
		t.Fatalf("fail msg-2: %v", err)
	}
	if err := guard.TryBegin(ctx, "msg-2", ttl); err != nil {
		t.Fatalf("expected retry after failure to succeed, got %v", err)
	}
}

// TestConsumeGuardTTLAppliesToConsumingMark 消费中标记带 TTL：消费者崩溃后标记自动过期，消息可重投。
func TestConsumeGuardTTLAppliesToConsumingMark(t *testing.T) {
	guard, server := newConsumeGuard(t)
	ctx := context.Background()

	if err := guard.TryBegin(ctx, "msg-ttl", 30*time.Millisecond); err != nil {
		t.Fatalf("begin: %v", err)
	}
	server.FastForward(35 * time.Millisecond)
	if err := guard.TryBegin(ctx, "msg-ttl", time.Minute); err != nil {
		t.Fatalf("expected consuming mark to expire, got %v", err)
	}
}
