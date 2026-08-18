package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sort"
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
}

// SearchChannelResult contains the results from a single channel.
type SearchChannelResult struct {
	ChannelType SearchChannelType
	ChannelName string
	Chunks      []RetrievedChunk
	LatencyMs   int64
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
	channels       []SearchChannel
	postProcessors []SearchResultPostProcessor
	channelTimeout time.Duration
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

// NewMultiChannelRetrievalEngine creates a new retrieval engine.
func NewMultiChannelRetrievalEngine(channels []SearchChannel, postProcessors []SearchResultPostProcessor) *MultiChannelRetrievalEngine {
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Priority() < channels[j].Priority()
	})
	sort.Slice(postProcessors, func(i, j int) bool {
		return postProcessors[i].Order() < postProcessors[j].Order()
	})
	return &MultiChannelRetrievalEngine{
		channels:       channels,
		postProcessors: postProcessors,
	}
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
}

// NewFusionPostProcessor creates an RRF post-processor.
func NewFusionPostProcessor(rrfK int) *FusionPostProcessor {
	return NewFusionPostProcessorWithLimit(rrfK, 0)
}

// NewFusionPostProcessorWithLimit creates an RRF post-processor with an optional candidate limit.
func NewFusionPostProcessorWithLimit(rrfK int, rerankCandidateLimit int) *FusionPostProcessor {
	if rrfK <= 0 {
		rrfK = 60
	}
	return &FusionPostProcessor{rrfK: rrfK, rerankCandidateLimit: rerankCandidateLimit}
}

func (f *FusionPostProcessor) Name() string { return "fusion" }
func (f *FusionPostProcessor) Order() int   { return 5 }

func (f *FusionPostProcessor) Process(chunks []RetrievedChunk, results []SearchChannelResult) []RetrievedChunk {
	if len(chunks) == 0 || len(results) == 0 {
		return chunks
	}
	if len(results) == 1 {
		return f.truncateCandidates(chunks)
	}

	scores := make(map[string]float64)
	for _, result := range results {
		for rank, chunk := range result.Chunks {
			scores[chunkKey(chunk)] += 1.0 / float64(f.rrfK+rank+1)
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
