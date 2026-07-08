package embedding

import (
	"context"
	"errors"

	"go-base-agent/internal/infra/model"
)

// RoutingEmbeddingService implements Service with model routing.
// Aligns with Java RoutingEmbeddingService.
type RoutingEmbeddingService struct {
	executor *model.RoutingExecutor
	selector *model.Selector
	clients  map[string]Client
	dim      int
}

// NewRoutingEmbeddingService creates a new RoutingEmbeddingService.
func NewRoutingEmbeddingService(
	executor *model.RoutingExecutor,
	selector *model.Selector,
	clients []Client,
	dim int,
) *RoutingEmbeddingService {
	byProvider := make(map[string]Client, len(clients))
	for _, c := range clients {
		byProvider[c.Provider()] = c
	}
	return &RoutingEmbeddingService{
		executor: executor,
		selector: selector,
		clients:  byProvider,
		dim:      dim,
	}
}

func (s *RoutingEmbeddingService) Embed(ctx context.Context, text string) ([]float32, error) {
	return s.embedWithTargets(ctx, text, s.selector.SelectEmbeddingCandidates())
}

func (s *RoutingEmbeddingService) EmbedWithModel(ctx context.Context, text, modelID string) ([]float32, error) {
	if modelID == "" {
		return s.Embed(ctx, text)
	}

	for _, t := range s.selector.SelectEmbeddingCandidates() {
		if t.ID == modelID {
			return s.embedWithTargets(ctx, text, []model.Target{t})
		}
	}

	return nil, errors.New("embedding model not found: " + modelID)
}

func (s *RoutingEmbeddingService) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return s.embedBatchWithTargets(ctx, texts, s.selector.SelectEmbeddingCandidates())
}

func (s *RoutingEmbeddingService) EmbedBatchWithModel(ctx context.Context, texts []string, modelID string) ([][]float32, error) {
	if modelID == "" {
		return s.EmbedBatch(ctx, texts)
	}

	for _, t := range s.selector.SelectEmbeddingCandidates() {
		if t.ID == modelID {
			return s.embedBatchWithTargets(ctx, texts, []model.Target{t})
		}
	}

	return nil, errors.New("embedding model not found: " + modelID)
}

func (s *RoutingEmbeddingService) Dimension() int {
	return s.dim
}

func (s *RoutingEmbeddingService) embedWithTargets(ctx context.Context, text string, targets []model.Target) ([]float32, error) {
	return model.ExecuteWithFallback(
		s.executor,
		model.CapabilityEmbedding,
		targets,
		func(t model.Target) (Client, bool) {
			c, ok := s.clients[t.Candidate.Provider]
			return c, ok
		},
		func(client Client, t model.Target) ([]float32, error) {
			return client.Embed(ctx, text, t)
		},
	)
}

func (s *RoutingEmbeddingService) embedBatchWithTargets(ctx context.Context, texts []string, targets []model.Target) ([][]float32, error) {
	return model.ExecuteWithFallback(
		s.executor,
		model.CapabilityEmbedding,
		targets,
		func(t model.Target) (Client, bool) {
			c, ok := s.clients[t.Candidate.Provider]
			return c, ok
		},
		func(client Client, t model.Target) ([][]float32, error) {
			return client.EmbedBatch(ctx, texts, t)
		},
	)
}
