package rag

import (
	"context"
	"testing"
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
