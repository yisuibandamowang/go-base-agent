package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SearchChannelType enumerates retrieval channel types.
// Aligns with Java SearchChannelType.
type SearchChannelType string

const (
	ChannelVectorGlobal   SearchChannelType = "VECTOR_GLOBAL"
	ChannelIntentDirected SearchChannelType = "INTENT_DIRECTED"
	ChannelKeyword        SearchChannelType = "KEYWORD"
	ChannelGraph          SearchChannelType = "GRAPH"
	ChannelHybrid         SearchChannelType = "HYBRID"
	ChannelWebSearch      SearchChannelType = "WEB_SEARCH"
)

// SearchContext carries parameters for a retrieval channel.
// Aligns with Java SearchContext.
type SearchContext struct {
	OriginalQuestion  string
	RewrittenQuestion string
	SubQuestions      []string
	Intents           []SubQuestionIntent
	TopK              int
	KnowledgeBaseID   string
	RetrievalScope    *RetrievalScope
}

// SearchChannelResult contains the results from a single channel.
type SearchChannelResult struct {
	ChannelType SearchChannelType
	ChannelName string
	Chunks      []RetrievedChunk
	LatencyMs   int64
}

// FusionChannelWeights controls the relative contribution of retrieval channels to RRF.
type FusionChannelWeights struct {
	Vector    float64
	Keyword   float64
	Graph     float64
	WebSearch float64
}

// SearchChannel is a retrievable search channel.
// Aligns with Java SearchChannel.
type SearchChannel interface {
	Name() string
	Priority() int
	IsEnabled(ctx SearchContext) bool
	Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error)
	Type() SearchChannelType
}

// SearchResultPostProcessor processes retrieval results after channel execution.
// Aligns with Java SearchResultPostProcessor.
type SearchResultPostProcessor interface {
	Name() string
	Order() int
	Process(chunks []RetrievedChunk, results []SearchChannelResult) []RetrievedChunk
}

// MultiChannelRetrievalEngine coordinates parallel channel retrieval and post-processing.
// Aligns with Java MultiChannelRetrievalEngine.
type MultiChannelRetrievalEngine struct {
	channels                 []SearchChannel
	postProcessors           []SearchResultPostProcessor
	channelTimeout           time.Duration
	scopeConfidenceThreshold float64
	scopeMinIntentScore      float64
	scopeResolver            *RetrievalScopeResolver
}

// MultiChannelRetriever adapts MultiChannelRetrievalEngine to the Retriever interface.
type MultiChannelRetriever struct {
	engine *MultiChannelRetrievalEngine
}

// NewMultiChannelRetriever creates a Retriever backed by multi-channel retrieval.
func NewMultiChannelRetriever(engine *MultiChannelRetrievalEngine) *MultiChannelRetriever {
	return &MultiChannelRetriever{engine: engine}
}

// Retrieve runs the configured retrieval channels for one question.
func (r *MultiChannelRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	if r == nil || r.engine == nil {
		return nil, nil
	}
	return r.engine.Retrieve(ctx, SearchContext{
		OriginalQuestion:  question,
		RewrittenQuestion: question,
		TopK:              topK,
	})
}

// RetrieveWithContext runs the configured retrieval channels with the full search context.
func (r *MultiChannelRetriever) RetrieveWithContext(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error) {
	if r == nil || r.engine == nil {
		return nil, nil
	}
	return r.engine.Retrieve(ctx, sc)
}

// RetrieveWithContextResult preserves retrieval scope metadata for downstream prompt planning.
func (r *MultiChannelRetriever) RetrieveWithContextResult(ctx context.Context, sc SearchContext) (RetrievalResult, error) {
	if r == nil || r.engine == nil {
		return RetrievalResult{}, nil
	}
	return r.engine.RetrieveWithResult(ctx, sc)
}

// NewMultiChannelRetrievalEngine creates a new retrieval engine.
func NewMultiChannelRetrievalEngine(channels []SearchChannel, postProcessors []SearchResultPostProcessor) *MultiChannelRetrievalEngine {
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Priority() < channels[j].Priority()
	})
	sort.Slice(postProcessors, func(i, j int) bool {
		return postProcessors[i].Order() < postProcessors[j].Order()
	})
	return &MultiChannelRetrievalEngine{
		channels:                 channels,
		postProcessors:           postProcessors,
		scopeConfidenceThreshold: 0.6,
		scopeMinIntentScore:      0.4,
	}
}

// SetRetrievalScopeOptions configures the thresholds used to distinguish directed retrieval from global fallback.
func (e *MultiChannelRetrievalEngine) SetRetrievalScopeOptions(confidenceThreshold, minIntentScore float64) {
	if e == nil {
		return
	}
	if confidenceThreshold > 0 {
		e.scopeConfidenceThreshold = confidenceThreshold
	}
	if minIntentScore >= 0 {
		e.scopeMinIntentScore = minIntentScore
	}
}

// SetRetrievalScopeResolver configures the shared request-level scope resolver.
func (e *MultiChannelRetrievalEngine) SetRetrievalScopeResolver(resolver *RetrievalScopeResolver) {
	if e == nil {
		return
	}
	e.scopeResolver = resolver
}

// SetChannelTimeout configures per-channel timeout degradation.
func (e *MultiChannelRetrievalEngine) SetChannelTimeout(timeout time.Duration) {
	if e == nil {
		return
	}
	if timeout < 0 {
		timeout = 0
	}
	e.channelTimeout = timeout
}

// Retrieve runs enabled channels in parallel, then applies post-processors.
func (e *MultiChannelRetrievalEngine) Retrieve(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error) {
	if e.scopeResolver != nil && sc.RetrievalScope == nil {
		scope, err := e.scopeResolver.Resolve(ctx, sc.Intents)
		if err != nil {
			return nil, fmt.Errorf("resolve retrieval scope: %w", err)
		}
		sc.RetrievalScope = &scope
	}
	type result struct {
		result SearchChannelResult
		err    error
	}
	results := make([]result, len(e.channels))
	if len(e.channels) == 0 {
		return nil, nil
	}
	activeCount := 0
	outcomes := make(chan struct {
		idx int
		res SearchChannelResult
		err error
	}, len(e.channels))
	for i, ch := range e.channels {
		if !ch.IsEnabled(sc) {
			continue
		}
		activeCount++
		go func(idx int, channel SearchChannel) {
			start := time.Now()
			done := make(chan struct {
				res SearchChannelResult
				err error
			}, 1)
			go func() {
				r, err := channel.Search(ctx, sc)
				if err == nil {
					r.LatencyMs = time.Since(start).Milliseconds()
				}
				select {
				case done <- struct {
					res SearchChannelResult
					err error
				}{res: r, err: err}:
				default:
				}
			}()
			if e.channelTimeout <= 0 {
				outcome := <-done
				outcomes <- struct {
					idx int
					res SearchChannelResult
					err error
				}{idx: idx, res: outcome.res, err: outcome.err}
				return
			}
			timer := time.NewTimer(e.channelTimeout)
			defer timer.Stop()
			select {
			case outcome := <-done:
				slog.Debug("rag search channel completed", "channel", channel.Name(), "chunks", len(outcome.res.Chunks))
				outcomes <- struct {
					idx int
					res SearchChannelResult
					err error
				}{idx: idx, res: outcome.res, err: outcome.err}
			case <-timer.C:
				slog.Warn("rag search channel timed out", "channel", channel.Name(), "timeout_ms", e.channelTimeout.Milliseconds())
				outcomes <- struct {
					idx int
					res SearchChannelResult
					err error
				}{idx: idx, res: SearchChannelResult{ChannelType: channel.Type(), ChannelName: channel.Name()}, err: nil}
			}
		}(i, ch)
	}
	for i := 0; i < activeCount; i++ {
		outcome := <-outcomes
		results[outcome.idx] = result{result: outcome.res, err: outcome.err}
	}

	var allChunks []RetrievedChunk
	var allResults []SearchChannelResult
	for _, r := range results {
		if r.err == nil && len(r.result.Chunks) > 0 {
			allChunks = append(allChunks, r.result.Chunks...)
			allResults = append(allResults, r.result)
		}
	}

	for _, pp := range e.postProcessors {
		allChunks = pp.Process(allChunks, allResults)
	}
	slog.Debug("rag search fusion completed", "chunks", len(allChunks), "channels", len(allResults))

	return allChunks, nil
}

// RetrieveWithResult returns chunks together with the resolved directed intent IDs.
func (e *MultiChannelRetrievalEngine) RetrieveWithResult(ctx context.Context, sc SearchContext) (RetrievalResult, error) {
	if e == nil {
		return RetrievalResult{}, nil
	}
	if e.scopeResolver != nil && sc.RetrievalScope == nil {
		scope, err := e.scopeResolver.Resolve(ctx, sc.Intents)
		if err != nil {
			return RetrievalResult{}, fmt.Errorf("resolve retrieval scope: %w", err)
		}
		sc.RetrievalScope = &scope
	}
	chunks, err := e.Retrieve(ctx, sc)
	if err != nil {
		return RetrievalResult{Chunks: chunks}, err
	}
	if !e.hasDirectedChannel(sc) {
		return RetrievalResult{Chunks: chunks}, nil
	}
	return RetrievalResult{
		Chunks:            chunks,
		DirectedIntentIDs: e.resolveDirectedIntentIDs(sc),
	}, nil
}

func (e *MultiChannelRetrievalEngine) hasDirectedChannel(sc SearchContext) bool {
	for _, channel := range e.channels {
		if channel != nil && channel.Type() == ChannelIntentDirected && channel.IsEnabled(sc) {
			return true
		}
	}
	return false
}

func (e *MultiChannelRetrievalEngine) resolveDirectedIntentIDs(sc SearchContext) map[string]struct{} {
	ids := make(map[string]struct{})
	if sc.RetrievalScope != nil {
		if !sc.RetrievalScope.Directed {
			return ids
		}
		for _, nodeScore := range sc.RetrievalScope.Intents {
			if id := strings.TrimSpace(nodeScore.Node.ID); id != "" {
				ids[id] = struct{}{}
			}
		}
		return ids
	}
	if e == nil || len(sc.Intents) == 0 {
		return ids
	}

	maxScore := 0.0
	for _, subIntent := range sc.Intents {
		for _, nodeScore := range subIntent.NodeScores {
			if nodeScore.Node.Kind != IntentKindKB || nodeScore.Score < e.scopeMinIntentScore {
				continue
			}
			if len(nodeScore.Node.EffectiveCollectionNames()) == 0 {
				continue
			}
			if nodeScore.Score > maxScore {
				maxScore = nodeScore.Score
			}
		}
	}
	if maxScore < e.scopeConfidenceThreshold {
		return ids
	}

	for _, subIntent := range sc.Intents {
		for _, nodeScore := range subIntent.NodeScores {
			if nodeScore.Node.Kind != IntentKindKB || nodeScore.Score < e.scopeMinIntentScore {
				continue
			}
			if len(nodeScore.Node.EffectiveCollectionNames()) == 0 {
				continue
			}
			if id := strings.TrimSpace(nodeScore.Node.ID); id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	return ids
}

// DedupPostProcessor removes duplicate chunks by ID.
// Aligns with Java DeduplicationPostProcessor.
type DedupPostProcessor struct{}

func (d *DedupPostProcessor) Name() string { return "dedup" }
func (d *DedupPostProcessor) Order() int   { return 1 }
func (d *DedupPostProcessor) Process(chunks []RetrievedChunk, results []SearchChannelResult) []RetrievedChunk {
	if len(chunks) == 0 {
		return chunks
	}
	orderedResults := append([]SearchChannelResult(nil), results...)
	if len(orderedResults) == 0 {
		orderedResults = []SearchChannelResult{{Chunks: chunks}}
	}
	sort.SliceStable(orderedResults, func(i, j int) bool {
		return dedupChannelPriority(orderedResults[i].ChannelType) < dedupChannelPriority(orderedResults[j].ChannelType)
	})

	indexByKey := make(map[string]int)
	deduped := make([]RetrievedChunk, 0, len(chunks))
	for _, result := range orderedResults {
		for _, chunk := range result.Chunks {
			key := chunkKey(chunk)
			if idx, ok := indexByKey[key]; ok {
				if chunk.Score > deduped[idx].Score {
					deduped[idx] = chunk
				}
				continue
			}
			indexByKey[key] = len(deduped)
			deduped = append(deduped, chunk)
		}
	}
	return deduped
}

func dedupChannelPriority(typ SearchChannelType) int {
	switch typ {
	case ChannelIntentDirected:
		return 1
	case ChannelKeyword:
		return 2
	case ChannelVectorGlobal:
		return 3
	case ChannelGraph:
		return 4
	default:
		return 99
	}
}

// FusionPostProcessor applies reciprocal rank fusion across channel results.
// Aligns with the Java FusionPostProcessor capability.
type FusionPostProcessor struct {
	rrfK                 int
	rerankCandidateLimit int
	weights              FusionChannelWeights
}

// NewFusionPostProcessor creates an RRF post-processor.
func NewFusionPostProcessor(rrfK int) *FusionPostProcessor {
	return NewFusionPostProcessorWithLimit(rrfK, 0)
}

// NewFusionPostProcessorWithLimit creates an RRF post-processor with an optional candidate limit.
func NewFusionPostProcessorWithLimit(rrfK int, rerankCandidateLimit int) *FusionPostProcessor {
	return NewFusionPostProcessorWithWeights(rrfK, rerankCandidateLimit, FusionChannelWeights{
		Vector:    1,
		Keyword:   1,
		Graph:     1,
		WebSearch: 1,
	})
}

// NewFusionPostProcessorWithWeights creates an RRF post-processor with channel weights.
func NewFusionPostProcessorWithWeights(rrfK int, rerankCandidateLimit int, weights FusionChannelWeights) *FusionPostProcessor {
	if rrfK <= 0 {
		rrfK = 60
	}
	return &FusionPostProcessor{rrfK: rrfK, rerankCandidateLimit: rerankCandidateLimit, weights: weights}
}

func (f *FusionPostProcessor) Name() string { return "fusion" }
func (f *FusionPostProcessor) Order() int   { return 5 }

func (f *FusionPostProcessor) Process(chunks []RetrievedChunk, results []SearchChannelResult) []RetrievedChunk {
	if len(chunks) == 0 || len(results) == 0 {
		return chunks
	}
	// 通道归属是检索归因（Rerank 存活率）的基础：RetrievedChunk 不携带来源通道字段，
	// 按 chunk key 从通道结果反查标记，多路命中的证据取首个命中通道（对齐 Java ChannelAttribution 反查）。
	channelIndex := indexChannels(results)
	for i := range chunks {
		if channels, ok := channelIndex[chunkKey(chunks[i])]; ok && len(channels) > 0 {
			chunks[i].Metadata = withMetadataChannel(chunks[i].Metadata, channels[0])
		}
	}
	if len(results) == 1 {
		return f.truncateCandidates(chunks)
	}

	scores := make(map[string]float64)
	for _, result := range results {
		weight := f.weightOf(result.ChannelType)
		for rank, chunk := range result.Chunks {
			scores[chunkKey(chunk)] += weight / float64(f.rrfK+rank+1)
		}
	}

	fused := make([]RetrievedChunk, len(chunks))
	copy(fused, chunks)
	for i := range fused {
		if score, ok := scores[chunkKey(fused[i])]; ok {
			fused[i].Score = score
		}
	}

	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].Score == fused[j].Score {
			return i < j
		}
		return fused[i].Score > fused[j].Score
	})
	return f.truncateCandidates(fused)
}

func (f *FusionPostProcessor) weightOf(channelType SearchChannelType) float64 {
	weights := f.weights
	switch channelType {
	case ChannelVectorGlobal, ChannelIntentDirected:
		return weights.Vector
	case ChannelKeyword:
		return weights.Keyword
	case ChannelGraph:
		return weights.Graph
	case ChannelWebSearch:
		return weights.WebSearch
	default:
		return 1
	}
}

func (f *FusionPostProcessor) truncateCandidates(chunks []RetrievedChunk) []RetrievedChunk {
	if f.rerankCandidateLimit > 0 && len(chunks) > f.rerankCandidateLimit {
		return chunks[:f.rerankCandidateLimit]
	}
	return chunks
}

func chunkKey(chunk RetrievedChunk) string {
	if chunk.ID != "" {
		return chunk.ID
	}
	sum := sha256.Sum256([]byte(chunk.Text))
	return hex.EncodeToString(sum[:])
}

// indexChannels 反查每个 chunk key 命中的通道集合（一条证据可被多路命中，故值为集合），
// 对齐 Java ChannelAttribution.index。
func indexChannels(results []SearchChannelResult) map[string][]SearchChannelType {
	index := make(map[string][]SearchChannelType)
	for _, result := range results {
		for _, chunk := range result.Chunks {
			key := chunkKey(chunk)
			found := false
			for _, existing := range index[key] {
				if existing == result.ChannelType {
					found = true
					break
				}
			}
			if !found {
				index[key] = append(index[key], result.ChannelType)
			}
		}
	}
	return index
}

// withMetadataChannel 在 metadata 上记录通道归属标记，返回新 map（不修改原 map）。
func withMetadataChannel(metadata map[string]string, channel SearchChannelType) map[string]string {
	next := make(map[string]string, len(metadata)+1)
	for k, v := range metadata {
		next[k] = v
	}
	next["retrieval_channel"] = string(channel)
	return next
}

// channelAttributionLabel 通道可读标签，对齐 Java ChannelAttribution.label。
func channelAttributionLabel(channel SearchChannelType) string {
	switch channel {
	case ChannelVectorGlobal, ChannelIntentDirected:
		return "向量"
	case ChannelKeyword:
		return "关键词"
	case ChannelGraph:
		return "图谱"
	case ChannelWebSearch:
		return "联网"
	default:
		return string(channel)
	}
}

// countChannelsByAttribution 统计给定 chunks 按通道的分布，多路命中的 chunk 在每个命中通道各计一次。
// 归因来源是 fusion 阶段写入的 retrieval_channel metadata。返回的 map 键为通道标签。
func countChannelsByAttribution(chunks []RetrievedChunk) map[string]int {
	counts := make(map[string]int)
	for _, chunk := range chunks {
		channel := strings.TrimSpace(chunk.Metadata["retrieval_channel"])
		if channel == "" {
			continue
		}
		counts[channel]++
	}
	return counts
}

// formatChannelCounts 通道分布转中文可读串，如「向量=4 关键词=6」。
func formatChannelCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return "无"
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(channelAttributionLabel(SearchChannelType(k)))
		b.WriteString("=")
		b.WriteString(strconv.Itoa(counts[k]))
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}
