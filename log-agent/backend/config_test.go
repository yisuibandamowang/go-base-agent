package main

import "testing"

func TestLoadConfigReadsQihoo360APIKey(t *testing.T) {
	t.Setenv("LOG_AGENT_ANALYZER_API_KEY", "")
	t.Setenv("ZHINAO_API_KEY", "")
	t.Setenv("QIHOO360_API_KEY", "qihoo-key")

	cfg := loadConfig()
	if cfg.Analyzer.APIKey != "qihoo-key" {
		t.Fatalf("APIKey = %q, want qihoo-key", cfg.Analyzer.APIKey)
	}
}

func TestLoadConfigReadsBailianAPIKey(t *testing.T) {
	t.Setenv("BAILIAN_API_KEY", "bailian-key")
	t.Setenv("DASHSCOPE_API_KEY", "")

	cfg := loadConfig()
	if cfg.Analyzer.BailianAPIKey != "bailian-key" {
		t.Fatalf("BailianAPIKey = %q, want bailian-key", cfg.Analyzer.BailianAPIKey)
	}
	if cfg.Analyzer.BailianBaseURL != "https://dashscope.aliyuncs.com" {
		t.Fatalf("BailianBaseURL = %q", cfg.Analyzer.BailianBaseURL)
	}
	if cfg.Analyzer.BailianModel != "qwen3-max" {
		t.Fatalf("BailianModel = %q", cfg.Analyzer.BailianModel)
	}
}
