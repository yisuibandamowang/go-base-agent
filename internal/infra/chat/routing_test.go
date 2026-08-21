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
	_, svc := testRoutingServiceWithHealth(clients)
	return svc
}

func testRoutingServiceWithHealth(clients []ChatClient) (*model.HealthStore, *RoutingLLMService) {
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

	return health, NewRoutingLLMService(
		model.NewSelector(cfg, health),
		health,
		model.NewRoutingExecutor(health),
		clients,
		&noopFirstPacketProbe{},
		60*time.Second,
	)
}

// ctxAwareFirstPacketProbe 在 ctx 取消时返回 ctx 错误，否则返回成功。
type ctxAwareFirstPacketProbe struct{}

func (p *ctxAwareFirstPacketProbe) AwaitFirstPacket(bridge *ProbeBridge, timeout time.Duration) (ProbeResult, error) {
	ctx := bridge.Context()
	if ctx != nil {
		select {
		case result := <-bridge.AwaitResult():
			return result, nil
		case <-ctx.Done():
			return ProbeResult{}, ctx.Err()
		case <-time.After(timeout):
			return ProbeResult{Success: false, Error: errors.New("first packet timeout")}, nil
		}
	}
	select {
	case result := <-bridge.AwaitResult():
		return result, nil
	case <-time.After(timeout):
		return ProbeResult{Success: false, Error: errors.New("first packet timeout")}, nil
	}
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

func TestLLMService_ChatWithTierUsesTierCandidatesAndTimeout(t *testing.T) {
	var gotTarget model.Target
	var hasDeadline bool
	client := &fakeChatClient{name: "openai", chatFn: func(ctx context.Context, req Request, target model.Target) (string, error) {
		gotTarget = target
		_, hasDeadline = ctx.Deadline()
		return "tier-response", nil
	}}
	cfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"openai": {URL: "https://api.openai.com", Protocol: "openai-compatible"},
		},
		Chat: config.AIChatConfig{
			DefaultModel: "priority-first",
			DefaultTier:  "standard",
			Tiers: map[string]config.AIChatTierConfig{
				"fast":     {Candidates: []string{"fast-first"}, TimeoutMs: 5000},
				"standard": {Candidates: []string{"standard-first"}, TimeoutMs: 30000},
			},
			Candidates: []config.AICandidateConfig{
				{ID: "priority-first", Provider: "openai", Model: "gpt-4.1", Priority: 0},
				{ID: "fast-first", Provider: "openai", Model: "gpt-4.1-mini", Priority: 10},
				{ID: "standard-first", Provider: "openai", Model: "gpt-4.1", Priority: 10},
			},
		},
	}
	health := model.NewHealthStore(config.AISelectionConfig{})
	svc := NewRoutingLLMService(
		model.NewSelector(cfg, health), health, model.NewRoutingExecutor(health),
		[]ChatClient{client}, &noopFirstPacketProbe{}, time.Second,
	)

	result, err := svc.ChatWithTier(context.Background(), SimpleRequest("hello"), "fast")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "tier-response" || gotTarget.ID != "fast-first" || gotTarget.TimeoutMs != 5000 {
		t.Fatalf("unexpected tier route: result=%q target=%+v", result, gotTarget)
	}
	if !hasDeadline {
		t.Fatal("expected tier timeout to be applied to model context")
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

// TestLLMService_StreamChat_CancelReleasesHalfOpenSlot 验证首包探测期间请求被取消时，
// 持有者只释放半开探测名额：不标记失败、不占用后续探测。
// 对齐 Java RoutingLLMServiceHalfOpenRecoveryTest。
func TestLLMService_StreamChat_CancelReleasesHalfOpenSlot(t *testing.T) {
	health, svc := testRoutingServiceWithHealth([]ChatClient{
		&fakeChatClient{name: "openai", streamFn: func(ctx context.Context, req Request, cb StreamCallback, target model.Target) (StreamHandle, error) {
			// 启动成功但不发首包，让探测等待期间 ctx 取消
			return &noopStreamHandle{}, nil
		}},
	})
	svc.firstPacketProbe = &ctxAwareFirstPacketProbe{}

	// 先把模型打进 OPEN，再等冷却进入半开
	health.MarkFailure("gpt-4")
	health.MarkFailure("gpt-4")
	if !health.IsUnavailable("gpt-4") {
		t.Fatal("model should be open after threshold failures")
	}
	time.Sleep(120 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.StreamChat(ctx, SimpleRequest("hello"), &captureCallback{})
	if err == nil {
		t.Fatal("cancelled request should return error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	// 名额应已释放：半开状态仍允许下一次探测
	if !health.AllowCall("gpt-4") {
		t.Fatal("half-open probe slot should be reusable after cancellation")
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
