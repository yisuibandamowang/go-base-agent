package rag

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/ratelimit"
	"go-base-agent/internal/infra/chat"
)

type fakeQueueLimiter struct {
	acquire func(ctx context.Context, req ratelimit.AcquireRequest) error
}

func (f *fakeQueueLimiter) Acquire(ctx context.Context, req ratelimit.AcquireRequest) error {
	if f.acquire != nil {
		return f.acquire(ctx, req)
	}
	return nil
}

type queuedMemoryService struct {
	conversation *Conversation
	saved        []chat.Message
}

func (m *queuedMemoryService) LoadHistory(ctx context.Context, conversationID string) ([]chat.Message, error) {
	return nil, nil
}

func (m *queuedMemoryService) SaveMessage(ctx context.Context, conversationID string, msg chat.Message) error {
	_, err := m.AppendMessage(ctx, conversationID, msg)
	return err
}

func (m *queuedMemoryService) AppendMessage(ctx context.Context, conversationID string, msg chat.Message) (string, error) {
	m.saved = append(m.saved, msg)
	if m.conversation == nil {
		m.conversation = &Conversation{ID: conversationID, UserID: "user-1", Title: "会员咨询"}
	}
	return fmt.Sprintf("msg-%d", len(m.saved)), nil
}

func (m *queuedMemoryService) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	return m.conversation, nil
}

func TestQueuedChatService_TimeoutSendsRejectFinishAndDone(t *testing.T) {
	innerCalled := false
	limiter := &fakeQueueLimiter{
		acquire: func(ctx context.Context, req ratelimit.AcquireRequest) error {
			if req.OnTimeout == nil {
				t.Fatal("expected timeout callback")
			}
			req.OnTimeout()
			return fmt.Errorf("queue timeout after 1s")
		},
	}
	mem := &queuedMemoryService{conversation: &Conversation{ID: "conv-1", UserID: "user-1", Title: "已有标题"}}
	s, w := newTestSSESender(t)

	svc := NewQueuedChatService(&queueInnerRecorder{onStream: func(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
		innerCalled = true
	}}, limiter, mem, time.Second)
	svc.StreamChat(context.Background(), "会员怎么查？", "conv-1", "task-1", false, s)

	if innerCalled {
		t.Fatal("inner stream should not be called on timeout")
	}
	body := w.Body.String()
	for _, want := range []string{"event: reject", "event: finish", "event: done", `"messageId":"msg-2"`, `"title":"已有标题"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q, got: %s", want, body)
		}
	}
	if len(mem.saved) != 2 {
		t.Fatalf("expected user and reject messages to be saved, got %d", len(mem.saved))
	}
	if mem.saved[1].Content != "系统繁忙，请稍后再试" {
		t.Fatalf("unexpected reject message content: %+v", mem.saved[1])
	}
}

type queueInnerRecorder struct {
	onStream func(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender)
}

func (r *queueInnerRecorder) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	if r.onStream != nil {
		r.onStream(ctx, question, conversationID, taskID, deepThinking, sender)
	}
}

func (r *queueInnerRecorder) StopTask(taskID string) {}

// TestQueuedChatService_AcquireCallbackKeepsUserContext 验证排队获取许可后的回调
// 即使在新 goroutine 执行，携带的用户上下文仍然保留。
// 对齐 Java 修复：保留排队请求的用户上下文（TtlRunnable 等价物为闭包捕获 ctx）。
func TestQueuedChatService_AcquireCallbackKeepsUserContext(t *testing.T) {
	var gotUser *appctx.LoginUser
	var wg sync.WaitGroup
	wg.Add(1)
	limiter := &fakeQueueLimiter{
		acquire: func(ctx context.Context, req ratelimit.AcquireRequest) error {
			// 模拟限流器把回调投递到新 goroutine（如 t.req.OnAcquire() 由队列 poller 触发）
			go func() {
				defer wg.Done()
				req.OnAcquire()
			}()
			return nil
		},
	}
	mem := &queuedMemoryService{conversation: &Conversation{ID: "conv-1", UserID: "user-1", Title: "标题"}}

	svc := NewQueuedChatService(&queueInnerRecorder{onStream: func(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
		gotUser = appctx.User(ctx)
	}}, limiter, mem, time.Second)

	userCtx := appctx.WithUser(context.Background(), &appctx.LoginUser{UserID: "user-1", Username: "tester"})
	s, _ := newTestSSESender(t)
	svc.StreamChat(userCtx, "问题", "conv-1", "task-1", false, s)

	wg.Wait()
	if gotUser == nil {
		t.Fatal("queued acquire callback lost user context")
	}
	if gotUser.UserID != "user-1" {
		t.Fatalf("expected user-1 in callback context, got %q", gotUser.UserID)
	}
}
