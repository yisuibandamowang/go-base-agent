package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	initializerPreflight "go-base-agent/internal/initializer/preflight"
)

const defaultLockKey = "ragent:initializer:lock"
const defaultLockTTL = time.Hour
const confirmationToken = "RESET-ENTERPRISE-KNOWLEDGE-BASE"

// Options describes the cleanup workflow.
type Options struct {
	BaseURL           string
	AdminUsername     string
	AdminPassword     string
	Confirm           string
	ConfirmationToken string
	CleanupFile       string
	LockTTL           time.Duration
	HTTPClient        *http.Client
	CheckDB           func(context.Context) error
	CheckRedis        func(context.Context) error
	CheckIdle         func(context.Context) error
	ExpectedBackends  map[string]string
	RunPreflight      func(context.Context, initializerPreflight.Options) error
	AcquireLock       func(context.Context, string, time.Duration) (bool, error)
	ReleaseLock       func(context.Context, string) error
	// DeleteDocuments 在执行 SQL 清理前物理删除远端文档及其关联资源。
	DeleteDocuments func(context.Context) error
	// ClearCache 在 SQL 清理后删除初始化相关 Redis 缓存。
	ClearCache     func(context.Context) error
	ExecuteCleanup func(context.Context, string) error
}

// Run performs preflight, acquires the initializer lock, and executes the cleanup script.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	expectedConfirmation := strings.TrimSpace(opts.ConfirmationToken)
	if expectedConfirmation == "" {
		expectedConfirmation = confirmationToken
	}
	if strings.TrimSpace(opts.Confirm) != expectedConfirmation {
		return fmt.Errorf("请传入确认词 --confirm %s", expectedConfirmation)
	}
	if strings.TrimSpace(opts.CleanupFile) == "" {
		return errors.New("cleanup file 不能为空")
	}

	runPreflight := opts.RunPreflight
	if runPreflight == nil {
		runPreflight = initializerPreflight.Run
	}
	if err := runPreflight(ctx, initializerPreflight.Options{
		BaseURL:          opts.BaseURL,
		AdminUsername:    opts.AdminUsername,
		AdminPassword:    opts.AdminPassword,
		HTTPClient:       opts.HTTPClient,
		CheckDB:          opts.CheckDB,
		CheckRedis:       opts.CheckRedis,
		CheckIdle:        opts.CheckIdle,
		ExpectedBackends: opts.ExpectedBackends,
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
	deleteDocuments := opts.DeleteDocuments
	if deleteDocuments == nil {
		deleteDocuments = func(ctx context.Context) error {
			return deleteRemoteDocuments(ctx, opts)
		}
	}

	lockTTL := opts.LockTTL
	if lockTTL <= 0 {
		lockTTL = defaultLockTTL
	}
	ok, err := acquireLock(ctx, defaultLockKey, lockTTL)
	if err != nil {
		return fmt.Errorf("acquire cleanup lock: %w", err)
	}
	if !ok {
		return fmt.Errorf("已有另一个初始化任务正在运行")
	}
	defer func() {
		_ = releaseLock(context.Background(), defaultLockKey)
	}()

	if err := deleteDocuments(ctx); err != nil {
		return fmt.Errorf("delete remote documents: %w", err)
	}
	if err := executeCleanup(ctx, opts.CleanupFile); err != nil {
		return err
	}
	if opts.ClearCache != nil {
		if err := opts.ClearCache(ctx); err != nil {
			return fmt.Errorf("clear initializer cache: %w", err)
		}
	}
	return nil
}

type remoteClient struct {
	baseURL string
	http    *http.Client
	token   string
}

func deleteRemoteDocuments(ctx context.Context, opts Options) error {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return errors.New("base url 不能为空")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	c := &remoteClient{baseURL: baseURL, http: httpClient}
	if err := c.login(ctx, opts.AdminUsername, opts.AdminPassword); err != nil {
		return fmt.Errorf("登录清理客户端失败: %w", err)
	}
	bases, err := c.listPaged(ctx, "/api/ragent/knowledge-base")
	if err != nil {
		return fmt.Errorf("拉取知识库列表失败: %w", err)
	}
	for _, base := range bases {
		kbID := strValue(base["id"])
		if kbID == "" {
			return errors.New("知识库响应缺少 ID")
		}
		documents, err := c.listPaged(ctx, "/api/ragent/knowledge-base/"+url.PathEscape(kbID)+"/docs")
		if err != nil {
			return fmt.Errorf("拉取知识库文档列表失败 %s: %w", kbID, err)
		}
		for _, document := range documents {
			name := strValue(document["docName"])
			if strings.EqualFold(strValue(document["status"]), "running") {
				return fmt.Errorf("文档正在分块，拒绝清理: %s", name)
			}
			docID := strValue(document["id"])
			if docID == "" {
				return fmt.Errorf("文档响应缺少 ID: %s", name)
			}
			if err := c.delete(ctx, "/api/ragent/knowledge-base/docs/"+url.PathEscape(docID)); err != nil {
				return fmt.Errorf("删除文档失败 %s: %w", name, err)
			}
		}
	}
	return nil
}

func (c *remoteClient) login(ctx context.Context, username, password string) error {
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
			Role  string `json:"role"`
		} `json:"data"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, "/api/ragent/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, &resp); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Data.Role), "admin") {
		return fmt.Errorf("初始化账号不是 Admin，role=%s", resp.Data.Role)
	}
	if strings.TrimSpace(resp.Data.Token) == "" {
		return errors.New("登录响应缺少 token")
	}
	c.token = resp.Data.Token
	return nil
}

func (c *remoteClient) listPaged(ctx context.Context, path string) ([]map[string]any, error) {
	all := make([]map[string]any, 0)
	for current := 1; ; current++ {
		var resp struct {
			Data json.RawMessage `json:"data"`
		}
		var pagePath = fmt.Sprintf("%s?current=%d&size=500", path, current)
		if err := c.requestJSON(ctx, http.MethodGet, pagePath, nil, &resp); err != nil {
			return nil, err
		}
		raw := bytes.TrimSpace(resp.Data)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return all, nil
		}
		if raw[0] == '[' {
			var records []map[string]any
			if err := json.Unmarshal(raw, &records); err != nil {
				return nil, fmt.Errorf("解析列表响应失败: %w", err)
			}
			return append(all, records...), nil
		}
		var page struct {
			Records []map[string]any `json:"records"`
			Pages   int              `json:"pages"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("解析分页响应失败: %w", err)
		}
		all = append(all, page.Records...)
		if page.Pages <= current || page.Pages <= 0 {
			return all, nil
		}
	}
}

func (c *remoteClient) delete(ctx context.Context, path string) error {
	return c.requestJSON(ctx, http.MethodDelete, path, nil, nil)
}

func (c *remoteClient) requestJSON(ctx context.Context, method, path string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求失败: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败 %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败 %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("接口返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if dest == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("解析响应失败 %s: %w", path, err)
	}
	return nil
}

func strValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return fmt.Sprintf("%v", value)
}
