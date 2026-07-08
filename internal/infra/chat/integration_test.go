package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/embedding"
	"go-base-agent/internal/infra/model"
	"go-base-agent/internal/infra/rerank"
)

// TestFullChain_Chat_Fallback demonstrates the full chain:
// config → HealthStore → Selector → RoutingExecutor → RoutingLLMService → ChatClient → httptest
func TestFullChain_Chat_Fallback(t *testing.T) {
	// Setup: create mock OpenAI-compatible server (primary fails, backup succeeds)
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		if failCount == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"content": "fallback response"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Build config
	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"primary":  {URL: server.URL, Protocol: "openai-compatible", Endpoints: map[string]string{"chat": "/v1/chat/completions"}},
			"fallback": {URL: server.URL, Protocol: "openai-compatible", Endpoints: map[string]string{"chat": "/v1/chat/completions"}},
		},
		Selection: config.AISelectionConfig{
			FailureThreshold:          1,
			OpenDurationMs:            100,
			FirstPacketTimeoutSeconds: 60,
		},
		Chat: config.AIChatConfig{
			DefaultModel: "primary-model",
			Candidates: []config.AICandidateConfig{
				{ID: "primary-model", Provider: "primary", Model: "gpt-4", Priority: 1},
				{ID: "fallback-model", Provider: "fallback", Model: "gpt-3.5", Priority: 2},
			},
		},
		Embedding: config.AIEmbeddingConfig{Candidates: []config.AIEmbeddingCandidateConfig{}},
		Rerank:    config.AIRerankConfig{Candidates: []config.AIRerankCandidateConfig{}},
	}

	// Wire up model layer
	health := model.NewHealthStore(cfg.Selection)
	selector := model.NewSelector(cfg, health)
	executor := model.NewRoutingExecutor(health)

	// Wire up clients
	openaiClient := NewOpenAICompatibleChatClient("primary", nil)
	openaiClient.RequiresAPIKey = false
	fallbackClient := NewOpenAICompatibleChatClient("fallback", nil)
	fallbackClient.RequiresAPIKey = false

	// Wire up LLM service
	svc := NewRoutingLLMService(
		selector,
		health,
		executor,
		[]ChatClient{openaiClient, fallbackClient},
		NewFirstPacketProbe(),
		time.Duration(cfg.Selection.FirstPacketTimeoutSeconds)*time.Second,
	)

	// Execute: primary should fail, fallback succeeds
	result, err := svc.Chat(context.Background(), SimpleRequest("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "fallback response" {
		t.Fatalf("expected fallback, got: %s", result)
	}

	// Verify health: primary should be marked as unavailable
	if !health.IsUnavailable("primary-model") {
		t.Fatal("primary model should be unavailable after failure")
	}
}

// TestFullChain_Embedding_Integration verifies the embedding chain.
func TestFullChain_Embedding_Integration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"embedding": []float32{0.1, 0.2, 0.3}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"openai": {URL: server.URL, Protocol: "openai-compatible", Endpoints: map[string]string{"embedding": "/v1/embeddings"}},
		},
		Selection: config.AISelectionConfig{FailureThreshold: 2, OpenDurationMs: 100},
		Chat:      config.AIChatConfig{Candidates: []config.AICandidateConfig{}},
		Embedding: config.AIEmbeddingConfig{
			DefaultModel: "emb",
			Candidates: []config.AIEmbeddingCandidateConfig{
				{ID: "emb", Provider: "openai", Model: "text-embedding", Dimension: 3, Priority: 1},
			},
		},
		Rerank: config.AIRerankConfig{Candidates: []config.AIRerankCandidateConfig{}},
	}

	health := model.NewHealthStore(cfg.Selection)
	selector := model.NewSelector(cfg, health)
	executor := model.NewRoutingExecutor(health)

	embClient := embedding.NewOpenAICompatibleEmbeddingClient("openai", nil)
	embClient.RequiresAPIKey = false

	embSvc := embedding.NewRoutingEmbeddingService(executor, selector, []embedding.Client{embClient}, 3)

	result, err := embSvc.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 || result[0] != 0.1 {
		t.Fatalf("unexpected embedding: %v", result)
	}
}

// TestFullChain_Rerank_Integration verifies the rerank chain with NoopClient.
func TestFullChain_Rerank_Integration(t *testing.T) {
	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{},
		Selection: config.AISelectionConfig{FailureThreshold: 2, OpenDurationMs: 100},
		Chat:      config.AIChatConfig{Candidates: []config.AICandidateConfig{}},
		Embedding: config.AIEmbeddingConfig{Candidates: []config.AIEmbeddingCandidateConfig{}},
		Rerank: config.AIRerankConfig{
			DefaultModel: "noop",
			Candidates: []config.AIRerankCandidateConfig{
				{ID: "noop", Provider: "noop", Model: "noop", Priority: 100},
			},
		},
	}

	health := model.NewHealthStore(cfg.Selection)
	selector := model.NewSelector(cfg, health)
	executor := model.NewRoutingExecutor(health)

	rerankSvc := rerank.NewRoutingRerankService(executor, selector, []rerank.Client{&rerank.NoopClient{}})

	chunks := []rerank.Chunk{
		{ID: "1", Text: "a", Score: 0.9},
		{ID: "2", Text: "b", Score: 0.8},
		{ID: "3", Text: "c", Score: 0.7},
	}
	result, err := rerankSvc.Rerank(context.Background(), "query", chunks, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
}

// TestFullChain_StreamChat_Integration verifies the streaming chain.
func TestFullChain_StreamChat_Integration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"stream-result\"}}]}\n\n"))
		flusher.Flush()
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"openai": {URL: server.URL, Protocol: "openai-compatible", Endpoints: map[string]string{"chat": "/v1/chat/completions"}},
		},
		Selection: config.AISelectionConfig{
			FailureThreshold:          2,
			OpenDurationMs:            100,
			FirstPacketTimeoutSeconds: 5,
		},
		Chat: config.AIChatConfig{
			DefaultModel: "gpt",
			Candidates: []config.AICandidateConfig{
				{ID: "gpt", Provider: "openai", Model: "gpt-4", Priority: 1},
			},
		},
		Embedding: config.AIEmbeddingConfig{Candidates: []config.AIEmbeddingCandidateConfig{}},
		Rerank:    config.AIRerankConfig{Candidates: []config.AIRerankCandidateConfig{}},
	}

	health := model.NewHealthStore(cfg.Selection)
	selector := model.NewSelector(cfg, health)
	executor := model.NewRoutingExecutor(health)

	openaiClient := NewOpenAICompatibleChatClient("openai", nil)
	openaiClient.RequiresAPIKey = false

	svc := NewRoutingLLMService(
		selector, health, executor,
		[]ChatClient{openaiClient},
		NewFirstPacketProbe(),
		time.Duration(cfg.Selection.FirstPacketTimeoutSeconds)*time.Second,
	)

	done := make(chan string, 1)
	cb := &captureCallback{
		onContent: func(c string) { done <- c },
		onError:   func(err error) { t.Errorf("unexpected error: %v", err) },
	}

	_, err := svc.StreamChat(context.Background(), SimpleRequest("hello"), cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case content := <-done:
		if content != "stream-result" {
			t.Fatalf("unexpected content: %s", content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for stream")
	}
}
