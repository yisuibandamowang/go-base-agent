package vlm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/model"
)

func TestOpenAICompatibleClient_DescribeImageIncludesMaxTokens(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	client := NewOpenAICompatibleClient("openai", srv.Client())
	client.RequiresAPIKey = false
	result, err := client.DescribeImage(context.Background(), []byte("img"), "image/png", "prompt", model.Target{
		ID: "c1",
		Candidate: config.AICandidateConfig{
			URL:   srv.URL,
			Model: "test-model",
		},
	}, 777)
	if err != nil {
		t.Fatalf("describe image: %v", err)
	}
	if result != "ok" {
		t.Fatalf("unexpected result: %q", result)
	}
	if got, ok := body["max_tokens"].(float64); !ok || int(got) != 777 {
		t.Fatalf("expected max_tokens=777, got %#v", body["max_tokens"])
	}
}
