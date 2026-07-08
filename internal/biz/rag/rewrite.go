package rag

import (
	"context"

	"go-base-agent/internal/infra/chat"
)

// RewriteResult holds the output of query rewriting.
// Aligns with Java RewriteResult.
type RewriteResult struct {
	RewrittenQuestion string
	SubQuestions      []string
}

// QueryRewriter rewrites user questions for better retrieval.
// Aligns with Java QueryRewriteService.
type QueryRewriter interface {
	Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error)
}

// NoopRewriter passes the question through unchanged.
type NoopRewriter struct{}

func (n *NoopRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*RewriteResult, error) {
	return &RewriteResult{
		RewrittenQuestion: question,
	}, nil
}
