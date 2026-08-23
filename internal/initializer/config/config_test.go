package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExpandsEnvironmentValuesAndResolvesRelativePaths(t *testing.T) {
	t.Setenv("RAG_INIT_USER", "operator")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "initializer.properties")
	applicationDir := filepath.Join(dir, "..", "configs")
	if err := os.MkdirAll(applicationDir, 0o755); err != nil {
		t.Fatalf("create application config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(applicationDir, "config.yaml"), []byte("server:\n  port: 9090\n"), 0o644); err != nil {
		t.Fatalf("write application config: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"auth.username=${RAG_INIT_USER}",
		"auth.password=${MISSING_PASSWORD:secret}",
		"application.config=../configs/config.yaml",
		"redis.cleanup-patterns=one:*, two:*",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write initializer config: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("load initializer config: %v", err)
	}
	if got := loaded.Get("auth.username", ""); got != "operator" {
		t.Fatalf("unexpected expanded username: %q", got)
	}
	if got := loaded.Get("auth.password", ""); got != "secret" {
		t.Fatalf("unexpected default password: %q", got)
	}
	wantPath := filepath.Clean(filepath.Join(dir, "../configs/config.yaml"))
	gotPath, err := loaded.ResolvePath("application.config")
	if err != nil {
		t.Fatalf("resolve application config: %v", err)
	}
	if got := gotPath; got != wantPath {
		t.Fatalf("unexpected resolved application config: %q, want %q", got, wantPath)
	}
	if got := loaded.GetList("redis.cleanup-patterns"); len(got) != 2 || got[0] != "one:*" || got[1] != "two:*" {
		t.Fatalf("unexpected list value: %#v", got)
	}
}

func TestLoadRejectsMissingEnvironmentValue(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "initializer.properties")
	if err := os.WriteFile(configPath, []byte("auth.username=${MISSING_REQUIRED_VALUE}\n"), 0o644); err != nil {
		t.Fatalf("write initializer config: %v", err)
	}

	if _, err := Load(configPath); err == nil || !strings.Contains(err.Error(), "MISSING_REQUIRED_VALUE") {
		t.Fatalf("expected missing environment variable error, got %v", err)
	}
}
