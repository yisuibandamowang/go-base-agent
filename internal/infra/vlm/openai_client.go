package vlm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go-base-agent/internal/infra/model"
)

// OpenAICompatibleClient 实现 OpenAI-compatible VLM 调用。
type OpenAICompatibleClient struct {
	provider       string
	client         *http.Client
	RequiresAPIKey bool
}

// NewOpenAICompatibleClient 创建 VLM 客户端。
func NewOpenAICompatibleClient(provider string, httpClient *http.Client) *OpenAICompatibleClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OpenAICompatibleClient{
		provider:       provider,
		client:         httpClient,
		RequiresAPIKey: true,
	}
}

// Provider 返回 provider 标识。
func (c *OpenAICompatibleClient) Provider() string {
	return c.provider
}

// DescribeImage 发送包含文本和图片的多模态请求。
func (c *OpenAICompatibleClient) DescribeImage(ctx context.Context, image []byte, mimeType, prompt string, target model.Target) (string, error) {
	url, err := model.ResolveURL(target.Provider, target.Candidate, model.CapabilityVLM)
	if err != nil {
		return "", fmt.Errorf("resolve VLM url: %w", err)
	}
	body := map[string]any{
		"model": target.Candidate.Model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": prompt},
					{"type": "image_url", "image_url": map[string]any{"url": toDataURL(image, mimeType)}},
				},
			},
		},
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal vlm body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create vlm request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.RequiresAPIKey && target.Provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+target.Provider.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("vlm request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read vlm response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vlm HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return extractContent(respBody, c.provider)
}

func toDataURL(image []byte, mimeType string) string {
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image)
}

func extractContent(body []byte, provider string) (string, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("%s parse response: %w", provider, err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("%s response has no choices", provider)
	}
	content := resp.Choices[0].Message.Content
	if content == nil {
		return "", fmt.Errorf("%s response has no content: %s", provider, string(body))
	}
	return strings.TrimSpace(*content), nil
}
