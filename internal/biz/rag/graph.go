package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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

// GraphSyncClient syncs document writes and deletes to LightRAG-compatible graph services.
type GraphSyncClient interface {
	InsertText(ctx context.Context, text, fileSource string) error
	DeleteByDoc(ctx context.Context, docID string) error
	DeleteByCollection(ctx context.Context, collectionName string) error
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
		"mode":                  firstGraphNonEmpty(strings.TrimSpace(mode), "hybrid"),
		"only_need_context":     true,
		"include_references":    true,
		"include_chunk_content": true,
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

// InsertText writes or refreshes a document in LightRAG.
func (c *LightRagClient) InsertText(ctx context.Context, text, fileSource string) error {
	if c == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	body := map[string]any{
		"text": text,
	}
	if strings.TrimSpace(fileSource) != "" {
		body["file_source"] = strings.TrimSpace(fileSource)
	}
	if _, err := c.postJSON(ctx, "/documents/text", body, false); err != nil {
		slog.Warn("LightRAG document write failed", "file_source", fileSource, "err", err)
		return err
	}
	return nil
}

// DeleteByDoc deletes all graph data related to one document.
func (c *LightRagClient) DeleteByDoc(ctx context.Context, docID string) error {
	if c == nil || strings.TrimSpace(docID) == "" {
		return nil
	}
	if err := c.deleteMatching(ctx, func(filePath string) bool {
		return strings.Contains(filePath, docID)
	}, "docId="+strings.TrimSpace(docID)); err != nil {
		slog.Warn("LightRAG document delete failed", "doc_id", docID, "err", err)
		return err
	}
	return nil
}

// DeleteByCollection deletes all graph data related to one collection.
func (c *LightRagClient) DeleteByCollection(ctx context.Context, collectionName string) error {
	if c == nil || strings.TrimSpace(collectionName) == "" {
		return nil
	}
	if err := c.deleteMatching(ctx, func(filePath string) bool {
		source := ParseGraphFileSource(filePath)
		return source != nil && strings.EqualFold(source.CollectionName, strings.TrimSpace(collectionName))
	}, "collection="+strings.TrimSpace(collectionName)); err != nil {
		slog.Warn("LightRAG collection delete failed", "collection", collectionName, "err", err)
		return err
	}
	return nil
}

// FetchGraph fetches a graph sub-view from LightRAG.
func (c *LightRagClient) FetchGraph(ctx context.Context, label string, maxDepth, maxNodes int) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("lightRag client not configured")
	}
	values := url.Values{}
	values.Set("label", firstGraphNonEmpty(strings.TrimSpace(label), "*"))
	values.Set("max_depth", strconv.Itoa(maxGraphPositive(maxDepth, 1)))
	values.Set("max_nodes", strconv.Itoa(maxGraphPositive(maxNodes, 1)))
	return c.getJSON(ctx, "/graphs", values)
}

// FetchLabels fetches graph entity labels from LightRAG.
func (c *LightRagClient) FetchLabels(ctx context.Context, keyword string, limit int) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("lightRag client not configured")
	}
	keyword = strings.TrimSpace(keyword)
	path := "/graph/label/popular"
	values := url.Values{}
	if keyword == "" {
		values.Set("limit", strconv.Itoa(clampGraphLimit(limit, 300, 1000)))
	} else {
		path = "/graph/label/search"
		values.Set("q", keyword)
		values.Set("limit", strconv.Itoa(clampGraphLimit(limit, 50, 100)))
	}
	payload, err := c.getJSON(ctx, path, values)
	if err != nil {
		return nil, err
	}
	return parseGraphLabels(payload), nil
}

func (c *LightRagClient) postJSON(ctx context.Context, path string, body any, query bool) ([]byte, error) {
	return c.doJSON(ctx, http.MethodPost, path, body, query)
}

func (c *LightRagClient) getJSON(ctx context.Context, path string, values url.Values) ([]byte, error) {
	if len(values) > 0 {
		path += "?" + values.Encode()
	}
	return c.doJSON(ctx, http.MethodGet, path, nil, false)
}

func (c *LightRagClient) deleteJSON(ctx context.Context, path string, body any) ([]byte, error) {
	return c.doJSON(ctx, http.MethodDelete, path, body, false)
}

func (c *LightRagClient) doJSON(ctx context.Context, method, path string, body any, query bool) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("lightRag client not configured")
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal graph request: %w", err)
		}
	}
	reqCtx := ctx
	if query && c.queryTimeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, c.queryTimeout)
		defer cancel()
	}
	var bodyReader io.Reader
	if len(data) > 0 {
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+path, bodyReader)
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
		return nil, fmt.Errorf("graph request HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read graph response: %w", err)
	}
	return data, nil
}

func (c *LightRagClient) deleteMatching(ctx context.Context, filePathMatch func(string) bool, logKey string) error {
	if c == nil {
		return fmt.Errorf("lightRag client not configured")
	}
	docsPayload, err := c.doJSON(ctx, http.MethodGet, "/documents", nil, false)
	if err != nil {
		return err
	}
	var docs struct {
		Statuses map[string][]struct {
			ID       string `json:"id"`
			FilePath string `json:"file_path"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(docsPayload, &docs); err != nil {
		return fmt.Errorf("unmarshal graph documents: %w", err)
	}
	docIDs := make([]string, 0)
	for _, group := range docs.Statuses {
		for _, doc := range group {
			if strings.TrimSpace(doc.FilePath) != "" && filePathMatch(doc.FilePath) && strings.TrimSpace(doc.ID) != "" {
				docIDs = append(docIDs, doc.ID)
			}
		}
	}
	if len(docIDs) == 0 {
		return nil
	}
	body := map[string]any{"doc_ids": docIDs}
	if _, err := c.deleteJSON(ctx, "/documents/delete_document", body); err != nil {
		return fmt.Errorf("delete graph documents %s: %w", logKey, err)
	}
	return nil
}

func parseGraphLabels(payload []byte) []string {
	var raw []any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil
	}
	labels := make([]string, 0, len(raw))
	for _, item := range raw {
		switch v := item.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				labels = append(labels, strings.TrimSpace(v))
			}
		case map[string]any:
			label := graphLabelValue(v, "label")
			if label == "" {
				label = graphLabelValue(v, "name")
			}
			if strings.TrimSpace(label) != "" && label != "<nil>" {
				labels = append(labels, strings.TrimSpace(label))
			}
		}
	}
	return labels
}

func graphLabelValue(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func maxGraphPositive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func clampGraphLimit(value, fallback, max int) int {
	v := fallback
	if value > 0 {
		v = value
	}
	if v > max {
		return max
	}
	return v
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
	backend                  KnowledgeSearchBackend
	client                   GraphQueryClient
	queryMode                string
	priority                 int
	scopeConfidenceThreshold float64
	scopeMinIntentScore      float64
	supplementRatio          float64
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
		backend:                  backend,
		client:                   client,
		queryMode:                queryMode,
		priority:                 priority,
		scopeConfidenceThreshold: 0.6,
		scopeMinIntentScore:      0.4,
		supplementRatio:          0.25,
	}
}

// SetScopeOptions configures the shared confidence thresholds used for graph retrieval scope.
func (c *GraphSearchChannel) SetScopeOptions(confidenceThreshold, minIntentScore float64, supplementRatios ...float64) {
	if c == nil {
		return
	}
	if confidenceThreshold > 0 {
		c.scopeConfidenceThreshold = confidenceThreshold
	}
	if minIntentScore >= 0 {
		c.scopeMinIntentScore = minIntentScore
	}
	if len(supplementRatios) > 0 && supplementRatios[0] >= 0 {
		c.supplementRatio = supplementRatios[0]
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
	activeCollections := []string(nil)
	if sc.RetrievalScope != nil {
		activeCollections = append(activeCollections, sc.RetrievalScope.TargetCollections...)
		activeCollections = append(activeCollections, sc.RetrievalScope.SupplementCollections...)
	} else {
		kbs, err := c.backend.ListKnowledgeBases(ctx)
		if err != nil {
			return SearchChannelResult{ChannelType: ChannelGraph, ChannelName: c.Name(), LatencyMs: time.Since(start).Milliseconds()}, nil
		}
		activeCollections = activeKnowledgeCollections(kbs)
	}
	if len(activeCollections) == 0 {
		return SearchChannelResult{ChannelType: ChannelGraph, ChannelName: c.Name(), LatencyMs: time.Since(start).Milliseconds()}, nil
	}

	directed, targetCollections := c.resolveScope(sc, activeCollections)

	topK := sc.TopK
	if topK <= 0 {
		topK = 10
	}
	queryTopK := topK
	if directed {
		queryTopK = topK * 3
	}
	queryCollections := targetCollections
	evidence := c.client.RetrieveByScope(ctx, firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion), c.queryMode, queryTopK, queryCollections)

	primaryQuota, supplementQuota := graphScopeQuotas(topK, directed, c.supplementRatio)
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

func (c *GraphSearchChannel) resolveScope(sc SearchContext, activeCollections []string) (bool, []string) {
	if c == nil || len(activeCollections) == 0 {
		return false, activeCollections
	}
	if sc.RetrievalScope != nil {
		return sc.RetrievalScope.Directed, append([]string(nil), sc.RetrievalScope.TargetCollections...)
	}
	maxScore := 0.0
	bound := make(map[string]struct{})
	for _, subIntent := range sc.Intents {
		for _, nodeScore := range subIntent.NodeScores {
			if nodeScore.Node.Kind != IntentKindKB || nodeScore.Score < c.scopeMinIntentScore {
				continue
			}
			collections := nodeScore.Node.EffectiveCollectionNames()
			if len(collections) == 0 {
				continue
			}
			if nodeScore.Score > maxScore {
				maxScore = nodeScore.Score
			}
			for _, collection := range collections {
				bound[collection] = struct{}{}
			}
		}
	}
	if maxScore < c.scopeConfidenceThreshold {
		return false, activeCollections
	}

	targets := make([]string, 0, len(bound))
	for _, collection := range activeCollections {
		if _, ok := bound[collection]; ok {
			targets = append(targets, collection)
		}
	}
	if len(targets) == 0 {
		return false, activeCollections
	}
	return true, targets
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

func graphScopeQuotas(topK int, directed bool, ratios ...float64) (int, int) {
	if topK <= 0 {
		return 0, 0
	}
	if !directed {
		return topK, 0
	}
	ratio := 0.25
	if len(ratios) > 0 {
		ratio = ratios[0]
	}
	quota := SplitScopeQuota(&RetrievalScope{Directed: true, SupplementCollections: []string{"supplement"}}, topK, ratio)
	return quota.Primary, quota.Supplement
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
