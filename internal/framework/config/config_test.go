package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAIProvidersMapParsing(t *testing.T) {
	yaml := `
server:
  port: 9090
ai:
  providers:
    openai:
      url: https://api.openai.com
      api-key: test-key
      protocol: openai-compatible
      endpoints:
        chat: /v1/chat/completions
        embedding: /v1/embeddings
    anthropic:
      url: https://api.anthropic.com
      api-key: test-key-2
      protocol: anthropic
      endpoints:
        chat: /v1/messages
    noop:
      protocol: noop
  selection:
    failure-threshold: 3
    open-duration-ms: 5000
    first-packet-timeout-seconds: 30
  chat:
    default-model: gpt-4.1
    deep-thinking-model: claude-sonnet-4
    candidates:
      - id: gpt-4.1
        provider: openai
        model: gpt-4.1
        url: https://custom.openai.com
        priority: 1
      - id: claude-sonnet-4
        provider: anthropic
        model: claude-sonnet-4
        supports-thinking: true
        enabled: true
        priority: 2
      - id: disabled-model
        provider: openai
        model: gpt-3.5
        enabled: false
        priority: 3
  embedding:
    default-model: text-embedding-3
    candidates:
      - id: text-embedding-3
        provider: openai
        model: text-embedding-3-large
        dimension: 3072
        priority: 1
  rerank:
    default-model: rerank-noop
    candidates:
      - id: rerank-noop
        provider: noop
        model: noop
        priority: 100
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if len(cfg.AI.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(cfg.AI.Providers))
	}

	openai, ok := cfg.AI.Providers["openai"]
	if !ok {
		t.Fatal("openai provider missing")
	}
	if openai.Protocol != "openai-compatible" {
		t.Fatalf("unexpected protocol: %s", openai.Protocol)
	}
	if openai.Endpoints["chat"] != "/v1/chat/completions" {
		t.Fatalf("unexpected endpoint: %s", openai.Endpoints["chat"])
	}

	anthropic, ok := cfg.AI.Providers["anthropic"]
	if !ok {
		t.Fatal("anthropic provider missing")
	}
	if anthropic.Protocol != "anthropic" {
		t.Fatalf("unexpected protocol: %s", anthropic.Protocol)
	}

	noop, ok := cfg.AI.Providers["noop"]
	if !ok {
		t.Fatal("noop provider missing")
	}
	if noop.Protocol != "noop" {
		t.Fatalf("unexpected protocol: %s", noop.Protocol)
	}

	if cfg.AI.Selection.FailureThreshold != 3 {
		t.Fatalf("unexpected failure threshold: %d", cfg.AI.Selection.FailureThreshold)
	}
	if cfg.AI.Selection.FirstPacketTimeoutSeconds != 30 {
		t.Fatalf("unexpected first packet timeout: %d", cfg.AI.Selection.FirstPacketTimeoutSeconds)
	}
}

func TestCandidateIsEnabled(t *testing.T) {
	t.Run("nil means enabled", func(t *testing.T) {
		c := AICandidateConfig{}
		if !c.IsEnabled() {
			t.Fatal("expected nil Enabled to be true")
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		v := true
		c := AICandidateConfig{Enabled: &v}
		if !c.IsEnabled() {
			t.Fatal("expected true to be true")
		}
	})

	t.Run("explicit false", func(t *testing.T) {
		v := false
		c := AICandidateConfig{Enabled: &v}
		if c.IsEnabled() {
			t.Fatal("expected false to be false")
		}
	})
}

func TestCandidateURLOverride(t *testing.T) {
	yaml := `
server:
  port: 9090
ai:
  providers:
    openai:
      url: https://api.openai.com
      api-key: test
      protocol: openai-compatible
      endpoints:
        chat: /v1/chat/completions
  chat:
    default-model: gpt-4.1
    candidates:
      - id: gpt-4.1
        provider: openai
        model: gpt-4.1
        url: https://custom-endpoint.example.com
        priority: 1
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if len(cfg.AI.Chat.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cfg.AI.Chat.Candidates))
	}

	candidate := cfg.AI.Chat.Candidates[0]
	if candidate.URL != "https://custom-endpoint.example.com" {
		t.Fatalf("unexpected URL: %s", candidate.URL)
	}
}
