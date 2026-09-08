package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestOpenAIEmbeddingClient_SendsDimensionsForOllama(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if got := int(body["dimensions"].(float64)); got != 1536 {
			t.Fatalf("expected dimensions 1536, got %d", got)
		}

		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"embedding": make([]float32, 1536)},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenAICompatibleEmbeddingClient("ollama", nil)
	client.RequiresAPIKey = false
	target := model.Target{
		ID: "qwen3-embedding",
		Candidate: config.AICandidateConfig{
			Model:     "qwen3-embedding:8b-fp16",
			URL:       server.URL + "/v1/embeddings",
			Dimension: 1536,
		},
		Provider: config.AIProviderConfig{URL: server.URL},
	}

	result, err := client.Embed(context.Background(), "hello", target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1536 {
		t.Fatalf("expected 1536 dimensions, got %d", len(result))
	}
}

// TestOpenAIEmbeddingClient_BatchesBeyondProviderLimit 对齐 Java maxBatchSize：
// 超过单次批量上限时自动分片，按原顺序回填结果。
func TestOpenAIEmbeddingClient_BatchesBeyondProviderLimit(t *testing.T) {
	var requestCount int
	var lastBatchSize int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		lastBatchSize = len(body.Input)
		data := make([]map[string]interface{}, 0, len(body.Input))
		for range body.Input {
			data = append(data, map[string]interface{}{"embedding": []float32{0.1}})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
	}))
	defer server.Close()

	// 百炼上限 10：25 条应分 3 片（10+10+5），顺序保持。
	client := NewOpenAICompatibleEmbeddingClient("bailian", nil)
	if client.MaxBatchSize != 10 {
		t.Fatalf("expected bailian max batch 10, got %d", client.MaxBatchSize)
	}
	target := model.Target{
		Candidate: config.AICandidateConfig{Model: "text-embedding-v4", URL: server.URL},
		Provider:  config.AIProviderConfig{URL: server.URL},
	}

	texts := make([]string, 25)
	for i := range texts {
		texts[i] = fmt.Sprintf("chunk-%d", i)
	}
	results, err := client.EmbedBatch(context.Background(), texts, target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 3 || lastBatchSize != 5 {
		t.Fatalf("expected 3 requests with last batch 5, got %d requests last %d", requestCount, lastBatchSize)
	}
	if len(results) != 25 {
		t.Fatalf("expected 25 results, got %d", len(results))
	}
}

// TestOpenAIEmbeddingClient_SendsDimensionsAndEncodingFormat 对齐 Java：
// dimensions 与 encoding_format=float 对所有 OpenAI 兼容提供商发送（Ollama 不带 encoding_format）。
func TestOpenAIEmbeddingClient_SendsDimensionsAndEncodingFormat(t *testing.T) {
	var gotDimensions, gotEncodingFormat interface{}
	var hasEncoding bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotDimensions = body["dimensions"]
		gotEncodingFormat, hasEncoding = body["encoding_format"]
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{0.1, 0.2}}},
		})
	}))
	defer server.Close()

	target := model.Target{
		Candidate: config.AICandidateConfig{Model: "text-embedding-v4", URL: server.URL, Dimension: 2},
		Provider:  config.AIProviderConfig{URL: server.URL},
	}

	bailian := NewOpenAICompatibleEmbeddingClient("bailian", nil)
	if _, err := bailian.Embed(context.Background(), "文本", target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDimensions != float64(2) {
		t.Fatalf("expected dimensions sent for bailian, got %#v", gotDimensions)
	}
	if !hasEncoding || gotEncodingFormat != "float" {
		t.Fatalf("expected encoding_format float for bailian, got %#v", gotEncodingFormat)
	}

	gotDimensions, hasEncoding = nil, false
	ollama := NewOpenAICompatibleEmbeddingClient("ollama", nil)
	if _, err := ollama.Embed(context.Background(), "文本", target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDimensions != float64(2) {
		t.Fatalf("expected dimensions sent for ollama, got %#v", gotDimensions)
	}
	if hasEncoding {
		t.Fatalf("expected no encoding_format for ollama, got %#v", gotEncodingFormat)
	}
}

func TestOpenAIEmbeddingClient_RejectsDimensionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"embedding": []float32{0.1, 0.2}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenAICompatibleEmbeddingClient("openai", nil)
	target := model.Target{
		ID: "test",
		Candidate: config.AICandidateConfig{
			Model:     "text-embedding",
			URL:       server.URL + "/v1/embeddings",
			Dimension: 3,
		},
		Provider: config.AIProviderConfig{URL: server.URL},
	}

	_, err := client.Embed(context.Background(), "hello", target)
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !strings.Contains(err.Error(), "dimension mismatch") {
		t.Fatalf("unexpected error: %v", err)
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
