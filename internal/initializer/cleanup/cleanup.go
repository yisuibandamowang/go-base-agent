package cleanup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	initializerPreflight "go-base-agent/internal/initializer/preflight"
)

const defaultLockKey = "ragent:initializer:lock"
const defaultLockTTL = time.Hour
const confirmationToken = "RESET-ENTERPRISE-KNOWLEDGE-BASE"

// Options describes the cleanup workflow.
type Options struct {
	BaseURL        string
	AdminUsername  string
	AdminPassword  string
	Confirm        string
	CleanupFile    string
	HTTPClient     *http.Client
	CheckDB        func(context.Context) error
	CheckRedis     func(context.Context) error
	RunPreflight   func(context.Context, initializerPreflight.Options) error
	AcquireLock    func(context.Context, string, time.Duration) (bool, error)
	ReleaseLock    func(context.Context, string) error
	ExecuteCleanup func(context.Context, string) error
}

// Run performs preflight, acquires the initializer lock, and executes the cleanup script.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(opts.Confirm) != confirmationToken {
		return fmt.Errorf("请传入确认词 --confirm %s", confirmationToken)
	}
	if strings.TrimSpace(opts.CleanupFile) == "" {
		return errors.New("cleanup file 不能为空")
	}

	runPreflight := opts.RunPreflight
	if runPreflight == nil {
		runPreflight = initializerPreflight.Run
	}
	if err := runPreflight(ctx, initializerPreflight.Options{
		BaseURL:       opts.BaseURL,
		AdminUsername: opts.AdminUsername,
		AdminPassword: opts.AdminPassword,
		HTTPClient:    opts.HTTPClient,
		CheckDB:       opts.CheckDB,
		CheckRedis:    opts.CheckRedis,
	}); err != nil {
		return err
	}

	acquireLock := opts.AcquireLock
	if acquireLock == nil {
		return errors.New("acquire lock func 不能为空")
	}
	releaseLock := opts.ReleaseLock
	if releaseLock == nil {
		return errors.New("release lock func 不能为空")
	}
	executeCleanup := opts.ExecuteCleanup
	if executeCleanup == nil {
		return errors.New("execute cleanup func 不能为空")
	}

	ok, err := acquireLock(ctx, defaultLockKey, defaultLockTTL)
	if err != nil {
		return fmt.Errorf("acquire cleanup lock: %w", err)
	}
	if !ok {
		return fmt.Errorf("已有另一个初始化任务正在运行")
	}
	defer func() {
		_ = releaseLock(context.Background(), defaultLockKey)
	}()

	return executeCleanup(ctx, opts.CleanupFile)
}
