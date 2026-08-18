package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/framework/lock"
	initializerCleanup "go-base-agent/internal/initializer/cleanup"
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
	case "cleanup":
		if err := runCleanup(os.Args[2:]); err != nil {
			slog.Error("cleanup failed", "err", err)
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

func resolveCleanupFile(path string) string {
	if strings.TrimSpace(path) == "" {
		return "resources/database/cleanup_pg.sql"
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(path)
}
