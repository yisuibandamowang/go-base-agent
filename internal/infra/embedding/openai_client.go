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

func (c *OpenAICompatibleEmbeddingClient) EmbedBatch(ctx context.Context, texts []string, target model.Target) ([][]float32, error) {
	return c.doEmbed(ctx, texts, target)
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

	return parseEmbeddingResponse(respBody, c.provider)
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
