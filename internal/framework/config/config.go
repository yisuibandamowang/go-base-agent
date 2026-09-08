package config

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	RocketMQ RocketMQConfig `mapstructure:"rocketmq"`
	Milvus   MilvusConfig   `mapstructure:"milvus"`
	MinerU   MinerUConfig   `mapstructure:"mineru"`
	RAG      RAGConfig      `mapstructure:"rag"`
	AI       AIConfig       `mapstructure:"ai"`
	RustFS   RustFSConfig   `mapstructure:"rustfs"`
	Auth     AuthConfig     `mapstructure:"sa-token"`
	App      AppConfig      `mapstructure:"app"`
}

type ServerConfig struct {
	Port int `mapstructure:"port"`
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Name     string `mapstructure:"name"`
	SSLMode  string `mapstructure:"sslmode"`
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
}

func (r RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

func (r RedisConfig) NewClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         r.Addr(),
		Password:     r.Password,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		MaxRetries:   3,
	})
}

type RocketMQConfig struct {
	NameServer string                 `mapstructure:"name-server"`
	Producer   RocketMQProducerConfig `mapstructure:"producer"`
}

type RocketMQProducerConfig struct {
	Group              string `mapstructure:"group"`
	SendMessageTimeout int    `mapstructure:"send-message-timeout"`
}

type MilvusConfig struct {
	URI string `mapstructure:"uri"`
}

type MinerUConfig struct {
	APIURL           string `mapstructure:"api-url"`
	APIKey           string `mapstructure:"api-key"`
	PollIntervalSecs int    `mapstructure:"poll-interval-seconds"`
	TimeoutSecs      int    `mapstructure:"timeout-seconds"`
	EnableTable      bool   `mapstructure:"enable-table"`
	EnableFormula    bool   `mapstructure:"enable-formula"`
	OCR              bool   `mapstructure:"ocr"`
	Language         string `mapstructure:"language"`
	ConcurrencyLimit int64  `mapstructure:"concurrency-limit"`
	SemaphoreName    string `mapstructure:"semaphore-name"`
	MaxWaitSeconds   int    `mapstructure:"max-wait-seconds"`
	LeaseSeconds     int    `mapstructure:"lease-seconds"`
}

type RAGConfig struct {
	Engine       RAGEngineConfig       `mapstructure:"engine"`
	Vector       RAGVectorConfig       `mapstructure:"vector"`
	Graph        RAGGraphConfig        `mapstructure:"graph"`
	Default      RAGDefaultConfig      `mapstructure:"default"`
	Code         RAGCodeConfig         `mapstructure:"code"`
	Context      RAGContextConfig      `mapstructure:"context"`
	Citation     RAGCitationConfig     `mapstructure:"citation"`
	QueryRewrite RAGQueryRewriteConfig `mapstructure:"query-rewrite"`
	Rerank       RAGRerankConfig       `mapstructure:"rerank"`
	RateLimit    RAGRateLimitConfig    `mapstructure:"rate-limit"`
	Memory       RAGMemoryConfig       `mapstructure:"memory"`
	Parser       RAGParserConfig       `mapstructure:"parser"`
	ImageParse   RAGImageParseConfig   `mapstructure:"image-parse"`
	Semaphore    RAGSemaphoreConfig    `mapstructure:"semaphore"`
	Knowledge    RAGKnowledgeConfig    `mapstructure:"knowledge"`
	Upload       RAGUploadConfig       `mapstructure:"upload"`
	MCP          RAGMCPConfig          `mapstructure:"mcp"`
	Search       RAGSearchConfig       `mapstructure:"search"`
	Guidance     RAGGuidanceConfig     `mapstructure:"guidance"`
	Trace        RAGTraceConfig        `mapstructure:"trace"`
	AnswerCache  RAGAnswerCacheConfig  `mapstructure:"answer-cache"`
}

type RAGVectorConfig struct {
	Type string `mapstructure:"type"`
}

type RAGEngineConfig struct {
	Type string `mapstructure:"type"`
}

type RAGGraphConfig struct {
	Type           string                 `mapstructure:"type"`
	Lightrag       RAGGraphLightRAGConfig `mapstructure:"lightrag"`
	EmbeddingModel string                 `mapstructure:"embedding-model"`
}

type RAGGraphLightRAGConfig struct {
	BaseURL   string `mapstructure:"base-url"`
	APIKey    string `mapstructure:"api-key"`
	QueryMode string `mapstructure:"query-mode"`
}

type RAGDefaultConfig struct {
	CollectionName string `mapstructure:"collection-name"`
	Dimension      int    `mapstructure:"dimension"`
	MetricType     string `mapstructure:"metric-type"`
	SSETimeoutMs   int64  `mapstructure:"sse-timeout-ms"`
}

type RAGCodeConfig struct {
	RepoPath string `mapstructure:"repo-path"`
}

func (c RAGDefaultConfig) SSETimeoutDuration() time.Duration {
	if c.SSETimeoutMs <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(c.SSETimeoutMs) * time.Millisecond
}

type RAGContextConfig struct {
	Enrich RAGContextEnrichConfig `mapstructure:"enrich"`
}

type RAGCitationConfig struct {
	Enabled *bool `mapstructure:"enabled"`
}

func (c RAGCitationConfig) IsEnabledByDefault() bool {
	return c.Enabled != nil && *c.Enabled
}

type RAGContextEnrichConfig struct {
	Enabled *bool `mapstructure:"enabled"`
}

func (c RAGContextEnrichConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type RAGQueryRewriteConfig struct {
	Enabled            *bool `mapstructure:"enabled"`
	MaxHistoryMessages int   `mapstructure:"max-history-messages"`
	MaxHistoryChars    int   `mapstructure:"max-history-chars"`
}

func (c RAGQueryRewriteConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type RAGRerankConfig struct {
	Enabled *bool `mapstructure:"enabled"`
}

func (c RAGRerankConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type RAGRateLimitConfig struct {
	Global RAGRateLimitGlobalConfig `mapstructure:"global"`
}

type RAGRateLimitGlobalConfig struct {
	Enabled        *bool `mapstructure:"enabled"`
	MaxConcurrent  int   `mapstructure:"max-concurrent"`
	MaxWaitSeconds int   `mapstructure:"max-wait-seconds"`
	LeaseSeconds   int   `mapstructure:"lease-seconds"`
	PollIntervalMs int   `mapstructure:"poll-interval-ms"`
}

func (c RAGRateLimitGlobalConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type RAGMemoryConfig struct {
	HistoryKeepTurns  int  `mapstructure:"history-keep-turns"`
	SummaryStartTurns int  `mapstructure:"summary-start-turns"`
	SummaryEnabled    bool `mapstructure:"summary-enabled"`
	TTLMinutes        int  `mapstructure:"ttl-minutes"`
	SummaryMaxChars   int  `mapstructure:"summary-max-chars"`
	TitleMaxLength    int  `mapstructure:"title-max-length"`
}

type RAGParserConfig struct {
	TikaURL string `mapstructure:"tika-url"`
}

type RAGImageParseConfig struct {
	DescriptionPrompt string `mapstructure:"description-prompt"`
	MaxOutputTokens   int    `mapstructure:"max-output-tokens"`
}

type RAGSemaphoreConfig struct {
	DocumentUpload RAGSemaphoreEntryConfig `mapstructure:"document-upload"`
}

type RAGSemaphoreEntryConfig struct {
	Name           string `mapstructure:"name"`
	MaxConcurrent  int    `mapstructure:"max-concurrent"`
	MaxWaitSeconds int    `mapstructure:"max-wait-seconds"`
	LeaseSeconds   int    `mapstructure:"lease-seconds"`
}

type RAGKnowledgeConfig struct {
	Schedule   RAGKnowledgeScheduleConfig   `mapstructure:"schedule"`
	Feishu     RAGKnowledgeFeishuConfig     `mapstructure:"feishu"`
	Confluence RAGKnowledgeConfluenceConfig `mapstructure:"confluence"`
	Geelib     RAGKnowledgeGeelibConfig     `mapstructure:"geelib"`
}

type RAGKnowledgeScheduleConfig struct {
	ScanDelayMs           int `mapstructure:"scan-delay-ms"`
	LockSeconds           int `mapstructure:"lock-seconds"`
	BatchSize             int `mapstructure:"batch-size"`
	MinIntervalSeconds    int `mapstructure:"min-interval-seconds"`
	RunningTimeoutMinutes int `mapstructure:"running-timeout-minutes"`
}

// RAGUploadConfig controls multipart upload limits exposed by the service.
type RAGUploadConfig struct {
	MaxFileSizeBytes    int64 `mapstructure:"max-file-size-bytes"`
	MaxRequestSizeBytes int64 `mapstructure:"max-request-size-bytes"`
}

type RAGKnowledgeFeishuConfig struct {
	AppID       string `mapstructure:"app-id"`
	AppSecret   string `mapstructure:"app-secret"`
	AccessToken string `mapstructure:"access-token"`
	TenantToken string `mapstructure:"tenant-token"`
	BaseURL     string `mapstructure:"base-url"`
}

type RAGKnowledgeConfluenceConfig struct {
	BaseURL     string `mapstructure:"base-url"`
	Username    string `mapstructure:"username"`
	APIKey      string `mapstructure:"api-key"`
	AccessToken string `mapstructure:"access-token"`
}

type RAGKnowledgeGeelibConfig struct {
	Enabled                  *bool    `mapstructure:"enabled"`
	APIBaseURL               string   `mapstructure:"api-base-url"`
	AppToken                 string   `mapstructure:"app-token"`
	UserMail                 string   `mapstructure:"user-mail"`
	UserToken                string   `mapstructure:"user-token"`
	SessionCookie            string   `mapstructure:"session-cookie"`
	WorkDir                  string   `mapstructure:"work-dir"`
	Command                  string   `mapstructure:"command"`
	Tool                     string   `mapstructure:"tool"`
	TimeoutSeconds           int      `mapstructure:"timeout-seconds"`
	ImportTaskTimeoutMinutes int      `mapstructure:"import-task-timeout-minutes"`
	MaxBytes                 int64    `mapstructure:"max-bytes"`
	Domains                  []string `mapstructure:"domains"`
}

func (c RAGKnowledgeGeelibConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type RAGMCPConfig struct {
	Servers []RAGMCPServerConfig `mapstructure:"servers"`
}

type RAGMCPServerConfig struct {
	Name    string   `mapstructure:"name"`
	URL     string   `mapstructure:"url"`
	Domains []string `mapstructure:"domains"`
}

type RAGSearchConfig struct {
	DefaultTopK     int                     `mapstructure:"default-top-k"`
	SupplementRatio float64                 `mapstructure:"supplement-ratio"`
	Channels        RAGSearchChannelsConfig `mapstructure:"channels"`
	Fusion          RAGSearchFusionConfig   `mapstructure:"fusion"`
	Evidence        RAGSearchEvidenceConfig `mapstructure:"evidence"`
}

type RAGSearchChannelsConfig struct {
	TimeoutMs      int                    `mapstructure:"timeout-ms"`
	VectorGlobal   RAGSearchChannelConfig `mapstructure:"vector-global"`
	IntentDirected RAGSearchChannelConfig `mapstructure:"intent-directed"`
	Keyword        RAGSearchChannelConfig `mapstructure:"keyword"`
	Graph          RAGSearchChannelConfig `mapstructure:"graph"`
	WebSearch      RAGWebSearchConfig     `mapstructure:"web-search"`
}

type RAGSearchChannelConfig struct {
	Enabled                         *bool   `mapstructure:"enabled"`
	ConfidenceThreshold             float64 `mapstructure:"confidence-threshold"`
	SingleIntentSupplementThreshold float64 `mapstructure:"single-intent-supplement-threshold"`
	CandidateBudget                 int     `mapstructure:"candidate-budget"`
	TopKMultiplier                  int     `mapstructure:"top-k-multiplier"`
	MinIntentScore                  float64 `mapstructure:"min-intent-score"`
	Mode                            string  `mapstructure:"mode"`
}

type RAGSearchFusionConfig struct {
	Strategy             string                        `mapstructure:"strategy"`
	RRFK                 int                           `mapstructure:"rrf-k"`
	RerankCandidateLimit int                           `mapstructure:"rerank-candidate-limit"`
	ChannelWeights       RAGSearchChannelWeightsConfig `mapstructure:"channel-weights"`
}

// RAGSearchChannelWeightsConfig controls each channel's contribution to RRF.
type RAGSearchChannelWeightsConfig struct {
	Vector    float64 `mapstructure:"vector"`
	Keyword   float64 `mapstructure:"keyword"`
	Graph     float64 `mapstructure:"graph"`
	WebSearch float64 `mapstructure:"web-search"`
}

// RAGSearchEvidenceConfig 证据相关性闸门参数，对齐 Java SearchChannelProperties.Evidence。
type RAGSearchEvidenceConfig struct {
	// MinRerankScore 整批最高精排分低于此值则整批丢弃证据，<=0 关闭闸门。
	// 指针语义区分「未配置（默认 0.2）」与「显式 0（关闭）」，随 reranker 而变，换模型需按精排分布重测。
	MinRerankScore *float64 `mapstructure:"min-rerank-score"`
}

// EffectiveMinRerankScore 返回生效的证据闸门下限，未配置时取 Java 默认 0.2。
func (c RAGSearchEvidenceConfig) EffectiveMinRerankScore() float64 {
	if c.MinRerankScore == nil {
		return 0.2
	}
	return *c.MinRerankScore
}

// ChannelWeight returns a configured channel weight, falling back to the Java-compatible default.
func (c RAGSearchFusionConfig) ChannelWeight(channel string) float64 {
	var weight float64
	switch channel {
	case "vector":
		weight = c.ChannelWeights.Vector
	case "keyword":
		weight = c.ChannelWeights.Keyword
	case "graph":
		weight = c.ChannelWeights.Graph
	case "web-search":
		weight = c.ChannelWeights.WebSearch
	}
	if weight > 0 {
		return weight
	}
	switch channel {
	case "graph":
		return 0.8
	case "web-search":
		return 0.5
	default:
		return 1
	}
}

type RAGGuidanceConfig struct {
	Enabled             *bool   `mapstructure:"enabled"`
	AmbiguityScoreRatio float64 `mapstructure:"ambiguity-score-ratio"`
	AmbiguityMargin     float64 `mapstructure:"ambiguity-margin"`
	MaxOptions          int     `mapstructure:"max-options"`
}

func (c RAGGuidanceConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c RAGSearchChannelConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c RAGSearchChannelConfig) IsEnabledByDefaultWith(defaultEnabled bool) bool {
	if c.Enabled == nil {
		return defaultEnabled
	}
	return *c.Enabled
}

type RAGWebSearchConfig struct {
	Enabled        bool   `mapstructure:"enabled"`
	APIURL         string `mapstructure:"api-url"`
	APIKey         string `mapstructure:"api-key"`
	Count          int    `mapstructure:"count"`
	TimeoutSeconds int    `mapstructure:"timeout-seconds"`
}

type RAGTraceConfig struct {
	Enabled        *bool `mapstructure:"enabled"`
	MaxErrorLength int   `mapstructure:"max-error-length"`
}

func (c RAGTraceConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type RAGAnswerCacheConfig struct {
	Enabled    *bool `mapstructure:"enabled"`
	TTLMinutes int   `mapstructure:"ttl-minutes"`
}

func (c RAGAnswerCacheConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c RAGAnswerCacheConfig) TTLDuration() time.Duration {
	if c.TTLMinutes <= 0 {
		return time.Hour
	}
	return time.Duration(c.TTLMinutes) * time.Minute
}

type AIConfig struct {
	Providers AIProvidersConfig `mapstructure:"providers"`
	Selection AISelectionConfig `mapstructure:"selection"`
	Stream    AIStreamConfig    `mapstructure:"stream"`
	Chat      AIChatConfig      `mapstructure:"chat"`
	Embedding AIEmbeddingConfig `mapstructure:"embedding"`
	Rerank    AIRerankConfig    `mapstructure:"rerank"`
	VLM       AIVLMConfig       `mapstructure:"vlm"`
}

type AIProvidersConfig map[string]AIProviderConfig

type AIProviderConfig struct {
	URL       string            `mapstructure:"url"`
	APIKey    string            `mapstructure:"api-key"`
	Protocol  string            `mapstructure:"protocol"`
	Endpoints map[string]string `mapstructure:"endpoints"`
}

type AISelectionConfig struct {
	FailureThreshold          int `mapstructure:"failure-threshold"`
	OpenDurationMs            int `mapstructure:"open-duration-ms"`
	FirstPacketTimeoutSeconds int `mapstructure:"first-packet-timeout-seconds"`
}

type AIStreamConfig struct {
	MessageChunkSize int `mapstructure:"message-chunk-size"`
}

type AIChatConfig struct {
	DefaultModel      string                      `mapstructure:"default-model"`
	DeepThinkingModel string                      `mapstructure:"deep-thinking-model"`
	DefaultTier       string                      `mapstructure:"default-tier"`
	DeepThinkingTier  string                      `mapstructure:"deep-thinking-tier"`
	Tiers             map[string]AIChatTierConfig `mapstructure:"tiers"`
	Candidates        []AICandidateConfig         `mapstructure:"candidates"`
}

type AIChatTierConfig struct {
	Candidates []string `mapstructure:"candidates"`
	TimeoutMs  int      `mapstructure:"timeout-ms"`
}

type AICandidateConfig struct {
	ID               string `mapstructure:"id"`
	Provider         string `mapstructure:"provider"`
	Model            string `mapstructure:"model"`
	URL              string `mapstructure:"url"`
	Dimension        int    `mapstructure:"dimension"`
	SupportsThinking bool   `mapstructure:"supports-thinking"`
	Priority         int    `mapstructure:"priority"`
	Enabled          *bool  `mapstructure:"enabled"`
}

type AIEmbeddingConfig struct {
	DefaultModel string                       `mapstructure:"default-model"`
	Candidates   []AIEmbeddingCandidateConfig `mapstructure:"candidates"`
}

type AIEmbeddingCandidateConfig struct {
	ID        string `mapstructure:"id"`
	Provider  string `mapstructure:"provider"`
	Model     string `mapstructure:"model"`
	URL       string `mapstructure:"url"`
	Dimension int    `mapstructure:"dimension"`
	Priority  int    `mapstructure:"priority"`
	Enabled   *bool  `mapstructure:"enabled"`
}

type AIRerankConfig struct {
	DefaultModel string                    `mapstructure:"default-model"`
	Candidates   []AIRerankCandidateConfig `mapstructure:"candidates"`
}

type AIRerankCandidateConfig struct {
	ID       string `mapstructure:"id"`
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
	URL      string `mapstructure:"url"`
	Priority int    `mapstructure:"priority"`
	Enabled  *bool  `mapstructure:"enabled"`
}

type AIVLMConfig struct {
	DefaultModel string                 `mapstructure:"default-model"`
	Candidates   []AIVLMCandidateConfig `mapstructure:"candidates"`
}

type AIVLMCandidateConfig struct {
	ID       string `mapstructure:"id"`
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
	URL      string `mapstructure:"url"`
	Priority int    `mapstructure:"priority"`
	Enabled  *bool  `mapstructure:"enabled"`
}

func (c AIVLMCandidateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c AICandidateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c AIEmbeddingCandidateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c AIRerankCandidateConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type RustFSConfig struct {
	URL             string `mapstructure:"url"`
	AccessKeyID     string `mapstructure:"access-key-id"`
	SecretAccessKey string `mapstructure:"secret-access-key"`
	Region          string `mapstructure:"region"`
	KBBucket        string `mapstructure:"kb-bucket"`
	AssetBucket     string `mapstructure:"asset-bucket"`
}

type AuthConfig struct {
	TokenName      string `mapstructure:"token-name"`
	TimeoutSeconds int    `mapstructure:"timeout-seconds"`
	JWTSecret      string `mapstructure:"jwt-secret"`
}

type AppConfig struct {
	DemoMode   bool                `mapstructure:"demo-mode"`
	Eval       AppEvalConfig       `mapstructure:"eval"`
	IntentTree AppIntentTreeConfig `mapstructure:"intent-tree"`
}

type AppEvalConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

type AppIntentTreeConfig struct {
	InitFromFactory bool `mapstructure:"init-from-factory"`
}

// Load 加载配置。
//
// 加载链：
//  1. godotenv 加载 .env → os.Getenv()
//  2. 读取 config.yaml 原始内容
//  3. expandEnv 替换 ${VAR} 和 ${VAR:default} 占位符（对齐 Spring `${VAR:default}` 语法）
//  4. viper 解析 YAML 并反序列化到 Config 结构体
//
// 本地开发：env=配置文件覆盖 .env 中的环境变量。
// 生产环境：env 由配置中心注入（K8s Secret / Vault / Apollo），.env 不存在。
func Load(path string) (*Config, error) {
	_ = godotenv.Load(".env")

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	expanded := expandEnv(string(raw))

	v := viper.New()
	v.SetConfigType("yaml")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	if err := v.ReadConfig(strings.NewReader(expanded)); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config %s: %w", path, err)
	}
	applyDefaults(&cfg)
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.RAG.Engine.Type = normalizeOrchestrationMode(cfg.RAG.Engine.Type)
	ai := &cfg.AI
	if ai.Selection.FailureThreshold <= 0 {
		ai.Selection.FailureThreshold = 2
	}
	if ai.Selection.OpenDurationMs <= 0 {
		ai.Selection.OpenDurationMs = 30000
	}
	if ai.Selection.FirstPacketTimeoutSeconds <= 0 {
		ai.Selection.FirstPacketTimeoutSeconds = 60
	}
	if ai.Stream.MessageChunkSize <= 0 {
		ai.Stream.MessageChunkSize = 5
	}
	for i := range ai.Chat.Candidates {
		if ai.Chat.Candidates[i].Priority <= 0 {
			ai.Chat.Candidates[i].Priority = 100
		}
	}
	for i := range ai.Embedding.Candidates {
		if ai.Embedding.Candidates[i].Priority <= 0 {
			ai.Embedding.Candidates[i].Priority = 100
		}
	}
	for i := range ai.Rerank.Candidates {
		if ai.Rerank.Candidates[i].Priority <= 0 {
			ai.Rerank.Candidates[i].Priority = 100
		}
	}
	for i := range ai.VLM.Candidates {
		if ai.VLM.Candidates[i].Priority <= 0 {
			ai.VLM.Candidates[i].Priority = 100
		}
	}
	mem := &cfg.RAG.Memory
	if cfg.RAG.Upload.MaxFileSizeBytes <= 0 {
		cfg.RAG.Upload.MaxFileSizeBytes = 50 << 20
	}
	if cfg.RAG.Upload.MaxRequestSizeBytes <= 0 {
		cfg.RAG.Upload.MaxRequestSizeBytes = 100 << 20
	}
	if mem.HistoryKeepTurns <= 0 {
		mem.HistoryKeepTurns = 8
	}
	if mem.SummaryStartTurns <= 0 {
		mem.SummaryStartTurns = 9
	}
	if mem.SummaryMaxChars <= 0 {
		mem.SummaryMaxChars = 400
	}
	if mem.TitleMaxLength <= 0 {
		mem.TitleMaxLength = 30
	}
	graph := &cfg.RAG.Graph
	if strings.TrimSpace(graph.Type) == "" {
		graph.Type = "none"
	}
	if strings.TrimSpace(graph.Lightrag.BaseURL) == "" {
		graph.Lightrag.BaseURL = "http://127.0.0.1:9621"
	}
	if strings.TrimSpace(graph.Lightrag.QueryMode) == "" {
		graph.Lightrag.QueryMode = "hybrid"
	}
	search := &cfg.RAG.Search
	if search.DefaultTopK <= 0 {
		search.DefaultTopK = 10
	}
	if search.SupplementRatio <= 0 {
		search.SupplementRatio = 0.25
	}
	if search.Channels.TimeoutMs <= 0 {
		search.Channels.TimeoutMs = 15000
	}
	if search.Channels.VectorGlobal.ConfidenceThreshold <= 0 {
		search.Channels.VectorGlobal.ConfidenceThreshold = 0.6
	}
	if search.Channels.VectorGlobal.SingleIntentSupplementThreshold <= 0 {
		search.Channels.VectorGlobal.SingleIntentSupplementThreshold = 0.8
	}
	if search.Channels.VectorGlobal.TopKMultiplier <= 0 {
		search.Channels.VectorGlobal.TopKMultiplier = 3
	}
	if search.Channels.VectorGlobal.CandidateBudget <= 0 {
		search.Channels.VectorGlobal.CandidateBudget = 100
	}
	if search.Channels.IntentDirected.MinIntentScore <= 0 {
		search.Channels.IntentDirected.MinIntentScore = 0.4
	}
	if search.Channels.IntentDirected.TopKMultiplier <= 0 {
		search.Channels.IntentDirected.TopKMultiplier = 2
	}
	if search.Channels.Keyword.Mode == "" {
		search.Channels.Keyword.Mode = "both"
	}
	if search.Channels.Keyword.TopKMultiplier <= 0 {
		search.Channels.Keyword.TopKMultiplier = 2
	}
	if search.Channels.Graph.Enabled == nil {
		disabled := false
		search.Channels.Graph.Enabled = &disabled
	}
	if search.Channels.WebSearch.Count <= 0 {
		search.Channels.WebSearch.Count = 5
	}
	if search.Channels.WebSearch.TimeoutSeconds <= 0 {
		search.Channels.WebSearch.TimeoutSeconds = 10
	}
	if search.Channels.WebSearch.APIURL == "" {
		search.Channels.WebSearch.APIURL = "https://ydc-index.io/v1/search"
	}
	if search.Fusion.Strategy == "" {
		search.Fusion.Strategy = "rrf"
	}
	if search.Fusion.RRFK <= 0 {
		search.Fusion.RRFK = 60
	}
	if search.Fusion.RerankCandidateLimit <= 0 {
		search.Fusion.RerankCandidateLimit = 50
	}
	search.Fusion.ChannelWeights.Vector = search.Fusion.ChannelWeight("vector")
	search.Fusion.ChannelWeights.Keyword = search.Fusion.ChannelWeight("keyword")
	search.Fusion.ChannelWeights.Graph = search.Fusion.ChannelWeight("graph")
	search.Fusion.ChannelWeights.WebSearch = search.Fusion.ChannelWeight("web-search")
	limit := &cfg.RAG.RateLimit.Global
	if limit.MaxConcurrent <= 0 {
		limit.MaxConcurrent = 50
	}
	if limit.MaxWaitSeconds <= 0 {
		limit.MaxWaitSeconds = 20
	}
	if limit.LeaseSeconds <= 0 {
		limit.LeaseSeconds = 600
	}
	if limit.PollIntervalMs <= 0 {
		limit.PollIntervalMs = 200
	}
	if cfg.MinerU.ConcurrencyLimit <= 0 {
		cfg.MinerU.ConcurrencyLimit = 16
	}
	if strings.TrimSpace(cfg.MinerU.SemaphoreName) == "" {
		cfg.MinerU.SemaphoreName = "rag:mineru:parse"
	}
	if cfg.MinerU.MaxWaitSeconds <= 0 {
		cfg.MinerU.MaxWaitSeconds = 30
	}
	if cfg.MinerU.LeaseSeconds <= 0 {
		cfg.MinerU.LeaseSeconds = 900
	}
}

func normalizeOrchestrationMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "agent":
		return "agent"
	default:
		return "workflow"
	}
}

func validateConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	mem := cfg.RAG.Memory
	if mem.HistoryKeepTurns < 1 || mem.HistoryKeepTurns > 100 {
		return fmt.Errorf("validate rag.memory: history-keep-turns (%d) must be between 1 and 100", mem.HistoryKeepTurns)
	}
	if mem.SummaryMaxChars < 200 || mem.SummaryMaxChars > 1000 {
		return fmt.Errorf("validate rag.memory: summary-max-chars (%d) must be between 200 and 1000", mem.SummaryMaxChars)
	}
	if mem.TitleMaxLength < 10 || mem.TitleMaxLength > 100 {
		return fmt.Errorf("validate rag.memory: title-max-length (%d) must be between 10 and 100", mem.TitleMaxLength)
	}
	if mem.SummaryEnabled && mem.SummaryStartTurns <= mem.HistoryKeepTurns {
		return fmt.Errorf(
			"validate rag.memory: summary-start-turns (%d) must be greater than history-keep-turns (%d) when summary-enabled=true",
			mem.SummaryStartTurns,
			mem.HistoryKeepTurns,
		)
	}
	if cfg.RAG.Search.Channels.Graph.IsEnabledByDefaultWith(false) && !strings.EqualFold(strings.TrimSpace(cfg.RAG.Graph.Type), "lightrag") {
		return fmt.Errorf(
			"validate rag.search.channels.graph: rag.search.channels.graph.enabled=true requires rag.graph.type=lightrag, got rag.graph.type=%q",
			cfg.RAG.Graph.Type,
		)
	}
	// 精排分按 0~1 输出，下限高于 1 则全部证据被丢，表现与「库里没料」一致，线上无从分辨
	minRerankScore := cfg.RAG.Search.Evidence.EffectiveMinRerankScore()
	if math.IsNaN(minRerankScore) || minRerankScore > 1 {
		return fmt.Errorf(
			"validate rag.search.evidence: min-rerank-score (%v) must be <= 1: 精排分按 0~1 输出，高于 1 会让全部证据被闸门丢弃、KB 侧恒为空；关闭闸门请填 0",
			minRerankScore,
		)
	}
	// 闸门的唯一判据来自精排：闸门开着却关了精排，判据永远读不到，启动即失败而不是上线后恒放行
	if minRerankScore > 0 && !cfg.RAG.Rerank.IsEnabledByDefault() {
		return fmt.Errorf(
			"validate rag.search.evidence: min-rerank-score (%v) 需要精排出分，但 rag.rerank.enabled=false：闸门将无分可读、恒放行；请开启精排或把下限填 0",
			minRerankScore,
		)
	}
	if err := validateRAGSearchBudget(cfg.RAG.Search); err != nil {
		return err
	}
	if err := validateChatTiers(cfg.AI.Chat); err != nil {
		return err
	}
	return nil
}

// intentMinScoreFloor 与 rag.IntentMinScore（对齐 Java RAGConstant.INTENT_MIN_SCORE）保持一致。
// 不直接引用 rag 包，避免 framework 配置层反向依赖业务层。
const intentMinScoreFloor = 0.35

// validateRAGSearchBudget 校验检索预算漏斗单调不变式与作用域闸门串联，对齐 Java
// SearchChannelProperties.afterPropertiesSet：违反即配置矛盾，启动失败胜过线上悄悄少召回或死代码分支。
func validateRAGSearchBudget(search RAGSearchConfig) error {
	contextTopK := search.DefaultTopK
	if contextTopK <= 0 {
		return fmt.Errorf("validate rag.search: default-top-k (%d) 必须为正数", contextTopK)
	}
	if candidateLimit := search.Fusion.RerankCandidateLimit; candidateLimit > 0 && candidateLimit < contextTopK {
		return fmt.Errorf(
			"validate rag.search.fusion: 检索预算漏斗不变式被破坏：rerank-candidate-limit(%d) < default-top-k(%d)，送入 Rerank 的候选池不得小于最终条数，请调大 rag.search.fusion.rerank-candidate-limit 或调小 rag.search.default-top-k",
			candidateLimit, contextTopK,
		)
	}
	if minIntentScore := search.Channels.IntentDirected.MinIntentScore; minIntentScore > 0 && minIntentScore < intentMinScoreFloor {
		return fmt.Errorf(
			"validate rag.search.channels.intent-directed: min-intent-score (%v) 低于上游意图过滤下限 INTENT_MIN_SCORE(%v)，该配置不会产生任何效果，请调高此值，或先下调 INTENT_MIN_SCORE",
			minIntentScore, intentMinScoreFloor,
		)
	}
	// 作用域的两道闸门必须真的串联：意图先被 min-intent-score 过滤，存活的分数恒 >= 它，
	// 阈值若不高于最低分，「低置信退化为全局」这条兜底路就永不触发
	confidenceThreshold := search.Channels.VectorGlobal.ConfidenceThreshold
	if confidenceThreshold > 0 && (confidenceThreshold <= search.Channels.IntentDirected.MinIntentScore || confidenceThreshold > 1) {
		return fmt.Errorf(
			"validate rag.search.channels.vector-global: confidence-threshold (%v) 必须落在 (min-intent-score(%v), 1] 内：不高于最低分则「低置信退化为全局」永不触发，大于 1 则「高置信收窄到命中库」永不触发（意图分按 0~1 输出）",
			confidenceThreshold, search.Channels.IntentDirected.MinIntentScore,
		)
	}
	if ratio := search.SupplementRatio; math.IsNaN(ratio) || ratio >= 1 {
		return fmt.Errorf(
			"validate rag.search: supplement-ratio (%v) 必须小于 1：该比例是从主路划给补充路的份额，取到 1 等于把高置信命中库的名额清零，与「定向优先、补充兜底」相反",
			ratio,
		)
	}
	return nil
}

func validateChatTiers(chatCfg AIChatConfig) error {
	if len(chatCfg.Tiers) == 0 {
		return nil
	}
	if strings.TrimSpace(chatCfg.DefaultTier) == "" {
		return fmt.Errorf("validate ai.chat: default-tier is required when tiers are configured")
	}
	if _, ok := chatCfg.Tiers[chatCfg.DefaultTier]; !ok {
		return fmt.Errorf("validate ai.chat: default-tier %q does not reference a configured tier", chatCfg.DefaultTier)
	}
	if strings.TrimSpace(chatCfg.DeepThinkingTier) == "" {
		return fmt.Errorf("validate ai.chat: deep-thinking-tier is required when tiers are configured")
	}
	if _, ok := chatCfg.Tiers[chatCfg.DeepThinkingTier]; !ok {
		return fmt.Errorf("validate ai.chat: deep-thinking-tier %q does not reference a configured tier", chatCfg.DeepThinkingTier)
	}

	registry := make(map[string]AICandidateConfig, len(chatCfg.Candidates))
	for _, candidate := range chatCfg.Candidates {
		id := candidate.ID
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("%s::%s", candidate.Provider, candidate.Model)
		}
		if _, exists := registry[id]; exists {
			return fmt.Errorf("validate ai.chat: duplicate candidate id %q", id)
		}
		registry[id] = candidate
	}
	for name, tier := range chatCfg.Tiers {
		if tier.TimeoutMs <= 0 {
			return fmt.Errorf("validate ai.chat: tier %q timeout-ms must be positive", name)
		}
		if len(tier.Candidates) == 0 {
			return fmt.Errorf("validate ai.chat: tier %q candidates must not be empty", name)
		}
		for _, id := range tier.Candidates {
			if _, ok := registry[id]; !ok {
				return fmt.Errorf("validate ai.chat: tier %q references unknown candidate %q", name, id)
			}
		}
	}
	deep := chatCfg.Tiers[chatCfg.DeepThinkingTier]
	for _, id := range deep.Candidates {
		candidate := registry[id]
		if candidate.IsEnabled() && candidate.SupportsThinking {
			return nil
		}
	}
	return fmt.Errorf("validate ai.chat: deep-thinking-tier %q has no enabled thinking candidate", chatCfg.DeepThinkingTier)
}

// expandEnv 替换字符串中的环境变量占位符。
// 支持两种语法：
//
//	${VAR}        — os.ExpandEnv 标准语法
//	${VAR:default} — 对齐 Spring 语法，若 VAR 未设置则使用 default
var envPattern = regexp.MustCompile(`\$\{([^}:]+)(?::([^}]*))?\}`)

func expandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := envPattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		name := parts[1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		if len(parts) >= 3 {
			return parts[2]
		}
		return ""
	})
}
