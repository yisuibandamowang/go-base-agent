package rag

import (
	"context"
	"strings"
	"testing"

	"go-base-agent/internal/infra/chat"
)

type fakeLLMService struct {
	chatFn   func(ctx context.Context, req chat.Request) (string, error)
	streamFn func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error)
}

func (f *fakeLLMService) Chat(ctx context.Context, req chat.Request) (string, error) {
	if f.chatFn != nil {
		return f.chatFn(ctx, req)
	}
	return "response", nil
}
func (f *fakeLLMService) ChatWithModel(ctx context.Context, req chat.Request, modelID string) (string, error) {
	return f.Chat(ctx, req)
}
func (f *fakeLLMService) StreamChat(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
	if f.streamFn != nil {
		return f.streamFn(ctx, req, cb)
	}
	return &fakeHandle{}, nil
}

type fakeHandle struct{}

func (f *fakeHandle) Cancel() {}

func TestPipeline_StreamChat_Basic(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnContent("hello")
				cb.OnContent(" world")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder())
	go p.StreamChat("test", "conv-1", "task-1", false, s)

	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Fatal("missing message event")
	}
	if !strings.Contains(body, `"delta":"hello"`) {
		t.Fatal("missing content")
	}
	if !strings.Contains(body, `"delta":" world"`) {
		t.Fatal("missing content")
	}
	if !strings.Contains(body, "event: finish") {
		t.Fatal("missing finish event")
	}
	if !strings.Contains(body, "event: done") {
		t.Fatal("missing done event")
	}
}

func TestPipeline_StreamChat_DeepThinking(t *testing.T) {
	var capturedReq chat.Request
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			cb.OnComplete()
			return &fakeHandle{}, nil
		},
	}

	s, _ := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder())
	p.StreamChat("test", "conv-1", "task-1", true, s)

	if capturedReq.Thinking == nil || !*capturedReq.Thinking {
		t.Fatal("expected thinking=true in request")
	}
}

func TestPipeline_StreamChat_Messages(t *testing.T) {
	var capturedReq chat.Request
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			capturedReq = req
			go func() {
				cb.OnContent("ok")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder())
	go p.StreamChat("hello world", "conv-1", "task-1", false, s)

	<-done

	if len(capturedReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(capturedReq.Messages))
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: message") {
		t.Fatal("missing message event")
	}
}

func TestPipeline_StreamChat_ThinkingCallback(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnThinking("let me think...")
				cb.OnContent("answer")
				cb.OnComplete()
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder())
	go p.StreamChat("test", "conv-1", "task-1", true, s)

	<-done

	body := w.Body.String()
	if !strings.Contains(body, `"type":"think"`) {
		t.Fatal("missing think type")
	}
	if !strings.Contains(body, `"delta":"let me think..."`) {
		t.Fatal("missing thinking content")
	}
	if !strings.Contains(body, `"type":"response"`) {
		t.Fatal("missing response type")
	}
}

func TestPipeline_StreamChat_Error(t *testing.T) {
	done := make(chan struct{})
	llm := &fakeLLMService{
		streamFn: func(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
			go func() {
				cb.OnError(nil)
				close(done)
			}()
			return &fakeHandle{}, nil
		},
	}

	s, w := newTestSSESender(t)
	p := NewPipeline(llm, NewDefaultPromptBuilder())
	go p.StreamChat("test", "conv-1", "task-1", false, s)

	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: finish") {
		t.Fatalf("missing finish event on error, body: %s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatal("missing done event on error")
	}
}
