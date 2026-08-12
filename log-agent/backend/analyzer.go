package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	qihoo360DefaultBaseURL  = "https://api.360.cn/v1"
	bailianDefaultBaseURL   = "https://dashscope.aliyuncs.com"
	bailianChatEndpoint     = "/compatible-mode/v1/chat/completions"
	bailianDefaultModel     = "qwen3-max"
	openAIChatEndpoint      = "/chat/completions"
	analyzerProviderQihoo   = "360"
	analyzerProviderBailian = "bailian"
)

var qihoo360ModelFallbacks = []string{
	"codex-ccmax/gpt-5.5",
	"deepseek/deepseek-v4-flash-internal",
	"deepseek/deepseek-v4-pro",
	"deepseek/deepseek-v4-flash",
}

type AnalysisInput struct {
	Question     string
	LogText      string
	CodeEvidence []CodeEvidence
	DBText       string
}

type AnalysisResult struct {
	Content      string         `json:"content"`
	CodeEvidence []CodeEvidence `json:"code_evidence,omitempty"`
	Error        string         `json:"error,omitempty"`
}

type Analyzer interface {
	Analyze(ctx context.Context, input AnalysisInput) (*AnalysisResult, error)
}

type StreamingAnalyzer interface {
	AnalyzeStream(ctx context.Context, input AnalysisInput, onDelta func(string)) (*AnalysisResult, error)
}

type ZhinuoAnalyzer struct {
	conf   AnalyzerConfig
	client *http.Client
}

func NewZhinuoAnalyzer(conf AnalyzerConfig, client *http.Client) *ZhinuoAnalyzer {
	conf.Model = firstAnalyzerModel()
	if strings.TrimSpace(conf.BaseURL) == "" {
		conf.BaseURL = qihoo360DefaultBaseURL
	}
	if strings.TrimSpace(conf.BailianBaseURL) == "" {
		conf.BailianBaseURL = bailianDefaultBaseURL
	}
	if strings.TrimSpace(conf.BailianModel) == "" {
		conf.BailianModel = bailianDefaultModel
	}
	if conf.Timeout <= 0 {
		conf.Timeout = 30 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: conf.Timeout}
	}
	return &ZhinuoAnalyzer{conf: conf, client: client}
}

func (a *ZhinuoAnalyzer) Analyze(ctx context.Context, input AnalysisInput) (*AnalysisResult, error) {
	return a.analyze(ctx, input, false, nil)
}

func (a *ZhinuoAnalyzer) AnalyzeStream(ctx context.Context, input AnalysisInput, onDelta func(string)) (*AnalysisResult, error) {
	return a.analyze(ctx, input, true, onDelta)
}

func (a *ZhinuoAnalyzer) analyze(ctx context.Context, input AnalysisInput, stream bool, onDelta func(string)) (*AnalysisResult, error) {
	if a == nil || !a.conf.Enable {
		return &AnalysisResult{Content: "智能分析未启用。"}, nil
	}
	routes := a.routes()
	if len(routes) == 0 {
		return &AnalysisResult{Error: "未配置模型 API Key，请设置 QIHOO360_API_KEY；如需阿里云百炼兜底，请设置 BAILIAN_API_KEY。"}, nil
	}
	var failures []string
	for _, route := range routes {
		slog.Info("analyzer route started", "provider", route.provider, "model", route.model, "stream", stream)
		result, err := a.analyzeWithRoute(ctx, input, stream, onDelta, route)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s/%s: %v", route.provider, route.model, err))
			slog.Error("analyzer route failed", "provider", route.provider, "model", route.model, "err", err)
			continue
		}
		slog.Info("analyzer route completed", "provider", route.provider, "model", route.model)
		return result, nil
	}
	return nil, fmt.Errorf("failed to call all analyzer routes: %s", strings.Join(failures, " | "))
}

func (a *ZhinuoAnalyzer) analyzeWithRoute(ctx context.Context, input AnalysisInput, stream bool, onDelta func(string), route analyzerRoute) (*AnalysisResult, error) {
	payload := map[string]interface{}{
		"model": route.model,
		"messages": []map[string]string{
			{"role": "system", "content": "你是会员微服务线上故障排查助手。必须基于日志证据和代码线索分析，不要编造不存在的调用链、表名或接口。"},
			{"role": "user", "content": buildAnalysisPrompt(input)},
		},
		"temperature": 0.2,
	}
	if stream {
		payload["stream"] = true
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(route.baseURL, route.endpoint), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create analyzer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+route.apiKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call analyzer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, fmt.Errorf("failed to read analyzer response: %w", err)
		}
		return nil, fmt.Errorf("failed to call analyzer: http status %d: %s", resp.StatusCode, compactText(string(respBody), 1200))
	}
	if stream {
		return decodeAnalysisStream(resp.Body, input.CodeEvidence, onDelta)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read analyzer response: %w", err)
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("failed to decode analyzer response: %w", err)
	}
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return &AnalysisResult{Error: "360 智脑未返回分析内容。"}, nil
	}
	return &AnalysisResult{Content: decoded.Choices[0].Message.Content, CodeEvidence: input.CodeEvidence}, nil
}

type analyzerRoute struct {
	provider string
	baseURL  string
	endpoint string
	apiKey   string
	model    string
}

func (a *ZhinuoAnalyzer) routes() []analyzerRoute {
	routes := make([]analyzerRoute, 0, len(qihoo360ModelFallbacks)+1)
	if strings.TrimSpace(a.conf.APIKey) != "" {
		baseURL := strings.TrimRight(strings.TrimSpace(a.conf.BaseURL), "/")
		if baseURL == "" {
			baseURL = qihoo360DefaultBaseURL
		}
		for _, model := range qihoo360ModelFallbacks {
			routes = append(routes, analyzerRoute{
				provider: analyzerProviderQihoo,
				baseURL:  baseURL,
				endpoint: openAIChatEndpoint,
				apiKey:   strings.TrimSpace(a.conf.APIKey),
				model:    model,
			})
		}
	}
	if strings.TrimSpace(a.conf.BailianAPIKey) != "" {
		baseURL := strings.TrimRight(strings.TrimSpace(a.conf.BailianBaseURL), "/")
		if baseURL == "" {
			baseURL = bailianDefaultBaseURL
		}
		model := strings.TrimSpace(a.conf.BailianModel)
		if model == "" {
			model = bailianDefaultModel
		}
		routes = append(routes, analyzerRoute{
			provider: analyzerProviderBailian,
			baseURL:  baseURL,
			endpoint: bailianChatEndpoint,
			apiKey:   strings.TrimSpace(a.conf.BailianAPIKey),
			model:    model,
		})
	}
	return routes
}

func firstAnalyzerModel() string {
	if len(qihoo360ModelFallbacks) == 0 {
		return ""
	}
	return qihoo360ModelFallbacks[0]
}

func joinURL(baseURL, endpoint string) string {
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
}

func decodeAnalysisStream(body io.Reader, codeEvidence []CodeEvidence, onDelta func(string)) (*AnalysisResult, error) {
	var content strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("failed to decode analyzer stream chunk: %w", err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			content.WriteString(choice.Delta.Content)
			if onDelta != nil {
				onDelta(choice.Delta.Content)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read analyzer stream: %w", err)
	}
	if strings.TrimSpace(content.String()) == "" {
		return &AnalysisResult{Error: "360 智脑未返回分析内容。", CodeEvidence: codeEvidence}, nil
	}
	return &AnalysisResult{Content: content.String(), CodeEvidence: codeEvidence}, nil
}

func buildAnalysisPrompt(input AnalysisInput) string {
	var b strings.Builder
	b.WriteString("请结合用户问题、日志证据和代码链路进行故障定位。必须先使用“确定性日志解析”中的结构化日志事实，再结合代码解释原因；若已经给出直接结论，必须优先采用该结论，不要退回到模糊判断。\n")
	b.WriteString("输出格式：\n")
	b.WriteString("1. 初步结论：说明最可能的问题点。\n")
	b.WriteString("2. 日志证据：引用关键日志字段或错误信息。\n")
	b.WriteString("3. 代码链路：结合提供的代码位置说明可能经过的函数、接口或模块。\n")
	b.WriteString("4. 下一步排查：给出 2-4 个可执行动作。\n")
	b.WriteString("若证据不足，明确说明缺少哪些日志、时间范围、服务或代码线索。\n\n")
	if findings := deterministicLogFindings(input.LogText); len(findings) > 0 {
		b.WriteString("确定性日志解析：\n")
		for _, finding := range findings {
			b.WriteString("- ")
			b.WriteString(finding)
			b.WriteByte('\n')
		}
		b.WriteString("\n")
	}
	b.WriteString("用户问题：\n")
	b.WriteString(emptyFallback(input.Question, "用户未填写具体问题，请根据日志做通用故障分析。"))
	b.WriteString("\n\n日志证据：\n")
	b.WriteString(compactText(input.LogText, 12000))
	if strings.TrimSpace(input.DBText) != "" {
		b.WriteString("\n\n数据库查询结果：\n")
		b.WriteString(compactText(input.DBText, 6000))
	}
	b.WriteString("\n\n代码链路线索：\n")
	if len(input.CodeEvidence) == 0 {
		b.WriteString("未检索到代码线索。\n")
		return b.String()
	}
	for _, item := range input.CodeEvidence {
		b.WriteString("- ")
		b.WriteString(item.File)
		if item.Line != "" {
			b.WriteString(":")
			b.WriteString(item.Line)
		}
		b.WriteString(" ")
		b.WriteString(compactText(item.Content, 500))
		b.WriteByte('\n')
	}
	return b.String()
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
