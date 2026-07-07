package model

import (
	"testing"

	"go-base-agent/internal/framework/config"
)

func testSelector() *Selector {
	enabled := func(v bool) *bool { return &v }

	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"openai":    {URL: "https://api.openai.com", Protocol: "openai-compatible"},
			"anthropic": {URL: "https://api.anthropic.com", Protocol: "anthropic"},
			"bailian":   {URL: "https://dashscope.aliyuncs.com", Protocol: "openai-compatible"},
		},
		Chat: config.AIChatConfig{
			DefaultModel:      "gpt-4.1",
			DeepThinkingModel: "claude-sonnet-4",
			Candidates: []config.AICandidateConfig{
				{ID: "gpt-4.1", Provider: "openai", Model: "gpt-4.1", Priority: 2},
				{ID: "gpt-4.1-disabled", Provider: "openai", Model: "gpt-4.1", Priority: 1, Enabled: enabled(false)},
				{ID: "claude-sonnet-4", Provider: "anthropic", Model: "claude-sonnet-4", Priority: 3, SupportsThinking: true},
				{ID: "qwen-backup", Provider: "bailian", Model: "qwen-plus", Priority: 5},
				{ID: "unknown-provider", Provider: "nonexistent", Model: "test", Priority: 1},
			},
		},
		Embedding: config.AIEmbeddingConfig{
			DefaultModel: "text-embedding-3",
			Candidates: []config.AIEmbeddingCandidateConfig{
				{ID: "text-embedding-3", Provider: "openai", Model: "text-embedding-3-large", Priority: 1},
				{ID: "emb-noop", Provider: "noop", Model: "noop", Priority: 100},
			},
		},
		Rerank: config.AIRerankConfig{
			DefaultModel: "rerank-noop",
			Candidates: []config.AIRerankCandidateConfig{
				{ID: "rerank-noop", Provider: "noop", Model: "noop", Priority: 100},
			},
		},
	}

	return NewSelector(cfg, NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 2,
		OpenDurationMs:   100,
	}))
}

func TestSelector_ChatCandidates_DefaultModelFirst(t *testing.T) {
	s := testSelector()
	targets := s.SelectChatCandidates(false)

	if len(targets) == 0 {
		t.Fatal("expected at least one target")
	}
	if targets[0].ID != "gpt-4.1" {
		t.Fatalf("expected default model first, got %q", targets[0].ID)
	}
}

func TestSelector_ChatCandidates_DisabledExcluded(t *testing.T) {
	s := testSelector()
	targets := s.SelectChatCandidates(false)

	for _, target := range targets {
		if target.ID == "gpt-4.1-disabled" {
			t.Fatal("disabled candidate should be excluded")
		}
	}
}

func TestSelector_ChatCandidates_DeepThinking(t *testing.T) {
	s := testSelector()
	targets := s.SelectChatCandidates(true)

	if len(targets) == 0 {
		t.Fatal("expected at least one target")
	}
	if targets[0].ID != "claude-sonnet-4" {
		t.Fatalf("expected deep-thinking model first, got %q", targets[0].ID)
	}
	for _, target := range targets {
		if !target.Candidate.SupportsThinking {
			t.Fatalf("non-thinking model %q should be excluded in deep-thinking mode", target.ID)
		}
	}
}

func TestSelector_ChatCandidates_UnknownProviderExcluded(t *testing.T) {
	s := testSelector()
	targets := s.SelectChatCandidates(false)

	for _, target := range targets {
		if target.ID == "unknown-provider" {
			t.Fatal("candidate with unknown provider should be excluded")
		}
	}
}

func TestSelector_ChatCandidates_PriorityOrder(t *testing.T) {
	s := testSelector()
	targets := s.SelectChatCandidates(false)

	for i := 1; i < len(targets); i++ {
		if targets[i-1].ID == "gpt-4.1" && targets[i].ID == "qwen-backup" && targets[i-1].Candidate.Priority > targets[i].Candidate.Priority {
			t.Fatal("default model should be first regardless of priority")
		}
		if targets[i-1].ID != "gpt-4.1" {
			if targets[i-1].Candidate.Priority > targets[i].Candidate.Priority {
				t.Fatalf("priority out of order: %q(%d) > %q(%d)",
					targets[i-1].ID, targets[i-1].Candidate.Priority,
					targets[i].ID, targets[i].Candidate.Priority)
			}
		}
	}
}

func TestSelector_EmbeddingCandidates(t *testing.T) {
	s := testSelector()
	targets := s.SelectEmbeddingCandidates()

	if len(targets) == 0 {
		t.Fatal("expected at least one target")
	}
	if targets[0].ID != "text-embedding-3" {
		t.Fatalf("expected default model first, got %q", targets[0].ID)
	}

	hasNoop := false
	for _, target := range targets {
		if target.ID == "emb-noop" {
			hasNoop = true
			if target.Provider.URL != "" || target.Provider.Protocol != "" {
				t.Fatal("noop provider should have empty config")
			}
		}
	}
	if !hasNoop {
		t.Fatal("noop embedding candidate should be included")
	}
}

func TestSelector_RerankCandidates(t *testing.T) {
	s := testSelector()
	targets := s.SelectRerankCandidates()

	if len(targets) == 0 {
		t.Fatal("expected at least one target")
	}
	if targets[0].ID != "rerank-noop" {
		t.Fatalf("expected noop rerank model, got %q", targets[0].ID)
	}
}

func TestSelector_VlmCandidates_Empty(t *testing.T) {
	s := testSelector()
	targets := s.SelectVlmCandidates()

	if len(targets) != 0 {
		t.Fatal("expected empty VLM candidates")
	}
}

func TestSelector_HealthUnavailable(t *testing.T) {
	enabled := func(v bool) *bool { return &v }

	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"openai": {URL: "https://api.openai.com", Protocol: "openai-compatible"},
		},
		Chat: config.AIChatConfig{
			DefaultModel: "gpt-4.1",
			Candidates: []config.AICandidateConfig{
				{ID: "gpt-4.1", Provider: "openai", Model: "gpt-4.1", Priority: 1},
				{ID: "backup", Provider: "openai", Model: "gpt-3.5", Priority: 2},
			},
		},
		Embedding: config.AIEmbeddingConfig{
			Candidates: []config.AIEmbeddingCandidateConfig{
				{ID: "emb", Provider: "openai", Model: "emb", Enabled: enabled(true)},
			},
		},
		Rerank: config.AIRerankConfig{
			Candidates: []config.AIRerankCandidateConfig{
				{ID: "rank", Provider: "noop", Model: "noop", Enabled: enabled(true)},
			},
		},
	}

	health := NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 1,
		OpenDurationMs:   5000,
	})

	s := NewSelector(cfg, health)

	health.MarkFailure("gpt-4.1")
	targets := s.SelectChatCandidates(false)

	if len(targets) != 1 {
		t.Fatalf("expected 1 candidate after unhealthy first, got %d", len(targets))
	}
	if targets[0].ID != "backup" {
		t.Fatalf("expected backup candidate, got %q", targets[0].ID)
	}
}
