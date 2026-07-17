package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/mq"
	infrarerank "go-base-agent/internal/infra/rerank"

	"github.com/gin-gonic/gin"
)

func TestStatusProbeRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	registerStatusRoutes(r)

	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
	}{
		{name: "api health", path: "/api/ragent/health", contentType: "application/json", body: `"code":"0"`},
		{name: "root health", path: "/health", contentType: "application/json", body: `"code":"0"`},
		{name: "healthz", path: "/healthz", contentType: "application/json", body: `"code":"0"`},
		{name: "live", path: "/live", contentType: "application/json", body: `"code":"0"`},
		{name: "livez", path: "/livez", contentType: "application/json", body: `"code":"0"`},
		{name: "ready", path: "/ready", contentType: "application/json", body: `"code":"0"`},
		{name: "readyz", path: "/readyz", contentType: "application/json", body: `"code":"0"`},
		{name: "metrics", path: "/metrics", contentType: "text/plain", body: "# HELP ragent_up"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, tt.path, nil)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d with body %s", tt.path, w.Code, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Fatalf("expected Content-Type to contain %q, got %q", tt.contentType, ct)
			}
			if body := w.Body.String(); !strings.Contains(body, tt.body) {
				t.Fatalf("expected body to contain %q, got %s", tt.body, body)
			}
		})
	}
}

func TestRagEvalHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/api/ragent/rag/eval", ragEval(&fakeEvalRetriever{}))

	t.Run("missing question returns client error", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"A000001"`) {
			t.Fatalf("expected client error for missing question, got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("returns retrieved chunks", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval?question=hello", nil)
		r.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `"retrievedChunkIds":["chunk-1"]`) || !strings.Contains(body, `"hasKb":true`) {
			t.Fatalf("expected eval retrieval response, got %d %s", w.Code, body)
		}
	})
}

func TestSetupMQ_FallsBackToNoopWhenNameServerMissing(t *testing.T) {
	producer, consumer, shutdown := setupMQ(config.RocketMQConfig{})
	defer shutdown()

	if _, ok := producer.(interface {
		Send(context.Context, mq.Message) (*mq.SendResult, error)
	}); !ok {
		t.Fatalf("producer should implement mq.Producer")
	}
	if _, ok := consumer.(*mq.NoopConsumer); !ok {
		t.Fatalf("expected noop consumer without name server, got %T", consumer)
	}
}

func TestBuildChatClients_IncludesAnthropicProvider(t *testing.T) {
	clients := buildChatClients(config.AIConfig{
		Providers: config.AIProvidersConfig{
			"anthropic-main": {
				Protocol: "anthropic",
				URL:      "https://api.anthropic.com",
				Endpoints: map[string]string{
					"chat": "/v1/messages",
				},
			},
		},
	})
	if len(clients) != 1 {
		t.Fatalf("expected 1 chat client, got %d", len(clients))
	}
	if clients[0].Provider() != "anthropic-main" {
		t.Fatalf("unexpected provider %q", clients[0].Provider())
	}
}

func TestBuildRerankClients_IncludesHTTPProviderAndNoopFallback(t *testing.T) {
	clients := buildRerankClients(config.AIConfig{
		Providers: config.AIProvidersConfig{
			"bailian": {
				Protocol: "openai-compatible",
				URL:      "https://dashscope.aliyuncs.com",
				Endpoints: map[string]string{
					"rerank": "/api/v1/services/rerank/text-rerank/text-rerank",
				},
			},
		},
	})
	if len(clients) != 2 {
		t.Fatalf("expected http rerank client plus noop fallback, got %d", len(clients))
	}
	if _, ok := clients[0].(*infrarerank.HTTPClient); !ok {
		t.Fatalf("expected first rerank client to be HTTPClient, got %T", clients[0])
	}
	if clients[0].Provider() != "bailian" {
		t.Fatalf("unexpected provider %q", clients[0].Provider())
	}
	if _, ok := clients[1].(*infrarerank.NoopClient); !ok {
		t.Fatalf("expected noop fallback, got %T", clients[1])
	}
}

func TestRagSettingsExposesFullConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		RAG: config.RAGConfig{
			Vector: config.RAGVectorConfig{Type: "pg"},
			Default: config.RAGDefaultConfig{
				CollectionName: "rag_default_store",
				Dimension:      1536,
				MetricType:     "COSINE",
			},
			QueryRewrite: config.RAGQueryRewriteConfig{
				Enabled:            true,
				MaxHistoryMessages: 4,
				MaxHistoryChars:    500,
			},
			RateLimit: config.RAGRateLimitConfig{
				Global: config.RAGRateLimitGlobalConfig{
					Enabled:        true,
					MaxConcurrent:  1,
					MaxWaitSeconds: 3,
					LeaseSeconds:   30,
					PollIntervalMs: 200,
				},
			},
			Memory: config.RAGMemoryConfig{
				HistoryKeepTurns:  4,
				SummaryStartTurns: 5,
				SummaryEnabled:    true,
				TTLMinutes:        60,
				SummaryMaxChars:   200,
				TitleMaxLength:    30,
			},
		},
		AI: config.AIConfig{
			Providers: config.AIProvidersConfig{
				"openai": {
					URL:      "https://api.openai.com",
					APIKey:   "sk-1234567890abcdef",
					Protocol: "openai-compatible",
					Endpoints: map[string]string{
						"chat": "/v1/chat/completions",
					},
				},
			},
			Selection: config.AISelectionConfig{
				FailureThreshold:          2,
				OpenDurationMs:            30000,
				FirstPacketTimeoutSeconds: 60,
			},
			Stream: config.AIStreamConfig{
				MessageChunkSize: 1,
			},
			Chat: config.AIChatConfig{
				DefaultModel:      "qwen3-max",
				DeepThinkingModel: "qwen3-max",
				Candidates: []config.AICandidateConfig{
					{
						ID:               "qwen3-max",
						Provider:         "openai",
						Model:            "qwen3-max",
						SupportsThinking: true,
						Priority:         1,
					},
				},
			},
			Embedding: config.AIEmbeddingConfig{
				DefaultModel: "qwen-emb-8b",
				Candidates: []config.AIEmbeddingCandidateConfig{
					{
						ID:        "qwen-emb-8b",
						Provider:  "openai",
						Model:     "qwen-emb-8b",
						Dimension: 1536,
						Priority:  1,
					},
				},
			},
			Rerank: config.AIRerankConfig{
				DefaultModel: "qwen3-rerank",
				Candidates: []config.AIRerankCandidateConfig{
					{
						ID:       "qwen3-rerank",
						Provider: "openai",
						Model:    "qwen3-rerank",
						Priority: 1,
					},
				},
			},
			VLM: config.AIVLMConfig{
				DefaultModel: "qwen-vl",
			},
		},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ragent/rag/settings", nil)
	ragSettings(cfg)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Code string         `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if resp.Code != "0" {
		t.Fatalf("unexpected code %s", resp.Code)
	}

	upload := resp.Data["upload"].(map[string]any)
	if upload["maxRequestSize"].(float64) != 100<<20 {
		t.Fatalf("unexpected maxRequestSize: %#v", upload["maxRequestSize"])
	}
	ragCfg := resp.Data["rag"].(map[string]any)
	if ragCfg["default"].(map[string]any)["dimension"].(float64) != 1536 {
		t.Fatalf("unexpected rag default dimension: %#v", ragCfg["default"])
	}
	if ragCfg["memory"].(map[string]any)["titleMaxLength"].(float64) != 30 {
		t.Fatalf("unexpected memory settings: %#v", ragCfg["memory"])
	}
	if ragCfg["rateLimit"].(map[string]any)["global"].(map[string]any)["pollIntervalMs"].(float64) != 200 {
		t.Fatalf("unexpected rate limit settings: %#v", ragCfg["rateLimit"])
	}
	aiCfg := resp.Data["ai"].(map[string]any)
	provider := aiCfg["providers"].(map[string]any)["openai"].(map[string]any)
	if provider["apiKey"].(string) == "sk-1234567890abcdef" || !strings.Contains(provider["apiKey"].(string), "***") {
		t.Fatalf("expected masked apiKey, got %#v", provider["apiKey"])
	}
	if aiCfg["chat"].(map[string]any)["defaultModel"].(string) != "qwen3-max" {
		t.Fatalf("unexpected chat config: %#v", aiCfg["chat"])
	}
}

func TestBuildDocumentParserRegistryPrefersMinerUForPdf(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file-urls/batch":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"batch_id":"batch-1","file_urls":["` + server.URL + `/upload-1"]}}`))
		case "/extract-results/batch/batch-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"extract_result":[{"state":"SUCCEEDED","full_zip_url":"` + server.URL + `/zip-1","err_msg":""}]}}`))
		case "/upload-1":
			w.WriteHeader(http.StatusOK)
		case "/zip-1":
			_, _ = w.Write(testMinerUZipBytes(t))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		MinerU: config.MinerUConfig{
			APIURL:           server.URL,
			APIKey:           "token",
			PollIntervalSecs: 1,
			TimeoutSecs:      5,
			EnableTable:      true,
			EnableFormula:    true,
			Language:         "ch",
		},
		RustFS: config.RustFSConfig{
			URL:             "http://localhost:9000",
			AccessKeyID:     "key",
			SecretAccessKey: "secret",
			KBBucket:        "kb",
			AssetBucket:     "assets",
		},
	}

	reg := buildDocumentParserRegistry(cfg, nil, false, nil)
	parsed, err := reg.Parse(context.Background(), []byte("pdf-bytes"), "application/pdf", map[string]string{
		"sourceFile": "会员能力.pdf",
		"documentId": "doc-1",
	})
	if err != nil {
		t.Fatalf("parse pdf via registry: %v", err)
	}
	if parsed.Metadata["parser"] != string(rag.ParserMinerU) {
		t.Fatalf("expected mineru parser, got %+v", parsed.Metadata)
	}
	if !strings.Contains(rag.RenderBlocks(parsed.Blocks), "MinerU 解析成功") {
		t.Fatalf("expected mineru markdown content, got %+v", parsed.Blocks)
	}
}

func testMinerUZipBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("result.md")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte("# MinerU\n\nMinerU 解析成功")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestDemoRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	api := r.Group("/api/ragent")
	registerDemoRoutes(r, api, newDemoHandler())

	t.Run("hello", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test/langchain4j/hello", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hello from Go") {
			t.Fatalf("unexpected hello response: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("simple stream", func(t *testing.T) {
		ts := httptest.NewServer(r)
		defer ts.Close()

		resp, err := http.Get(ts.URL + "/api/ragent/test/langchain4j/simple-stream-chat?question=hello")
		if err != nil {
			t.Fatalf("stream request: %v", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read stream body: %v", err)
		}
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "event:message") || !strings.Contains(string(body), "[DONE]") {
			t.Fatalf("unexpected stream response: %d %s", resp.StatusCode, string(body))
		}
	})

	t.Run("image generation", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/test/langchain4j/image-generation", strings.NewReader(`{"prompt":"一只猫"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "data:image/png;base64") {
			t.Fatalf("unexpected image generation response: %d %s", w.Code, body)
		}
	})

	t.Run("image analysis", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/ragent/test/langchain4j/image-analysis", strings.NewReader(`{"imageUrl":"https://example.com/a.png","prompt":"看看这张图"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "demo 分析") || !strings.Contains(body, "example.com/a.png") {
			t.Fatalf("unexpected image analysis response: %d %s", w.Code, body)
		}
	})
}

type fakeEvalRetriever struct{}

func (f *fakeEvalRetriever) Retrieve(ctx context.Context, question string, topK int) ([]rag.RetrievedChunk, error) {
	return []rag.RetrievedChunk{{
		ID:    "chunk-1",
		Text:  "hello context",
		Score: 0.91,
		Metadata: map[string]string{
			"doc_id":  "doc-1",
			"kb_name": "kb",
		},
	}}, nil
}
