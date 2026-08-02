package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go-base-agent/internal/infra/model"
)

// OpenAICompatibleChatClient implements ChatClient for OpenAI-compatible APIs.
// Covers OpenAI, SiliconFlow, Bailian (compatible mode), Ollama, AIHubMix, etc.
// Aligns with Java AbstractOpenAIStyleChatClient.
type OpenAICompatibleChatClient struct {
	provider string
	client   *http.Client
	streamer *StreamExecutor

	// RequiresAPIKey controls whether the Authorization header is set.
	// Defaults to true. Set to false for Ollama.
	RequiresAPIKey bool

	// CustomizeBody allows provider-specific request body modifications.
	CustomizeBody func(body map[string]interface{}, req Request)
}

// NewOpenAICompatibleChatClient creates a new OpenAICompatibleChatClient.
func NewOpenAICompatibleChatClient(provider string, httpClient *http.Client) *OpenAICompatibleChatClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OpenAICompatibleChatClient{
		provider:       provider,
		client:         httpClient,
		streamer:       NewStreamExecutor(httpClient),
		RequiresAPIKey: true,
	}
}

// Provider returns the provider identifier.
func (c *OpenAICompatibleChatClient) Provider() string {
	return c.provider
}

// Chat performs a synchronous chat request.
func (c *OpenAICompatibleChatClient) Chat(ctx context.Context, req Request, target model.Target) (string, error) {
	url, err := model.ResolveURL(target.Provider, target.Candidate, model.CapabilityChat)
	if err != nil {
		return "", fmt.Errorf("resolve URL: %w", err)
	}

	body := c.buildRequestBody(req, target, false)
	httpReq, err := c.newRequest(ctx, url, body, target.Provider.APIKey)
	if err != nil {
		return "", err
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chat HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return extractContent(respBody, c.provider)
}

// StreamChat performs a streaming chat request.
func (c *OpenAICompatibleChatClient) StreamChat(ctx context.Context, req Request, cb StreamCallback, target model.Target) (StreamHandle, error) {
	url, err := model.ResolveURL(target.Provider, target.Candidate, model.CapabilityChat)
	if err != nil {
		return nil, fmt.Errorf("resolve URL: %w", err)
	}

	body := c.buildRequestBody(req, target, true)
	httpReq, err := c.newRequest(ctx, url, body, target.Provider.APIKey)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	reasoningEnabled := req.Thinking != nil && *req.Thinking
	return c.streamer.Execute(ctx, httpReq, cb, reasoningEnabled)
}

func (c *OpenAICompatibleChatClient) buildRequestBody(req Request, target model.Target, stream bool) map[string]interface{} {
	body := map[string]interface{}{
		"model":    target.Candidate.Model,
		"messages": buildMessages(req.Messages),
	}
	if stream {
		body["stream"] = true
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		body["top_k"] = *req.TopK
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}

	if c.CustomizeBody != nil {
		c.CustomizeBody(body, req)
	} else if req.Thinking != nil {
		body["enable_thinking"] = *req.Thinking
	}

	return body
}

func (c *OpenAICompatibleChatClient) newRequest(ctx context.Context, url string, body map[string]interface{}, apiKey string) (*http.Request, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if c.RequiresAPIKey && apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	return httpReq, nil
}

func buildMessages(messages []Message) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(messages))
	for _, m := range messages {
		role := mapRoleToOpenAI(m.Role)
		result = append(result, map[string]interface{}{
			"role":    role,
			"content": m.Content,
		})
	}
	return result
}

func mapRoleToOpenAI(role Role) string {
	switch role {
	case RoleSystem:
		return "system"
	case RoleUser:
		return "user"
	case RoleAssistant:
		return "assistant"
	default:
		return "user"
	}
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
