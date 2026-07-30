package chat

import (
	"context"
	"errors"
	"testing"
)

type fakeLLMService struct {
	chatFn          func(ctx context.Context, req Request) (string, error)
	chatWithModelFn func(ctx context.Context, req Request, modelID string) (string, error)
	streamFn        func(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error)
	chatCalls       int
	modelCalls      int
	streamCalls     int
}

func (f *fakeLLMService) Chat(ctx context.Context, req Request) (string, error) {
	f.chatCalls++
	if f.chatFn != nil {
		return f.chatFn(ctx, req)
	}
	return "", nil
}

func (f *fakeLLMService) ChatWithModel(ctx context.Context, req Request, modelID string) (string, error) {
	f.modelCalls++
	if f.chatWithModelFn != nil {
		return f.chatWithModelFn(ctx, req, modelID)
	}
	return f.Chat(ctx, req)
}

func (f *fakeLLMService) StreamChat(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error) {
	f.streamCalls++
	if f.streamFn != nil {
		return f.streamFn(ctx, req, cb)
	}
	return &noopStreamHandle{}, nil
}

func TestFallbackLLMService_ChatUsesPrimaryFirst(t *testing.T) {
	primary := &fakeLLMService{chatFn: func(ctx context.Context, req Request) (string, error) {
		return "local", nil
	}}
	fallback := &fakeLLMService{chatFn: func(ctx context.Context, req Request) (string, error) {
		return "cloud", nil
	}}

	svc := NewFallbackLLMService(primary, fallback)
	got, err := svc.Chat(context.Background(), SimpleRequest("hello"))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got != "local" {
		t.Fatalf("expected local response, got %q", got)
	}
	if primary.chatCalls != 1 || fallback.chatCalls != 0 {
		t.Fatalf("expected only primary call, got primary=%d fallback=%d", primary.chatCalls, fallback.chatCalls)
	}
}

func TestFallbackLLMService_ChatFallsBackWhenPrimaryFails(t *testing.T) {
	primary := &fakeLLMService{chatFn: func(ctx context.Context, req Request) (string, error) {
		return "", errors.New("ollama unavailable")
	}}
	fallback := &fakeLLMService{chatFn: func(ctx context.Context, req Request) (string, error) {
		return "cloud", nil
	}}

	svc := NewFallbackLLMService(primary, fallback)
	got, err := svc.Chat(context.Background(), SimpleRequest("hello"))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got != "cloud" {
		t.Fatalf("expected cloud fallback, got %q", got)
	}
	if primary.chatCalls != 1 || fallback.chatCalls != 1 {
		t.Fatalf("expected primary then fallback, got primary=%d fallback=%d", primary.chatCalls, fallback.chatCalls)
	}
}

func TestFallbackLLMService_ChatWithModelFallsBackToDefaultRouteWhenPrimaryFails(t *testing.T) {
	primary := &fakeLLMService{chatWithModelFn: func(ctx context.Context, req Request, modelID string) (string, error) {
		if modelID != "qwen3-local" {
			t.Fatalf("expected primary to receive requested local model, got %q", modelID)
		}
		return "", errors.New("ollama unavailable")
	}}
	fallback := &fakeLLMService{chatFn: func(ctx context.Context, req Request) (string, error) {
		return "cloud-default", nil
	}}

	svc := NewFallbackLLMService(primary, fallback)
	got, err := svc.ChatWithModel(context.Background(), SimpleRequest("hello"), "qwen3-local")
	if err != nil {
		t.Fatalf("chat with model: %v", err)
	}
	if got != "cloud-default" {
		t.Fatalf("expected cloud default route fallback, got %q", got)
	}
	if primary.modelCalls != 1 || fallback.chatCalls != 1 || fallback.modelCalls != 0 {
		t.Fatalf("expected primary model call then fallback default chat, got primaryModel=%d fallbackChat=%d fallbackModel=%d", primary.modelCalls, fallback.chatCalls, fallback.modelCalls)
	}
}

func TestFallbackLLMService_StreamChatFallsBackWhenPrimaryFails(t *testing.T) {
	primary := &fakeLLMService{streamFn: func(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error) {
		return nil, errors.New("ollama unavailable")
	}}
	fallback := &fakeLLMService{streamFn: func(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error) {
		cb.OnContent("cloud-stream")
		cb.OnComplete()
		return &noopStreamHandle{}, nil
	}}
	received := ""
	cb := &captureCallback{onContent: func(content string) {
		received = content
	}}

	svc := NewFallbackLLMService(primary, fallback)
	if _, err := svc.StreamChat(context.Background(), SimpleRequest("hello"), cb); err != nil {
		t.Fatalf("stream chat: %v", err)
	}
	if received != "cloud-stream" {
		t.Fatalf("expected cloud stream content, got %q", received)
	}
	if primary.streamCalls != 1 || fallback.streamCalls != 1 {
		t.Fatalf("expected primary then fallback stream, got primary=%d fallback=%d", primary.streamCalls, fallback.streamCalls)
	}
}
