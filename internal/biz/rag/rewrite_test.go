package rag

import (
	"context"
	"strings"
	"testing"
	"time"

	"go-base-agent/internal/infra/chat"
)

func TestNoopRewriter(t *testing.T) {
	r := &NoopRewriter{}
	result, err := r.Rewrite(context.Background(), "你好", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RewrittenQuestion != "你好" {
		t.Fatalf("expected same question, got %q", result.RewrittenQuestion)
	}
}

func TestLLMRewriter_ParsesRewriteAndSubQuestions(t *testing.T) {
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			cb.OnContent(`{"rewrite":"会员权益查询","sub_questions":["会员权益查询","会员权益怎么查"]}`)
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}
	rewriter := NewLLMRewriter(llm, 4, 500, true)

	result, err := rewriter.Rewrite(context.Background(), "VIP权益怎么查", []chat.Message{chat.NewUserMessage("会员权益是什么")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RewrittenQuestion != "会员权益查询" {
		t.Fatalf("unexpected rewritten question: %q", result.RewrittenQuestion)
	}
	if len(result.SubQuestions) != 2 {
		t.Fatalf("expected 2 sub questions, got %+v", result.SubQuestions)
	}
}

func TestLLMRewriter_DisablesThinkingInRequest(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnContent(`{"rewrite":"会员权益查询","sub_questions":["会员权益查询"]}`)
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	rewriter := NewLLMRewriter(llm, 4, 500, true)
	_, err := rewriter.Rewrite(context.Background(), "VIP权益怎么查", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedReq.Thinking == nil || *capturedReq.Thinking {
		t.Fatal("expected rewrite request to disable thinking")
	}
}

func TestLLMRewriter_PromptProtectsEllipticalFollowUps(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnContent(`{"rewrite":"保险系统数据安全怎么做","sub_questions":["保险系统数据安全怎么做"]}`)
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	rewriter := NewLLMRewriter(llm, 4, 500, true)
	_, err := rewriter.Rewrite(context.Background(), "保险系统呢", []chat.Message{
		chat.NewUserMessage("OA系统数据安全怎么做的？"),
		chat.NewAssistantMessage("OA系统的数据安全规范围绕数据全生命周期。"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedReq.Messages) == 0 {
		t.Fatal("expected rewrite prompt")
	}
	prompt := capturedReq.Messages[0].Content
	for _, want := range []string{"省略续问", "不得将历史 Assistant 回答中的答案", "非查询轮次"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected rewrite prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestLLMRewriter_WaitsForStreamCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent(`{"rewrite":"会员权益查询"`)
				close(started)
				<-release
				cb.OnContent(`,"sub_questions":["会员权益查询"]}`)
				cb.OnComplete()
				close(done)
			}()
			return &waitHandle{done: done}, nil
		},
	}
	rewriter := NewLLMRewriter(llm, 4, 500, true)

	resultCh := make(chan *RewriteResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := rewriter.Rewrite(context.Background(), "VIP权益怎么查", nil)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected stream to start")
	}

	select {
	case result := <-resultCh:
		t.Fatalf("rewrite returned before stream completion: %+v", result)
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case result := <-resultCh:
		if result.RewrittenQuestion != "会员权益查询" {
			t.Fatalf("unexpected rewritten question: %q", result.RewrittenQuestion)
		}
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(time.Second):
		t.Fatal("expected rewrite to finish after release")
	}
}

func TestLLMRewriter_RuleBasedSplitWhenDisabled(t *testing.T) {
	rewriter := NewLLMRewriter(nil, 4, 500, false)

	result, err := rewriter.Rewrite(context.Background(), "会员权益怎么查？会员积分怎么看？", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RewrittenQuestion != "会员权益怎么查？会员积分怎么看？" {
		t.Fatalf("unexpected rewritten question: %q", result.RewrittenQuestion)
	}
	if len(result.SubQuestions) != 2 {
		t.Fatalf("expected split sub questions, got %+v", result.SubQuestions)
	}
}

type waitHandle struct {
	done chan struct{}
}

func (h *waitHandle) Cancel() {}
func (h *waitHandle) Wait()   { <-h.done }
