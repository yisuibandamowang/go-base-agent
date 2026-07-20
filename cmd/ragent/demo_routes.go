package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go-base-agent/internal/framework/config"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/infra/vlm"

	"github.com/gin-gonic/gin"
)

const (
	demoUploadMaxFileSize    int64 = 50 << 20
	demoUploadMaxRequestSize int64 = 100 << 20
	demoImageDataURI               = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aXv0AAAAASUVORK5CYII="
)

type demoHandler struct {
	vlmService  vlm.Service
	imageClient *http.Client
}

func newDemoHandler() *demoHandler {
	return &demoHandler{imageClient: &http.Client{Timeout: 10 * time.Second}}
}

func newDemoHandlerWithVLM(vlmService vlm.Service) *demoHandler {
	h := newDemoHandler()
	h.vlmService = vlmService
	return h
}

func registerDemoRoutes(root *gin.Engine, api *gin.RouterGroup, h *demoHandler) {
	registerDemoGroup := func(group *gin.RouterGroup) {
		test := group.Group("/langchain4j")
		{
			test.GET("/hello", h.Hello)
			test.GET("/simple-stream-chat", h.SimpleStreamChat)
			test.POST("/image-generation", h.ImageGeneration)
			test.POST("/image-analysis", h.ImageAnalysis)
		}
	}

	registerDemoGroup(root.Group("/test"))
	registerDemoGroup(api.Group("/test"))
}

func (h *demoHandler) Hello(c *gin.Context) {
	c.JSON(http.StatusOK, convention.Success(map[string]any{
		"message": "hello from Go",
	}))
}

func (h *demoHandler) SimpleStreamChat(c *gin.Context) {
	question := strings.TrimSpace(c.Query("question"))
	if question == "" {
		question = "hello"
	}
	chunks := []string{
		"demo stream: ",
		question,
		" -> ok",
	}
	index := 0
	c.Stream(func(w io.Writer) bool {
		if index < len(chunks) {
			c.SSEvent("message", chunks[index])
			index++
			return true
		}
		if index == len(chunks) {
			c.SSEvent("finish", "")
			c.SSEvent("done", "[DONE]")
			index++
		}
		return false
	})
}

func (h *demoHandler) ImageGeneration(c *gin.Context) {
	prompt := strings.TrimSpace(c.Query("prompt"))
	if prompt == "" {
		prompt = strings.TrimSpace(c.Query("question"))
	}
	if prompt == "" {
		var req struct {
			Prompt string `json:"prompt"`
		}
		_ = c.ShouldBindJSON(&req)
		prompt = strings.TrimSpace(req.Prompt)
	}
	if prompt == "" {
		prompt = "demo image"
	}
	c.JSON(http.StatusOK, convention.Success(map[string]any{
		"mode":      "demo",
		"prompt":    prompt,
		"mimeType":  "image/png",
		"imageUrl":  demoImageDataURI,
		"width":     1,
		"height":    1,
		"generated": true,
	}))
}

func (h *demoHandler) ImageAnalysis(c *gin.Context) {
	var req struct {
		ImageURL string `json:"imageUrl"`
		Prompt   string `json:"prompt"`
	}
	_ = c.ShouldBindJSON(&req)
	imageURL := strings.TrimSpace(firstDemoNonEmpty(req.ImageURL, c.Query("imageUrl")))
	prompt := strings.TrimSpace(firstDemoNonEmpty(req.Prompt, c.Query("prompt")))
	if prompt == "" {
		prompt = "请描述这张图片"
	}
	if imageURL == "" {
		imageURL = demoImageDataURI
	}
	if h.vlmService != nil {
		image, mimeType, err := h.loadImage(c.Request.Context(), imageURL)
		if err != nil {
			c.JSON(http.StatusOK, convention.Failure("A000001", "读取图片失败: "+err.Error()))
			return
		}
		analysis, err := h.vlmService.DescribeImage(c.Request.Context(), image, mimeType, prompt)
		if err != nil {
			c.JSON(http.StatusOK, convention.Failure("B000001", "图片分析失败: "+err.Error()))
			return
		}
		c.JSON(http.StatusOK, convention.Success(map[string]any{
			"mode":      "vlm",
			"imageUrl":  imageURL,
			"prompt":    prompt,
			"analysis":  analysis,
			"generated": false,
		}))
		return
	}
	c.JSON(http.StatusOK, convention.Success(map[string]any{
		"mode":      "demo",
		"imageUrl":  imageURL,
		"prompt":    prompt,
		"analysis":  fmt.Sprintf("demo 分析：已收到图片地址 %s，当前未接入真实 VLM，返回占位分析结果。", shortenForDemo(imageURL, 80)),
		"generated": false,
	}))
}

func (h *demoHandler) loadImage(ctx context.Context, imageURL string) ([]byte, string, error) {
	if strings.HasPrefix(imageURL, "data:") {
		return parseDemoDataURL(imageURL)
	}
	if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
		return nil, "", fmt.Errorf("仅支持 data URL 或 http/https 图片地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("创建图片请求失败: %w", err)
	}
	client := h.imageClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("下载图片失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("图片地址返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, demoUploadMaxFileSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("读取图片内容失败: %w", err)
	}
	if int64(len(data)) > demoUploadMaxFileSize {
		return nil, "", fmt.Errorf("图片超过大小限制")
	}
	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, nil
}

func parseDemoDataURL(value string) ([]byte, string, error) {
	header, payload, ok := strings.Cut(value, ",")
	if !ok {
		return nil, "", fmt.Errorf("data URL 缺少内容")
	}
	mimeType := "image/png"
	if strings.HasPrefix(header, "data:") {
		meta := strings.TrimPrefix(header, "data:")
		if idx := strings.Index(meta, ";"); idx >= 0 {
			if meta[:idx] != "" {
				mimeType = meta[:idx]
			}
		} else if meta != "" {
			mimeType = meta
		}
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", fmt.Errorf("解析 base64 图片失败: %w", err)
	}
	if int64(len(data)) > demoUploadMaxFileSize {
		return nil, "", fmt.Errorf("图片超过大小限制")
	}
	return data, mimeType, nil
}

func firstDemoNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ragSettingsPayload(cfg *config.Config) map[string]any {
	return map[string]any{
		"upload": map[string]any{
			"maxFileSize":    demoUploadMaxFileSize,
			"maxRequestSize": demoUploadMaxRequestSize,
			"allowedTypes":   []string{".pdf", ".docx", ".md", ".txt", ".html", ".csv"},
		},
		"rag": map[string]any{
			"vector": map[string]any{
				"type": cfg.RAG.Vector.Type,
			},
			"default": map[string]any{
				"collectionName": cfg.RAG.Default.CollectionName,
				"dimension":      cfg.RAG.Default.Dimension,
				"metricType":     cfg.RAG.Default.MetricType,
			},
			"queryRewrite": map[string]any{
				"enabled":            cfg.RAG.QueryRewrite.Enabled,
				"maxHistoryMessages": cfg.RAG.QueryRewrite.MaxHistoryMessages,
				"maxHistoryChars":    cfg.RAG.QueryRewrite.MaxHistoryChars,
			},
			"deepThinkingEnabled": hasDeepThinkingSupport(cfg),
			"rateLimit": map[string]any{
				"global": map[string]any{
					"enabled":        cfg.RAG.RateLimit.Global.Enabled,
					"maxConcurrent":  cfg.RAG.RateLimit.Global.MaxConcurrent,
					"maxWaitSeconds": cfg.RAG.RateLimit.Global.MaxWaitSeconds,
					"leaseSeconds":   cfg.RAG.RateLimit.Global.LeaseSeconds,
					"pollIntervalMs": cfg.RAG.RateLimit.Global.PollIntervalMs,
				},
			},
			"memory": map[string]any{
				"historyKeepTurns":  cfg.RAG.Memory.HistoryKeepTurns,
				"summaryEnabled":    cfg.RAG.Memory.SummaryEnabled,
				"summaryStartTurns": cfg.RAG.Memory.SummaryStartTurns,
				"summaryMaxChars":   cfg.RAG.Memory.SummaryMaxChars,
				"titleMaxLength":    cfg.RAG.Memory.TitleMaxLength,
				"ttlMinutes":        cfg.RAG.Memory.TTLMinutes,
			},
		},
		"ai": map[string]any{
			"providers": buildProviderSettings(cfg.AI.Providers),
			"selection": buildSelectionSettings(cfg.AI.Selection),
			"stream":    map[string]any{"messageChunkSize": cfg.AI.Stream.MessageChunkSize},
			"chat":      buildChatSettings(cfg.AI.Chat),
			"embedding": buildEmbeddingSettings(cfg.AI.Embedding),
			"rerank":    buildRerankSettings(cfg.AI.Rerank),
			"vlm":       buildVlmSettings(cfg.AI.VLM),
		},
	}
}

func buildProviderSettings(providers config.AIProvidersConfig) map[string]any {
	result := make(map[string]any, len(providers))
	for name, provider := range providers {
		result[name] = map[string]any{
			"url":       provider.URL,
			"apiKey":    maskAPIKey(provider.APIKey),
			"protocol":  provider.Protocol,
			"endpoints": provider.Endpoints,
		}
	}
	return result
}

func buildSelectionSettings(cfg config.AISelectionConfig) map[string]any {
	return map[string]any{
		"failureThreshold":          cfg.FailureThreshold,
		"openDurationMs":            cfg.OpenDurationMs,
		"firstPacketTimeoutSeconds": cfg.FirstPacketTimeoutSeconds,
	}
}

func buildChatSettings(cfg config.AIChatConfig) map[string]any {
	return map[string]any{
		"defaultModel":      cfg.DefaultModel,
		"deepThinkingModel": cfg.DeepThinkingModel,
		"candidates":        buildChatCandidates(cfg.Candidates),
	}
}

func buildEmbeddingSettings(cfg config.AIEmbeddingConfig) map[string]any {
	return map[string]any{
		"defaultModel": cfg.DefaultModel,
		"candidates":   buildEmbeddingCandidates(cfg.Candidates),
	}
}

func buildRerankSettings(cfg config.AIRerankConfig) map[string]any {
	return map[string]any{
		"defaultModel": cfg.DefaultModel,
		"candidates":   buildRerankCandidates(cfg.Candidates),
	}
}

func buildVlmSettings(cfg config.AIVLMConfig) map[string]any {
	return map[string]any{
		"defaultModel": cfg.DefaultModel,
		"candidates":   buildVlmCandidates(cfg.Candidates),
	}
}

func buildChatCandidates(candidates []config.AICandidateConfig) []map[string]any {
	result := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, map[string]any{
			"id":               c.ID,
			"provider":         c.Provider,
			"model":            c.Model,
			"url":              c.URL,
			"dimension":        c.Dimension,
			"priority":         c.Priority,
			"enabled":          c.IsEnabled(),
			"supportsThinking": c.SupportsThinking,
		})
	}
	return result
}

func buildEmbeddingCandidates(candidates []config.AIEmbeddingCandidateConfig) []map[string]any {
	result := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, map[string]any{
			"id":        c.ID,
			"provider":  c.Provider,
			"model":     c.Model,
			"url":       c.URL,
			"dimension": c.Dimension,
			"priority":  c.Priority,
			"enabled":   c.IsEnabled(),
		})
	}
	return result
}

func buildRerankCandidates(candidates []config.AIRerankCandidateConfig) []map[string]any {
	result := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, map[string]any{
			"id":       c.ID,
			"provider": c.Provider,
			"model":    c.Model,
			"url":      c.URL,
			"priority": c.Priority,
			"enabled":  c.IsEnabled(),
		})
	}
	return result
}

func buildVlmCandidates(candidates []config.AIVLMCandidateConfig) []map[string]any {
	result := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, map[string]any{
			"id":       c.ID,
			"provider": c.Provider,
			"model":    c.Model,
			"url":      c.URL,
			"priority": c.Priority,
			"enabled":  c.IsEnabled(),
		})
	}
	return result
}

func hasDeepThinkingSupport(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if strings.TrimSpace(cfg.AI.Chat.DeepThinkingModel) != "" {
		return true
	}
	for _, candidate := range cfg.AI.Chat.Candidates {
		if candidate.IsEnabled() && candidate.SupportsThinking {
			return true
		}
	}
	return false
}

func maskAPIKey(apiKey string) string {
	trimmed := strings.TrimSpace(apiKey)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 10 {
		return "******"
	}
	return trimmed[:6] + "***" + trimmed[len(trimmed)-4:]
}

func shortenForDemo(value string, maxLen int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "..."
}
