package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
)

// GraphEvidence describes graph retrieval results split by collection ownership.
type GraphEvidence struct {
	Matched   []RetrievedChunk
	Unmatched []RetrievedChunk
}

// GraphFileSource encodes and decodes LightRAG file_path values.
type GraphFileSource struct {
	CollectionName string
	DocID          string
}

var graphFileSourcePattern = regexp.MustCompile(`^(.+)_(\d+)((?:\.[A-Za-z0-9]+)*)$`)

// EncodeGraphFileSource joins collection name and doc ID into the LightRAG file_path format.
func EncodeGraphFileSource(collectionName, docID string) string {
	return strings.TrimSpace(collectionName) + "_" + strings.TrimSpace(docID)
}

// ParseGraphFileSource restores a LightRAG file_path into collection and doc ID.
func ParseGraphFileSource(filePath string) *GraphFileSource {
	if strings.TrimSpace(filePath) == "" {
		return nil
	}
	baseName := filePath[strings.LastIndex(filePath, "/")+1:]
	matches := graphFileSourcePattern.FindStringSubmatch(baseName)
	if len(matches) != 4 {
		return nil
	}
	return &GraphFileSource{
		CollectionName: matches[1],
		DocID:          matches[2],
	}
}

// GraphQueryClient retrieves graph evidence from LightRAG-compatible services.
type GraphQueryClient interface {
	RetrieveByScope(ctx context.Context, question, mode string, topK int, collections []string) GraphEvidence
}

// LightRagClient calls the LightRAG HTTP API.
type LightRagClient struct {
	baseURL      string
	apiKey       string
	client       *http.Client
	queryTimeout time.Duration
}

// NewLightRagClient creates a new LightRAG HTTP client.
func NewLightRagClient(baseURL, apiKey string, client *http.Client, queryTimeoutMs int) *LightRagClient {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "http://127.0.0.1:9621"
	}
	return &LightRagClient{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       strings.TrimSpace(apiKey),
		client:       client,
		queryTimeout: time.Duration(queryTimeoutMs) * time.Millisecond,
	}
}

// RetrieveByScope queries graph evidence and splits it by collection ownership.
func (c *LightRagClient) RetrieveByScope(ctx context.Context, question, mode string, topK int, collections []string) GraphEvidence {
	if c == nil || strings.TrimSpace(question) == "" {
		return GraphEvidence{}
	}
	body := map[string]any{
		"query":                 question,
		"mode":                  firstGraphNonEmpty(strings.TrimSpace(mode), "mix"),
		"only_need_context":     true,
		"include_references":    true,
		"include_chunk_content":  true,
	}
	if topK > 0 {
		body["top_k"] = topK
	}
	payload, err := c.postJSON(ctx, "/query", body, true)
	if err != nil {
		slog.Warn("LightRAG query failed", "err", err)
		return GraphEvidence{}
	}
	return parseGraphEvidence(payload, collections)
}

func (c *LightRagClient) postJSON(ctx context.Context, path string, body any, query bool) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("lightRag client not configured")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal graph request: %w", err)
	}
	reqCtx := ctx
	if query && c.queryTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, c.queryTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create graph request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do graph request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return nil, fmt.Errorf("graph query HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read graph response: %w", err)
	}
	return data, nil
}

func parseGraphEvidence(payload []byte, collections []string) GraphEvidence {
	filter := make(map[string]struct{}, len(collections))
	for _, collection := range collections {
		if collection = strings.TrimSpace(collection); collection != "" {
			filter[collection] = struct{}{}
		}
	}
	filterEnabled := len(filter) > 0

	var root struct {
		Response   string `json:"response"`
		References []struct {
			ReferenceID string   `json:"reference_id"`
			FilePath    string   `json:"file_path"`
			Content     []string `json:"content"`
		} `json:"references"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return GraphEvidence{}
	}

	if len(root.References) == 0 {
		if filterEnabled {
			return GraphEvidence{}
		}
		text := strings.TrimSpace(root.Response)
		if text == "" {
			return GraphEvidence{}
		}
		return GraphEvidence{Matched: []RetrievedChunk{{
			ID:    "graph:context",
			Text:  text,
			Score: 1,
			Metadata: map[string]string{
				"retrieval_channel": "graph",
			},
		}}}
	}

	matched := make([]RetrievedChunk, 0, len(root.References))
	unmatched := make([]RetrievedChunk, 0)
	for idx, ref := range root.References {
		body := strings.TrimSpace(strings.Join(ref.Content, "\n"))
		if body == "" {
			body = strings.TrimSpace(ref.FilePath)
		}
		if body == "" {
			continue
		}
		chunk := RetrievedChunk{
			ID:    firstGraphNonEmpty(strings.TrimSpace(ref.ReferenceID), fmt.Sprintf("graph:%d", idx)),
			Text:  body,
			Score: 1 / float64(idx+1),
			Metadata: map[string]string{
				"retrieval_channel": "graph",
			},
		}
		if source := ParseGraphFileSource(ref.FilePath); source != nil {
			chunk.Metadata["collection_name"] = source.CollectionName
			chunk.Metadata["doc_id"] = source.DocID
		}
		if !filterEnabled || graphReferenceMatches(ref.FilePath, filter) {
			matched = append(matched, chunk)
			continue
		}
		unmatched = append(unmatched, chunk)
	}
	return GraphEvidence{Matched: matched, Unmatched: unmatched}
}

func graphReferenceMatches(filePath string, filter map[string]struct{}) bool {
	if len(filter) == 0 {
		return true
	}
	source := ParseGraphFileSource(filePath)
	if source == nil {
		return false
	}
	_, ok := filter[source.CollectionName]
	return ok
}

// GraphSearchChannel queries LightRAG and feeds graph evidence into the multi-channel retriever.
type GraphSearchChannel struct {
	backend   KnowledgeSearchBackend
	client    GraphQueryClient
	queryMode string
	priority   int
}

// NewGraphSearchChannel creates a graph search channel.
func NewGraphSearchChannel(backend KnowledgeSearchBackend, client GraphQueryClient, queryMode string, priority int) *GraphSearchChannel {
	if priority <= 0 {
		priority = 8
	}
	queryMode = strings.TrimSpace(queryMode)
	if queryMode == "" {
		queryMode = "hybrid"
	}
	return &GraphSearchChannel{
		backend:   backend,
		client:    client,
		queryMode: queryMode,
		priority:   priority,
	}
}

func (c *GraphSearchChannel) Name() string            { return "GraphSearch" }
func (c *GraphSearchChannel) Priority() int           { return c.priority }
func (c *GraphSearchChannel) Type() SearchChannelType { return ChannelGraph }
func (c *GraphSearchChannel) IsEnabled(sc SearchContext) bool {
	return c != nil && c.backend != nil && c.client != nil && strings.TrimSpace(firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)) != ""
}

func (c *GraphSearchChannel) Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error) {
	start := time.Now()
	if c == nil || c.backend == nil || c.client == nil {
		return SearchChannelResult{ChannelType: ChannelGraph, ChannelName: "GraphSearch"}, nil
	}
	kbs, err := c.backend.ListKnowledgeBases(ctx)
	if err != nil {
		return SearchChannelResult{ChannelType: ChannelGraph, ChannelName: c.Name(), LatencyMs: time.Since(start).Milliseconds()}, nil
	}
	activeCollections := activeKnowledgeCollections(kbs)
	if len(activeCollections) == 0 {
		return SearchChannelResult{ChannelType: ChannelGraph, ChannelName: c.Name(), LatencyMs: time.Since(start).Milliseconds()}, nil
	}

	intentCollections := keywordIntentCollections(sc)
	directed := len(intentCollections) > 0
	targetCollections := intentCollections
	if len(targetCollections) == 0 {
		targetCollections = activeCollections
	}

	topK := sc.TopK
	if topK <= 0 {
		topK = 10
	}
	queryTopK := topK
	if directed {
		queryTopK = topK * 3
	}
	evidence := c.client.RetrieveByScope(ctx, firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion), c.queryMode, queryTopK, targetCollections)

	primaryQuota, supplementQuota := graphScopeQuotas(topK, directed)
	chunks := capGraphChunks(evidence.Matched, primaryQuota)
	if directed {
		chunks = mergeGraphChunks(chunks, capGraphChunks(evidence.Unmatched, supplementQuota))
	} else if len(evidence.Unmatched) > 0 {
		slog.Warn("graph search dropped unmatched evidence in global scope", "unmatched", len(evidence.Unmatched))
	}

	return SearchChannelResult{
		ChannelType: ChannelGraph,
		ChannelName: c.Name(),
		Chunks:      chunks,
		LatencyMs:   time.Since(start).Milliseconds(),
	}, nil
}

func activeKnowledgeCollections(kbs []knowledgeModel.KnowledgeBase) []string {
	seen := make(map[string]struct{}, len(kbs))
	collections := make([]string, 0, len(kbs))
	for _, kb := range kbs {
		collection := strings.TrimSpace(kb.CollectionName)
		if collection == "" {
			continue
		}
		if _, ok := seen[collection]; ok {
			continue
		}
		seen[collection] = struct{}{}
		collections = append(collections, collection)
	}
	return collections
}

func graphScopeQuotas(topK int, directed bool) (int, int) {
	if topK <= 0 {
		return 0, 0
	}
	if !directed {
		return topK, 0
	}
	supplement := int(math.Ceil(float64(topK) * 0.25))
	if supplement > topK {
		supplement = topK
	}
	primary := topK - supplement
	if primary < 0 {
		primary = 0
	}
	return primary, supplement
}

func capGraphChunks(chunks []RetrievedChunk, limit int) []RetrievedChunk {
	if limit <= 0 || len(chunks) <= limit {
		return append([]RetrievedChunk(nil), chunks...)
	}
	return append([]RetrievedChunk(nil), chunks[:limit]...)
}

func mergeGraphChunks(groups ...[]RetrievedChunk) []RetrievedChunk {
	if len(groups) == 0 {
		return nil
	}
	byID := make(map[string]RetrievedChunk)
	for _, group := range groups {
		for _, chunk := range group {
			if existing, ok := byID[chunk.ID]; !ok || chunk.Score > existing.Score {
				byID[chunk.ID] = chunk
			}
		}
	}
	chunks := make([]RetrievedChunk, 0, len(byID))
	for _, chunk := range byID {
		chunks = append(chunks, chunk)
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].Score == chunks[j].Score {
			return chunks[i].ID < chunks[j].ID
		}
		return chunks[i].Score > chunks[j].Score
	})
	return chunks
}

func firstGraphNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
