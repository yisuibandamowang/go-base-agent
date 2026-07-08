package rerank

import (
	"context"

	"go-base-agent/internal/infra/model"
)

// Client is a provider-specific rerank implementation.
// Aligns with Java RerankClient.
type Client interface {
	Provider() string
	Rerank(ctx context.Context, query string, candidates []Chunk, topN int, target model.Target) ([]Chunk, error)
}
