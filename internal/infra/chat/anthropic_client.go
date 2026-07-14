package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"go-base-agent/internal/infra/model"
)

const anthropicVersion = "2023-06-01"

type AnthropicChatClient struct {
	provider string
	client   *http.Client
}

func NewAnthropicChatClient(provider string, httpClient *http.Client) *AnthropicChatClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &AnthropicChatClient{provider: provider, client: httpClient}
}

func (c *AnthropicChatClient) Provider() string {
	return c.provider
}

func (c *AnthropicChatClient) Chat(ctx context.Context, req Request, target model.Target) (string, error) {
	url, err := model.ResolveURL(target.Provider, target.Candidate, model.CapabilityChat)
	if err != nil {
		return "", fmt.Errorf("resolve URL: %w", err)
	}

	body := buildAnthropicRequestBody(req, target)
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal anthropic request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create anthropic request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if target.Provider.APIKey != "" {
		httpReq.Header.Set("x-api-key", target.Provider.APIKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic chat request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read anthropic response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic chat HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return extractAnthropicContent(respBody)
}

func (c *AnthropicChatClient) StreamChat(ctx context.Context, req Request, cb StreamCallback, target model.Target) (StreamHandle, error) {
	ctx, cancel := context.WithCancel(ctx)
	handle := &anthropicStreamHandle{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(handle.done)
		content, err := c.Chat(ctx, req, target)
		if err != nil {
			cb.OnError(err)
			return
		}
		if content != "" {
			cb.OnContent(content)
		}
		cb.OnComplete()
	}()
	return handle, nil
}

type anthropicStreamHandle struct {
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

func (h *anthropicStreamHandle) Cancel() {
	h.once.Do(h.cancel)
}

func (h *anthropicStreamHandle) Wait() {
	<-h.done
}

func buildAnthropicRequestBody(req Request, target model.Target) map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(req.Messages))
	var systemParts []string
	for _, msg := range req.Messages {
		if msg.Role == RoleSystem {
			if strings.TrimSpace(msg.Content) != "" {
				systemParts = append(systemParts, msg.Content)
			}
			continue
		}
		role := "user"
		if msg.Role == RoleAssistant {
			role = "assistant"
		}
		messages = append(messages, map[string]interface{}{
			"role":    role,
			"content": msg.Content,
		})
	}
	body := map[string]interface{}{
		"model":      target.Candidate.Model,
		"messages":   messages,
		"max_tokens": 1024,
	}
	if len(systemParts) > 0 {
		body["system"] = strings.Join(systemParts, "\n\n")
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	return body
}

func extractAnthropicContent(body []byte) (string, error) {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("anthropic parse response: %w", err)
	}
	var parts []string
	for _, item := range resp.Content {
		if item.Type == "" || item.Type == "text" {
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("anthropic response has no text content: %s", string(body))
	}
	return strings.TrimSpace(strings.Join(parts, "")), nil
}
