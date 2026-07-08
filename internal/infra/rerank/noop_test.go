package rerank

import (
	"context"
	"testing"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/model"
)

func testRerankService(clients []Client) *RoutingRerankService {
	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{},
		Rerank: config.AIRerankConfig{
			DefaultModel: "rerank-noop",
			Candidates: []config.AIRerankCandidateConfig{
				{ID: "rerank-noop", Provider: "noop", Model: "noop", Priority: 100},
			},
		},
		Chat:      config.AIChatConfig{Candidates: []config.AICandidateConfig{}},
		Embedding: config.AIEmbeddingConfig{Candidates: []config.AIEmbeddingCandidateConfig{}},
	}

	health := model.NewHealthStore(config.AISelectionConfig{FailureThreshold: 2, OpenDurationMs: 100})
	return NewRoutingRerankService(
		model.NewRoutingExecutor(health),
		model.NewSelector(cfg, health),
		clients,
	)
}

func TestNoopRerank_Truncate(t *testing.T) {
	candidates := []Chunk{
		{ID: "1", Text: "a", Score: 0.9},
		{ID: "2", Text: "b", Score: 0.8},
		{ID: "3", Text: "c", Score: 0.7},
	}

	noop := &NoopClient{}
	result, err := noop.Rerank(context.Background(), "query", candidates, 2, model.Target{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	if result[0].ID != "1" {
		t.Fatalf("expected id 1, got %s", result[0].ID)
	}
}

func TestNoopRerank_NoTruncate(t *testing.T) {
	candidates := []Chunk{
		{ID: "1", Text: "a", Score: 0.9},
	}

	noop := &NoopClient{}
	result, err := noop.Rerank(context.Background(), "query", candidates, 5, model.Target{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
}

func TestNoopRerank_Provider(t *testing.T) {
	noop := &NoopClient{}
	if noop.Provider() != "noop" {
		t.Fatalf("expected 'noop', got %s", noop.Provider())
	}
}

func TestRoutingRerankService(t *testing.T) {
	svc := testRerankService([]Client{&NoopClient{}})
	candidates := []Chunk{
		{ID: "1", Text: "a", Score: 0.9},
		{ID: "2", Text: "b", Score: 0.8},
		{ID: "3", Text: "c", Score: 0.7},
	}
	result, err := svc.Rerank(context.Background(), "query", candidates, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
}
