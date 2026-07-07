package model

import (
	"testing"

	"go-base-agent/internal/framework/config"
)

func TestResolveURL(t *testing.T) {
	t.Run("candidate URL takes priority", func(t *testing.T) {
		provider := config.AIProviderConfig{
			URL: "https://api.openai.com",
			Endpoints: map[string]string{
				"chat": "/v1/chat/completions",
			},
		}
		candidate := config.AICandidateConfig{
			URL: "https://custom.example.com/v1",
		}
		url, err := ResolveURL(provider, candidate, CapabilityChat)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://custom.example.com/v1" {
			t.Fatalf("expected candidate URL, got %q", url)
		}
	})

	t.Run("provider URL with endpoint", func(t *testing.T) {
		provider := config.AIProviderConfig{
			URL: "https://api.openai.com",
			Endpoints: map[string]string{
				"chat": "/v1/chat/completions",
			},
		}
		candidate := config.AICandidateConfig{}
		url, err := ResolveURL(provider, candidate, CapabilityChat)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "https://api.openai.com/v1/chat/completions" {
			t.Fatalf("unexpected URL: %q", url)
		}
	})

	t.Run("provider base URL missing", func(t *testing.T) {
		provider := config.AIProviderConfig{
			Endpoints: map[string]string{
				"chat": "/v1/chat/completions",
			},
		}
		candidate := config.AICandidateConfig{}
		_, err := ResolveURL(provider, candidate, CapabilityChat)
		if err == nil {
			t.Fatal("expected error for missing base URL")
		}
	})

	t.Run("endpoint missing", func(t *testing.T) {
		provider := config.AIProviderConfig{
			URL: "https://api.openai.com",
		}
		candidate := config.AICandidateConfig{}
		_, err := ResolveURL(provider, candidate, CapabilityChat)
		if err == nil {
			t.Fatal("expected error for missing endpoint")
		}
	})
}

func TestJoinURL(t *testing.T) {
	tests := []struct {
		base, path, expected string
	}{
		{"https://api.com", "/v1/chat", "https://api.com/v1/chat"},
		{"https://api.com/", "/v1/chat", "https://api.com/v1/chat"},
		{"https://api.com", "v1/chat", "https://api.com/v1/chat"},
		{"https://api.com/", "v1/chat", "https://api.com/v1/chat"},
	}
	for _, tt := range tests {
		got := joinURL(tt.base, tt.path)
		if got != tt.expected {
			t.Errorf("joinURL(%q, %q) = %q, want %q", tt.base, tt.path, got, tt.expected)
		}
	}
}
