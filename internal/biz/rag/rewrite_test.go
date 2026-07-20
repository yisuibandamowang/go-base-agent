package rag

import (
	"context"
	"testing"

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
