package service

import (
	"context"
	"strings"
	"testing"

	"go-base-agent/internal/infra/chat"
)

type recordingTitleLLM struct {
	request chat.Request
	output  string
	err     error
	calls   int
}

func (r *recordingTitleLLM) Chat(ctx context.Context, req chat.Request) (string, error) {
	r.calls++
	r.request = req
	return r.output, r.err
}

func (r *recordingTitleLLM) ChatWithModel(ctx context.Context, req chat.Request, modelID string) (string, error) {
	return r.Chat(ctx, req)
}

func (r *recordingTitleLLM) StreamChat(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
	return nil, nil
}

type fakeConversationTitleGenerator struct {
	output string
	err    error
	calls  int
}

func (f *fakeConversationTitleGenerator) Generate(ctx context.Context, question string) (string, error) {
	f.calls++
	return f.output, f.err
}

func TestLLMTitleGenerator_GeneratesTitleAndTrims(t *testing.T) {
	llm := &recordingTitleLLM{output: "  1234567890abcdefghij  \n"}
	gen := NewLLMTitleGenerator(llm, "", 8)

	title, err := gen.Generate(context.Background(), "会员Agent支持哪些能力？")
	if err != nil {
		t.Fatalf("generate title: %v", err)
	}
	if title != "12345678" {
		t.Fatalf("unexpected title: %q", title)
	}
	if llm.calls != 1 {
		t.Fatalf("expected one llm call, got %d", llm.calls)
	}
	if got := llm.request.Messages[0].Content; !strings.Contains(got, "会员Agent支持哪些能力？") || !strings.Contains(got, "8") {
		t.Fatalf("prompt not rendered as expected: %q", got)
	}
}
