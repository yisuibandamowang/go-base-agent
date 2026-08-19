// Package initialize 提供企业知识库一键种子工作流。
// 对齐 Java InitializeMain：preflight → cleanup → 知识库 → 文档 → 意图树 → 示例问题 → verify → warmup。
package initialize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PhaseFunc 单个初始化环节，返回 error 时终止整个工作流。
type PhaseFunc func(ctx context.Context) error

// Options 描述 initialize 工作流的可注入依赖。
type Options struct {
	BaseURL       string
	AdminUsername string
	AdminPassword string
	AgentTypeDir  string
	HTTPClient    *http.Client
	DryRun        bool
	SkipWarmup    bool
	// Cleanup 执行清理环节（复用 cleanup.Run）。
	Cleanup func(ctx context.Context) error
	// Preflight 执行预检环节（复用 preflight.Run）。
	Preflight func(ctx context.Context) error
	// Warmup 按数据集串行提问；nil 且未跳过时报错。
	Warmup func(ctx context.Context, questions []Question) error
}

// Question 数据集中的一条演示问题定义。
type Question struct {
	Ref         string
	Title       string
	Description string
	Text        string
	FollowUps   []string
}

// KnowledgeBase 数据集中的一条知识库定义。
type KnowledgeBase struct {
	Ref            string
	Name           string
	CollectionName string
	EmbeddingModel string
	DocumentsDir   string
}

// Intent 数据集中的一条意图节点定义。
type Intent struct {
	Code            string
	Name            string
	Level           int
	ParentCode      string
	Description     string
	Examples        string
	Kind            int
	SortOrder       int
	Enabled         bool
	TopK            int
	PromptSnippet   string
	PromptTemplate  string
	KnowledgeBaseRef string
}

// Dataset 一次初始化使用的不可变数据集。
type Dataset struct {
	KnowledgeBases []KnowledgeBase
	Intents        []Intent
	Questions      []Question
}

// Phase 单个已命名的初始化环节。
type Phase struct {
	Name string
	Run  PhaseFunc
}

// DefaultPhases 返回按顺序执行的初始化环节。
// 各环节只依赖前序环节产出，缺失依赖时立即报错终止。
func DefaultPhases(opts Options, dataset *Dataset) []Phase {
	phases := []Phase{
		{Name: "preflight", Run: func(ctx context.Context) error {
			if opts.Preflight == nil {
				return errors.New("preflight 依赖未注入")
			}
			return opts.Preflight(ctx)
		}},
		{Name: "cleanup", Run: func(ctx context.Context) error {
			if opts.DryRun {
				return nil
			}
			if opts.Cleanup == nil {
				return errors.New("cleanup 依赖未注入")
			}
			return opts.Cleanup(ctx)
		}},
		{Name: "knowledge-base", Run: func(ctx context.Context) error {
			client, err := newClient(opts)
			if err != nil {
				return err
			}
			return initKnowledgeBases(ctx, client, dataset, opts.DryRun)
		}},
		{Name: "intent-tree", Run: func(ctx context.Context) error {
			client, err := newClient(opts)
			if err != nil {
				return err
			}
			return initIntentTree(ctx, client, dataset, opts.DryRun)
		}},
		{Name: "sample-questions", Run: func(ctx context.Context) error {
			client, err := newClient(opts)
			if err != nil {
				return err
			}
			return initSampleQuestions(ctx, client, dataset, opts.DryRun)
		}},
		{Name: "verify", Run: func(ctx context.Context) error {
			if opts.DryRun {
				return nil
			}
			client, err := newClient(opts)
			if err != nil {
				return err
			}
			return verify(ctx, client, dataset)
		}},
	}
	if !opts.SkipWarmup {
		phases = append(phases, Phase{Name: "warmup", Run: func(ctx context.Context) error {
			if opts.DryRun {
				return nil
			}
			if opts.Warmup == nil {
				return errors.New("warmup 依赖未注入")
			}
			return opts.Warmup(ctx, dataset.Questions)
		}})
	}
	return phases
}

// Run 执行完整的初始化工作流。
func Run(ctx context.Context, opts Options, dataset *Dataset) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if dataset == nil {
		return errors.New("dataset 不能为空")
	}
	if strings.TrimSpace(opts.AgentTypeDir) == "" {
		return errors.New("必须指定智能体类型数据集目录")
	}
	for _, phase := range DefaultPhases(opts, dataset) {
		start := time.Now()
		if err := phase.Run(ctx); err != nil {
			return fmt.Errorf("初始化环节 %s 失败: %w", phase.Name, err)
		}
		fmt.Printf("[%s] 完成，耗时 %s\n", phase.Name, time.Since(start).Round(time.Second))
	}
	fmt.Println("[initializer] SUCCESS")
	return nil
}

// LoadDataset 从智能体类型目录加载数据集定义。
// 目录结构与 Java 侧一致：knowledge-bases.properties / intents/*.properties / questions.properties。
func LoadDataset(agentTypeDir string) (*Dataset, error) {
	dir := strings.TrimSpace(agentTypeDir)
	if dir == "" {
		return nil, errors.New("智能体类型目录不能为空")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("访问智能体类型目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("智能体类型目录不存在: %s", dir)
	}

	kbs, err := loadKnowledgeBases(dir)
	if err != nil {
		return nil, err
	}
	intents, err := loadIntents(dir, kbs)
	if err != nil {
		return nil, err
	}
	questions, err := loadQuestions(dir)
	if err != nil {
		return nil, err
	}
	return &Dataset{KnowledgeBases: kbs, Intents: intents, Questions: questions}, nil
}

func loadKnowledgeBases(dir string) ([]KnowledgeBase, error) {
	props, err := loadPropertiesFile(filepath.Join(dir, "knowledge-bases.properties"))
	if err != nil {
		return nil, err
	}
	refs := strings.Split(strings.TrimSpace(props["knowledge-base.refs"]), ",")
	result := make([]KnowledgeBase, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		docDir := strings.TrimSpace(props["knowledge-base."+ref+".documents"])
		if docDir != "" && !filepath.IsAbs(docDir) {
			docDir = filepath.Join(dir, docDir)
		}
		result = append(result, KnowledgeBase{
			Ref:            ref,
			Name:           strings.TrimSpace(props["knowledge-base."+ref+".name"]),
			CollectionName: strings.TrimSpace(props["knowledge-base."+ref+".collection-name"]),
			EmbeddingModel: strings.TrimSpace(props["knowledge-base."+ref+".embedding-model"]),
			DocumentsDir:   docDir,
		})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("数据集未定义任何知识库: %s", dir)
	}
	return result, nil
}

func loadIntents(dir string, kbs []KnowledgeBase) ([]Intent, error) {
	intentDir := filepath.Join(dir, "intents")
	entries, err := os.ReadDir(intentDir)
	if err != nil {
		return nil, fmt.Errorf("读取意图目录失败: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".properties") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	result := make([]Intent, 0, len(names))
	for _, name := range names {
		props, err := loadPropertiesFile(filepath.Join(intentDir, name))
		if err != nil {
			return nil, err
		}
		intent := Intent{
			Code:             strings.TrimSpace(props["code"]),
			Name:             strings.TrimSpace(props["name"]),
			Level:            atoiDefault(props["level"], 0),
			ParentCode:       strings.TrimSpace(props["parent-code"]),
			Description:      strings.TrimSpace(props["description"]),
			Examples:         strings.TrimSpace(props["examples"]),
			Kind:             atoiDefault(props["kind"], 0),
			SortOrder:        atoiDefault(props["sort-order"], 0),
			Enabled:          parseBoolDefault(props["enabled"], true),
			TopK:             atoiDefault(props["top-k"], 0),
			PromptSnippet:    strings.TrimSpace(props["prompt-snippet"]),
			PromptTemplate:   strings.TrimSpace(props["prompt-template"]),
			KnowledgeBaseRef: strings.TrimSpace(props["knowledge-base-ref"]),
		}
		if intent.Code == "" {
			return nil, fmt.Errorf("意图定义缺少 code: %s", name)
		}
		if intent.KnowledgeBaseRef != "" && !kbRefExists(kbs, intent.KnowledgeBaseRef) {
			return nil, fmt.Errorf("意图 %s 引用了未知知识库 ref: %s", intent.Code, intent.KnowledgeBaseRef)
		}
		result = append(result, intent)
	}
	return result, nil
}

func loadQuestions(dir string) ([]Question, error) {
	props, err := loadPropertiesFile(filepath.Join(dir, "questions.properties"))
	if err != nil {
		return nil, err
	}
	refs := splitCSV(props["question.refs"])
	result := make([]Question, 0, len(refs))
	for _, ref := range refs {
		question := Question{
			Ref:         ref,
			Title:       strings.TrimSpace(props["question."+ref+".title"]),
			Description: strings.TrimSpace(props["question."+ref+".description"]),
			Text:        strings.TrimSpace(props["question."+ref+".text"]),
		}
		for _, followUp := range splitPipe(props["question."+ref+".follow-ups"]) {
			question.FollowUps = append(question.FollowUps, followUp)
		}
		if question.Text == "" {
			return nil, fmt.Errorf("示例问题缺少 text: %s", ref)
		}
		result = append(result, question)
	}
	return result, nil
}

func kbRefExists(kbs []KnowledgeBase, ref string) bool {
	for _, kb := range kbs {
		if kb.Ref == ref {
			return true
		}
	}
	return false
}

func loadPropertiesFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	props := make(map[string]string)
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return props, nil
}

func atoiDefault(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return fallback
	}
	return parsed
}

func parseBoolDefault(value string, fallback bool) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return fallback
	}
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

// splitCSV splits follow-ups which use | as the delimiter, matching the Java dataset format.
func splitPipe(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "|")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

// client 封装对 Ragent 服务的认证 HTTP 调用。
type client struct {
	baseURL string
	http    *http.Client
	token   string
}

func newClient(opts Options) (*client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("base url 不能为空")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	c := &client{baseURL: baseURL, http: httpClient}
	if err := c.login(opts.AdminUsername, opts.AdminPassword); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *client) login(username, password string) error {
	body := map[string]string{"username": username, "password": password}
	var resp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
			Role  string `json:"role"`
		} `json:"data"`
	}
	if err := c.postJSON("/api/ragent/auth/login", body, &resp); err != nil {
		return fmt.Errorf("登录失败: %w", err)
	}
	if strings.TrimSpace(resp.Data.Token) == "" {
		return errors.New("登录响应缺少 token")
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Data.Role), "admin") {
		return fmt.Errorf("初始化账号不是 Admin，role=%s", resp.Data.Role)
	}
	c.token = resp.Data.Token
	return nil
}

func (c *client) getJSON(path string, dest any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.doJSON(req, dest)
}

func (c *client) postJSON(path string, body any, dest any) error {
	rawBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, strings.NewReader(string(rawBody)))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.doJSON(req, dest)
}

func (c *client) doJSON(req *http.Request, dest any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败 %s: %w", req.URL.Path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败 %s: %w", req.URL.Path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("接口返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("解析响应失败 %s: %w", req.URL.Path, err)
	}
	return nil
}

func (c *client) delete(path string) error {
	req, err := http.NewRequest(http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.doJSON(req, nil)
}

func encodePathValue(value string) string {
	return url.PathEscape(value)
}
