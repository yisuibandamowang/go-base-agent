package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Options describes the runtime checks executed by the preflight command.
type Options struct {
	BaseURL          string
	AdminUsername    string
	AdminPassword    string
	HTTPClient       *http.Client
	CheckDB          func(context.Context) error
	CheckRedis       func(context.Context) error
	CheckIdle        func(context.Context) error
	ExpectedBackends map[string]string
}

type apiResult[T any] struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type loginData struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
	Token  string `json:"token"`
	Avatar string `json:"avatar"`
}

type currentUserData struct {
	UserID   string `json:"userId"`
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Avatar   string `json:"avatar"`
}

// Run executes the preflight checks against the running service.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	if err := runCheck(ctx, "数据库", opts.CheckDB); err != nil {
		return err
	}
	if err := runCheck(ctx, "Redis", opts.CheckRedis); err != nil {
		return err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return errors.New("base url 不能为空")
	}

	for _, path := range []string{"/health", "/readyz", "/api/ragent/health"} {
		if err := checkOK(ctx, client, joinURL(baseURL, path)); err != nil {
			return err
		}
	}

	login, err := login(ctx, client, joinURL(baseURL, "/api/ragent/auth/login"), opts.AdminUsername, opts.AdminPassword)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(login.Role), "admin") {
		return fmt.Errorf("当前登录账号不是 admin，role=%s", login.Role)
	}
	if strings.TrimSpace(login.Token) == "" {
		return errors.New("登录响应缺少 token")
	}

	if err := checkCurrentUser(ctx, client, joinURL(baseURL, "/api/ragent/auth/current-user"), login.Token); err != nil {
		return err
	}
	settingsURL := joinURL(baseURL, "/api/ragent/rag/settings")
	if len(opts.ExpectedBackends) == 0 {
		if err := checkOK(ctx, client, settingsURL); err != nil {
			return err
		}
	} else if err := checkBackends(ctx, client, settingsURL, opts.ExpectedBackends); err != nil {
		return err
	}
	if err := runCheck(ctx, "活动任务", opts.CheckIdle); err != nil {
		return err
	}
	return nil
}

func checkBackends(ctx context.Context, client *http.Client, rawURL string, expected map[string]string) error {
	var payload apiResult[map[string]any]
	if err := getJSON(ctx, client, rawURL, "", &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Code) != "" && payload.Code != "0" {
		if strings.TrimSpace(payload.Message) == "" {
			return fmt.Errorf("预检接口失败: %s", rawURL)
		}
		return fmt.Errorf("预检接口失败: %s", payload.Message)
	}
	backends, _ := payload.Data["backends"].(map[string]any)
	for name, want := range expected {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		backend, _ := backends[name].(map[string]any)
		got := strings.TrimSpace(fmt.Sprint(backend["type"]))
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("RAG %s 后端类型不一致: expected=%s, actual=%s", name, want, got)
		}
	}
	return nil
}

func runCheck(ctx context.Context, name string, fn func(context.Context) error) error {
	if fn == nil {
		return nil
	}
	if err := fn(ctx); err != nil {
		return fmt.Errorf("预检%s失败: %w", name, err)
	}
	return nil
}

func checkOK(ctx context.Context, client *http.Client, rawURL string) error {
	var payload apiResult[json.RawMessage]
	if err := getJSON(ctx, client, rawURL, "", &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Code) != "" && payload.Code != "0" {
		if strings.TrimSpace(payload.Message) == "" {
			return fmt.Errorf("预检接口失败: %s", rawURL)
		}
		return fmt.Errorf("预检接口失败: %s", payload.Message)
	}
	return nil
}

func login(ctx context.Context, client *http.Client, rawURL, username, password string) (*loginData, error) {
	resp := apiResult[loginData]{}
	if err := postJSON(ctx, client, rawURL, map[string]string{
		"username": username,
		"password": password,
	}, &resp); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.Code) != "" && resp.Code != "0" {
		if strings.TrimSpace(resp.Message) == "" {
			return nil, fmt.Errorf("登录失败: %s", rawURL)
		}
		return nil, fmt.Errorf("登录失败: %s", resp.Message)
	}
	return &resp.Data, nil
}

func checkCurrentUser(ctx context.Context, client *http.Client, rawURL, token string) error {
	var resp apiResult[currentUserData]
	if err := getJSON(ctx, client, rawURL, token, &resp); err != nil {
		return err
	}
	if strings.TrimSpace(resp.Code) != "" && resp.Code != "0" {
		if strings.TrimSpace(resp.Message) == "" {
			return fmt.Errorf("校验当前用户失败: %s", rawURL)
		}
		return fmt.Errorf("校验当前用户失败: %s", resp.Message)
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Data.Role), "admin") {
		return fmt.Errorf("当前用户不是 admin，role=%s", resp.Data.Role)
	}
	return nil
}

func getJSON(ctx context.Context, client *http.Client, rawURL, token string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doJSON(client, req, dest)
}

func postJSON(ctx context.Context, client *http.Client, rawURL string, body any, dest any) error {
	rawBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON(client, req, dest)
}

func doJSON(client *http.Client, req *http.Request, dest any) error {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败 %s: %w", req.URL.Path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败 %s: %w", req.URL.Path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("预检接口返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("解析响应失败 %s: %w", req.URL.Path, err)
	}
	return nil
}

func joinURL(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}
