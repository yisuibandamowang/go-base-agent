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

	intentHandler "go-base-agent/internal/biz/intent_tree/handler"
	intentModel "go-base-agent/internal/biz/intent_tree/model"
	intentRepo "go-base-agent/internal/biz/intent_tree/repo"
	intentService "go-base-agent/internal/biz/intent_tree/service"
	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/mq"
	"go-base-agent/internal/infra/chat"
	infrarerank "go-base-agent/internal/infra/rerank"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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

func TestMaybeWrapMetadataEnrichingRetrieverDisabled(t *testing.T) {
	base := &rag.NoopRetriever{}
	got := maybeWrapMetadataEnrichingRetriever(base, nil, false)
	if got != base {
		t.Fatalf("expected context enrich disabled to keep base retriever, got %T", got)
	}
}

func TestMaybeWrapMetadataEnrichingRetrieverEnabled(t *testing.T) {
	base := &rag.NoopRetriever{}
	got := maybeWrapMetadataEnrichingRetriever(base, nil, true)
	if _, ok := got.(*rag.MetadataEnrichingRetriever); !ok {
		t.Fatalf("expected context enrich enabled to wrap metadata retriever, got %T", got)
	}
}

func TestMaybeWrapRerankRetrieverDisabled(t *testing.T) {
	base := &rag.NoopRetriever{}
	got := maybeWrapRerankRetriever(base, nil, false)
	if got != base {
		t.Fatalf("expected rerank disabled to keep base retriever, got %T", got)
	}
}

func TestMaybeWrapRerankRetrieverEnabled(t *testing.T) {
	base := &rag.NoopRetriever{}
	got := maybeWrapRerankRetriever(base, nil, true)
	if _, ok := got.(*rag.RerankRetriever); !ok {
		t.Fatalf("expected rerank enabled to wrap rerank retriever, got %T", got)
	}
}

func TestKeywordSearchChannelDefaultsEnabled(t *testing.T) {
	if !keywordSearchChannelEnabled(config.RAGSearchChannelConfig{}) {
		t.Fatal("expected keyword search channel to default enabled")
	}
	disabled := false
	if keywordSearchChannelEnabled(config.RAGSearchChannelConfig{Enabled: &disabled}) {
		t.Fatal("expected explicit keyword disabled config to be honored")
	}
}

func TestRagEvalHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	api := r.Group("/api/ragent")
	registerRagEvalRoute(api, &fakeEvalRewriter{}, &fakeEvalRetriever{}, false, 10)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval?question=hello", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected eval route to be disabled by config, got %d %s", w.Code, w.Body.String())
	}

	registerRagEvalRoute(api, &fakeEvalRewriter{}, &fakeEvalRetriever{}, true, 10)

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
		for _, want := range []string{
			`"retrievedChunkIds":["chunk-1"]`,
			`"retrievedDocIds":["FAQ_VAC_001"]`,
			`"retrievedContextDocIds":["FAQ_VAC_001"]`,
			`"hasKb":true`,
			`"hasMcp":false`,
			`"mcpContext":""`,
			`"subIntents":["hello","hello follow-up"]`,
			`"intentLeafIds":[null,null]`,
			`"latencyMs":`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected eval response to contain %s, got %d %s", want, w.Code, body)
			}
		}
		if w.Code != http.StatusOK {
			t.Fatalf("expected eval retrieval response, got %d %s", w.Code, body)
		}
	})
}

func TestRagEvalHandlerReturnsResolvedIntentLeafIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	api := r.Group("/api/ragent")
	registerRagEvalRoute(api, &fakeEvalRewriter{}, &fakeEvalRetriever{}, true, 10, fakeEvalIntentResolver{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval?question=hello", nil)
	r.ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", w.Code, body)
	}
	if !strings.Contains(body, `"intentLeafIds":["leaf-1","leaf-2"]`) {
		t.Fatalf("expected resolved leaf ids, got %s", body)
	}
}

func TestRagEvalHandlerPassesIntentContextToRetriever(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	api := r.Group("/api/ragent")
	retriever := &fakeEvalContextRetriever{}
	registerRagEvalRoute(api, &fakeEvalRewriter{}, retriever, true, 12, fakeEvalIntentResolver{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval?question=hello", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}
	if retriever.simpleCalls != 0 || retriever.contextCalls != 1 {
		t.Fatalf("expected context retrieval only, simple=%d context=%d", retriever.simpleCalls, retriever.contextCalls)
	}
	sc := retriever.lastContext
	if sc.OriginalQuestion != "hello" || sc.RewrittenQuestion != "hello 改写" || sc.TopK != 12 {
		t.Fatalf("unexpected search context basics: %+v", sc)
	}
	if len(sc.SubQuestions) != 2 || sc.SubQuestions[1] != "hello follow-up" {
		t.Fatalf("expected rewritten sub questions, got %+v", sc.SubQuestions)
	}
	if len(sc.Intents) != 2 || sc.Intents[0].TopLeafID() != "leaf-1" || sc.Intents[1].TopLeafID() != "leaf-2" {
		t.Fatalf("expected resolved intents, got %+v", sc.Intents)
	}
}

func TestRagEvalHandlerIncludesMcpContextWhenRetrieverProvidesIt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	api := r.Group("/api/ragent")
	retriever := &fakeEvalMcpContextRetriever{}
	registerRagEvalRoute(api, &fakeEvalRewriter{}, retriever, true, 10, fakeEvalIntentResolver{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ragent/rag/eval?question=hello", nil)
	r.ServeHTTP(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", w.Code, body)
	}
	if retriever.mcpCalls != 1 {
		t.Fatalf("expected one mcp context call, got %d", retriever.mcpCalls)
	}
	for _, want := range []string{
		`"hasMcp":true`,
		`"mcpContext":"\u003ctool-data\u003e`,
		`weather_query`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected eval response to contain %s, got %s", want, body)
		}
	}
}

func TestBuildDocumentParserRegistryRegistersTikaWhenConfigured(t *testing.T) {
	cfg := &config.Config{
		RAG: config.RAGConfig{
			Parser: config.RAGParserConfig{TikaURL: "http://localhost:9998"},
		},
	}
	reg := buildDocumentParserRegistry(cfg, nil, false, nil)
	if !reg.Supports("application/rtf") {
		t.Fatal("expected tika parser to be registered for rtf")
	}
	foundTika := false
	for _, typ := range reg.List() {
		if typ == rag.ParserTika {
			foundTika = true
		}
	}
	if !foundTika {
		t.Fatal("expected tika parser type in registry")
	}
	parsed, err := reg.Parse(context.Background(), []byte("能力,说明\n积分查询,支持"), "text/csv; charset=utf-8", nil)
	if err != nil {
		t.Fatalf("expected csv parser to win before tika: %v", err)
	}
	if len(parsed.Blocks) != 1 || parsed.Blocks[0].Type != rag.BlockTable {
		t.Fatalf("expected csv table block before tika fallback, got %+v", parsed.Blocks)
	}
}

func TestRegisterIntentRoutesIncludesTermMappingDetailCompatibilityPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&intentModel.QueryTermMapping{}); err != nil {
		t.Fatalf("migrate mapping: %v", err)
	}
	mapping := &intentModel.QueryTermMapping{Domain: "member", SourceTerm: "VIP", TargetTerm: "会员", Enabled: 1}
	if err := gdb.Create(mapping).Error; err != nil {
		t.Fatalf("seed mapping: %v", err)
	}

	r := gin.New()
	api := r.Group("/api/ragent")
	h := intentHandler.NewIntentHandler(intentService.NewIntentService(
		intentRepo.NewIntentRepo(gdb),
		intentRepo.NewTermMappingRepo(gdb),
		gdb,
	))
	registerIntentRoutes(api, h)

	for _, path := range []string{
		"/api/ragent/mappings/" + mapping.ID,
		"/api/ragent/intent-tree/term-mappings/" + mapping.ID,
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sourceTerm":"VIP"`) {
			t.Fatalf("expected mapping detail from %s, got %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestSetupMQ_FallsBackToNoopWhenNameServerMissing(t *testing.T) {
	producer, consumer, shutdown, enabled := setupMQ(config.RocketMQConfig{})
	defer shutdown()

	if _, ok := producer.(interface {
		Send(context.Context, mq.Message) (*mq.SendResult, error)
	}); !ok {
		t.Fatalf("producer should implement mq.Producer")
	}
	if _, ok := consumer.(*mq.NoopConsumer); !ok {
		t.Fatalf("expected noop consumer without name server, got %T", consumer)
	}
	if enabled {
		t.Fatal("expected mq to be disabled without name server")
	}
}

func TestMaybeInitIntentTreeFromFactoryHonorsConfig(t *testing.T) {
	ctx := context.Background()
	disabledSvc := &fakeIntentTreeFactoryInitializer{created: 18}
	if err := maybeInitIntentTreeFromFactory(ctx, config.AppConfig{}, disabledSvc); err != nil {
		t.Fatalf("disabled init should not fail: %v", err)
	}
	if disabledSvc.calls != 0 {
		t.Fatalf("expected disabled config to skip init, got %d calls", disabledSvc.calls)
	}

	enabledSvc := &fakeIntentTreeFactoryInitializer{created: 18}
	if err := maybeInitIntentTreeFromFactory(ctx, config.AppConfig{
		IntentTree: config.AppIntentTreeConfig{InitFromFactory: true},
	}, enabledSvc); err != nil {
		t.Fatalf("enabled init should not fail: %v", err)
	}
	if enabledSvc.calls != 1 {
		t.Fatalf("expected enabled config to call init once, got %d", enabledSvc.calls)
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

type fakeIntentTreeFactoryInitializer struct {
	calls   int
	created int
}

func (f *fakeIntentTreeFactoryInitializer) InitFromFactory(context.Context) (int, error) {
	f.calls++
	return f.created, nil
}

func TestBuildLocalPreferredChatConfig_SynthesizesOllamaCandidate(t *testing.T) {
	aiCfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"ollama": {
				Protocol: "ollama",
			},
		},
	}

	got, ok := buildLocalPreferredChatConfig(aiCfg)
	if !ok {
		t.Fatal("expected ollama provider to enable local preferred chat config")
	}
	if got.Chat.DefaultModel != preferredLocalChatID {
		t.Fatalf("expected default model %q, got %q", preferredLocalChatID, got.Chat.DefaultModel)
	}
	if len(got.Chat.Candidates) != 1 {
		t.Fatalf("expected 1 preferred candidate, got %d", len(got.Chat.Candidates))
	}
	candidate := got.Chat.Candidates[0]
	if candidate.ID != preferredLocalChatID || candidate.Provider != preferredLocalChatProvider || candidate.Model != preferredLocalChatModel {
		t.Fatalf("unexpected preferred candidate: %+v", candidate)
	}
	provider, ok := got.Providers[preferredLocalChatProvider]
	if !ok {
		t.Fatal("expected ollama provider to remain present")
	}
	if provider.Protocol != "openai-compatible" {
		t.Fatalf("expected ollama protocol to be normalized, got %q", provider.Protocol)
	}
	if provider.URL != defaultOllamaURL {
		t.Fatalf("expected ollama URL to default to %q, got %q", defaultOllamaURL, provider.URL)
	}
	if provider.Endpoints["chat"] != "/v1/chat/completions" {
		t.Fatalf("expected ollama chat endpoint to default correctly, got %q", provider.Endpoints["chat"])
	}
}

func TestBuildPreferredLLMService_UsesLocalFallbackWrapper(t *testing.T) {
	aiCfg := config.AIConfig{
		Providers: config.AIProvidersConfig{
			"ollama": {
				Protocol: "ollama",
			},
			"bailian": {
				URL:      "https://dashscope.aliyuncs.com",
				Protocol: "openai-compatible",
				Endpoints: map[string]string{
					"chat": "/compatible-mode/v1/chat/completions",
				},
			},
		},
	}
	fallback := &fakePreferredLLMService{}

	got := buildPreferredLLMService(aiCfg, nil, nil, nil, fallback)
	if _, ok := got.(*chat.FallbackLLMService); !ok {
		t.Fatalf("expected preferred LLM service to wrap local route with fallback, got %T", got)
	}
}

type fakePreferredLLMService struct{}

func (f *fakePreferredLLMService) Chat(context.Context, chat.Request) (string, error) {
	return "", nil
}

func (f *fakePreferredLLMService) ChatWithModel(context.Context, chat.Request, string) (string, error) {
	return "", nil
}

func (f *fakePreferredLLMService) StreamChat(context.Context, chat.Request, chat.StreamCallback) (chat.StreamHandle, error) {
	return nil, nil
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

func TestToMcpServerSpecsCarriesDomains(t *testing.T) {
	specs := toMcpServerSpecs([]config.RAGMCPServerConfig{{
		Name:    "ticket",
		URL:     "http://localhost:9099",
		Domains: []string{"ticket"},
	}})
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if len(specs[0].Domains) != 1 || specs[0].Domains[0] != "ticket" {
		t.Fatalf("expected domains to be propagated, got %+v", specs[0].Domains)
	}
}

func TestRagSettingsExposesFullConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	queryRewriteEnabled := true
	rerankEnabled := true
	contextEnrichEnabled := true
	traceEnabled := true
	webSearchEnabled := true
	rateLimitEnabled := true
	codeRepoPath := "/Users/work_project/360/member"

	cfg := &config.Config{
		RAG: config.RAGConfig{
			Vector: config.RAGVectorConfig{Type: "pg"},
			Default: config.RAGDefaultConfig{
				CollectionName: "rag_default_store",
				Dimension:      1536,
				MetricType:     "COSINE",
			},
			Context: config.RAGContextConfig{
				Enrich: config.RAGContextEnrichConfig{Enabled: &contextEnrichEnabled},
			},
			Code: config.RAGCodeConfig{
				RepoPath: codeRepoPath,
			},
			QueryRewrite: config.RAGQueryRewriteConfig{
				Enabled:            &queryRewriteEnabled,
				MaxHistoryMessages: 4,
				MaxHistoryChars:    500,
			},
			Rerank: config.RAGRerankConfig{Enabled: &rerankEnabled},
			RateLimit: config.RAGRateLimitConfig{
				Global: config.RAGRateLimitGlobalConfig{
					Enabled:        &rateLimitEnabled,
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
			Search: config.RAGSearchConfig{
				DefaultTopK: 8,
				Channels: config.RAGSearchChannelsConfig{
					VectorGlobal: config.RAGSearchChannelConfig{
						Enabled:                         &queryRewriteEnabled,
						ConfidenceThreshold:             0.7,
						SingleIntentSupplementThreshold: 0.85,
						CandidateBudget:                 88,
						TopKMultiplier:                  4,
					},
					IntentDirected: config.RAGSearchChannelConfig{
						Enabled:        &queryRewriteEnabled,
						MinIntentScore: 0.45,
						TopKMultiplier: 3,
					},
					Keyword: config.RAGSearchChannelConfig{
						Enabled:        &queryRewriteEnabled,
						Mode:           "both",
						TopKMultiplier: 2,
					},
					WebSearch: config.RAGWebSearchConfig{
						Enabled:        webSearchEnabled,
						APIKey:         "ydc-secret",
						Count:          6,
						TimeoutSeconds: 11,
					},
				},
				Fusion: config.RAGSearchFusionConfig{
					Strategy:             "rrf",
					RRFK:                 61,
					RerankCandidateLimit: 40,
				},
			},
			Trace: config.RAGTraceConfig{Enabled: &traceEnabled},
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
		RustFS: config.RustFSConfig{
			URL:         "http://localhost:9000",
			Region:      "us-east-1",
			KBBucket:    "ragent-kb",
			AssetBucket: "ragent-assets",
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
	engine := resp.Data["engine"].(map[string]any)
	if engine["type"].(string) != "workflow" {
		t.Fatalf("unexpected engine settings: %#v", engine)
	}
	backends := resp.Data["backends"].(map[string]any)
	if backends["storage"].(map[string]any)["type"].(string) != "s3" {
		t.Fatalf("unexpected storage backend: %#v", backends["storage"])
	}
	if backends["vector"].(map[string]any)["type"].(string) != "pg" {
		t.Fatalf("unexpected vector backend: %#v", backends["vector"])
	}
	if backends["keyword"].(map[string]any)["type"].(string) != "pg" {
		t.Fatalf("unexpected keyword backend: %#v", backends["keyword"])
	}
	if backends["graph"].(map[string]any)["type"].(string) != "none" {
		t.Fatalf("unexpected graph backend: %#v", backends["graph"])
	}
	ragCfg := resp.Data["rag"].(map[string]any)
	if ragCfg["default"].(map[string]any)["dimension"].(float64) != 1536 {
		t.Fatalf("unexpected rag default dimension: %#v", ragCfg["default"])
	}
	if ragCfg["features"].(map[string]any)["trace"].(bool) != true {
		t.Fatalf("unexpected feature settings: %#v", ragCfg["features"])
	}
	search := ragCfg["search"].(map[string]any)
	if search["recallBudget"].(float64) != 88 {
		t.Fatalf("unexpected search recall budget: %#v", search)
	}
	channels := search["channels"].(map[string]any)
	if _, ok := channels["timeoutMs"]; !ok {
		t.Fatalf("missing search channel timeout: %#v", channels)
	}
	if channels["webSearch"].(map[string]any)["apiKeyConfigured"].(bool) != true {
		t.Fatalf("unexpected web search settings: %#v", channels["webSearch"])
	}
	if search["fusion"].(map[string]any)["rrfK"].(float64) != 61 {
		t.Fatalf("unexpected fusion settings: %#v", search["fusion"])
	}
	if ragCfg["memory"].(map[string]any)["titleMaxLength"].(float64) != 30 {
		t.Fatalf("unexpected memory settings: %#v", ragCfg["memory"])
	}
	if ragCfg["rateLimit"].(map[string]any)["global"].(map[string]any)["pollIntervalMs"].(float64) != 200 {
		t.Fatalf("unexpected rate limit settings: %#v", ragCfg["rateLimit"])
	}
	if ragCfg["code"].(map[string]any)["repoPath"].(string) != codeRepoPath {
		t.Fatalf("unexpected code repo path: %#v", ragCfg["code"])
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

func TestDemoImageAnalysisUsesVLMWhenConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	api := r.Group("/api/ragent")
	vlmSvc := &fakeDemoVLMService{description: "图片里是一张会员权益说明图"}
	registerDemoRoutes(r, api, newDemoHandlerWithVLM(vlmSvc))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test/langchain4j/image-analysis", strings.NewReader(`{"imageUrl":"`+demoImageDataURI+`","prompt":"看看这张图"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"mode":"vlm"`) || !strings.Contains(body, "会员权益说明图") {
		t.Fatalf("unexpected vlm image analysis response: %d %s", w.Code, body)
	}
	if vlmSvc.calls != 1 {
		t.Fatalf("expected one vlm call, got %d", vlmSvc.calls)
	}
	if vlmSvc.mimeType != "image/png" {
		t.Fatalf("expected image/png, got %q", vlmSvc.mimeType)
	}
}

type fakeDemoVLMService struct {
	calls       int
	description string
	mimeType    string
}

func (f *fakeDemoVLMService) DescribeImage(ctx context.Context, image []byte, mimeType, prompt string, maxOutputTokens ...int) (string, error) {
	f.calls++
	f.mimeType = mimeType
	return f.description, nil
}

type fakeEvalRewriter struct{}

func (f *fakeEvalRewriter) Rewrite(ctx context.Context, question string, history []chat.Message) (*rag.RewriteResult, error) {
	return &rag.RewriteResult{
		RewrittenQuestion: question + " 改写",
		SubQuestions:      []string{question, question + " follow-up"},
	}, nil
}

type fakeEvalRetriever struct{}

func (f *fakeEvalRetriever) Retrieve(ctx context.Context, question string, topK int) ([]rag.RetrievedChunk, error) {
	return []rag.RetrievedChunk{{
		ID:    "chunk-1",
		Text:  "hello context",
		Score: 0.91,
		Metadata: map[string]string{
			"doc_id":   "internal-doc-1",
			"doc_name": "FAQ_VAC_001.md",
			"kb_name":  "kb",
		},
	}}, nil
}

type fakeEvalIntentResolver struct{}

func (f fakeEvalIntentResolver) ResolveQuestions(ctx context.Context, questions []string) ([]rag.SubQuestionIntent, error) {
	out := make([]rag.SubQuestionIntent, 0, len(questions))
	for i, question := range questions {
		leafID := "leaf-1"
		if i > 0 {
			leafID = "leaf-2"
		}
		out = append(out, rag.SubQuestionIntent{
			SubQuestion: question,
			NodeScores: []rag.NodeScore{
				{Node: rag.IntentNode{ID: leafID, Name: "会员积分"}, Score: 0.95},
			},
		})
	}
	return out, nil
}

type fakeEvalContextRetriever struct {
	simpleCalls  int
	contextCalls int
	lastContext  rag.SearchContext
}

func (f *fakeEvalContextRetriever) Retrieve(ctx context.Context, question string, topK int) ([]rag.RetrievedChunk, error) {
	f.simpleCalls++
	return []rag.RetrievedChunk{{ID: "simple", Text: question}}, nil
}

func (f *fakeEvalContextRetriever) RetrieveWithContext(ctx context.Context, sc rag.SearchContext) ([]rag.RetrievedChunk, error) {
	f.contextCalls++
	f.lastContext = sc
	return []rag.RetrievedChunk{{ID: "context", Text: sc.RewrittenQuestion}}, nil
}

type fakeEvalMcpContextRetriever struct {
	fakeEvalContextRetriever
	mcpCalls int
}

func (f *fakeEvalMcpContextRetriever) BuildMcpContext(ctx context.Context, question string, subIntents []rag.SubQuestionIntent) (string, error) {
	f.mcpCalls++
	return "<tool-data>\n<data>\n工具：weather_query\n北京 今日晴\n</data>\n</tool-data>", nil
}
