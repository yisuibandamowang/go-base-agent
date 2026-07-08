package rerank

import (
	"context"

	"go-base-agent/internal/infra/model"
)

// NoopClient is a fallback rerank client that truncates results to topN.
// Aligns with Java NoopRerankClient.
type NoopClient struct{}

func (n *NoopClient) Provider() string { return "noop" }

func (n *NoopClient) Rerank(ctx context.Context, query string, candidates []Chunk, topN int, target model.Target) ([]Chunk, error) {
	if topN < len(candidates) {
		return candidates[:topN], nil
	}
	return candidates, nil
}
