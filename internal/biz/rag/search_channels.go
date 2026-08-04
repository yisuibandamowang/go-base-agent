package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/infra/embedding"

	"gorm.io/gorm"
	"net/http"
)

// RetrieverSearchChannel adapts a Retriever as a SearchChannel.
type RetrieverSearchChannel struct {
	name                   string
	typ                    SearchChannelType
	priority               int
	retriever              Retriever
	vectorGlobalConfigured bool
	intentDirectedEnabled  bool
	confidenceThreshold    float64
	supplementThreshold    float64
	topKMultiplier         int
	candidateBudget        int
}

// NewRetrieverSearchChannel creates a channel backed by a Retriever.
func NewRetrieverSearchChannel(name string, typ SearchChannelType, priority int, retriever Retriever) *RetrieverSearchChannel {
	return &RetrieverSearchChannel{name: name, typ: typ, priority: priority, retriever: retriever}
}

// SetVectorGlobalOptions configures Java-compatible vector global fallback behavior.
func (c *RetrieverSearchChannel) SetVectorGlobalOptions(intentDirectedEnabled bool, confidenceThreshold float64, topKMultiplier int, singleIntentSupplementThresholds ...float64) {
	if c == nil {
		return
	}
	if topKMultiplier <= 0 {
		topKMultiplier = 1
	}
	supplementThreshold := 0.8
	if len(singleIntentSupplementThresholds) > 0 && singleIntentSupplementThresholds[0] > 0 {
		supplementThreshold = singleIntentSupplementThresholds[0]
	}
	c.vectorGlobalConfigured = true
	c.intentDirectedEnabled = intentDirectedEnabled
	c.confidenceThreshold = confidenceThreshold
	c.supplementThreshold = supplementThreshold
	c.topKMultiplier = topKMultiplier
}

// SetVectorGlobalCandidateBudget configures the total budget for global vector retrieval.
func (c *RetrieverSearchChannel) SetVectorGlobalCandidateBudget(candidateBudget int) {
	if c == nil {
		return
	}
	c.candidateBudget = candidateBudget
}

func (c *RetrieverSearchChannel) Name() string            { return c.name }
func (c *RetrieverSearchChannel) Priority() int           { return c.priority }
func (c *RetrieverSearchChannel) Type() SearchChannelType { return c.typ }
func (c *RetrieverSearchChannel) IsEnabled(sc SearchContext) bool {
	if c == nil || c.retriever == nil {
		return false
	}
	if c.typ != ChannelVectorGlobal || !c.vectorGlobalConfigured {
		return true
	}
	if !c.intentDirectedEnabled {
		return true
	}
	maxScore, count := intentScoreStats(sc.Intents)
	if count == 0 {
		return true
	}
	if maxScore < c.confidenceThreshold {
		return true
	}
	return count == 1 && maxScore < c.supplementThreshold
}

func (c *RetrieverSearchChannel) Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error) {
	start := time.Now()
	var (
		chunks []RetrievedChunk
		err    error
	)
	if intentAware, ok := c.retriever.(IntentAwareRetriever); ok {
		chunks, err = intentAware.RetrieveWithContext(ctx, sc)
	} else if globalRetriever, ok := c.retriever.(GlobalRetriever); ok && c.shouldUseGlobalRetriever(globalRetriever) {
		query := firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)
		chunks, err = globalRetriever.RetrieveGlobal(ctx, query, c.candidateBudget)
	} else {
		query := firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)
		chunks, err = c.retriever.Retrieve(ctx, query, c.resolveTopK(sc.TopK))
	}
	if err != nil {
		return SearchChannelResult{}, err
	}
	return SearchChannelResult{
		ChannelType: c.typ,
		ChannelName: c.name,
		Chunks:      chunks,
		LatencyMs:   time.Since(start).Milliseconds(),
	}, nil
}

func (c *RetrieverSearchChannel) shouldUseGlobalRetriever(globalRetriever GlobalRetriever) bool {
	return c != nil &&
		c.typ == ChannelVectorGlobal &&
		c.vectorGlobalConfigured &&
		c.candidateBudget > 0 &&
		globalRetriever != nil &&
		globalRetriever.SupportsGlobalRetrieval()
}

func (c *RetrieverSearchChannel) resolveTopK(topK int) int {
	if c == nil || c.typ != ChannelVectorGlobal || !c.vectorGlobalConfigured || topK <= 0 {
		return topK
	}
	multiplier := c.topKMultiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	return topK * multiplier
}

func intentScoreStats(intents []SubQuestionIntent) (float64, int) {
	count := 0
	maxScore := 0.0
	for _, subIntent := range intents {
		for _, nodeScore := range subIntent.NodeScores {
			if nodeScore.Node.Kind != IntentKindKB || strings.TrimSpace(nodeScore.Node.CollectionName) == "" {
				continue
			}
			if count == 0 || nodeScore.Score > maxScore {
				maxScore = nodeScore.Score
			}
			count++
		}
	}
	return maxScore, count
}

// BackendKeywordSearchChannel keeps keyword recall behind a search backend abstraction.
type BackendKeywordSearchChannel struct {
	backend        KnowledgeSearchBackend
	mode           string
	topKMultiplier int
	priority       int
}

// PgKeywordSearchChannel is kept as a compatibility alias.
type PgKeywordSearchChannel = BackendKeywordSearchChannel

// NewBackendKeywordSearchChannel creates a keyword recall channel backed by the unified backend.
func NewBackendKeywordSearchChannel(backend KnowledgeSearchBackend, priority int) *BackendKeywordSearchChannel {
	return &BackendKeywordSearchChannel{backend: backend, priority: priority}
}

// NewPgKeywordSearchChannel keeps the old constructor for compatibility.
func NewPgKeywordSearchChannel(vectorDB *gorm.DB, kbRepo knowledgeBaseLister, priority int) *BackendKeywordSearchChannel {
	return NewBackendKeywordSearchChannel(NewPgKnowledgeSearchBackend(vectorDB, kbRepo), priority)
}

// SetKeywordOptions configures Java-compatible keyword search scope and candidate expansion.
func (c *BackendKeywordSearchChannel) SetKeywordOptions(mode string, topKMultiplier int) {
	if c == nil {
		return
	}
	c.mode = strings.ToLower(strings.TrimSpace(mode))
	if topKMultiplier <= 0 {
		topKMultiplier = 1
	}
	c.topKMultiplier = topKMultiplier
}

func (c *BackendKeywordSearchChannel) Name() string            { return "KeywordSearch" }
func (c *BackendKeywordSearchChannel) Priority() int           { return c.priority }
func (c *BackendKeywordSearchChannel) Type() SearchChannelType { return ChannelKeyword }
func (c *BackendKeywordSearchChannel) IsEnabled(sc SearchContext) bool {
	return c != nil && c.backend != nil && strings.TrimSpace(firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)) != ""
}

func (c *BackendKeywordSearchChannel) Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error) {
	start := time.Now()
	query := strings.TrimSpace(firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion))
	kbs, err := c.resolveKnowledgeBases(ctx, sc)
	if err != nil {
		return SearchChannelResult{}, fmt.Errorf("list knowledge bases: %w", err)
	}
	topK := c.resolveTopK(sc.TopK)

	chunks := make([]RetrievedChunk, 0)
	for _, kb := range kbs {
		result, err := c.backend.SearchKeywordChunks(ctx, kb, query, topK)
		if err != nil {
			continue
		}
		chunks = append(chunks, result...)
	}
	return SearchChannelResult{
		ChannelType: ChannelKeyword,
		ChannelName: c.Name(),
		Chunks:      chunks,
		LatencyMs:   time.Since(start).Milliseconds(),
	}, nil
}

func (c *BackendKeywordSearchChannel) resolveKnowledgeBases(ctx context.Context, sc SearchContext) ([]knowledgeModel.KnowledgeBase, error) {
	if c == nil || c.backend == nil {
		return nil, nil
	}
	kbs, err := c.backend.ListKnowledgeBases(ctx)
	if err != nil {
		return nil, err
	}

	mode := c.mode
	if mode == "" {
		mode = "both"
	}
	intentCollections := keywordIntentCollections(sc)
	switch mode {
	case "global":
		return kbs, nil
	case "intent":
		return filterKnowledgeBasesByCollections(kbs, intentCollections), nil
	default:
		filtered := filterKnowledgeBasesByCollections(kbs, intentCollections)
		if len(filtered) > 0 {
			return filtered, nil
		}
		return kbs, nil
	}
}

func (c *BackendKeywordSearchChannel) resolveTopK(topK int) int {
	if topK <= 0 {
		topK = 10
	}
	multiplier := 1
	if c != nil && c.topKMultiplier > 0 {
		multiplier = c.topKMultiplier
	}
	return topK * multiplier
}

// BackendIntentDirectedSearchChannel keeps intent recall behind a search backend abstraction.
type BackendIntentDirectedSearchChannel struct {
	backend      KnowledgeSearchBackend
	vectorSearch VectorSearchService
	emb          embedding.Service
	minScore     float64
	topKMultiple int
	priority     int
}

// PgIntentDirectedSearchChannel is kept as a compatibility alias.
type PgIntentDirectedSearchChannel = BackendIntentDirectedSearchChannel

// NewBackendIntentDirectedSearchChannel creates an intent-directed channel backed by a search backend.
func NewBackendIntentDirectedSearchChannel(backend KnowledgeSearchBackend, vectorSearch VectorSearchService, emb embedding.Service, priority int) *BackendIntentDirectedSearchChannel {
	return &BackendIntentDirectedSearchChannel{backend: backend, vectorSearch: vectorSearch, emb: emb, priority: priority}
}

// NewPgIntentDirectedSearchChannel keeps the old constructor for compatibility.
func NewPgIntentDirectedSearchChannel(vectorDB *gorm.DB, priority int) *BackendIntentDirectedSearchChannel {
	return NewBackendIntentDirectedSearchChannel(NewPgKnowledgeSearchBackend(vectorDB, nil), nil, nil, priority)
}

// NewPgIntentDirectedVectorSearchChannel keeps the old constructor for compatibility.
func NewPgIntentDirectedVectorSearchChannel(vectorDB *gorm.DB, vectorSearch VectorSearchService, emb embedding.Service, kbRepo knowledgeBaseLister, priority int) *BackendIntentDirectedSearchChannel {
	return NewBackendIntentDirectedSearchChannel(NewPgKnowledgeSearchBackend(vectorDB, kbRepo), vectorSearch, emb, priority)
}

// SetIntentOptions configures intent score filtering and per-intent TopK expansion.
func (c *BackendIntentDirectedSearchChannel) SetIntentOptions(minScore float64, topKMultiplier int) {
	if c == nil {
		return
	}
	c.minScore = minScore
	if topKMultiplier <= 0 {
		topKMultiplier = 1
	}
	c.topKMultiple = topKMultiplier
}

func (c *BackendIntentDirectedSearchChannel) Name() string            { return "IntentDirectedSearch" }
func (c *BackendIntentDirectedSearchChannel) Priority() int           { return c.priority }
func (c *BackendIntentDirectedSearchChannel) Type() SearchChannelType { return ChannelIntentDirected }
func (c *BackendIntentDirectedSearchChannel) IsEnabled(sc SearchContext) bool {
	if c == nil || c.backend == nil {
		return false
	}
	if len(sc.Intents) > 0 {
		return len(c.intentDirectedTargets(sc)) > 0
	}
	return strings.TrimSpace(firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)) != ""
}

func (c *BackendIntentDirectedSearchChannel) Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error) {
	start := time.Now()
	query := strings.TrimSpace(firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion))
	targets := c.intentDirectedTargets(sc)
	if len(targets) == 0 {
		if len(sc.Intents) > 0 {
			collections, err := c.matchIntentCollections(ctx, query)
			if err != nil || len(collections) == 0 {
				return SearchChannelResult{ChannelType: ChannelIntentDirected, ChannelName: c.Name(), LatencyMs: time.Since(start).Milliseconds()}, nil
			}
			targets = make([]intentDirectedTarget, 0, len(collections))
			for _, collection := range collections {
				targets = append(targets, intentDirectedTarget{collectionName: collection, topK: sc.TopK})
			}
		}
	}
	if c.canVectorSearch() && len(targets) > 0 {
		if chunks, searched := c.searchIntentVectors(ctx, query, targets); searched {
			return SearchChannelResult{ChannelType: ChannelIntentDirected, ChannelName: c.Name(), Chunks: chunks, LatencyMs: time.Since(start).Milliseconds()}, nil
		}
	}
	chunks := make([]RetrievedChunk, 0)
	for _, target := range targets {
		topK := target.topK
		if topK <= 0 {
			topK = 10
		}
		result, err := c.backend.SearchRecentChunks(ctx, target.collectionName, topK)
		if err != nil {
			continue
		}
		chunks = append(chunks, result...)
	}
	return SearchChannelResult{ChannelType: ChannelIntentDirected, ChannelName: c.Name(), Chunks: chunks, LatencyMs: time.Since(start).Milliseconds()}, nil
}

func (c *BackendIntentDirectedSearchChannel) canVectorSearch() bool {
	return c != nil && c.backend != nil && c.vectorSearch != nil && c.emb != nil
}

func (c *BackendIntentDirectedSearchChannel) searchIntentVectors(ctx context.Context, query string, targets []intentDirectedTarget) ([]RetrievedChunk, bool) {
	if c == nil || c.backend == nil {
		return nil, false
	}
	kbs, err := c.backend.ListKnowledgeBases(ctx)
	if err != nil {
		return nil, false
	}
	kbByCollection := make(map[string]knowledgeModel.KnowledgeBase, len(kbs))
	for _, kb := range kbs {
		collectionName := strings.TrimSpace(kb.CollectionName)
		if collectionName != "" {
			kbByCollection[collectionName] = kb
		}
	}

	chunks := make([]RetrievedChunk, 0)
	searched := false
	for _, target := range targets {
		kb, ok := kbByCollection[target.collectionName]
		if !ok {
			continue
		}
		searched = true
		topK := target.topK
		if topK <= 0 {
			topK = 10
		}
		vec, err := c.emb.EmbedWithModel(ctx, query, kb.EmbeddingModel)
		if err != nil {
			continue
		}
		vectorChunks, err := c.vectorSearch.Search(ctx, target.collectionName, vec, topK)
		if err != nil {
			continue
		}
		for _, chunk := range vectorChunks {
			chunks = append(chunks, RetrievedChunk{
				ID:       chunk.ChunkID,
				Text:     chunk.Content,
				Score:    chunk.Score,
				Metadata: metadataWithKnowledgeBase(chunk.Metadata, kb),
			})
		}
	}
	return chunks, searched
}

type intentDirectedTarget struct {
	collectionName string
	topK           int
}

func (c *BackendIntentDirectedSearchChannel) intentDirectedTargets(sc SearchContext) []intentDirectedTarget {
	minScore := 0.0
	topKMultiplier := 1
	if c != nil {
		minScore = c.minScore
		if c.topKMultiple > 0 {
			topKMultiplier = c.topKMultiple
		}
	}
	return intentDirectedTargetsFromContext(sc, minScore, topKMultiplier)
}

func intentDirectedTargetsFromContext(sc SearchContext, minScore float64, topKMultipliers ...int) []intentDirectedTarget {
	topKMultiplier := 1
	if len(topKMultipliers) > 0 {
		topKMultiplier = topKMultipliers[0]
	}
	if topKMultiplier <= 0 {
		topKMultiplier = 1
	}
	seen := make(map[string]int)
	targets := make([]intentDirectedTarget, 0)
	for _, subIntent := range sc.Intents {
		for _, ns := range subIntent.NodeScores {
			if ns.Score < minScore || ns.Node.Kind != IntentKindKB {
				continue
			}
			collectionName := strings.TrimSpace(ns.Node.CollectionName)
			if collectionName == "" {
				continue
			}
			topK := resolveIntentDirectedTopK(ns, sc.TopK) * topKMultiplier
			if idx, ok := seen[collectionName]; ok {
				if topK > targets[idx].topK {
					targets[idx].topK = topK
				}
				continue
			}
			seen[collectionName] = len(targets)
			targets = append(targets, intentDirectedTarget{
				collectionName: collectionName,
				topK:           topK,
			})
		}
	}
	return targets
}

func resolveIntentDirectedTopK(ns NodeScore, fallbackTopK int) int {
	if fallbackTopK <= 0 {
		fallbackTopK = 10
	}
	topK := fallbackTopK
	if ns.Node.TopK > 0 {
		topK = ns.Node.TopK
	}
	if topK <= 0 {
		topK = fallbackTopK
	}
	return topK
}

func (c *BackendIntentDirectedSearchChannel) matchIntentCollections(ctx context.Context, query string) ([]string, error) {
	if c == nil || c.backend == nil {
		return nil, nil
	}
	return c.backend.MatchIntentCollections(ctx, query, 5)
}

func keywordIntentCollections(sc SearchContext) []string {
	seen := make(map[string]bool)
	collections := make([]string, 0)
	for _, subIntent := range sc.Intents {
		for _, nodeScore := range subIntent.NodeScores {
			if nodeScore.Node.Kind != IntentKindKB {
				continue
			}
			collectionName := strings.TrimSpace(nodeScore.Node.CollectionName)
			if collectionName == "" || seen[collectionName] {
				continue
			}
			seen[collectionName] = true
			collections = append(collections, collectionName)
		}
	}
	return collections
}

func filterKnowledgeBasesByCollections(kbs []knowledgeModel.KnowledgeBase, collections []string) []knowledgeModel.KnowledgeBase {
	if len(kbs) == 0 || len(collections) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(collections))
	for _, collection := range collections {
		if strings.TrimSpace(collection) != "" {
			allowed[collection] = true
		}
	}
	filtered := make([]knowledgeModel.KnowledgeBase, 0, len(collections))
	for _, kb := range kbs {
		if allowed[strings.TrimSpace(kb.CollectionName)] {
			filtered = append(filtered, kb)
		}
	}
	return filtered
}

func firstSearchText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// YouComWebSearchChannel recalls public web snippets from a You.com-compatible API.
type YouComWebSearchChannel struct {
	apiURL  string
	apiKey  string
	count   int
	client  *http.Client
	enabled bool
}

// NewYouComWebSearchChannel creates an optional web-search channel.
func NewYouComWebSearchChannel(apiURL, apiKey string, count, timeoutSeconds int, enabled bool) *YouComWebSearchChannel {
	if count <= 0 {
		count = 5
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 5
	}
	return &YouComWebSearchChannel{
		apiURL:  strings.TrimSpace(apiURL),
		apiKey:  strings.TrimSpace(apiKey),
		count:   count,
		enabled: enabled,
		client:  &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

func (c *YouComWebSearchChannel) Name() string            { return "YouComWebSearch" }
func (c *YouComWebSearchChannel) Priority() int           { return 20 }
func (c *YouComWebSearchChannel) Type() SearchChannelType { return ChannelWebSearch }
func (c *YouComWebSearchChannel) IsEnabled(sc SearchContext) bool {
	return c != nil && c.enabled && c.apiURL != "" && c.apiKey != "" && strings.TrimSpace(firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)) != ""
}

func (c *YouComWebSearchChannel) Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error) {
	start := time.Now()
	query := strings.TrimSpace(firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL, nil)
	if err != nil {
		return SearchChannelResult{}, err
	}
	q := req.URL.Query()
	q.Set("query", query)
	q.Set("count", fmt.Sprintf("%d", c.count))
	req.URL.RawQuery = q.Encode()
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return SearchChannelResult{ChannelType: ChannelWebSearch, ChannelName: c.Name(), LatencyMs: time.Since(start).Milliseconds()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SearchChannelResult{ChannelType: ChannelWebSearch, ChannelName: c.Name(), LatencyMs: time.Since(start).Milliseconds()}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return SearchChannelResult{}, err
	}
	chunks := parseWebSearchChunks(body, c.count)
	return SearchChannelResult{ChannelType: ChannelWebSearch, ChannelName: c.Name(), Chunks: chunks, LatencyMs: time.Since(start).Milliseconds()}, nil
}

func parseWebSearchChunks(body []byte, max int) []RetrievedChunk {
	var payload struct {
		Results struct {
			Web  []webSearchItem `json:"web"`
			News []webSearchItem `json:"news"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	items := append(payload.Results.Web, payload.Results.News...)
	if max > 0 && len(items) > max {
		items = items[:max]
	}
	chunks := make([]RetrievedChunk, 0, len(items))
	for idx, item := range items {
		text := strings.TrimSpace(strings.Join([]string{item.Title, item.Description, strings.Join(item.Snippets, "\n"), item.URL}, "\n"))
		if text == "" {
			continue
		}
		chunks = append(chunks, RetrievedChunk{
			ID:    firstSearchText(item.URL, fmt.Sprintf("web-%d", idx)),
			Text:  text,
			Score: 1 / float64(idx+1),
			Metadata: map[string]string{
				"doc_name":   firstSearchText(item.Title, item.URL),
				"source_url": item.URL,
			},
		})
	}
	return chunks
}

type webSearchItem struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Snippets    []string `json:"snippets"`
}
