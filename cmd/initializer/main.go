package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/framework/lock"
	initializerCleanup "go-base-agent/internal/initializer/cleanup"
	initializerInitialize "go-base-agent/internal/initializer/initialize"
	initializerMigrate "go-base-agent/internal/initializer/migrate"
	initializerPreflight "go-base-agent/internal/initializer/preflight"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: initializer preflight|cleanup [flags]")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "preflight":
		if err := runPreflight(os.Args[2:]); err != nil {
			slog.Error("preflight failed", "err", err)
			os.Exit(1)
		}
	case "migrate":
		if err := runMigrate(os.Args[2:]); err != nil {
			slog.Error("migrate failed", "err", err)
			os.Exit(1)
		}
	case "cleanup":
		if err := runCleanup(os.Args[2:]); err != nil {
			slog.Error("cleanup failed", "err", err)
			os.Exit(1)
		}
	case "initialize":
		if err := runInitialize(os.Args[2:]); err != nil {
			slog.Error("initialize failed", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func runPreflight(args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	configPath := fs.String("config", "configs/config.yaml", "config file path")
	baseURL := fs.String("base-url", "", "service base url")
	adminUsername := fs.String("admin-username", "admin", "admin username")
	adminPassword := fs.String("admin-password", "admin", "admin password")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(strings.TrimSpace(*configPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	serviceBaseURL := strings.TrimSpace(*baseURL)
	if serviceBaseURL == "" {
		serviceBaseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	}

	gormDB, err := db.NewDB(cfg.Database)
	if err != nil {
		return fmt.Errorf("database check failed: %w", err)
	}
	defer func() {
		if closeErr := db.Close(gormDB); closeErr != nil {
			slog.Warn("close database failed", "err", closeErr)
		}
	}()

	redisClient := cfg.Redis.NewClient()
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			slog.Warn("close redis client failed", "err", closeErr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis check failed: %w", err)
	}

	slog.Info("preflight checks start", "base_url", serviceBaseURL)
	if err := initializerPreflight.Run(context.Background(), initializerPreflight.Options{
		BaseURL:       serviceBaseURL,
		AdminUsername: *adminUsername,
		AdminPassword: *adminPassword,
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		CheckDB: func(ctx context.Context) error {
			return db.Ping(ctx, gormDB)
		},
		CheckRedis: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return redisClient.Ping(pingCtx).Err()
		},
	}); err != nil {
		return err
	}
	slog.Info("preflight checks passed", "base_url", serviceBaseURL)
	return nil
}

func runCleanup(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	configPath := fs.String("config", "configs/config.yaml", "config file path")
	baseURL := fs.String("base-url", "", "service base url")
	adminUsername := fs.String("admin-username", "admin", "admin username")
	adminPassword := fs.String("admin-password", "admin", "admin password")
	confirm := fs.String("confirm", "", "confirmation token")
	cleanupFile := fs.String("cleanup-file", "resources/database/cleanup_pg.sql", "cleanup sql file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*confirm) != "RESET-ENTERPRISE-KNOWLEDGE-BASE" {
		return fmt.Errorf("请传入确认词 --confirm RESET-ENTERPRISE-KNOWLEDGE-BASE")
	}

	cfg, err := config.Load(strings.TrimSpace(*configPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	serviceBaseURL := strings.TrimSpace(*baseURL)
	if serviceBaseURL == "" {
		serviceBaseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	}

	gormDB, err := db.NewDB(cfg.Database)
	if err != nil {
		return fmt.Errorf("database check failed: %w", err)
	}
	defer func() {
		if closeErr := db.Close(gormDB); closeErr != nil {
			slog.Warn("close database failed", "err", closeErr)
		}
	}()

	redisClient := cfg.Redis.NewClient()
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			slog.Warn("close redis client failed", "err", closeErr)
		}
	}()

	slog.Info("cleanup preflight start", "base_url", serviceBaseURL)
	if err := initializerCleanup.Run(context.Background(), initializerCleanup.Options{
		BaseURL:       serviceBaseURL,
		AdminUsername: *adminUsername,
		AdminPassword: *adminPassword,
		Confirm:       *confirm,
		CleanupFile:   resolveCleanupFile(*cleanupFile),
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
		CheckDB: func(ctx context.Context) error {
			return db.Ping(ctx, gormDB)
		},
		CheckRedis: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return redisClient.Ping(pingCtx).Err()
		},
		RunPreflight: initializerPreflight.Run,
		AcquireLock: func(ctx context.Context, key string, ttl time.Duration) (bool, error) {
			return lock.New(redisClient).Acquire(ctx, key, ttl)
		},
		ReleaseLock: func(ctx context.Context, key string) error {
			return lock.New(redisClient).Release(ctx, key)
		},
		ExecuteCleanup: func(ctx context.Context, path string) error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read cleanup sql: %w", err)
			}
			sqlDB, err := gormDB.DB()
			if err != nil {
				return fmt.Errorf("get sql db: %w", err)
			}
			if _, err := sqlDB.ExecContext(ctx, string(raw)); err != nil {
				return fmt.Errorf("execute cleanup sql: %w", err)
			}
			return nil
		},
	}); err != nil {
		return err
	}
	slog.Info("cleanup completed", "base_url", serviceBaseURL)
	return nil
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configPath := fs.String("config", "configs/config.yaml", "config file path")
	schemaFile := fs.String("schema-file", "resources/database/schema_pg.sql", "schema sql file path")
	initFile := fs.String("init-file", "resources/database/init_data_pg.sql", "initial data sql file path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(strings.TrimSpace(*configPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	gormDB, err := db.NewDB(cfg.Database)
	if err != nil {
		return fmt.Errorf("database check failed: %w", err)
	}
	defer func() {
		if closeErr := db.Close(gormDB); closeErr != nil {
			slog.Warn("close database failed", "err", closeErr)
		}
	}()

	slog.Info("migrate start", "schema_file", resolveSQLFile(*schemaFile), "init_file", resolveSQLFile(*initFile))
	if err := initializerMigrate.Run(context.Background(), initializerMigrate.Options{
		CheckDB: func(ctx context.Context) error {
			return db.Ping(ctx, gormDB)
		},
		SchemaFile: resolveSQLFile(*schemaFile),
		InitFile:   resolveSQLFile(*initFile),
		ExecuteSQL: func(ctx context.Context, path string) error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read sql file: %w", err)
			}
			sqlDB, err := gormDB.DB()
			if err != nil {
				return fmt.Errorf("get sql db: %w", err)
			}
			if _, err := sqlDB.ExecContext(ctx, string(raw)); err != nil {
				return fmt.Errorf("execute sql: %w", err)
			}
			return nil
		},
	}); err != nil {
		return err
	}
	slog.Info("migrate completed")
	return nil
}

func runInitialize(args []string) error {
	fs := flag.NewFlagSet("initialize", flag.ContinueOnError)
	configPath := fs.String("config", "configs/config.yaml", "config file path")
	baseURL := fs.String("base-url", "", "service base url")
	adminUsername := fs.String("admin-username", "admin", "admin username")
	adminPassword := fs.String("admin-password", "admin", "admin password")
	agentTypeDir := fs.String("agent-type-dir", "", "agent type dataset directory")
	confirm := fs.String("confirm", "", "confirmation token")
	cleanupFile := fs.String("cleanup-file", "resources/database/cleanup_pg.sql", "cleanup sql file path")
	dryRun := fs.Bool("dry-run", false, "print planned actions without mutating")
	skipWarmup := fs.Bool("skip-warmup", false, "skip warmup phase")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*agentTypeDir) == "" {
		return fmt.Errorf("必须传入 --agent-type-dir <智能体类型目录>")
	}
	if !*dryRun && strings.TrimSpace(*confirm) != "RESET-ENTERPRISE-KNOWLEDGE-BASE" {
		return fmt.Errorf("这是破坏性操作，请传入确认词 --confirm RESET-ENTERPRISE-KNOWLEDGE-BASE")
	}

	dataset, err := initializerInitialize.LoadDataset(*agentTypeDir)
	if err != nil {
		return fmt.Errorf("load dataset: %w", err)
	}

	cfg, err := config.Load(strings.TrimSpace(*configPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	serviceBaseURL := strings.TrimSpace(*baseURL)
	if serviceBaseURL == "" {
		serviceBaseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	}

	gormDB, err := db.NewDB(cfg.Database)
	if err != nil {
		return fmt.Errorf("database check failed: %w", err)
	}
	defer func() {
		if closeErr := db.Close(gormDB); closeErr != nil {
			slog.Warn("close database failed", "err", closeErr)
		}
	}()

	redisClient := cfg.Redis.NewClient()
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			slog.Warn("close redis client failed", "err", closeErr)
		}
	}()

	slog.Info("initialize start",
		"base_url", serviceBaseURL,
		"agent_type_dir", *agentTypeDir,
		"knowledge_bases", len(dataset.KnowledgeBases),
		"intents", len(dataset.Intents),
		"questions", len(dataset.Questions),
		"dry_run", *dryRun,
	)

	return initializerInitialize.Run(context.Background(), initializerInitialize.Options{
		BaseURL:       serviceBaseURL,
		AdminUsername: *adminUsername,
		AdminPassword: *adminPassword,
		AgentTypeDir:  *agentTypeDir,
		HTTPClient:    &http.Client{Timeout: 120 * time.Second},
		DryRun:        *dryRun,
		SkipWarmup:    *skipWarmup,
		Preflight: func(ctx context.Context) error {
			return initializerPreflight.Run(ctx, initializerPreflight.Options{
				BaseURL:       serviceBaseURL,
				AdminUsername: *adminUsername,
				AdminPassword: *adminPassword,
				HTTPClient:    &http.Client{Timeout: 10 * time.Second},
				CheckDB: func(ctx context.Context) error {
					return db.Ping(ctx, gormDB)
				},
				CheckRedis: func(ctx context.Context) error {
					pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					return redisClient.Ping(pingCtx).Err()
				},
			})
		},
		Cleanup: func(ctx context.Context) error {
			return initializerCleanup.Run(ctx, initializerCleanup.Options{
				BaseURL:       serviceBaseURL,
				AdminUsername: *adminUsername,
				AdminPassword: *adminPassword,
				Confirm:       *confirm,
				CleanupFile:   resolveCleanupFile(*cleanupFile),
				HTTPClient:    &http.Client{Timeout: 10 * time.Second},
				CheckDB: func(ctx context.Context) error {
					return db.Ping(ctx, gormDB)
				},
				CheckRedis: func(ctx context.Context) error {
					pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					return redisClient.Ping(pingCtx).Err()
				},
				RunPreflight: func(ctx context.Context, opts initializerPreflight.Options) error {
					return nil
				},
				AcquireLock: func(ctx context.Context, key string, ttl time.Duration) (bool, error) {
					return lock.New(redisClient).Acquire(ctx, key, ttl)
				},
				ReleaseLock: func(ctx context.Context, key string) error {
					return lock.New(redisClient).Release(ctx, key)
				},
				ExecuteCleanup: func(ctx context.Context, path string) error {
					raw, err := os.ReadFile(path)
					if err != nil {
						return fmt.Errorf("read cleanup sql: %w", err)
					}
					sqlDB, err := gormDB.DB()
					if err != nil {
						return fmt.Errorf("get sql db: %w", err)
					}
					if _, err := sqlDB.ExecContext(ctx, string(raw)); err != nil {
						return fmt.Errorf("execute cleanup sql: %w", err)
					}
					return nil
				},
			})
		},
		Warmup: func(ctx context.Context, questions []initializerInitialize.Question) error {
			return runWarmup(ctx, serviceBaseURL, *adminUsername, *adminPassword, questions)
		},
	}, dataset)
}

// runWarmup 按数据集串行提问补齐对话数据。
// 对齐 Java WarmupMain：每题独立会话，失败重试用尽后跳过该轮。
func runWarmup(ctx context.Context, baseURL, username, password string, questions []initializerInitialize.Question) error {
	if len(questions) == 0 {
		fmt.Println("[warmup] 数据集没有配置演示问题，跳过")
		return nil
	}
	client := &http.Client{Timeout: 600 * time.Second}
	token, err := warmupLogin(ctx, client, baseURL, username, password)
	if err != nil {
		return fmt.Errorf("warmup login: %w", err)
	}
	totalTurns := 0
	for _, question := range questions {
		totalTurns += 1 + len(question.FollowUps)
	}
	turn := 0
	for _, question := range questions {
		conversationID := ""
		texts := append([]string{question.Text}, question.FollowUps...)
		for i, text := range texts {
			turn++
			label := question.Ref
			if i > 0 {
				label = fmt.Sprintf("%s-追问%d", question.Ref, i)
			}
			fmt.Printf("[warmup] (%d/%d) %s %s\n", turn, totalTurns, label, text)
			id, err := warmupAsk(ctx, client, baseURL, token, text, conversationID)
			if err != nil {
				fmt.Printf("[warmup] %s 提问失败，跳过该轮: %v\n", label, err)
				break
			}
			conversationID = id
		}
	}
	fmt.Printf("[warmup] 提问流程结束：问题 %d 个，共 %d 轮\n", len(questions), totalTurns)
	return nil
}

func warmupLogin(ctx context.Context, client *http.Client, baseURL, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/ragent/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var payload struct {
		Code string `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("parse login response: %w", err)
	}
	if payload.Data.Token == "" {
		return "", fmt.Errorf("login response missing token: %s", strings.TrimSpace(string(raw)))
	}
	return payload.Data.Token, nil
}

// warmupAsk 提问一轮 SSE 并读取到流结束，返回本轮会话 ID。
func warmupAsk(ctx context.Context, client *http.Client, baseURL, token, question, conversationID string) (string, error) {
	path := "/rag/v3/chat?question=" + url.QueryEscape(question) + "&deepThinking=false"
	if strings.TrimSpace(conversationID) != "" {
		path += "&conversationId=" + url.QueryEscape(conversationID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var (
		gotDone bool
		gotMeta string
	)
	for scanner.Scan() {
		line := scanner.Text()
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				gotDone = true
				continue
			}
			var event struct {
				ConversationID string `json:"conversationId"`
			}
			if err := json.Unmarshal([]byte(data), &event); err == nil && event.ConversationID != "" {
				gotMeta = event.ConversationID
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read sse stream: %w", err)
	}
	if !gotDone {
		return "", fmt.Errorf("SSE 流未收到 done 事件")
	}
	if gotMeta != "" {
		return gotMeta, nil
	}
	return conversationID, nil
}

func resolveCleanupFile(path string) string {
	if strings.TrimSpace(path) == "" {
		return "resources/database/cleanup_pg.sql"
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(path)
}

func resolveSQLFile(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(path)
}
