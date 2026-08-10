package main

import (
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	defaultAddress    = ":9108"
	defaultScriptPath = "/Users/mima0000/.codex/skills/member-k8s-pod-log-read/scripts/read_pod_logs.mjs"
)

type AppConfig struct {
	Address     string
	FrontendDir string
	LogReader   LogReaderConfig
	Analyzer    AnalyzerConfig
}

type LogReaderConfig struct {
	NodePath       string
	ScriptPath     string
	Timeout        time.Duration
	AllowedEnvs    []string
	Services       []string
	MaxLines       int
	MaxStdoutLines int
	MaxLineChars   int
	MaxConcurrency int
}

type AnalyzerConfig struct {
	Enable         bool
	APIKey         string
	BaseURL        string
	Model          string
	BailianAPIKey  string
	BailianBaseURL string
	BailianModel   string
	Timeout        time.Duration
	CodeRepoPath   string
	CodeMaxLines   int
}

func loadConfig() AppConfig {
	v := viper.New()
	v.SetEnvPrefix("LOG_AGENT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	v.SetDefault("address", defaultAddress)
	v.SetDefault("frontend_dir", "log-agent/frontend")
	v.SetDefault("log_reader.node_path", "node")
	v.SetDefault("log_reader.script_path", defaultScriptPath)
	v.SetDefault("log_reader.timeout_ms", 15000)
	v.SetDefault("log_reader.allowed_envs", "test,test2,test3,test4,regress,online")
	v.SetDefault("log_reader.services", strings.Join(defaultServices(), ","))
	v.SetDefault("log_reader.max_lines", 120)
	v.SetDefault("log_reader.max_stdout_lines", 80)
	v.SetDefault("log_reader.max_line_chars", 1200)
	v.SetDefault("log_reader.max_concurrency", 4)
	v.SetDefault("analyzer.enable", true)
	v.SetDefault("analyzer.base_url", "https://api.360.cn/v1")
	v.SetDefault("analyzer.bailian_base_url", "https://dashscope.aliyuncs.com")
	v.SetDefault("analyzer.bailian_model", "qwen3-max")
	v.SetDefault("analyzer.timeout_ms", 30000)
	v.SetDefault("analyzer.code_repo_path", "/Users/work_project/360/member")
	v.SetDefault("analyzer.code_max_lines", 80)

	return AppConfig{
		Address:     strings.TrimSpace(v.GetString("address")),
		FrontendDir: strings.TrimSpace(v.GetString("frontend_dir")),
		LogReader: LogReaderConfig{
			NodePath:       strings.TrimSpace(v.GetString("log_reader.node_path")),
			ScriptPath:     strings.TrimSpace(v.GetString("log_reader.script_path")),
			Timeout:        time.Duration(v.GetInt("log_reader.timeout_ms")) * time.Millisecond,
			AllowedEnvs:    splitCSV(v.GetString("log_reader.allowed_envs")),
			Services:       splitCSV(v.GetString("log_reader.services")),
			MaxLines:       v.GetInt("log_reader.max_lines"),
			MaxStdoutLines: v.GetInt("log_reader.max_stdout_lines"),
			MaxLineChars:   v.GetInt("log_reader.max_line_chars"),
			MaxConcurrency: v.GetInt("log_reader.max_concurrency"),
		},
		Analyzer: AnalyzerConfig{
			Enable:         v.GetBool("analyzer.enable"),
			APIKey:         firstNonEmpty(strings.TrimSpace(os.Getenv("QIHOO360_API_KEY")), strings.TrimSpace(v.GetString("analyzer.api_key")), strings.TrimSpace(os.Getenv("LOG_AGENT_ANALYZER_API_KEY")), strings.TrimSpace(os.Getenv("ZHINAO_API_KEY"))),
			BaseURL:        strings.TrimRight(strings.TrimSpace(v.GetString("analyzer.base_url")), "/"),
			Model:          firstAnalyzerModel(),
			BailianAPIKey:  firstNonEmpty(strings.TrimSpace(os.Getenv("BAILIAN_API_KEY")), strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY")), strings.TrimSpace(v.GetString("analyzer.bailian_api_key"))),
			BailianBaseURL: strings.TrimRight(strings.TrimSpace(v.GetString("analyzer.bailian_base_url")), "/"),
			BailianModel:   strings.TrimSpace(v.GetString("analyzer.bailian_model")),
			Timeout:        time.Duration(v.GetInt("analyzer.timeout_ms")) * time.Millisecond,
			CodeRepoPath:   strings.TrimSpace(v.GetString("analyzer.code_repo_path")),
			CodeMaxLines:   v.GetInt("analyzer.code_max_lines"),
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
