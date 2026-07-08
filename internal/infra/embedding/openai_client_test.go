package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/model"
)

func testEmbeddingService(clients []Client) *RoutingEmbeddingService {
	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"openai": {URL: "https://api.openai.com", Protocol: "openai-compatible"},
		},
		Embedding: config.AIEmbeddingConfig{
			DefaultModel: "text-embedding-3",
			Candidates: []config.AIEmbeddingCandidateConfig{
				{ID: "text-embedding-3", Provider: "openai", Model: "text-embedding-3-large", Dimension: 3072, Priority: 1},
			},
		},
		Chat: config.AIChatConfig{
			Candidates: []config.AICandidateConfig{},
		},
		Rerank: config.AIRerankConfig{
			Candidates: []config.AIRerankCandidateConfig{},
		},
	}

	health := model.NewHealthStore(config.AISelectionConfig{FailureThreshold: 2, OpenDurationMs: 100})
	return NewRoutingEmbeddingService(
		model.NewRoutingExecutor(health),
		model.NewSelector(cfg, health),
		clients,
		3072,
	)
}

type fakeEmbeddingClient struct {
	name    string
	embedFn func(ctx context.Context, text string, target model.Target) ([]float32, error)
	batchFn func(ctx context.Context, texts []string, target model.Target) ([][]float32, error)
}

func (f *fakeEmbeddingClient) Provider() string { return f.name }
func (f *fakeEmbeddingClient) Embed(ctx context.Context, text string, target model.Target) ([]float32, error) {
	if f.embedFn != nil {
		return f.embedFn(ctx, text, target)
	}
	return []float32{0.1, 0.2, 0.3}, nil
}
func (f *fakeEmbeddingClient) EmbedBatch(ctx context.Context, texts []string, target model.Target) ([][]float32, error) {
	if f.batchFn != nil {
		return f.batchFn(ctx, texts, target)
	}
	results := make([][]float32, len(texts))
	for i := range texts {
		results[i] = []float32{0.1, 0.2, 0.3}
	}
	return results, nil
}

func TestEmbeddingService_Embed(t *testing.T) {
	svc := testEmbeddingService([]Client{&fakeEmbeddingClient{name: "openai"}})
	result, err := svc.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 dimensions, got %d", len(result))
	}
}

func TestEmbeddingService_EmbedBatch(t *testing.T) {
	svc := testEmbeddingService([]Client{&fakeEmbeddingClient{name: "openai"}})
	results, err := svc.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestEmbeddingService_Dimension(t *testing.T) {
	svc := testEmbeddingService([]Client{&fakeEmbeddingClient{name: "openai"}})
	if svc.Dimension() != 3072 {
		t.Fatalf("expected dimension 3072, got %d", svc.Dimension())
	}
}

func TestOpenAIEmbeddingClient_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"embedding": []float32{0.1, 0.2, 0.3}},
				{"embedding": []float32{0.4, 0.5, 0.6}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenAICompatibleEmbeddingClient("openai", nil)
	target := model.Target{
		ID: "test",
		Candidate: config.AICandidateConfig{
			Model: "text-embedding-3",
			URL:   server.URL + "/v1/embeddings",
		},
		Provider: config.AIProviderConfig{URL: server.URL},
	}

	results, err := client.EmbedBatch(context.Background(), []string{"a", "b"}, target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if len(results[0]) != 3 || results[0][0] != 0.1 {
		t.Fatal("unexpected embedding values")
	}
}

func TestParseEmbeddingResponse(t *testing.T) {
	body := []byte(`{"data":[{"embedding":[1.0,2.0]},{"embedding":[3.0,4.0]}]}`)
	results, err := parseEmbeddingResponse(body, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestParseEmbeddingResponse_EmptyData(t *testing.T) {
	_, err := parseEmbeddingResponse([]byte(`{"data":[]}`), "test")
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}
