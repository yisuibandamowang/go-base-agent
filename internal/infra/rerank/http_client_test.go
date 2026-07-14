package rerank

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/model"
)

func TestHTTPClient_RerankDashScopeResponse(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/rerank" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"output": {
				"results": [
					{"index": 1, "relevance_score": 0.91},
					{"index": 0, "relevance_score": 0.73}
				]
			}
		}`))
	}))
	defer server.Close()

	client := NewHTTPClient("bailian", server.Client())
	result, err := client.Rerank(t.Context(), "怎么开通会员", []Chunk{
		{ID: "a", Text: "普通账号说明", Score: 0.1},
		{ID: "b", Text: "会员开通说明", Score: 0.2},
	}, 2, model.Target{
		Candidate: config.AICandidateConfig{Model: "qwen3-rerank", URL: server.URL + "/rerank"},
		Provider:  config.AIProviderConfig{APIKey: "token-1"},
	})
	if err != nil {
		t.Fatalf("rerank failed: %v", err)
	}
	if gotAuth != "Bearer token-1" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
	if gotBody["model"] != "qwen3-rerank" {
		t.Fatalf("unexpected model: %#v", gotBody["model"])
	}
	input, ok := gotBody["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected dashscope input object, got %#v", gotBody["input"])
	}
	if input["query"] != "怎么开通会员" {
		t.Fatalf("unexpected query: %#v", input["query"])
	}
	docs, ok := input["documents"].([]any)
	if !ok || len(docs) != 2 || docs[1] != "会员开通说明" {
		t.Fatalf("unexpected documents: %#v", input["documents"])
	}
	if len(result) != 2 || result[0].ID != "b" || result[0].Score != 0.91 || result[1].ID != "a" {
		t.Fatalf("unexpected reranked chunks: %#v", result)
	}
}

func TestHTTPClient_RerankRootResultsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"index":0,"score":0.8}]}`))
	}))
	defer server.Close()

	client := NewHTTPClient("jina", server.Client())
	result, err := client.Rerank(t.Context(), "query", []Chunk{
		{ID: "a", Text: "doc a", Score: 0.1},
		{ID: "b", Text: "doc b", Score: 0.2},
	}, 1, model.Target{
		Candidate: config.AICandidateConfig{Model: "reranker", URL: server.URL},
	})
	if err != nil {
		t.Fatalf("rerank failed: %v", err)
	}
	if len(result) != 1 || result[0].ID != "a" || result[0].Score != 0.8 {
		t.Fatalf("unexpected reranked chunks: %#v", result)
	}
}
