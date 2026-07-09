package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/model"
)

func testRoutingService(clients []ChatClient) *RoutingLLMService {
	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"openai":  {URL: "https://api.openai.com", Protocol: "openai-compatible"},
			"bailian": {URL: "https://dashscope.aliyuncs.com", Protocol: "openai-compatible"},
		},
		Chat: config.AIChatConfig{
			DefaultModel: "gpt-4",
			Candidates: []config.AICandidateConfig{
				{ID: "gpt-4", Provider: "openai", Model: "gpt-4", Priority: 1},
				{ID: "qwen-backup", Provider: "bailian", Model: "qwen-plus", Priority: 5},
			},
		},
		Embedding: config.AIEmbeddingConfig{Candidates: []config.AIEmbeddingCandidateConfig{}},
		Rerank:    config.AIRerankConfig{Candidates: []config.AIRerankCandidateConfig{}},
	}

	health := model.NewHealthStore(config.AISelectionConfig{FailureThreshold: 2, OpenDurationMs: 100})

	return NewRoutingLLMService(
		model.NewSelector(cfg, health),
		health,
		model.NewRoutingExecutor(health),
		clients,
		&noopFirstPacketProbe{},
		60*time.Second,
	)
}

type noopFirstPacketProbe struct{}

func (n *noopFirstPacketProbe) AwaitFirstPacket(_ *ProbeBridge, _ time.Duration) (ProbeResult, error) {
	return ProbeResult{Success: true}, nil
}

type fakeChatClient struct {
	name     string
	chatFn   func(ctx context.Context, req Request, target model.Target) (string, error)
	streamFn func(ctx context.Context, req Request, cb StreamCallback, target model.Target) (StreamHandle, error)
}

func (f *fakeChatClient) Provider() string { return f.name }
func (f *fakeChatClient) Chat(ctx context.Context, req Request, target model.Target) (string, error) {
	if f.chatFn != nil {
		return f.chatFn(ctx, req, target)
	}
	return f.name + "-response", nil
}
func (f *fakeChatClient) StreamChat(ctx context.Context, req Request, cb StreamCallback, target model.Target) (StreamHandle, error) {
	if f.streamFn != nil {
		return f.streamFn(ctx, req, cb, target)
	}
	go func() {
		cb.OnContent(f.name + "-stream")
		cb.OnComplete()
	}()
	return &noopStreamHandle{}, nil
}

type noopStreamHandle struct{}

func (n *noopStreamHandle) Cancel() {}
func (n *noopStreamHandle) Wait()   {}

func TestLLMService_Chat_Success(t *testing.T) {
	svc := testRoutingService([]ChatClient{&fakeChatClient{name: "openai"}})
	result, err := svc.Chat(context.Background(), SimpleRequest("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "openai-response" {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestLLMService_Chat_Fallback(t *testing.T) {
	svc := testRoutingService([]ChatClient{
		&fakeChatClient{name: "openai", chatFn: func(ctx context.Context, req Request, target model.Target) (string, error) {
			return "", errors.New("fail")
		}},
		&fakeChatClient{name: "bailian"},
	})
	result, err := svc.Chat(context.Background(), SimpleRequest("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "bailian-response" {
		t.Fatalf("expected fallback, got %s", result)
	}
}

func TestLLMService_StreamChat_Success(t *testing.T) {
	svc := testRoutingService([]ChatClient{&fakeChatClient{name: "openai"}})
	done := make(chan string, 1)
	cb := &captureCallback{
		onContent: func(c string) { done <- c },
	}
	_, err := svc.StreamChat(context.Background(), SimpleRequest("hello"), cb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case content := <-done:
		if content != "openai-stream" {
			t.Fatalf("unexpected content: %s", content)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for stream content")
	}
}

func TestMessageConstructors(t *testing.T) {
	sys := NewSystemMessage("system prompt")
	if sys.Role != RoleSystem || sys.Content != "system prompt" {
		t.Fatal("system message mismatch")
	}

	user := NewUserMessage("hello")
	if user.Role != RoleUser || user.Content != "hello" {
		t.Fatal("user message mismatch")
	}

	asst := NewAssistantMessage("response")
	if asst.Role != RoleAssistant || asst.Content != "response" {
		t.Fatal("assistant message mismatch")
	}
}

type captureCallback struct {
	onContent  func(string)
	onThinking func(string)
	onComplete func()
	onError    func(error)
}

func (c *captureCallback) OnContent(content string) {
	if c.onContent != nil {
		c.onContent(content)
	}
}
func (c *captureCallback) OnThinking(content string) {
	if c.onThinking != nil {
		c.onThinking(content)
	}
}
func (c *captureCallback) OnComplete() {
	if c.onComplete != nil {
		c.onComplete()
	}
}
func (c *captureCallback) OnError(err error) {
	if c.onError != nil {
		c.onError(err)
	}
}
