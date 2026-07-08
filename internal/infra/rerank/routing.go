package rerank

import (
	"context"

	"go-base-agent/internal/infra/model"
)

// RoutingRerankService implements Service with model routing.
// Aligns with Java RoutingRerankService.
type RoutingRerankService struct {
	executor *model.RoutingExecutor
	selector *model.Selector
	clients  map[string]Client
}

// NewRoutingRerankService creates a new RoutingRerankService.
func NewRoutingRerankService(
	executor *model.RoutingExecutor,
	selector *model.Selector,
	clients []Client,
) *RoutingRerankService {
	byProvider := make(map[string]Client, len(clients))
	for _, c := range clients {
		byProvider[c.Provider()] = c
	}
	return &RoutingRerankService{
		executor: executor,
		selector: selector,
		clients:  byProvider,
	}
}

func (s *RoutingRerankService) Rerank(ctx context.Context, query string, candidates []Chunk, topN int) ([]Chunk, error) {
	return model.ExecuteWithFallback(
		s.executor,
		model.CapabilityRerank,
		s.selector.SelectRerankCandidates(),
		func(t model.Target) (Client, bool) {
			c, ok := s.clients[t.Candidate.Provider]
			return c, ok
		},
		func(client Client, t model.Target) ([]Chunk, error) {
			return client.Rerank(ctx, query, candidates, topN, t)
		},
	)
}
