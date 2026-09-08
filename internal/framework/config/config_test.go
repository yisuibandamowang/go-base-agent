package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAIProvidersMapParsing(t *testing.T) {
	yaml := `
server:
  port: 9090
ai:
  providers:
    openai:
      url: https://api.openai.com
      api-key: test-key
      protocol: openai-compatible
      endpoints:
        chat: /v1/chat/completions
        embedding: /v1/embeddings
    anthropic:
      url: https://api.anthropic.com
      api-key: test-key-2
      protocol: anthropic
      endpoints:
        chat: /v1/messages
    noop:
      protocol: noop
  selection:
    failure-threshold: 3
    open-duration-ms: 5000
    first-packet-timeout-seconds: 30
  chat:
    default-model: gpt-4.1
    deep-thinking-model: claude-sonnet-4
    default-tier: standard
    deep-thinking-tier: deep
    tiers:
      fast:
        candidates: [gpt-4.1]
        timeout-ms: 5000
      standard:
        candidates: [claude-sonnet-4, gpt-4.1]
        timeout-ms: 30000
      deep:
        candidates: [claude-sonnet-4]
        timeout-ms: 120000
    candidates:
      - id: gpt-4.1
        provider: openai
        model: gpt-4.1
        url: https://custom.openai.com
        priority: 1
      - id: claude-sonnet-4
        provider: anthropic
        model: claude-sonnet-4
        supports-thinking: true
        enabled: true
        priority: 2
      - id: disabled-model
        provider: openai
        model: gpt-3.5
        enabled: false
        priority: 3
  embedding:
    default-model: text-embedding-3
    candidates:
      - id: text-embedding-3
        provider: openai
        model: text-embedding-3-large
        dimension: 3072
        priority: 1
  rerank:
    default-model: rerank-noop
    candidates:
      - id: rerank-noop
        provider: noop
        model: noop
        priority: 100
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if len(cfg.AI.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(cfg.AI.Providers))
	}

	openai, ok := cfg.AI.Providers["openai"]
	if !ok {
		t.Fatal("openai provider missing")
	}
	if openai.Protocol != "openai-compatible" {
		t.Fatalf("unexpected protocol: %s", openai.Protocol)
	}
	if openai.Endpoints["chat"] != "/v1/chat/completions" {
		t.Fatalf("unexpected endpoint: %s", openai.Endpoints["chat"])
	}

	anthropic, ok := cfg.AI.Providers["anthropic"]
	if !ok {
		t.Fatal("anthropic provider missing")
	}
	if anthropic.Protocol != "anthropic" {
		t.Fatalf("unexpected protocol: %s", anthropic.Protocol)
	}

	noop, ok := cfg.AI.Providers["noop"]
	if !ok {
		t.Fatal("noop provider missing")
	}
	if noop.Protocol != "noop" {
		t.Fatalf("unexpected protocol: %s", noop.Protocol)
	}

	if cfg.AI.Selection.FailureThreshold != 3 {
		t.Fatalf("unexpected failure threshold: %d", cfg.AI.Selection.FailureThreshold)
	}
	if cfg.AI.Selection.FirstPacketTimeoutSeconds != 30 {
		t.Fatalf("unexpected first packet timeout: %d", cfg.AI.Selection.FirstPacketTimeoutSeconds)
	}
	if cfg.AI.Chat.DefaultTier != "standard" {
		t.Fatalf("unexpected chat default tier: %s", cfg.AI.Chat.DefaultTier)
	}
	if cfg.AI.Chat.DeepThinkingTier != "deep" {
		t.Fatalf("unexpected chat deep thinking tier: %s", cfg.AI.Chat.DeepThinkingTier)
	}
	if len(cfg.AI.Chat.Tiers) != 3 {
		t.Fatalf("unexpected chat tier count: %d", len(cfg.AI.Chat.Tiers))
	}
	if cfg.AI.Chat.Tiers["deep"].TimeoutMs != 120000 {
		t.Fatalf("unexpected deep tier timeout: %d", cfg.AI.Chat.Tiers["deep"].TimeoutMs)
	}
}

func TestLoadParsesRAGEngineType(t *testing.T) {
	yaml := `
rag:
  engine:
    type: agent
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.RAG.Engine.Type != "agent" {
		t.Fatalf("unexpected rag engine type: %s", cfg.RAG.Engine.Type)
	}
}

func TestCandidateIsEnabled(t *testing.T) {
	t.Run("nil means enabled", func(t *testing.T) {
		c := AICandidateConfig{}
		if !c.IsEnabled() {
			t.Fatal("expected nil Enabled to be true")
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		v := true
		c := AICandidateConfig{Enabled: &v}
		if !c.IsEnabled() {
			t.Fatal("expected true to be true")
		}
	})

	t.Run("explicit false", func(t *testing.T) {
		v := false
		c := AICandidateConfig{Enabled: &v}
		if c.IsEnabled() {
			t.Fatal("expected false to be false")
		}
	})
}

func TestCandidateURLOverride(t *testing.T) {
	yaml := `
server:
  port: 9090
ai:
  providers:
    openai:
      url: https://api.openai.com
      api-key: test
      protocol: openai-compatible
      endpoints:
        chat: /v1/chat/completions
  chat:
    default-model: gpt-4.1
    candidates:
      - id: gpt-4.1
        provider: openai
        model: gpt-4.1
        url: https://custom-endpoint.example.com
        priority: 1
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if len(cfg.AI.Chat.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cfg.AI.Chat.Candidates))
	}

	candidate := cfg.AI.Chat.Candidates[0]
	if candidate.URL != "https://custom-endpoint.example.com" {
		t.Fatalf("unexpected URL: %s", candidate.URL)
	}
}

func TestLoadAppliesAIJavaDefaults(t *testing.T) {
	yaml := `
ai:
  providers:
    openai:
      url: https://api.openai.com
      api-key: test
      protocol: openai-compatible
      endpoints:
        chat: /v1/chat/completions
        embedding: /v1/embeddings
        rerank: /v1/rerank
        vlm: /v1/chat/completions
  chat:
    candidates:
      - id: chat-default
        provider: openai
        model: chat-default
  embedding:
    candidates:
      - id: emb-default
        provider: openai
        model: emb-default
  rerank:
    candidates:
      - id: rerank-default
        provider: openai
        model: rerank-default
  vlm:
    candidates:
      - id: vlm-default
        provider: openai
        model: vlm-default
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.AI.Selection.FailureThreshold != 2 {
		t.Fatalf("unexpected default failure threshold: %d", cfg.AI.Selection.FailureThreshold)
	}
	if cfg.AI.Selection.OpenDurationMs != 30000 {
		t.Fatalf("unexpected default open duration: %d", cfg.AI.Selection.OpenDurationMs)
	}
	if cfg.AI.Selection.FirstPacketTimeoutSeconds != 60 {
		t.Fatalf("unexpected default first packet timeout: %d", cfg.AI.Selection.FirstPacketTimeoutSeconds)
	}
	if cfg.AI.Stream.MessageChunkSize != 5 {
		t.Fatalf("unexpected default stream chunk size: %d", cfg.AI.Stream.MessageChunkSize)
	}
	if cfg.AI.Chat.Candidates[0].Priority != 100 ||
		cfg.AI.Embedding.Candidates[0].Priority != 100 ||
		cfg.AI.Rerank.Candidates[0].Priority != 100 ||
		cfg.AI.VLM.Candidates[0].Priority != 100 {
		t.Fatalf("expected Java default candidate priorities 100, got chat=%d embedding=%d rerank=%d vlm=%d",
			cfg.AI.Chat.Candidates[0].Priority,
			cfg.AI.Embedding.Candidates[0].Priority,
			cfg.AI.Rerank.Candidates[0].Priority,
			cfg.AI.VLM.Candidates[0].Priority,
		)
	}
	if cfg.RAG.RateLimit.Global.MaxConcurrent != 50 ||
		cfg.RAG.RateLimit.Global.MaxWaitSeconds != 20 ||
		cfg.RAG.RateLimit.Global.LeaseSeconds != 600 ||
		cfg.RAG.RateLimit.Global.PollIntervalMs != 200 {
		t.Fatalf("expected Java rate-limit defaults, got %+v", cfg.RAG.RateLimit.Global)
	}
}

func TestLoadParsesRAGSearchConfig(t *testing.T) {
	yaml := `
rag:
  search:
    default-top-k: 12
    channels:
      vector-global:
        candidate-budget: 80
    fusion:
      strategy: rrf
      rrf-k: 42
      rerank-candidate-limit: 25
      channel-weights:
        vector: 1.2
        keyword: 0.9
        graph: 0.4
        web-search: 0.3
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.RAG.Search.Fusion.Strategy != "rrf" {
		t.Fatalf("unexpected fusion strategy: %s", cfg.RAG.Search.Fusion.Strategy)
	}
	if cfg.RAG.Search.Fusion.RRFK != 42 {
		t.Fatalf("unexpected rrf-k: %d", cfg.RAG.Search.Fusion.RRFK)
	}
	if cfg.RAG.Search.Fusion.RerankCandidateLimit != 25 {
		t.Fatalf("unexpected rerank candidate limit: %d", cfg.RAG.Search.Fusion.RerankCandidateLimit)
	}
	weights := cfg.RAG.Search.Fusion.ChannelWeights
	if weights.Vector != 1.2 || weights.Keyword != 0.9 || weights.Graph != 0.4 || weights.WebSearch != 0.3 {
		t.Fatalf("unexpected channel weights: %+v", weights)
	}
	if cfg.RAG.Search.DefaultTopK != 12 {
		t.Fatalf("unexpected default topK: %d", cfg.RAG.Search.DefaultTopK)
	}
	if cfg.RAG.Search.Channels.VectorGlobal.CandidateBudget != 80 {
		t.Fatalf("unexpected candidate budget: %d", cfg.RAG.Search.Channels.VectorGlobal.CandidateBudget)
	}
	if !cfg.RAG.Search.Channels.VectorGlobal.IsEnabledByDefault() {
		t.Fatal("expected vector global channel to default enabled like Java")
	}
	if !cfg.RAG.Search.Channels.Keyword.IsEnabledByDefault() {
		t.Fatal("expected keyword channel to default enabled for recall safety")
	}
}

func TestLoadParsesRAGGraphConfig(t *testing.T) {
	yaml := `
rag:
  graph:
    type: lightrag
    lightrag:
      base-url: http://127.0.0.1:9621
      query-mode: hybrid
    embedding-model: qwen-emb-8b
  search:
    channels:
      graph:
        enabled: true
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.RAG.Graph.Type != "lightrag" {
		t.Fatalf("unexpected graph type: %s", cfg.RAG.Graph.Type)
	}
	if cfg.RAG.Graph.Lightrag.BaseURL != "http://127.0.0.1:9621" {
		t.Fatalf("unexpected graph base url: %s", cfg.RAG.Graph.Lightrag.BaseURL)
	}
	if cfg.RAG.Graph.Lightrag.QueryMode != "hybrid" {
		t.Fatalf("unexpected graph query mode: %s", cfg.RAG.Graph.Lightrag.QueryMode)
	}
	if cfg.RAG.Graph.EmbeddingModel != "qwen-emb-8b" {
		t.Fatalf("unexpected graph embedding model: %s", cfg.RAG.Graph.EmbeddingModel)
	}
	if !cfg.RAG.Search.Channels.Graph.IsEnabledByDefaultWith(false) {
		t.Fatal("expected graph channel enable flag to parse from config")
	}
}

func TestLoadRejectsEnabledGraphChannelWithoutLightragBackend(t *testing.T) {
	yaml := `
rag:
  graph:
    type: none
  search:
    channels:
      graph:
        enabled: true
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected load to fail when graph channel is enabled without lightrag backend")
	}
	if !strings.Contains(err.Error(), "rag.search.channels.graph.enabled") || !strings.Contains(err.Error(), "rag.graph.type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAppliesRAGSearchJavaDefaults(t *testing.T) {
	yaml := `rag: {}`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	search := cfg.RAG.Search
	if search.DefaultTopK != 10 {
		t.Fatalf("unexpected default topK: %d", search.DefaultTopK)
	}
	if search.SupplementRatio != 0.25 {
		t.Fatalf("unexpected supplement ratio: %v", search.SupplementRatio)
	}
	if search.Channels.VectorGlobal.TopKMultiplier != 3 ||
		search.Channels.VectorGlobal.CandidateBudget != 100 ||
		search.Channels.VectorGlobal.ConfidenceThreshold != 0.6 ||
		search.Channels.VectorGlobal.SingleIntentSupplementThreshold != 0.8 {
		t.Fatalf("unexpected vector-global defaults: %+v", search.Channels.VectorGlobal)
	}
	if search.Channels.IntentDirected.MinIntentScore != 0.4 ||
		search.Channels.IntentDirected.TopKMultiplier != 2 {
		t.Fatalf("unexpected intent-directed defaults: %+v", search.Channels.IntentDirected)
	}
	if search.Channels.Keyword.Mode != "both" || search.Channels.Keyword.TopKMultiplier != 2 {
		t.Fatalf("unexpected keyword defaults: %+v", search.Channels.Keyword)
	}
	if search.Channels.WebSearch.Count != 5 ||
		search.Channels.WebSearch.TimeoutSeconds != 10 ||
		search.Channels.WebSearch.APIURL != "https://ydc-index.io/v1/search" {
		t.Fatalf("unexpected web-search defaults: %+v", search.Channels.WebSearch)
	}
	if search.Fusion.Strategy != "rrf" ||
		search.Fusion.RRFK != 60 ||
		search.Fusion.RerankCandidateLimit != 50 ||
		search.Fusion.ChannelWeights.Vector != 1 ||
		search.Fusion.ChannelWeights.Keyword != 1 ||
		search.Fusion.ChannelWeights.Graph != 0.8 ||
		search.Fusion.ChannelWeights.WebSearch != 0.5 {
		t.Fatalf("unexpected fusion defaults: %+v", search.Fusion)
	}
}

func TestLoadParsesRAGCodeRepoPath(t *testing.T) {
	yaml := `
rag:
  code:
    repo-path: /Users/work_project/360/member
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.RAG.Code.RepoPath != "/Users/work_project/360/member" {
		t.Fatalf("unexpected code repo path: %q", cfg.RAG.Code.RepoPath)
	}
}

func TestLoadExpandsEmptyDefaultEnvPlaceholder(t *testing.T) {
	yaml := `
rag:
  knowledge:
    geelib:
      work-dir: ${GEELIB_CLI_WORK_DIR:}
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_ = os.Unsetenv("GEELIB_CLI_WORK_DIR")

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Knowledge.Geelib.WorkDir != "" {
		t.Fatalf("expected empty work-dir, got %q", cfg.RAG.Knowledge.Geelib.WorkDir)
	}
}

func TestLoadParsesRAGContextEnrichConfig(t *testing.T) {
	yaml := `
rag:
  context:
    enrich:
      enabled: false
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Context.Enrich.IsEnabledByDefault() {
		t.Fatal("expected explicit context enrich false to disable metadata enrichment")
	}
}

func TestRAGContextEnrichDefaultEnabled(t *testing.T) {
	var cfg RAGContextEnrichConfig
	if !cfg.IsEnabledByDefault() {
		t.Fatal("expected omitted context enrich enabled to default true like Java")
	}
}

func TestLoadParsesRAGRerankEnabledConfig(t *testing.T) {
	// 闸门开着却关了精排是启动错误，这里显式关闸门以单测 rerank 开关本身
	yaml := `
rag:
  rerank:
    enabled: false
  search:
    evidence:
      min-rerank-score: 0
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Rerank.IsEnabledByDefault() {
		t.Fatal("expected explicit rerank false to disable rerank")
	}
}

func TestRAGRerankDefaultEnabled(t *testing.T) {
	var cfg RAGRerankConfig
	if !cfg.IsEnabledByDefault() {
		t.Fatal("expected omitted rerank enabled to default true like Java")
	}
}

func TestLoadParsesRAGQueryRewriteEnabledConfig(t *testing.T) {
	yaml := `
rag:
  query-rewrite:
    enabled: false
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.QueryRewrite.IsEnabledByDefault() {
		t.Fatal("expected explicit query rewrite false to disable rewrite")
	}
}

func TestRAGQueryRewriteDefaultEnabled(t *testing.T) {
	var cfg RAGQueryRewriteConfig
	if !cfg.IsEnabledByDefault() {
		t.Fatal("expected omitted query rewrite enabled to default true like Java")
	}
}

func TestLoadParsesRAGRateLimitGlobalEnabledConfig(t *testing.T) {
	yaml := `
rag:
  rate-limit:
    global:
      enabled: false
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.RateLimit.Global.IsEnabledByDefault() {
		t.Fatal("expected explicit rate limit false to disable limiter")
	}
}

func TestRAGRateLimitGlobalDefaultEnabled(t *testing.T) {
	var cfg RAGRateLimitGlobalConfig
	if !cfg.IsEnabledByDefault() {
		t.Fatal("expected omitted rate limit enabled to default true like Java")
	}
}

func TestLoadParsesRAGDefaultSSETimeout(t *testing.T) {
	yaml := `
rag:
  default:
    sse-timeout-ms: 60000
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.RAG.Default.SSETimeoutDuration(); got != 60*time.Second {
		t.Fatalf("unexpected sse timeout: %s", got)
	}
}

func TestRAGDefaultSSETimeoutDefault(t *testing.T) {
	var cfg RAGDefaultConfig
	if got := cfg.SSETimeoutDuration(); got != 5*time.Minute {
		t.Fatalf("expected default sse timeout 5m, got %s", got)
	}
}

func TestLoadParsesRAGGuidanceEnabledConfig(t *testing.T) {
	yaml := `
rag:
  guidance:
    enabled: false
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Guidance.IsEnabledByDefault() {
		t.Fatal("expected explicit guidance false to disable guidance")
	}
}

func TestRAGGuidanceDefaultEnabled(t *testing.T) {
	var cfg RAGGuidanceConfig
	if !cfg.IsEnabledByDefault() {
		t.Fatal("expected omitted guidance enabled to default true like Java")
	}
}

func TestLoadParsesRAGTraceEnabledConfig(t *testing.T) {
	yaml := `
rag:
  trace:
    enabled: false
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Trace.IsEnabledByDefault() {
		t.Fatal("expected explicit trace false to disable trace recorder")
	}
}

func TestRAGTraceDefaultEnabled(t *testing.T) {
	var cfg RAGTraceConfig
	if !cfg.IsEnabledByDefault() {
		t.Fatal("expected omitted trace enabled to default true like Java")
	}
}

func TestLoadParsesRAGKnowledgeScheduleRunningTimeout(t *testing.T) {
	yaml := `
rag:
  knowledge:
    schedule:
      running-timeout-minutes: 45
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Knowledge.Schedule.RunningTimeoutMinutes != 45 {
		t.Fatalf("unexpected running timeout minutes: %d", cfg.RAG.Knowledge.Schedule.RunningTimeoutMinutes)
	}
}

func TestLoadParsesRAGUploadLimits(t *testing.T) {
	yaml := `
rag:
  upload:
    max-file-size-bytes: 111
    max-request-size-bytes: 222
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Upload.MaxFileSizeBytes != 111 || cfg.RAG.Upload.MaxRequestSizeBytes != 222 {
		t.Fatalf("unexpected upload limits: %+v", cfg.RAG.Upload)
	}
}

func TestRAGUploadLimitsDefault(t *testing.T) {
	cfg := Config{}
	applyDefaults(&cfg)

	if cfg.RAG.Upload.MaxFileSizeBytes != 50<<20 || cfg.RAG.Upload.MaxRequestSizeBytes != 100<<20 {
		t.Fatalf("unexpected default upload limits: %+v", cfg.RAG.Upload)
	}
}

func TestMinerUDistributedSemaphoreDefaults(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	if cfg.MinerU.ConcurrencyLimit != 16 {
		t.Fatalf("expected MinerU concurrency limit 16, got %d", cfg.MinerU.ConcurrencyLimit)
	}
	if cfg.MinerU.SemaphoreName != "rag:mineru:parse" {
		t.Fatalf("unexpected MinerU semaphore name: %q", cfg.MinerU.SemaphoreName)
	}
	if cfg.MinerU.MaxWaitSeconds != 30 || cfg.MinerU.LeaseSeconds != 900 {
		t.Fatalf("unexpected MinerU semaphore timings: wait=%d lease=%d", cfg.MinerU.MaxWaitSeconds, cfg.MinerU.LeaseSeconds)
	}
}

func TestLoadParsesRAGKnowledgeGeelibImportTaskTimeout(t *testing.T) {
	yaml := `
rag:
  knowledge:
    geelib:
      import-task-timeout-minutes: 45
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Knowledge.Geelib.ImportTaskTimeoutMinutes != 45 {
		t.Fatalf("unexpected geelib import task timeout minutes: %d", cfg.RAG.Knowledge.Geelib.ImportTaskTimeoutMinutes)
	}
}

func TestLoadParsesAppIntentTreeInitFromFactory(t *testing.T) {
	yaml := `
app:
  intent-tree:
    init-from-factory: true
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.App.IntentTree.InitFromFactory {
		t.Fatal("expected app.intent-tree.init-from-factory to be true")
	}
}

func TestLoadRejectsInvalidMemorySummaryWindow(t *testing.T) {
	yaml := `
rag:
  memory:
    history-keep-turns: 4
    summary-start-turns: 4
    summary-enabled: true
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected invalid memory config to fail")
	}
	if !strings.Contains(err.Error(), "summary-start-turns") {
		t.Fatalf("expected memory validation error, got: %v", err)
	}
}

func TestLoadAppliesCurrentMemoryDefaults(t *testing.T) {
	yaml := `rag: {}`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Memory.HistoryKeepTurns != 8 ||
		cfg.RAG.Memory.SummaryStartTurns != 9 ||
		cfg.RAG.Memory.SummaryMaxChars != 400 {
		t.Fatalf("unexpected memory defaults: %+v", cfg.RAG.Memory)
	}
}

func TestLoadRejectsInvalidMemoryBounds(t *testing.T) {
	tests := []struct {
		name    string
		memory  string
		wantErr string
	}{
		{
			name: "history keep turns too large",
			memory: `
    history-keep-turns: 101
`,
			wantErr: "history-keep-turns",
		},
		{
			name: "summary max chars too small",
			memory: `
    summary-max-chars: 199
`,
			wantErr: "summary-max-chars",
		},
		{
			name: "title max length too small",
			memory: `
    title-max-length: 9
`,
			wantErr: "title-max-length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := "rag:\n  memory:\n" + tt.memory
			tmpDir := t.TempDir()
			cfgPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
				t.Fatalf("write temp config: %v", err)
			}

			_, err := Load(cfgPath)
			if err == nil {
				t.Fatal("expected invalid memory bounds to fail")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected %s validation error, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadRejectsInvalidChatTierReference(t *testing.T) {
	yaml := `
ai:
  chat:
    default-tier: standard
    deep-thinking-tier: deep
    tiers:
      standard:
        candidates: [model-a]
        timeout-ms: 30000
    candidates:
      - id: model-a
        provider: noop
        model: noop
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "deep-thinking-tier") {
		t.Fatalf("expected invalid chat tier reference error, got: %v", err)
	}
}

func TestLoadEvidenceGateDefaultsAndExplicitOff(t *testing.T) {
	writeCfg := func(yaml string) string {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write temp config: %v", err)
		}
		return cfgPath
	}

	// 未配置：取 Java 默认 0.2
	cfg, err := Load(writeCfg("rag: {}"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Search.Evidence.MinRerankScore != nil {
		t.Fatalf("expected unset min-rerank-score, got %v", *cfg.RAG.Search.Evidence.MinRerankScore)
	}
	if got := cfg.RAG.Search.Evidence.EffectiveMinRerankScore(); got != 0.2 {
		t.Fatalf("unexpected default min-rerank-score: %v", got)
	}

	// 显式 0 是配置侧的关闭路径，不回退到默认值
	cfg, err = Load(writeCfg(`
rag:
  search:
    evidence:
      min-rerank-score: 0
`))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.RAG.Search.Evidence.MinRerankScore == nil || *cfg.RAG.Search.Evidence.MinRerankScore != 0 {
		t.Fatalf("expected explicit zero to be preserved, got %+v", cfg.RAG.Search.Evidence)
	}
	if got := cfg.RAG.Search.Evidence.EffectiveMinRerankScore(); got != 0 {
		t.Fatalf("unexpected effective min-rerank-score: %v", got)
	}
}

func TestLoadRejectsEvidenceFloorAboveOne(t *testing.T) {
	yaml := `
rag:
  search:
    evidence:
      min-rerank-score: 1.5
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected load to fail when min-rerank-score is above 1")
	}
	if !strings.Contains(err.Error(), "rag.search.evidence") || !strings.Contains(err.Error(), "min-rerank-score") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsEvidenceGateOnButRerankOff(t *testing.T) {
	yaml := `
rag:
  rerank:
    enabled: false
  search:
    evidence:
      min-rerank-score: 0.2
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected load to fail when gate is on but rerank is off")
	}
	if !strings.Contains(err.Error(), "rag.rerank.enabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}
