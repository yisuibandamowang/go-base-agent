package main

import (
	"testing"

	"go-base-agent/internal/framework/config"
	infrarerank "go-base-agent/internal/infra/rerank"
)

func TestBuildRerankClients_IncludesHTTPProviderAndNoopFallback(t *testing.T) {
	clients := buildRerankClients(config.AIConfig{
		Providers: config.AIProvidersConfig{
			"bailian": {
				Protocol: "openai-compatible",
				URL:      "https://dashscope.aliyuncs.com",
				Endpoints: map[string]string{
					"rerank": "/api/v1/services/rerank/text-rerank/text-rerank",
				},
			},
		},
	})
	if len(clients) != 2 {
		t.Fatalf("expected http rerank client plus noop fallback, got %d", len(clients))
	}
	if _, ok := clients[0].(*infrarerank.HTTPClient); !ok {
		t.Fatalf("expected first rerank client to be HTTPClient, got %T", clients[0])
	}
	if clients[0].Provider() != "bailian" {
		t.Fatalf("unexpected provider %q", clients[0].Provider())
	}
	if _, ok := clients[1].(*infrarerank.NoopClient); !ok {
		t.Fatalf("expected noop fallback, got %T", clients[1])
	}
}

func TestBuildChatClients_IncludesAnthropicProvider(t *testing.T) {
	clients := buildChatClients(config.AIConfig{
		Providers: config.AIProvidersConfig{
			"anthropic-main": {
				Protocol: "anthropic",
				URL:      "https://api.anthropic.com",
				Endpoints: map[string]string{
					"chat": "/v1/messages",
				},
			},
		},
	})
	if len(clients) != 1 {
		t.Fatalf("expected 1 chat client, got %d", len(clients))
	}
	if clients[0].Provider() != "anthropic-main" {
		t.Fatalf("unexpected provider %q", clients[0].Provider())
	}
}
