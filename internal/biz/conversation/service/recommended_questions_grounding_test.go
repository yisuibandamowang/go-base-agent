package service

import (
	"context"
	"strings"
	"testing"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/chat"
)

type groundingPromptLLM struct {
	prompt string
}

func (l *groundingPromptLLM) Chat(_ context.Context, req chat.Request) (string, error) {
	if len(req.Messages) > 0 {
		l.prompt = req.Messages[len(req.Messages)-1].Content
	}
	return "[\"继续了解详情？\"]", nil
}

func (l *groundingPromptLLM) ChatWithModel(ctx context.Context, req chat.Request, _ string) (string, error) {
	return l.Chat(ctx, req)
}

func (l *groundingPromptLLM) StreamChat(context.Context, chat.Request, chat.StreamCallback) (chat.StreamHandle, error) {
	return nil, nil
}

func TestLLMRecommendedQuestionGeneratorPromptContainsGrounding(t *testing.T) {
	llm := &groundingPromptLLM{}
	gen := NewLLMRecommendedQuestionGenerator(llm, "")

	got := gen.Generate(context.Background(), "会员规则是什么？", "会员分为三个等级。", []rag.GroundingChunk{{
		DocName: "会员手册",
		Text:    "白金会员享受专属权益。",
	}})
	if got.Status != RecommendedQuestionsStatusSuccess {
		t.Fatalf("expected successful generation, got %+v", got)
	}
	if !strings.Contains(llm.prompt, "会员手册") || !strings.Contains(llm.prompt, "白金会员享受专属权益") {
		t.Fatalf("expected grounding in prompt, got %q", llm.prompt)
	}
}
