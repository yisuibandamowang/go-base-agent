package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go-base-agent/internal/infra/model"
)

// OpenAICompatibleEmbeddingClient implements Client for OpenAI-compatible embedding APIs.
// Covers OpenAI, SiliconFlow, Ollama, AIHubMix, etc.
// Aligns with Java AbstractOpenAIStyleEmbeddingClient.
type OpenAICompatibleEmbeddingClient struct {
	provider       string
	client         *http.Client
	RequiresAPIKey bool
	// MaxBatchSize 单次请求最大批量大小，0 表示不限制。
	// 百炼 compatible-mode 上限 10、SiliconFlow/AIHubMix 上限 32：
	// 超限不是慢而是整批 400，摄取长文档必然踩到。
	MaxBatchSize int
}

// NewOpenAICompatibleEmbeddingClient creates a new embedding client.
func NewOpenAICompatibleEmbeddingClient(provider string, httpClient *http.Client) *OpenAICompatibleEmbeddingClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OpenAICompatibleEmbeddingClient{
		provider:       provider,
		client:         httpClient,
		RequiresAPIKey: true,
		MaxBatchSize:   defaultEmbeddingMaxBatchSize(provider),
	}
}

// defaultEmbeddingMaxBatchSize 对齐 Java 各客户端覆写的批量上限。
func defaultEmbeddingMaxBatchSize(provider string) int {
	switch provider {
	case "bailian":
		return 10
	case "siliconflow", "aihubmix":
		return 32
	default:
		return 0
	}
}

func (c *OpenAICompatibleEmbeddingClient) Provider() string {
	return c.provider
}

func (c *OpenAICompatibleEmbeddingClient) Embed(ctx context.Context, text string, target model.Target) ([]float32, error) {
	results, err := c.doEmbed(ctx, []string{text}, target)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("%s embedding returned no results", c.provider)
	}
	return results[0], nil
}

// EmbedBatch 批量向量化；超过单次请求批量上限时自动分片，按原顺序回填结果。
func (c *OpenAICompatibleEmbeddingClient) EmbedBatch(ctx context.Context, texts []string, target model.Target) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	batch := c.MaxBatchSize
	if batch <= 0 || len(texts) <= batch {
		return c.doEmbed(ctx, texts, target)
	}
	results := make([][]float32, len(texts))
	for i := 0; i < len(texts); i += batch {
		end := i + batch
		if end > len(texts) {
			end = len(texts)
		}
		part, err := c.doEmbed(ctx, texts[i:end], target)
		if err != nil {
			return nil, err
		}
		copy(results[i:end], part)
	}
	return results, nil
}

func (c *OpenAICompatibleEmbeddingClient) doEmbed(ctx context.Context, texts []string, target model.Target) ([][]float32, error) {
	url, err := model.ResolveURL(target.Provider, target.Candidate, model.CapabilityEmbedding)
	if err != nil {
		return nil, fmt.Errorf("resolve URL: %w", err)
	}

	body := map[string]interface{}{
		"model": target.Candidate.Model,
		"input": texts,
	}
	// dimensions 对所有 OpenAI 兼容提供商发送：Matryoshka 系模型（如 text-embedding-v4）
	// 依赖该参数指定输出维度，缺省会输出默认维度，与向量库物理空间不符。
	if target.Candidate.Dimension > 0 {
		body["dimensions"] = target.Candidate.Dimension
	}
	if c.provider != "ollama" {
		body["encoding_format"] = "float"
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if c.RequiresAPIKey && target.Provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+target.Provider.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	results, err := parseEmbeddingResponse(respBody, c.provider)
	if err != nil {
		return nil, err
	}
	if target.Candidate.Dimension > 0 {
		for i, result := range results {
			if len(result) != target.Candidate.Dimension {
				return nil, fmt.Errorf("%s embedding result %d dimension mismatch: expected %d, got %d",
					c.provider, i, target.Candidate.Dimension, len(result))
			}
		}
	}
	return results, nil
}

func parseEmbeddingResponse(body []byte, provider string) ([][]float32, error) {
	var resp struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%s parse embedding response: %w", provider, err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("%s embedding response has no data", provider)
	}

	results := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("%s embedding result %d is empty", provider, i)
		}
		results[i] = d.Embedding
	}
	return results, nil
}
