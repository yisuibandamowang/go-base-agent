package config

import (
	"fmt"
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
}

type RAGConfig struct {
	Vector       RAGVectorConfig       `mapstructure:"vector"`
	Default      RAGDefaultConfig      `mapstructure:"default"`
	QueryRewrite RAGQueryRewriteConfig `mapstructure:"query-rewrite"`
	RateLimit    RAGRateLimitConfig    `mapstructure:"rate-limit"`
	Memory       RAGMemoryConfig       `mapstructure:"memory"`
	Parser       RAGParserConfig       `mapstructure:"parser"`
	ImageParse   RAGImageParseConfig   `mapstructure:"image-parse"`
	Semaphore    RAGSemaphoreConfig    `mapstructure:"semaphore"`
	Knowledge    RAGKnowledgeConfig    `mapstructure:"knowledge"`
	MCP          RAGMCPConfig          `mapstructure:"mcp"`
	Search       RAGSearchConfig       `mapstructure:"search"`
	Trace        RAGTraceConfig        `mapstructure:"trace"`
}

type RAGVectorConfig struct {
	Type string `mapstructure:"type"`
}

type RAGDefaultConfig struct {
	CollectionName string `mapstructure:"collection-name"`
	Dimension      int    `mapstructure:"dimension"`
	MetricType     string `mapstructure:"metric-type"`
}

type RAGQueryRewriteConfig struct {
	Enabled            bool `mapstructure:"enabled"`
	MaxHistoryMessages int  `mapstructure:"max-history-messages"`
	MaxHistoryChars    int  `mapstructure:"max-history-chars"`
}

type RAGRateLimitConfig struct {
	Global RAGRateLimitGlobalConfig `mapstructure:"global"`
}

type RAGRateLimitGlobalConfig struct {
	Enabled        bool `mapstructure:"enabled"`
	MaxConcurrent  int  `mapstructure:"max-concurrent"`
	MaxWaitSeconds int  `mapstructure:"max-wait-seconds"`
	LeaseSeconds   int  `mapstructure:"lease-seconds"`
	PollIntervalMs int  `mapstructure:"poll-interval-ms"`
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
}

type RAGKnowledgeScheduleConfig struct {
	ScanDelayMs        int `mapstructure:"scan-delay-ms"`
	LockSeconds        int `mapstructure:"lock-seconds"`
	BatchSize          int `mapstructure:"batch-size"`
	MinIntervalSeconds int `mapstructure:"min-interval-seconds"`
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

type RAGMCPConfig struct {
	Servers []RAGMCPServerConfig `mapstructure:"servers"`
}

type RAGMCPServerConfig struct {
	Name string `mapstructure:"name"`
	URL  string `mapstructure:"url"`
}

type RAGSearchConfig struct {
	Channels RAGSearchChannelsConfig `mapstructure:"channels"`
}

type RAGSearchChannelsConfig struct {
	VectorGlobal   RAGSearchChannelConfig `mapstructure:"vector-global"`
	IntentDirected RAGSearchChannelConfig `mapstructure:"intent-directed"`
	Keyword        RAGSearchChannelConfig `mapstructure:"keyword"`
	WebSearch      RAGWebSearchConfig     `mapstructure:"web-search"`
}

type RAGSearchChannelConfig struct {
	Enabled             *bool   `mapstructure:"enabled"`
	ConfidenceThreshold float64 `mapstructure:"confidence-threshold"`
	TopKMultiplier      int     `mapstructure:"top-k-multiplier"`
	MinIntentScore      float64 `mapstructure:"min-intent-score"`
}

func (c RAGSearchChannelConfig) IsEnabledByDefault() bool {
	if c.Enabled == nil {
		return true
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
	Enabled        bool `mapstructure:"enabled"`
	MaxErrorLength int  `mapstructure:"max-error-length"`
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
	DefaultModel      string              `mapstructure:"default-model"`
	DeepThinkingModel string              `mapstructure:"deep-thinking-model"`
	Candidates        []AICandidateConfig `mapstructure:"candidates"`
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
	DemoMode bool          `mapstructure:"demo-mode"`
	Eval     AppEvalConfig `mapstructure:"eval"`
}

type AppEvalConfig struct {
	Enabled bool `mapstructure:"enabled"`
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

	return &cfg, nil
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
		if len(parts) >= 3 && parts[2] != "" {
			return parts[2]
		}
		return match
	})
}
