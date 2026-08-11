package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	defaultAddress         = ":9108"
	defaultScriptPath      = "/Users/mima0000/.codex/skills/member-k8s-pod-log-read/scripts/read_pod_logs.mjs"
	defaultFuyaoScriptPath = "/Users/work_project/360/ad-platform-bot/.codex/skills/ad-platform-runtime-readonly/scripts/k8s_pod_logs.mjs"
	defaultFuyaoWorkDir    = "/Users/work_project/360/ad-platform-bot"
)

type AppConfig struct {
	Address     string
	FrontendDir string
	LogReader   LogReaderConfig
	Analyzer    AnalyzerConfig
	SQL         SQLConfig
}

type LogReaderConfig struct {
	NodePath        string
	ScriptPath      string
	FuyaoScriptPath string
	FuyaoWorkDir    string
	Timeout         time.Duration
	AllowedEnvs     []string
	Services        []string
	MaxLines        int
	MaxStdoutLines  int
	MaxLineChars    int
	MaxConcurrency  int
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

// SQLConfig controls the optional read-only SQL assistant.
type SQLConfig struct {
	Enable      bool
	Dialect     string
	DSN         string
	Timeout     time.Duration
	MaxRows     int
	SSHProfiles map[string]SSHProfileConfig
}

// SSHProfileConfig controls one project-level SSH tunnel profile.
type SSHProfileConfig struct {
	Enable bool
	Host   string
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
	v.SetDefault("log_reader.fuyao_script_path", defaultFuyaoScriptPath)
	v.SetDefault("log_reader.fuyao_work_dir", defaultFuyaoWorkDir)
	v.SetDefault("log_reader.timeout_ms", 120000)
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
	v.SetDefault("sql.enable", false)
	v.SetDefault("sql.dialect", "postgres")
	v.SetDefault("sql.dsn", "")
	v.SetDefault("sql.timeout_ms", 3000)
	v.SetDefault("sql.max_rows", 50)
	for _, project := range []string{logProjectMember, logProjectFuyao} {
		v.SetDefault("sql.ssh_profiles."+project+".enable", false)
	}
	readConfigFile(v)

	return AppConfig{
		Address:     strings.TrimSpace(v.GetString("address")),
		FrontendDir: strings.TrimSpace(v.GetString("frontend_dir")),
		LogReader: LogReaderConfig{
			NodePath:        strings.TrimSpace(v.GetString("log_reader.node_path")),
			ScriptPath:      strings.TrimSpace(v.GetString("log_reader.script_path")),
			FuyaoScriptPath: strings.TrimSpace(v.GetString("log_reader.fuyao_script_path")),
			FuyaoWorkDir:    strings.TrimSpace(v.GetString("log_reader.fuyao_work_dir")),
			Timeout:         time.Duration(v.GetInt("log_reader.timeout_ms")) * time.Millisecond,
			AllowedEnvs:     splitCSV(v.GetString("log_reader.allowed_envs")),
			Services:        splitCSV(v.GetString("log_reader.services")),
			MaxLines:        v.GetInt("log_reader.max_lines"),
			MaxStdoutLines:  v.GetInt("log_reader.max_stdout_lines"),
			MaxLineChars:    v.GetInt("log_reader.max_line_chars"),
			MaxConcurrency:  v.GetInt("log_reader.max_concurrency"),
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
		SQL: SQLConfig{
			Enable:      v.GetBool("sql.enable"),
			Dialect:     strings.TrimSpace(v.GetString("sql.dialect")),
			DSN:         strings.TrimSpace(firstNonEmpty(os.Getenv("LOG_AGENT_SQL_DSN"), v.GetString("sql.dsn"))),
			Timeout:     time.Duration(v.GetInt("sql.timeout_ms")) * time.Millisecond,
			MaxRows:     v.GetInt("sql.max_rows"),
			SSHProfiles: loadSSHProfiles(v),
		},
	}
}

func readConfigFile(v *viper.Viper) {
	configFile := strings.TrimSpace(os.Getenv("LOG_AGENT_CONFIG_FILE"))
	if configFile == "" {
		configFile = strings.TrimSpace(os.Getenv("LOG_AGENT_CONFIG"))
	}
	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to read log-agent config file %s: %v\n", configFile, err)
		}
		return
	}
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("log-agent")
	v.AddConfigPath(".")
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			_, _ = fmt.Fprintf(os.Stderr, "failed to read optional log-agent config file: %v\n", err)
		}
	}
}

func loadSSHProfiles(v *viper.Viper) map[string]SSHProfileConfig {
	profiles := make(map[string]SSHProfileConfig, 2)
	for _, project := range []string{logProjectMember, logProjectFuyao} {
		prefix := "sql.ssh_profiles." + project + "."
		profiles[project] = SSHProfileConfig{
			Enable: v.GetBool(prefix + "enable"),
			Host:   strings.TrimSpace(v.GetString(prefix + "host")),
		}
	}
	return profiles
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
