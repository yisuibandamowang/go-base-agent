package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestLoadConfigDisablesSQLAssistantByDefault(t *testing.T) {
	t.Setenv("LOG_AGENT_SQL_ENABLE", "")
	t.Setenv("LOG_AGENT_SQL_DSN", "")

	cfg := loadConfig()
	if cfg.SQL.Enable {
		t.Fatal("SQL.Enable = true, want false by default")
	}
	if cfg.SQL.MaxRows != 50 {
		t.Fatalf("SQL.MaxRows = %d, want 50", cfg.SQL.MaxRows)
	}
}

func TestLoadConfigReadsSQLAssistantEnv(t *testing.T) {
	t.Setenv("LOG_AGENT_SQL_ENABLE", "true")
	t.Setenv("LOG_AGENT_SQL_DSN", "postgres://readonly@example/db")
	t.Setenv("LOG_AGENT_SQL_DIALECT", "postgres")
	t.Setenv("LOG_AGENT_SQL_MAX_ROWS", "25")

	cfg := loadConfig()
	if !cfg.SQL.Enable {
		t.Fatal("SQL.Enable = false, want true")
	}
	if cfg.SQL.DSN != "postgres://readonly@example/db" {
		t.Fatalf("SQL.DSN = %q", cfg.SQL.DSN)
	}
	if cfg.SQL.MaxRows != 25 {
		t.Fatalf("SQL.MaxRows = %d, want 25", cfg.SQL.MaxRows)
	}
}

func TestLoadConfigReadsProjectSSHProfiles(t *testing.T) {
	t.Setenv("LOG_AGENT_SQL_ENABLE", "true")
	t.Setenv("LOG_AGENT_SQL_SSH_PROFILES_MEMBER_ENABLE", "true")
	t.Setenv("LOG_AGENT_SQL_SSH_PROFILES_MEMBER_HOST", "chenhongyi")

	cfg := loadConfig()
	profile := cfg.SQL.SSHProfiles["member"]
	if !profile.Enable {
		t.Fatal("member ssh profile disabled, want enabled")
	}
	if profile.Host != "chenhongyi" {
		t.Fatalf("member ssh profile = %#v", profile)
	}
}

func TestLoadConfigReadsProjectSSHProfilesFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "log-agent.yaml")
	configText := `
sql:
  enable: true
  ssh_profiles:
    member:
      enable: true
      host: chenhongyi
`
	if err := os.WriteFile(configPath, []byte(configText), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("LOG_AGENT_CONFIG_FILE", configPath)

	cfg := loadConfig()
	profile := cfg.SQL.SSHProfiles["member"]
	if !cfg.SQL.Enable {
		t.Fatal("SQL.Enable = false, want true from config file")
	}
	if !profile.Enable {
		t.Fatal("member ssh profile disabled, want enabled from config file")
	}
	if profile.Host != "chenhongyi" {
		t.Fatalf("member ssh profile = %#v", profile)
	}
}

func TestSSHProfileAllowsOpenSSHHostAlias(t *testing.T) {
	err := validateSSHProfile(SSHProfileConfig{
		Enable: true,
		Host:   "chenhongyi",
	})
	if err != nil {
		t.Fatalf("validateSSHProfile() error = %v", err)
	}
}

func TestSSHTunnelCommandUsesOpenSSHHostAlias(t *testing.T) {
	cmd := sshTunnelCommand(context.Background(), "chenhongyi", "127.0.0.1", 15432, "pg-member.internal", 5432)
	got := strings.Join(cmd.Args, " ")
	want := "ssh -N -L 127.0.0.1:15432:pg-member.internal:5432 chenhongyi"
	if got != want {
		t.Fatalf("ssh args = %q, want %q", got, want)
	}
}
