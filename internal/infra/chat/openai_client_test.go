package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/infra/model"
)

func TestParseSSELine_Content(t *testing.T) {
	event := ParseSSELine(`data: {"choices":[{"delta":{"content":"hello"}}]}`, false)
	if !event.HasContent() {
		t.Fatal("expected content")
	}
	if event.Content() != "hello" {
		t.Fatalf("unexpected content: %s", event.Content())
	}
	if event.Completed() {
		t.Fatal("should not be completed")
	}
}

func TestParseSSELine_ContentNoPrefix(t *testing.T) {
	event := ParseSSELine(`{"choices":[{"delta":{"content":"world"}}]}`, false)
	if !event.HasContent() {
		t.Fatal("expected content without data prefix")
	}
	if event.Content() != "world" {
		t.Fatalf("unexpected content: %s", event.Content())
	}
}

func TestParseSSELine_Done(t *testing.T) {
	event := ParseSSELine("data: [DONE]", false)
	if !event.Completed() {
		t.Fatal("expected completed")
	}
}

func TestParseSSELine_Reasoning(t *testing.T) {
	line := `data: {"choices":[{"delta":{"content":"answer","reasoning_content":"I think..."}}]}`
	event := ParseSSELine(line, true)
	if !event.HasReasoning() {
		t.Fatal("expected reasoning")
	}
	if event.Reasoning() != "I think..." {
		t.Fatalf("unexpected reasoning: %s", event.Reasoning())
	}
}

func TestParseSSELine_Blank(t *testing.T) {
	event := ParseSSELine("", false)
	if event.HasContent() || event.Completed() {
		t.Fatal("blank line should be empty")
	}
}

func TestParseSSELine_NoChoices(t *testing.T) {
	event := ParseSSELine(`data: {"choices":[]}`, false)
	if event.HasContent() || event.Completed() || event.HasReasoning() {
		t.Fatal("no choices should be empty")
	}
}

func TestParseSSELine_InvalidJSON(t *testing.T) {
	event := ParseSSELine("data: not-json", false)
	if event.HasContent() || event.Completed() {
		t.Fatal("invalid json should be empty")
	}
}

func TestParseSSELine_FinishReason(t *testing.T) {
	event := ParseSSELine(`data: {"choices":[{"finish_reason":"stop"}]}`, false)
	if !event.Completed() {
		t.Fatal("expected completed")
	}
}

func TestOpenAIClient_Chat_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"content": "hello from server"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenAICompatibleChatClient("test", nil)
	target := model.Target{
		ID: "test-model",
		Candidate: config.AICandidateConfig{
			Model: "test-model",
			URL:   server.URL + "/v1/chat/completions",
		},
		Provider: config.AIProviderConfig{URL: server.URL},
	}

	result, err := client.Chat(context.Background(), SimpleRequest("hello"), target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello from server" {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestOpenAIClient_Chat_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewOpenAICompatibleChatClient("test", nil)
	target := model.Target{
		ID: "test-model",
		Candidate: config.AICandidateConfig{
			Model: "test-model",
			URL:   server.URL + "/v1/chat/completions",
		},
		Provider: config.AIProviderConfig{URL: server.URL},
	}

	_, err := client.Chat(context.Background(), SimpleRequest("hello"), target)
	if err == nil {
		t.Fatal("expected error for HTTP error")
	}
}

func TestOpenAIClient_StreamChat_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "text/event-stream") {
			t.Errorf("expected SSE Accept header, got %s", accept)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n"))
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte("data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewOpenAICompatibleChatClient("test", nil)
	target := model.Target{
		ID: "test-model",
		Candidate: config.AICandidateConfig{
			Model: "test-model",
			URL:   server.URL + "/v1/chat/completions",
		},
		Provider: config.AIProviderConfig{URL: server.URL},
	}

	var contents []string
	done := make(chan struct{})
	cb := &captureCallback{
		onContent: func(c string) {
			contents = append(contents, c)
		},
		onComplete: func() {
			close(done)
		},
		onError: func(err error) {
			t.Errorf("unexpected error: %v", err)
			close(done)
		},
	}

	_, err := client.StreamChat(context.Background(), SimpleRequest("hello"), cb, target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case <-done:
		if len(contents) != 2 {
			t.Fatalf("expected 2 content chunks, got %d: %v", len(contents), contents)
		}
		if contents[0] != "hello" || contents[1] != "world" {
			t.Fatalf("unexpected contents: %v", contents)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for stream")
	}
}

func TestOpenAIClient_StreamChat_Cancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"chunk1\"}}]}\n\n"))
		flusher.Flush()
		time.Sleep(time.Second) // block to allow cancellation
	}))
	defer server.Close()

	client := NewOpenAICompatibleChatClient("test", nil)
	target := model.Target{
		ID: "test-model",
		Candidate: config.AICandidateConfig{
			Model: "test-model",
			URL:   server.URL + "/v1/chat/completions",
		},
		Provider: config.AIProviderConfig{URL: server.URL},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cb := &captureCallback{}

	handle, err := client.StreamChat(ctx, SimpleRequest("hello"), cb, target)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cancel()
	handle.Cancel()
	// should not hang
}

func TestDefaultFirstPacketProbe_Success(t *testing.T) {
	bridge := NewProbeBridge(&captureCallback{})
	go bridge.OnContent("test")
	probe := NewFirstPacketProbe()
	result, err := probe.AwaitFirstPacket(bridge, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestProbeBridge_OnContentDoesNotBlockAfterFirstPacket(t *testing.T) {
	var contents []string
	bridge := NewProbeBridge(&captureCallback{
		onContent: func(c string) {
			contents = append(contents, c)
		},
	})

	bridge.OnContent("first")

	probe := NewFirstPacketProbe()
	result, err := probe.AwaitFirstPacket(bridge, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected first packet success")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.OnContent("second")
		bridge.OnContent("third")
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnContent blocked after first packet probe")
	}

	if got := strings.Join(contents, ""); got != "firstsecondthird" {
		t.Fatalf("unexpected forwarded content: %s", got)
	}
}

func TestProbeBridge_OnThinkingCountsAsFirstPacket(t *testing.T) {
	var thinking []string
	bridge := NewProbeBridge(&captureCallback{
		onThinking: func(c string) {
			thinking = append(thinking, c)
		},
	})

	go bridge.OnThinking("ponder")

	probe := NewFirstPacketProbe()
	result, err := probe.AwaitFirstPacket(bridge, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected thinking to count as first packet")
	}
	if len(thinking) != 1 || thinking[0] != "ponder" {
		t.Fatalf("unexpected forwarded thinking: %v", thinking)
	}
}

func TestDefaultFirstPacketProbe_Timeout(t *testing.T) {
	bridge := NewProbeBridge(&captureCallback{})
	probe := NewFirstPacketProbe()
	result, err := probe.AwaitFirstPacket(bridge, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected timeout failure")
	}
}

func TestOpenAIClient_BuildRequestBodyDisablesThinkingExplicitly(t *testing.T) {
	client := NewOpenAICompatibleChatClient("test", nil)
	falseVal := false
	req := SimpleRequest("hello")
	req.Thinking = &falseVal
	target := model.Target{
		ID: "test-model",
		Candidate: config.AICandidateConfig{
			Model: "test-model",
		},
	}

	body := client.buildRequestBody(req, target, false)
	value, ok := body["enable_thinking"]
	if !ok {
		t.Fatal("expected enable_thinking to be set")
	}
	if enabled, ok := value.(bool); !ok || enabled {
		t.Fatalf("expected enable_thinking=false, got %#v", value)
	}
}

func TestExtractContent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"test response"}}]}`)
	result, err := extractContent(body, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "test response" {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestExtractContent_NoChoices(t *testing.T) {
	_, err := extractContent([]byte(`{"choices":[]}`), "test")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStreamError_Error(t *testing.T) {
	e := &streamError{code: 500, body: "internal error"}
	if e.Error() == "" {
		t.Fatal("expected non-empty error")
	}
}
