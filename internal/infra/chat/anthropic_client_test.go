package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/model"
)

func TestAnthropicClient_Chat(t *testing.T) {
	var capturedHeader http.Header
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hello anthropic"}]}`))
	}))
	defer server.Close()

	client := NewAnthropicChatClient("anthropic", server.Client())
	result, err := client.Chat(context.Background(), Request{
		Messages: []Message{
			NewSystemMessage("你是助手"),
			NewUserMessage("你好"),
		},
	}, model.Target{
		Candidate: config.AICandidateConfig{Model: "claude-test", URL: server.URL},
		Provider:  config.AIProviderConfig{APIKey: "test-key"},
	})
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if result != "hello anthropic" {
		t.Fatalf("unexpected result %q", result)
	}
	if capturedHeader.Get("x-api-key") != "test-key" {
		t.Fatalf("expected x-api-key header, got %q", capturedHeader.Get("x-api-key"))
	}
	if capturedHeader.Get("anthropic-version") == "" {
		t.Fatal("expected anthropic-version header")
	}
	if capturedBody["model"] != "claude-test" || capturedBody["system"] != "你是助手" {
		t.Fatalf("unexpected request body: %+v", capturedBody)
	}
	messages := capturedBody["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "你好" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
}

func TestAnthropicClient_StreamChatUsesCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"stream content"}]}`))
	}))
	defer server.Close()

	client := NewAnthropicChatClient("anthropic", server.Client())
	cb := &recordingCallback{}
	handle, err := client.StreamChat(context.Background(), SimpleRequest("hello"), cb, model.Target{
		Candidate: config.AICandidateConfig{Model: "claude-test", URL: server.URL},
		Provider:  config.AIProviderConfig{APIKey: "test-key"},
	})
	if err != nil {
		t.Fatalf("stream chat failed: %v", err)
	}
	handle.Wait()
	if cb.content != "stream content" || !cb.completed {
		t.Fatalf("unexpected callback state: content=%q completed=%v", cb.content, cb.completed)
	}
}

type recordingCallback struct {
	content   string
	completed bool
	err       error
}

func (c *recordingCallback) OnContent(content string) {
	c.content += content
}

func (c *recordingCallback) OnThinking(content string) {}

func (c *recordingCallback) OnComplete() {
	c.completed = true
}

func (c *recordingCallback) OnError(err error) {
	c.err = err
}
