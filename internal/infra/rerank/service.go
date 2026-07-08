package rerank

import "context"

// Service re-ranks retrieved chunks by relevance to the query.
// Aligns with Java RerankService.
type Service interface {
	Rerank(ctx context.Context, query string, candidates []Chunk, topN int) ([]Chunk, error)
}
