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

func TestHTTPClient_DualWritesScoreAndRerankScore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.77}]}`))
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
	if len(result) != 1 || result[0].ID != "a" {
		t.Fatalf("unexpected reranked chunks: %#v", result)
	}
	if !result[0].HasRerankScore() || *result[0].RerankScore != 0.77 {
		t.Fatalf("同一个分应写两处：score 会被下游覆写，rerankScore 留给证据闸门, got %#v", result[0])
	}
}

func TestHTTPClient_UnscoredEntriesSinkToZeroWithoutRerankScore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// b 未出分：不能保留名次派生的 RRF 分，否则会压过被判弱相关的精排分
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9}]}`))
	}))
	defer server.Close()

	client := NewHTTPClient("jina", server.Client())
	result, err := client.Rerank(t.Context(), "query", []Chunk{
		{ID: "a", Text: "doc a", Score: 0.03},
		{ID: "b", Text: "doc b", Score: 0.02},
	}, 2, model.Target{
		Candidate: config.AICandidateConfig{Model: "reranker", URL: server.URL},
	})
	if err != nil {
		t.Fatalf("rerank failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("API 返回不足 topN 时按原顺序回填未打分候选, got %#v", result)
	}
	if result[0].ID != "b" || result[0].Score != 0.9 || !result[0].HasRerankScore() {
		t.Fatalf("unexpected scored entry: %#v", result[0])
	}
	if result[1].ID != "a" || result[1].Score != 0 || result[1].HasRerankScore() {
		t.Fatalf("精排没出分的候选压到 0 沉底且不写 rerankScore, got %#v", result[1])
	}
}
