package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	initializerInitialize "go-base-agent/internal/initializer/initialize"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestResolveCleanupConfirmationRequiresExplicitFlag(t *testing.T) {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	confirm := fs.String("confirm", "", "confirmation token")
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	if got := resolveCleanupConfirmation(fs, *confirm); got != "" {
		t.Fatalf("expected missing --confirm to stay empty, got %q", got)
	}

	fs = flag.NewFlagSet("cleanup", flag.ContinueOnError)
	confirm = fs.String("confirm", "", "confirmation token")
	if err := fs.Parse([]string{"--confirm", "CUSTOM-CONFIRM"}); err != nil {
		t.Fatalf("parse explicit confirm: %v", err)
	}
	if got := resolveCleanupConfirmation(fs, *confirm); got != "CUSTOM-CONFIRM" {
		t.Fatalf("expected explicit confirmation token, got %q", got)
	}
}

func TestLoadInitializerPropertiesUsesAgentTypeDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "initializer.properties"), []byte("auth.username=operator\n"), 0o644); err != nil {
		t.Fatalf("write initializer properties: %v", err)
	}

	loaded, err := loadInitializerProperties(dir, "")
	if err != nil {
		t.Fatalf("load initializer properties: %v", err)
	}
	if got := loaded.Get("auth.username", ""); got != "operator" {
		t.Fatalf("unexpected initializer username: %q", got)
	}
}

func TestLoadInitializerPropertiesRejectsExplicitMissingFile(t *testing.T) {
	_, err := loadInitializerProperties(t.TempDir(), filepath.Join(t.TempDir(), "missing.properties"))
	if err == nil {
		t.Fatal("expected missing initializer properties error")
	}
}

func TestWarmupAskRequiresNormalFinishBeforeDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: done\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	_, err := warmupAsk(context.Background(), server.Client(), server.URL, "token", "问题", "")
	if err == nil || !strings.Contains(err.Error(), "finish") {
		t.Fatalf("expected missing finish error, got: %v", err)
	}
}

func TestRunWarmupRetriesFailedTurn(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ragent/auth/login":
			writeWarmupSuccess(t, w, map[string]any{"token": "token"})
		case "/rag/v3/chat":
			attempts++
			if attempts == 1 {
				http.Error(w, "temporary failure", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: meta\ndata: {\"conversationId\":\"conv-1\"}\n\n")
			fmt.Fprint(w, "event: message\ndata: {\"type\":\"response\",\"delta\":\"回答\"}\n\n")
			fmt.Fprint(w, "event: finish\ndata: {\"messageId\":\"msg-1\",\"messageStatus\":\"NORMAL\"}\n\n")
			fmt.Fprint(w, "event: done\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := runWarmupWithOptions(context.Background(), server.URL, "admin", "admin", []initializerInitialize.Question{{Ref: "q1", Text: "问题"}}, warmupOptions{
		MaxAttempts:   3,
		RetryInterval: 0,
	})
	if err != nil {
		t.Fatalf("run warmup: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected one retry, got %d attempts", attempts)
	}
}

func TestOrderWarmupQuestionsUsesReproducibleSeed(t *testing.T) {
	questions := []initializerInitialize.Question{
		{Ref: "q1", Text: "问题1"},
		{Ref: "q2", Text: "问题2"},
		{Ref: "q3", Text: "问题3"},
	}
	seed := int64(42)
	first := orderWarmupQuestions(questions, &seed)
	second := orderWarmupQuestions(questions, &seed)
	if len(first) != len(questions) || len(second) != len(questions) {
		t.Fatalf("unexpected ordered question count: %d, %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Ref != second[i].Ref {
			t.Fatalf("expected same order for same seed, got %v and %v", first, second)
		}
	}
	if questions[0].Ref != "q1" {
		t.Fatalf("order helper must not mutate input: %v", questions)
	}
}

func writeWarmupSuccess(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": data}); err != nil {
		t.Fatalf("write warmup response: %v", err)
	}
}

func TestClearInitializerRedisDeletesOnlyKnownKeys(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	keys := []string{
		"ragent:intent:tree",
		"ragent:agent:resolved-prompts:v2",
		"ragent:query-term:mappings:default",
		"ragent:rag:answer:old",
		"ragent:memory:summary:lock:user:conversation",
		"ragent:agent:running:task",
		"ragent:stream:cancel:task",
		"ragent:stream:owner:task",
		"ragent:storage:bucket:init:kb",
		"ragent:vector:space:init:kb",
		"ragent:kb:space:init:kb",
		"ragent:initializer:lock",
		"unrelated:key",
	}
	for _, key := range keys {
		if err := client.Set(context.Background(), key, "1", 0).Err(); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	if err := clearInitializerRedis(context.Background(), client); err != nil {
		t.Fatalf("clearInitializerRedis: %v", err)
	}
	for _, key := range keys[:len(keys)-2] {
		if client.Exists(context.Background(), key).Val() != 0 {
			t.Fatalf("expected key to be deleted: %s", key)
		}
	}
	for _, key := range keys[len(keys)-2:] {
		if client.Exists(context.Background(), key).Val() == 0 {
			t.Fatalf("expected key to be preserved: %s", key)
		}
	}
}
