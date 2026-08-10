package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestZhinuoAnalyzerUsesFirstFallbackModel(t *testing.T) {
	var gotModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ := body["model"].(string)
		gotModels = append(gotModels, gotModel)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"分析结果"}}]}`))
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:  true,
		APIKey:  "test-key",
		BaseURL: server.URL + "/v1",
		Model:   "other-model",
	}, nil)

	result, err := analyzer.Analyze(context.Background(), AnalysisInput{
		Question: "为什么失败",
		LogText:  "ERROR PayCenterFailed",
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Content != "分析结果" {
		t.Fatalf("Content = %q", result.Content)
	}
	if strings.Join(gotModels, ",") != "codex-ccmax/gpt-5.5" {
		t.Fatalf("models = %#v", gotModels)
	}
}

func TestZhinuoAnalyzerFallsBackToNext360Model(t *testing.T) {
	var gotModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ := body["model"].(string)
		gotModels = append(gotModels, gotModel)
		if gotModel == "codex-ccmax/gpt-5.5" {
			http.Error(w, `{"error":{"message":"no available channel"}}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"降级分析结果"}}]}`))
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:  true,
		APIKey:  "test-key",
		BaseURL: server.URL + "/v1",
	}, nil)

	result, err := analyzer.Analyze(context.Background(), AnalysisInput{Question: "为什么失败", LogText: "ERROR"})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Content != "降级分析结果" {
		t.Fatalf("Content = %q", result.Content)
	}
	want := "codex-ccmax/gpt-5.5,deepseek/deepseek-v4-flash-internal"
	if strings.Join(gotModels, ",") != want {
		t.Fatalf("models = %#v, want %s", gotModels, want)
	}
}

func TestZhinuoAnalyzerFallsBackToBailianAfter360ModelsFail(t *testing.T) {
	var gotPaths []string
	var gotModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ := body["model"].(string)
		gotModels = append(gotModels, gotModel)
		if strings.Contains(r.URL.Path, "/compatible-mode/") {
			if r.Header.Get("Authorization") != "Bearer bailian-key" {
				t.Fatalf("bailian Authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"百炼兜底结果"}}]}`))
			return
		}
		http.Error(w, `{"error":{"message":"no available channel"}}`, http.StatusBadRequest)
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:         true,
		APIKey:         "test-key",
		BaseURL:        server.URL + "/v1",
		BailianAPIKey:  "bailian-key",
		BailianBaseURL: server.URL,
	}, nil)

	result, err := analyzer.Analyze(context.Background(), AnalysisInput{Question: "为什么失败", LogText: "ERROR"})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if result.Content != "百炼兜底结果" {
		t.Fatalf("Content = %q", result.Content)
	}
	if gotModels[len(gotModels)-1] != "qwen3-max" {
		t.Fatalf("last model = %q", gotModels[len(gotModels)-1])
	}
	if !strings.Contains(gotPaths[len(gotPaths)-1], "/compatible-mode/v1/chat/completions") {
		t.Fatalf("last path = %q", gotPaths[len(gotPaths)-1])
	}
}

func TestZhinuoAnalyzerStreamsContentChunks(t *testing.T) {
	var gotModel string
	var gotStream bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotModel, _ = body["model"].(string)
		gotStream, _ = body["stream"].(bool)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"第一段\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"第二段\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	analyzer := NewZhinuoAnalyzer(AnalyzerConfig{
		Enable:  true,
		APIKey:  "test-key",
		BaseURL: server.URL + "/v1",
	}, nil)

	var chunks []string
	result, err := analyzer.AnalyzeStream(context.Background(), AnalysisInput{
		Question: "为什么失败",
		LogText:  "ERROR request host access denied!",
	}, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("AnalyzeStream() error = %v", err)
	}
	if gotModel != "codex-ccmax/gpt-5.5" {
		t.Fatalf("model = %q", gotModel)
	}
	if !gotStream {
		t.Fatal("stream = false, want true")
	}
	if result.Content != "第一段第二段" {
		t.Fatalf("Content = %q", result.Content)
	}
	if strings.Join(chunks, "") != "第一段第二段" {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestBuildAnalysisPromptIncludesQuestionLogsAndCode(t *testing.T) {
	prompt := buildAnalysisPrompt(AnalysisInput{
		Question: "订单为什么失败",
		LogText:  "PayCenterFailed code=9253310473",
		CodeEvidence: []CodeEvidence{{
			File:    "/Users/work_project/360/member/pay/service/pay.go",
			Line:    "88",
			Content: "return PayCenterFailed",
		}},
	})

	for _, want := range []string{"订单为什么失败", "PayCenterFailed", "pay.go:88", "代码链路"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain %q\n%s", want, prompt)
		}
	}
}
