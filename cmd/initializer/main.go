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
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/framework/lock"
	initializerCleanup "go-base-agent/internal/initializer/cleanup"
	initializerConfig "go-base-agent/internal/initializer/config"
	initializerInitialize "go-base-agent/internal/initializer/initialize"
	initializerMigrate "go-base-agent/internal/initializer/migrate"
	initializerPreflight "go-base-agent/internal/initializer/preflight"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const initializerActiveWindow = 30 * time.Minute

func expectedInitializerBackends(cfg *config.Config) map[string]string {
	if cfg == nil {
		return nil
	}
	return map[string]string{
		"vector":  firstNonEmptyInitializer(cfg.RAG.Vector.Type, "pg"),
		"storage": "s3",
		"keyword": "pg",
		"graph":   firstNonEmptyInitializer(cfg.RAG.Graph.Type, "none"),
	}
}

func firstNonEmptyInitializer(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func loadInitializerProperties(agentTypeDir, explicitPath string) (*initializerConfig.Config, error) {
	path := strings.TrimSpace(explicitPath)
	if path == "" {
		dir := strings.TrimSpace(agentTypeDir)
		if dir == "" {
			return nil, nil
		}
		path = filepath.Join(dir, "initializer.properties")
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("检查初始化器配置失败: %w", err)
		}
	}
	return initializerConfig.Load(path)
}

func resolveInitializerConfigPath(props *initializerConfig.Config, cliPath string) (string, error) {
	if path := strings.TrimSpace(cliPath); path != "" {
		return path, nil
	}
	if props != nil {
		if path := props.Get("application.config", ""); path != "" {
			return props.ResolvePath("application.config")
		}
	}
	return "configs/config.yaml", nil
}

func resolveInitializerString(props *initializerConfig.Config, cliValue, key, defaultValue string) string {
	if value := strings.TrimSpace(cliValue); value != "" {
		return value
	}
	if props != nil {
		if value := props.Get(key, ""); value != "" {
			return value
		}
	}
	return defaultValue
}

func resolveInitializerBackends(cfg *config.Config, props *initializerConfig.Config) map[string]string {
	backends := expectedInitializerBackends(cfg)
	if props == nil {
		return backends
	}
	for name, key := range map[string]string{
		"vector":  "execution.expected-vector-type",
		"storage": "execution.expected-storage-type",
		"keyword": "execution.expected-keyword-type",
		"graph":   "execution.expected-graph-type",
	} {
		if value := props.Get(key, ""); value != "" {
			backends[name] = value
		}
	}
	return backends
}

func initializerFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func resolveCleanupConfirmation(fs *flag.FlagSet, value string) string {
	if !initializerFlagSet(fs, "confirm") {
		return ""
	}
	return value
}

func initializerDurationSeconds(props *initializerConfig.Config, key string, defaultValue time.Duration) time.Duration {
	if props == nil {
		return defaultValue
	}
	seconds, err := props.GetInt(key, int(defaultValue/time.Second))
	if err != nil || seconds <= 0 {
		return defaultValue
	}
	return time.Duration(seconds) * time.Second
}

func initializerInt(props *initializerConfig.Config, key string, defaultValue int) int {
	if props == nil {
		return defaultValue
	}
	value, err := props.GetInt(key, defaultValue)
	if err != nil {
		return defaultValue
	}
	return value
}

func resolveCleanupPath(props *initializerConfig.Config, cliPath, agentTypeDir string) string {
	if path := strings.TrimSpace(cliPath); path != "" {
		return resolveCleanupFile(path)
	}
	if props != nil {
		if path := props.Get("cleanup.file", ""); path != "" {
			if resolved, err := props.ResolvePath("cleanup.file"); err == nil {
				return resolved
			}
		}
	}
	if dir := strings.TrimSpace(agentTypeDir); dir != "" {
		path := filepath.Join(dir, "cleanup.sql")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return resolveCleanupFile("resources/database/cleanup_pg.sql")
}

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
	configPath := fs.String("config", "", "config file path")
	agentTypeDir := fs.String("agent-type-dir", "", "agent type dataset directory")
	initializerConfigPath := fs.String("initializer-config", "", "initializer properties file path")
	baseURL := fs.String("base-url", "", "service base url")
	adminUsername := fs.String("admin-username", "", "admin username")
	adminPassword := fs.String("admin-password", "", "admin password")
	if err := fs.Parse(args); err != nil {
		return err
	}

	props, err := loadInitializerProperties(*agentTypeDir, *initializerConfigPath)
	if err != nil {
		return err
	}
	resolvedConfigPath, err := resolveInitializerConfigPath(props, *configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(strings.TrimSpace(resolvedConfigPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	serviceBaseURL := resolveInitializerString(props, *baseURL, "server.base-url", fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port))
	username := resolveInitializerString(props, *adminUsername, "auth.username", "admin")
	password := resolveInitializerString(props, *adminPassword, "auth.password", "admin")
	requestTimeout := initializerDurationSeconds(props, "server.request-timeout-seconds", 10*time.Second)

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
		AdminUsername: username,
		AdminPassword: password,
		HTTPClient:    &http.Client{Timeout: requestTimeout},
		CheckDB: func(ctx context.Context) error {
			return db.Ping(ctx, gormDB)
		},
		CheckRedis: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return redisClient.Ping(pingCtx).Err()
		},
		CheckIdle: func(ctx context.Context) error {
			return checkInitializerIdle(ctx, gormDB, redisClient)
		},
		ExpectedBackends: resolveInitializerBackends(cfg, props),
	}); err != nil {
		return err
	}
	slog.Info("preflight checks passed", "base_url", serviceBaseURL)
	return nil
}

func runCleanup(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path")
	agentTypeDir := fs.String("agent-type-dir", "", "agent type dataset directory")
	initializerConfigPath := fs.String("initializer-config", "", "initializer properties file path")
	baseURL := fs.String("base-url", "", "service base url")
	adminUsername := fs.String("admin-username", "", "admin username")
	adminPassword := fs.String("admin-password", "", "admin password")
	confirm := fs.String("confirm", "", "confirmation token")
	cleanupFile := fs.String("cleanup-file", "", "cleanup sql file path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	props, err := loadInitializerProperties(*agentTypeDir, *initializerConfigPath)
	if err != nil {
		return err
	}
	resolvedConfigPath, err := resolveInitializerConfigPath(props, *configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(strings.TrimSpace(resolvedConfigPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	expectedConfirmation := resolveInitializerString(props, "", "cleanup.confirmation", "RESET-ENTERPRISE-KNOWLEDGE-BASE")
	confirmation := resolveCleanupConfirmation(fs, *confirm)
	if strings.TrimSpace(confirmation) != expectedConfirmation {
		return fmt.Errorf("请传入确认词 --confirm %s", expectedConfirmation)
	}
	username := resolveInitializerString(props, *adminUsername, "auth.username", "admin")
	password := resolveInitializerString(props, *adminPassword, "auth.password", "admin")
	serviceBaseURL := resolveInitializerString(props, *baseURL, "server.base-url", fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port))
	cleanupPath := resolveCleanupPath(props, *cleanupFile, *agentTypeDir)
	requestTimeout := initializerDurationSeconds(props, "server.request-timeout-seconds", 10*time.Second)
	lockTTL := initializerDurationSeconds(props, "cleanup.lock-seconds", time.Hour)

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
		BaseURL:           serviceBaseURL,
		AdminUsername:     username,
		AdminPassword:     password,
		Confirm:           confirmation,
		ConfirmationToken: expectedConfirmation,
		CleanupFile:       cleanupPath,
		LockTTL:           lockTTL,
		HTTPClient:        &http.Client{Timeout: requestTimeout},
		CheckDB: func(ctx context.Context) error {
			return db.Ping(ctx, gormDB)
		},
		CheckRedis: func(ctx context.Context) error {
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return redisClient.Ping(pingCtx).Err()
		},
		CheckIdle: func(ctx context.Context) error {
			return checkInitializerIdle(ctx, gormDB, redisClient)
		},
		ExpectedBackends: resolveInitializerBackends(cfg, props),
		RunPreflight:     initializerPreflight.Run,
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
		ClearCache: func(ctx context.Context) error {
			return clearInitializerRedis(ctx, redisClient)
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
	configPath := fs.String("config", "", "config file path")
	initializerConfigPath := fs.String("initializer-config", "", "initializer properties file path")
	baseURL := fs.String("base-url", "", "service base url")
	adminUsername := fs.String("admin-username", "", "admin username")
	adminPassword := fs.String("admin-password", "", "admin password")
	agentTypeDir := fs.String("agent-type-dir", "", "agent type dataset directory")
	confirm := fs.String("confirm", "", "confirmation token")
	cleanupFile := fs.String("cleanup-file", "", "cleanup sql file path")
	dryRun := fs.Bool("dry-run", false, "print planned actions without mutating")
	skipWarmup := fs.Bool("skip-warmup", false, "skip warmup phase")
	replaceExisting := fs.Bool("replace-existing", true, "replace same-name documents; set false to keep successful documents")
	documentTimeout := fs.Duration("document-timeout", 20*time.Minute, "maximum wait for one document chunking")
	documentPollInterval := fs.Duration("document-poll-interval", 3*time.Second, "document chunking status poll interval")
	warmupMaxAttempts := fs.Int("warmup-max-attempts", 3, "maximum attempts for one warmup turn")
	warmupRetryInterval := fs.Duration("warmup-retry-interval", 10*time.Second, "interval between warmup retries")
	warmupInterval := fs.Duration("warmup-interval", 3*time.Second, "interval between warmup turns")
	warmupShuffleSeed := fs.String("warmup-shuffle-seed", "", "fixed seed for reproducible warmup question order")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*agentTypeDir) == "" {
		return fmt.Errorf("必须传入 --agent-type-dir <智能体类型目录>")
	}
	props, err := loadInitializerProperties(*agentTypeDir, *initializerConfigPath)
	if err != nil {
		return err
	}
	resolvedConfigPath, err := resolveInitializerConfigPath(props, *configPath)
	if err != nil {
		return err
	}
	expectedConfirmation := resolveInitializerString(props, "", "cleanup.confirmation", "RESET-ENTERPRISE-KNOWLEDGE-BASE")
	confirmation := resolveCleanupConfirmation(fs, *confirm)
	if !*dryRun && strings.TrimSpace(confirmation) != expectedConfirmation {
		return fmt.Errorf("这是破坏性操作，请传入确认词 --confirm %s", expectedConfirmation)
	}
	if err := initializerInitialize.VerifyChecksums(*agentTypeDir); err != nil {
		return fmt.Errorf("校验数据集 checksum 失败: %w", err)
	}

	dataset, err := initializerInitialize.LoadDataset(*agentTypeDir)
	if err != nil {
		return fmt.Errorf("load dataset: %w", err)
	}

	cfg, err := config.Load(strings.TrimSpace(resolvedConfigPath))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	serviceBaseURL := resolveInitializerString(props, *baseURL, "server.base-url", fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port))
	username := resolveInitializerString(props, *adminUsername, "auth.username", "admin")
	password := resolveInitializerString(props, *adminPassword, "auth.password", "admin")
	cleanupPath := resolveCleanupPath(props, *cleanupFile, *agentTypeDir)
	requestTimeout := initializerDurationSeconds(props, "server.request-timeout-seconds", 120*time.Second)
	documentTimeoutValue := *documentTimeout
	if !initializerFlagSet(fs, "document-timeout") {
		documentTimeoutValue = initializerDurationSeconds(props, "document.chunk-timeout-seconds", 20*time.Minute)
	}
	documentPollIntervalValue := *documentPollInterval
	if !initializerFlagSet(fs, "document-poll-interval") {
		documentPollIntervalValue = initializerDurationSeconds(props, "document.poll-interval-seconds", 3*time.Second)
	}
	replaceExistingValue := *replaceExisting
	if !initializerFlagSet(fs, "replace-existing") && props != nil {
		replaceExistingValue, err = props.GetBool("document.replace-existing", true)
		if err != nil {
			return err
		}
	}
	warmupMaxAttemptsValue := *warmupMaxAttempts
	if !initializerFlagSet(fs, "warmup-max-attempts") {
		warmupMaxAttemptsValue = initializerInt(props, "warmup.max-attempts", 3)
	}
	warmupRetryIntervalValue := *warmupRetryInterval
	if !initializerFlagSet(fs, "warmup-retry-interval") {
		warmupRetryIntervalValue = initializerDurationSeconds(props, "warmup.retry-interval-seconds", 10*time.Second)
	}
	warmupIntervalValue := *warmupInterval
	if !initializerFlagSet(fs, "warmup-interval") {
		warmupIntervalValue = initializerDurationSeconds(props, "warmup.interval-seconds", 3*time.Second)
	}
	shuffleSeedValue := *warmupShuffleSeed
	if !initializerFlagSet(fs, "warmup-shuffle-seed") && props != nil {
		shuffleSeedValue = props.Get("warmup.shuffle-seed", "")
	}
	shuffleSeed, err := parseWarmupSeed(shuffleSeedValue)
	if err != nil {
		return err
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
		"replace_existing", replaceExistingValue,
		"document_timeout", documentTimeoutValue,
		"document_poll_interval", documentPollIntervalValue,
		"warmup_max_attempts", warmupMaxAttemptsValue,
		"warmup_retry_interval", warmupRetryIntervalValue,
		"warmup_interval", warmupIntervalValue,
		"warmup_shuffle_seed", shuffleSeedValue,
	)

	return initializerInitialize.Run(context.Background(), initializerInitialize.Options{
		BaseURL:              serviceBaseURL,
		AdminUsername:        username,
		AdminPassword:        password,
		AgentTypeDir:         *agentTypeDir,
		HTTPClient:           &http.Client{Timeout: requestTimeout},
		DryRun:               *dryRun,
		SkipWarmup:           *skipWarmup,
		ReplaceExisting:      &replaceExistingValue,
		DocumentTimeout:      documentTimeoutValue,
		DocumentPollInterval: documentPollIntervalValue,
		Preflight: func(ctx context.Context) error {
			return initializerPreflight.Run(ctx, initializerPreflight.Options{
				BaseURL:       serviceBaseURL,
				AdminUsername: username,
				AdminPassword: password,
				HTTPClient:    &http.Client{Timeout: 10 * time.Second},
				CheckDB: func(ctx context.Context) error {
					return db.Ping(ctx, gormDB)
				},
				CheckRedis: func(ctx context.Context) error {
					pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					return redisClient.Ping(pingCtx).Err()
				},
				CheckIdle: func(ctx context.Context) error {
					return checkInitializerIdle(ctx, gormDB, redisClient)
				},
				ExpectedBackends: resolveInitializerBackends(cfg, props),
			})
		},
		Cleanup: func(ctx context.Context) error {
			return initializerCleanup.Run(ctx, initializerCleanup.Options{
				BaseURL:           serviceBaseURL,
				AdminUsername:     username,
				AdminPassword:     password,
				Confirm:           confirmation,
				ConfirmationToken: expectedConfirmation,
				CleanupFile:       cleanupPath,
				LockTTL:           initializerDurationSeconds(props, "cleanup.lock-seconds", time.Hour),
				HTTPClient:        &http.Client{Timeout: 10 * time.Second},
				CheckDB: func(ctx context.Context) error {
					return db.Ping(ctx, gormDB)
				},
				CheckRedis: func(ctx context.Context) error {
					pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					return redisClient.Ping(pingCtx).Err()
				},
				CheckIdle: func(ctx context.Context) error {
					return checkInitializerIdle(ctx, gormDB, redisClient)
				},
				ExpectedBackends: resolveInitializerBackends(cfg, props),
				RunPreflight: func(ctx context.Context, opts initializerPreflight.Options) error {
					if opts.CheckIdle != nil {
						return opts.CheckIdle(ctx)
					}
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
				ClearCache: func(ctx context.Context) error {
					return clearInitializerRedis(ctx, redisClient)
				},
			})
		},
		Warmup: func(ctx context.Context, questions []initializerInitialize.Question) error {
			return runWarmupWithOptions(ctx, serviceBaseURL, username, password, questions, warmupOptions{
				MaxAttempts:   warmupMaxAttemptsValue,
				RetryInterval: warmupRetryIntervalValue,
				Interval:      warmupIntervalValue,
				ShuffleSeed:   shuffleSeed,
			})
		},
	}, dataset)
}

func checkInitializerIdle(ctx context.Context, gormDB *gorm.DB, redisClient *redis.Client) error {
	if gormDB == nil {
		return fmt.Errorf("数据库连接不能为空")
	}
	if redisClient == nil {
		return fmt.Errorf("Redis 客户端不能为空")
	}

	const query = `SELECT
		(SELECT COUNT(*) FROM t_knowledge_document WHERE deleted = 0 AND lower(status) = 'running' AND update_time >= NOW() - ?::interval) +
		(SELECT COUNT(*) FROM t_rag_trace_run WHERE deleted = 0 AND upper(status) = 'RUNNING' AND update_time >= NOW() - ?::interval) +
		(SELECT COUNT(*) FROM t_ingestion_task WHERE deleted = 0 AND lower(status) = 'running' AND update_time >= NOW() - ?::interval)`
	window := fmt.Sprintf("%d minutes", int(initializerActiveWindow/time.Minute))
	var runningRows int64
	if err := gormDB.WithContext(ctx).Raw(query, window, window, window).Scan(&runningRows).Error; err != nil {
		return fmt.Errorf("查询运行中任务失败: %w", err)
	}

	activeRedis := int64(0)
	for _, pattern := range []string{"ragent:agent:running:*", "ragent:stream:owner:*"} {
		var cursor uint64
		for {
			keys, next, err := redisClient.Scan(ctx, cursor, pattern, 500).Result()
			if err != nil {
				return fmt.Errorf("扫描运行中任务 Key 失败 %s: %w", pattern, err)
			}
			activeRedis += int64(len(keys))
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	if runningRows != 0 || activeRedis != 0 {
		return fmt.Errorf("检测到运行中的任务，拒绝初始化: db=%d, redis=%d", runningRows, activeRedis)
	}
	return nil
}

func clearInitializerRedis(ctx context.Context, client *redis.Client) error {
	if client == nil {
		return fmt.Errorf("redis client 不能为空")
	}
	const lockKey = "ragent:initializer:lock"
	exactKeys := []string{
		"ragent:intent:tree",
		"ragent:agent:resolved-prompts:v2",
	}
	patterns := []string{
		"ragent:query-term:mappings:*",
		"ragent:rag:answer:*",
		"ragent:memory:summary:lock:*",
		"ragent:agent:running:*",
		"ragent:stream:cancel:*",
		"ragent:stream:owner:*",
		"ragent:storage:bucket:init:*",
		"ragent:vector:space:init:*",
		"ragent:kb:space:init:*",
	}
	for _, key := range exactKeys {
		if key == lockKey {
			return fmt.Errorf("禁止清理初始化锁 Key")
		}
		if err := client.Del(ctx, key).Err(); err != nil {
			return fmt.Errorf("删除 Redis Key %s 失败: %w", key, err)
		}
	}
	for _, pattern := range patterns {
		if pattern == lockKey {
			return fmt.Errorf("禁止清理初始化锁 Key")
		}
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, pattern, 500).Result()
			if err != nil {
				return fmt.Errorf("扫描 Redis Key %s 失败: %w", pattern, err)
			}
			filtered := make([]string, 0, len(keys))
			for _, key := range keys {
				if key != lockKey {
					filtered = append(filtered, key)
				}
			}
			if len(filtered) > 0 {
				if err := client.Del(ctx, filtered...).Err(); err != nil {
					return fmt.Errorf("删除 Redis Key %s 失败: %w", pattern, err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	return nil
}

// runWarmup 按数据集串行提问补齐对话数据。
// 对齐 Java WarmupMain：每题独立会话，失败重试用尽后跳过该轮。
func runWarmup(ctx context.Context, baseURL, username, password string, questions []initializerInitialize.Question) error {
	return runWarmupWithOptions(ctx, baseURL, username, password, questions, warmupOptions{
		MaxAttempts:   3,
		RetryInterval: 10 * time.Second,
		Interval:      3 * time.Second,
	})
}

type warmupOptions struct {
	MaxAttempts   int
	RetryInterval time.Duration
	Interval      time.Duration
	ShuffleSeed   *int64
}

type warmupResult struct {
	ConversationID string
	MessageID      string
}

func (o warmupOptions) normalized() warmupOptions {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 1
	}
	if o.RetryInterval < 0 {
		o.RetryInterval = 0
	}
	if o.Interval < 0 {
		o.Interval = 0
	}
	return o
}

func runWarmupWithOptions(ctx context.Context, baseURL, username, password string, questions []initializerInitialize.Question, options warmupOptions) error {
	if len(questions) == 0 {
		fmt.Println("[warmup] 数据集没有配置演示问题，跳过")
		return nil
	}
	options = options.normalized()
	client := &http.Client{Timeout: 600 * time.Second}
	token, err := warmupLogin(ctx, client, baseURL, username, password)
	if err != nil {
		return fmt.Errorf("warmup login: %w", err)
	}
	ordered := orderWarmupQuestions(questions, options.ShuffleSeed)
	totalTurns := 0
	for _, question := range ordered {
		totalTurns += 1 + len(question.FollowUps)
	}
	turn := 0
	for _, question := range ordered {
		conversationID := ""
		texts := append([]string{question.Text}, question.FollowUps...)
		for i, text := range texts {
			turn++
			if turn > 1 {
				if err := waitWarmupRetry(ctx, options.Interval); err != nil {
					return err
				}
			}
			label := question.Ref
			if i > 0 {
				label = fmt.Sprintf("%s-追问%d", question.Ref, i)
			}
			fmt.Printf("[warmup] (%d/%d) %s %s\n", turn, totalTurns, label, text)
			result, err := warmupAskResult(ctx, client, baseURL, token, text, conversationID)
			if err != nil {
				for attempt := 2; attempt <= options.MaxAttempts; attempt++ {
					fmt.Printf("[warmup] %s 第 %d 次提问失败，重试间隔 %s: %v\n", label, attempt-1, options.RetryInterval, err)
					if waitWarmupRetry(ctx, options.RetryInterval) != nil {
						return ctx.Err()
					}
					result, err = warmupAskResult(ctx, client, baseURL, token, text, conversationID)
					if err == nil {
						break
					}
				}
				if err != nil {
					fmt.Printf("[warmup] %s 连续 %d 次提问失败，跳过该轮: %v\n", label, options.MaxAttempts, err)
					break
				}
			}
			if result.MessageID != "" {
				if err := warmupRecommendWithRetry(ctx, client, baseURL, token, result.MessageID, options); err != nil {
					fmt.Printf("[warmup] %s 推荐追问生成失败，只跳过推荐追问: %v\n", label, err)
				}
			}
			conversationID = result.ConversationID
		}
	}
	fmt.Printf("[warmup] 提问流程结束：问题 %d 个，共 %d 轮\n", len(ordered), totalTurns)
	return nil
}

func orderWarmupQuestions(questions []initializerInitialize.Question, seed *int64) []initializerInitialize.Question {
	ordered := append([]initializerInitialize.Question(nil), questions...)
	if len(ordered) < 2 {
		return ordered
	}
	var source rand.Source
	if seed != nil {
		source = rand.NewSource(*seed)
	} else {
		source = rand.NewSource(time.Now().UnixNano())
	}
	rand.New(source).Shuffle(len(ordered), func(i, j int) {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	})
	return ordered
}

func parseWarmupSeed(raw string) (*int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("warmup-shuffle-seed 必须是整数: %w", err)
	}
	return &seed, nil
}

func waitWarmupRetry(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	result, err := warmupAskResult(ctx, client, baseURL, token, question, conversationID)
	if err != nil {
		return "", err
	}
	return result.ConversationID, nil
}

func warmupAskResult(ctx context.Context, client *http.Client, baseURL, token, question, conversationID string) (warmupResult, error) {
	path := "/rag/v3/chat?question=" + url.QueryEscape(question) + "&deepThinking=false"
	if strings.TrimSpace(conversationID) != "" {
		path += "&conversationId=" + url.QueryEscape(conversationID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return warmupResult{}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return warmupResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return warmupResult{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var (
		eventName string
		result    warmupResult
		gotDone   bool
		gotFinish bool
		gotNormal bool
		gotAnswer strings.Builder
	)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(data)
			if eventName == "done" && data == "[DONE]" {
				gotDone = true
				continue
			}
			switch eventName {
			case "meta":
				var event struct {
					ConversationID string `json:"conversationId"`
				}
				if err := json.Unmarshal([]byte(data), &event); err == nil && event.ConversationID != "" {
					result.ConversationID = event.ConversationID
				}
			case "message":
				var event struct {
					Type  string `json:"type"`
					Delta string `json:"delta"`
				}
				if err := json.Unmarshal([]byte(data), &event); err == nil && event.Type == "response" {
					gotAnswer.WriteString(event.Delta)
				}
			case "finish":
				var event struct {
					MessageID     string `json:"messageId"`
					MessageStatus string `json:"messageStatus"`
				}
				if err := json.Unmarshal([]byte(data), &event); err == nil {
					result.MessageID = event.MessageID
					gotFinish = true
					gotNormal = strings.EqualFold(event.MessageStatus, "NORMAL")
				}
			case "reject", "cancel":
				return warmupResult{}, fmt.Errorf("SSE 流收到 %s 事件", eventName)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return warmupResult{}, fmt.Errorf("read sse stream: %w", err)
	}
	if !gotFinish {
		return warmupResult{}, fmt.Errorf("SSE 流未收到 finish 事件")
	}
	if strings.TrimSpace(result.MessageID) == "" {
		return warmupResult{}, fmt.Errorf("finish 事件缺少 messageId")
	}
	if !gotNormal {
		return warmupResult{}, fmt.Errorf("finish 事件消息状态不是 NORMAL")
	}
	if strings.TrimSpace(gotAnswer.String()) == "" {
		return warmupResult{}, fmt.Errorf("SSE 流回答为空")
	}
	if !gotDone {
		return warmupResult{}, fmt.Errorf("SSE 流未收到 done 事件")
	}
	if result.ConversationID == "" {
		result.ConversationID = conversationID
	}
	return result, nil
}

func warmupRecommendWithRetry(ctx context.Context, client *http.Client, baseURL, token, messageID string, options warmupOptions) error {
	var err error
	for attempt := 1; attempt <= options.MaxAttempts; attempt++ {
		err = warmupRecommend(ctx, client, baseURL, token, messageID)
		if err == nil {
			return nil
		}
		if attempt < options.MaxAttempts {
			if waitErr := waitWarmupRetry(ctx, options.RetryInterval); waitErr != nil {
				return waitErr
			}
		}
	}
	return err
}

func warmupRecommend(ctx context.Context, client *http.Client, baseURL, token, messageID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/api/ragent/conversations/messages/"+url.PathEscape(messageID)+"/recommended-questions", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("parse recommended questions response: %w", err)
	}
	if payload.Code != "" && payload.Code != "0" {
		return fmt.Errorf("推荐追问接口失败: %s", firstNonEmptyInitializer(payload.Message, payload.Code))
	}
	if strings.EqualFold(payload.Data.Status, "FAILED") {
		return fmt.Errorf("推荐追问生成失败")
	}
	return nil
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
