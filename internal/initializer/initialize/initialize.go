// Package initialize 提供企业知识库一键种子工作流。
// 对齐 Java InitializeMain：preflight → cleanup → 知识库 → 文档 → 意图树 → 示例问题 → verify → warmup。
package initialize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
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
	// ReplaceExisting 控制初始化时是否替换同名文档；nil 按 Java 默认值 true 处理。
	ReplaceExisting      *bool
	DocumentTimeout      time.Duration
	DocumentPollInterval time.Duration
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
	Code                string
	Name                string
	Level               int
	ParentCode          string
	Description         string
	Examples            string
	Kind                int
	SortOrder           int
	Enabled             bool
	TopK                int
	McpToolID           string
	PromptSnippet       string
	PromptTemplate      string
	ParamPromptTemplate string
	KnowledgeBaseRef    string
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
		{Name: "documents", Run: func(ctx context.Context) error {
			client, err := newClient(opts)
			if err != nil {
				return err
			}
			return initDocumentsWithReplace(ctx, client, dataset, opts.DryRun, opts.DocumentTimeout,
				opts.DocumentPollInterval, boolValueOrDefault(opts.ReplaceExisting, true))
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
	if checksumPath := filepath.Join(dir, "checksums.sha256"); fileExists(checksumPath) {
		if err := verifyChecksums(dir); err != nil {
			return nil, err
		}
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
	dataset := &Dataset{KnowledgeBases: kbs, Intents: intents, Questions: questions}
	if err := validateDataset(dataset); err != nil {
		return nil, err
	}
	return dataset, nil
}

func verifyChecksums(agentTypeDir string) error {
	root, err := filepath.Abs(strings.TrimSpace(agentTypeDir))
	if err != nil {
		return fmt.Errorf("解析 checksum 根目录失败: %w", err)
	}
	checksumPath := filepath.Join(root, "checksums.sha256")
	raw, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("读取 checksum 文件失败: %w", err)
	}
	checked := 0
	for _, rawLine := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		separator := strings.Index(line, "  ")
		if separator <= 0 {
			return fmt.Errorf("非法 checksum 行: %s", rawLine)
		}
		expected := strings.ToLower(strings.TrimSpace(line[:separator]))
		relative := strings.TrimSpace(line[separator+2:])
		if len(expected) != sha256.Size*2 {
			return fmt.Errorf("非法 checksum 值: %s", expected)
		}
		target := filepath.Clean(filepath.Join(root, relative))
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !fileExists(target) {
			return fmt.Errorf("checksum 引用了非法文件: %s", relative)
		}
		actual, err := fileSHA256(target)
		if err != nil {
			return fmt.Errorf("计算文件 checksum 失败 %s: %w", relative, err)
		}
		if actual != expected {
			return fmt.Errorf("文件 checksum 不一致: %s，expected=%s，actual=%s", relative, expected, actual)
		}
		checked++
	}
	if checked == 0 {
		return fmt.Errorf("checksums.sha256 中没有待校验文件")
	}
	return nil
}

// VerifyChecksums 校验智能体类型目录中的 checksums.sha256 文件。
func VerifyChecksums(agentTypeDir string) error {
	return verifyChecksums(agentTypeDir)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func validateDataset(dataset *Dataset) error {
	if dataset == nil {
		return errors.New("dataset 不能为空")
	}
	refs := make(map[string]struct{}, len(dataset.KnowledgeBases))
	collections := make(map[string]struct{}, len(dataset.KnowledgeBases))
	for _, definition := range dataset.KnowledgeBases {
		if strings.TrimSpace(definition.Ref) == "" || strings.TrimSpace(definition.Name) == "" ||
			strings.TrimSpace(definition.CollectionName) == "" || strings.TrimSpace(definition.EmbeddingModel) == "" {
			return fmt.Errorf("知识库定义字段不能为空: %s", definition.Ref)
		}
		if _, exists := refs[definition.Ref]; exists {
			return fmt.Errorf("知识库 ref 重复: %s", definition.Ref)
		}
		refs[definition.Ref] = struct{}{}
		if _, exists := collections[definition.CollectionName]; exists {
			return fmt.Errorf("知识库 collectionName 重复: %s", definition.CollectionName)
		}
		collections[definition.CollectionName] = struct{}{}
		files, err := collectDocumentFiles(definition.DocumentsDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("知识库文档目录不存在: %s", definition.DocumentsDir)
			}
			return fmt.Errorf("读取知识库文档目录失败 %s: %w", definition.Ref, err)
		}
		if len(files) == 0 {
			return fmt.Errorf("知识库没有文档: %s", definition.Ref)
		}
		fileNames := make(map[string]struct{}, len(files))
		for _, filePath := range files {
			name := filepath.Base(filePath)
			if _, exists := fileNames[name]; exists {
				return fmt.Errorf("同一知识库存在重名文档，无法区分: %s/%s", definition.Ref, name)
			}
			fileNames[name] = struct{}{}
		}
	}

	intentCodes := make(map[string]struct{}, len(dataset.Intents))
	intentOrder := make(map[string]int, len(dataset.Intents))
	for _, intent := range dataset.Intents {
		if strings.TrimSpace(intent.Code) == "" || strings.TrimSpace(intent.Name) == "" {
			return fmt.Errorf("意图定义字段不能为空: %s", intent.Code)
		}
		if _, exists := intentCodes[intent.Code]; exists {
			return fmt.Errorf("意图 code 重复: %s", intent.Code)
		}
		intentCodes[intent.Code] = struct{}{}
		intentOrder[intent.Code] = intent.SortOrder
		if intent.KnowledgeBaseRef != "" {
			if _, exists := refs[intent.KnowledgeBaseRef]; !exists {
				return fmt.Errorf("意图 %s 引用了未知知识库 ref: %s", intent.Code, intent.KnowledgeBaseRef)
			}
		}
	}
	for _, intent := range dataset.Intents {
		if strings.TrimSpace(intent.ParentCode) == "" {
			continue
		}
		parentOrder, exists := intentOrder[intent.ParentCode]
		if !exists {
			return fmt.Errorf("意图父节点不存在: %s -> %s", intent.Code, intent.ParentCode)
		}
		if parentOrder >= intent.SortOrder {
			return fmt.Errorf("父节点必须排在子节点之前: %s", intent.Code)
		}
	}

	questionRefs := make(map[string]struct{}, len(dataset.Questions))
	for _, question := range dataset.Questions {
		if _, exists := questionRefs[question.Ref]; exists {
			return fmt.Errorf("演示问题 ref 重复: %s", question.Ref)
		}
		questionRefs[question.Ref] = struct{}{}
	}
	return nil
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
		promptSnippet, err := loadIntentPrompt(dir, props, "prompt-snippet")
		if err != nil {
			return nil, fmt.Errorf("读取意图 %s prompt-snippet 失败: %w", name, err)
		}
		promptTemplate, err := loadIntentPrompt(dir, props, "prompt-template")
		if err != nil {
			return nil, fmt.Errorf("读取意图 %s prompt-template 失败: %w", name, err)
		}
		paramPromptTemplate, err := loadIntentPrompt(dir, props, "param-prompt-template")
		if err != nil {
			return nil, fmt.Errorf("读取意图 %s param-prompt-template 失败: %w", name, err)
		}
		intent := Intent{
			Code:                strings.TrimSpace(props["code"]),
			Name:                strings.TrimSpace(props["name"]),
			Level:               atoiDefault(props["level"], 0),
			ParentCode:          strings.TrimSpace(props["parent-code"]),
			Description:         strings.TrimSpace(props["description"]),
			Examples:            strings.TrimSpace(props["examples"]),
			Kind:                atoiDefault(props["kind"], 0),
			SortOrder:           atoiDefault(props["sort-order"], 0),
			Enabled:             parseBoolDefault(props["enabled"], true),
			TopK:                atoiDefault(props["top-k"], 0),
			McpToolID:           strings.TrimSpace(props["mcp-tool-id"]),
			PromptSnippet:       promptSnippet,
			PromptTemplate:      promptTemplate,
			ParamPromptTemplate: paramPromptTemplate,
			KnowledgeBaseRef:    strings.TrimSpace(props["knowledge-base-ref"]),
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

func loadIntentPrompt(agentTypeDir string, props map[string]string, key string) (string, error) {
	if relative := strings.TrimSpace(props[key+"-file"]); relative != "" {
		root, err := filepath.Abs(agentTypeDir)
		if err != nil {
			return "", err
		}
		target := filepath.Clean(filepath.Join(root, relative))
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !fileExists(target) {
			return "", fmt.Errorf("Prompt 文件不存在或越出智能体类型目录: %s", relative)
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	return strings.TrimSpace(props[key]), nil
}

func loadQuestions(dir string) ([]Question, error) {
	path := filepath.Join(dir, "questions.properties")
	props, err := loadPropertiesFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
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

func boolValueOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
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

func (c *client) postEmpty(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.doJSON(req, nil)
}

func (c *client) uploadDocument(ctx context.Context, kbID, filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("读取文档失败: %w", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("sourceType", "file"); err != nil {
		return "", fmt.Errorf("写入文档来源类型失败: %w", err)
	}
	if err := writer.WriteField("processMode", "chunk"); err != nil {
		return "", fmt.Errorf("写入文档处理模式失败: %w", err)
	}
	if err := writer.WriteField("scheduleEnabled", "0"); err != nil {
		return "", fmt.Errorf("写入文档调度配置失败: %w", err)
	}
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("创建文档文件字段失败: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("写入文档文件字段失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("关闭文档上传表单失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/ragent/knowledge-base/"+url.PathEscape(kbID)+"/docs/upload", &body)
	if err != nil {
		return "", fmt.Errorf("创建文档上传请求失败: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.doJSON(req, &resp); err != nil {
		return "", fmt.Errorf("上传文档失败: %w", err)
	}
	if strings.TrimSpace(resp.Data.ID) == "" {
		return "", fmt.Errorf("上传文档响应缺少文档 ID: %s", filepath.Base(filePath))
	}
	return resp.Data.ID, nil
}

func encodePathValue(value string) string {
	return url.PathEscape(value)
}
