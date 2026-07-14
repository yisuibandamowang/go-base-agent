package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go-base-agent/internal/infra/model"
)

// HTTPClient implements Client for HTTP rerank APIs.
// It supports DashScope/Bailian-style rerank responses and common root results responses.
type HTTPClient struct {
	provider string
	client   *http.Client
}

// NewHTTPClient creates a new HTTP rerank client.
func NewHTTPClient(provider string, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HTTPClient{provider: provider, client: httpClient}
}

func (c *HTTPClient) Provider() string {
	return c.provider
}

func (c *HTTPClient) Rerank(ctx context.Context, query string, candidates []Chunk, topN int, target model.Target) ([]Chunk, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	if topN <= 0 || topN > len(candidates) {
		topN = len(candidates)
	}
	url, err := model.ResolveURL(target.Provider, target.Candidate, model.CapabilityRerank)
	if err != nil {
		return nil, fmt.Errorf("resolve URL: %w", err)
	}

	body := buildRerankRequestBody(query, candidates, topN, target.Candidate.Model)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if target.Provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+target.Provider.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("rerank HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return parseRerankResponse(respBody, candidates, topN, c.provider)
}

func buildRerankRequestBody(query string, candidates []Chunk, topN int, modelName string) map[string]any {
	documents := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		documents = append(documents, candidate.Text)
	}
	return map[string]any{
		"model": modelName,
		"input": map[string]any{
			"query":     query,
			"documents": documents,
		},
		"parameters": map[string]any{
			"top_n":            topN,
			"return_documents": false,
		},
	}
}

func parseRerankResponse(body []byte, candidates []Chunk, topN int, provider string) ([]Chunk, error) {
	var resp struct {
		Output struct {
			Results []rerankResult `json:"results"`
		} `json:"output"`
		Results []rerankResult `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%s parse rerank response: %w", provider, err)
	}

	results := resp.Output.Results
	if len(results) == 0 {
		results = resp.Results
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("%s rerank response has no results", provider)
	}

	reranked := make([]Chunk, 0, min(topN, len(results)))
	for _, item := range results {
		if item.Index < 0 || item.Index >= len(candidates) {
			continue
		}
		chunk := candidates[item.Index]
		chunk.Score = item.score()
		reranked = append(reranked, chunk)
		if len(reranked) == topN {
			break
		}
	}
	if len(reranked) == 0 {
		return nil, fmt.Errorf("%s rerank response indexes are out of range", provider)
	}
	return reranked, nil
}

type rerankResult struct {
	Index         int      `json:"index"`
	Relevance     *float64 `json:"relevance_score"`
	Score         *float64 `json:"score"`
	RelevanceText *float64 `json:"relevanceScore"`
}

func (r rerankResult) score() float64 {
	if r.Relevance != nil {
		return *r.Relevance
	}
	if r.Score != nil {
		return *r.Score
	}
	if r.RelevanceText != nil {
		return *r.RelevanceText
	}
	return 0
}
