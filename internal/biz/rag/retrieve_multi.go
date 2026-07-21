package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// SearchChannelType enumerates retrieval channel types.
// Aligns with Java SearchChannelType.
type SearchChannelType string

const (
	ChannelVectorGlobal   SearchChannelType = "VECTOR_GLOBAL"
	ChannelIntentDirected SearchChannelType = "INTENT_DIRECTED"
	ChannelKeyword        SearchChannelType = "KEYWORD"
	ChannelHybrid         SearchChannelType = "HYBRID"
	ChannelWebSearch      SearchChannelType = "WEB_SEARCH"
)

// SearchContext carries parameters for a retrieval channel.
// Aligns with Java SearchContext.
type SearchContext struct {
	OriginalQuestion  string
	RewrittenQuestion string
	SubQuestions      []string
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

// Retrieve runs enabled channels in parallel, then applies post-processors.
func (e *MultiChannelRetrievalEngine) Retrieve(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error) {
	type result struct {
		result SearchChannelResult
		err    error
	}
	results := make([]result, len(e.channels))
	var wg sync.WaitGroup
	for i, ch := range e.channels {
		if !ch.IsEnabled(sc) {
			continue
		}
		wg.Add(1)
		go func(idx int, channel SearchChannel) {
			defer wg.Done()
			start := time.Now()
			r, err := channel.Search(ctx, sc)
			if err != nil {
				results[idx].err = err
				return
			}
			r.LatencyMs = time.Since(start).Milliseconds()
			results[idx].result = r
		}(i, ch)
	}
	wg.Wait()

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

	return allChunks, nil
}

// DedupPostProcessor removes duplicate chunks by ID.
// Aligns with Java DeduplicationPostProcessor.
type DedupPostProcessor struct{}

func (d *DedupPostProcessor) Name() string { return "dedup" }
func (d *DedupPostProcessor) Order() int   { return 1 }
func (d *DedupPostProcessor) Process(chunks []RetrievedChunk, results []SearchChannelResult) []RetrievedChunk {
	seen := make(map[string]bool)
	deduped := make([]RetrievedChunk, 0, len(chunks))
	for _, c := range chunks {
		key := chunkKey(c)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, c)
	}
	return deduped
}

// FusionPostProcessor applies reciprocal rank fusion across channel results.
// Aligns with the Java FusionPostProcessor capability.
type FusionPostProcessor struct {
	rrfK int
}

// NewFusionPostProcessor creates an RRF post-processor.
func NewFusionPostProcessor(rrfK int) *FusionPostProcessor {
	if rrfK <= 0 {
		rrfK = 60
	}
	return &FusionPostProcessor{rrfK: rrfK}
}

func (f *FusionPostProcessor) Name() string { return "fusion" }
func (f *FusionPostProcessor) Order() int   { return 5 }

func (f *FusionPostProcessor) Process(chunks []RetrievedChunk, results []SearchChannelResult) []RetrievedChunk {
	if len(chunks) == 0 || len(results) == 0 {
		return chunks
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
	return fused
}

func chunkKey(chunk RetrievedChunk) string {
	if chunk.ID != "" {
		return chunk.ID
	}
	sum := sha256.Sum256([]byte(chunk.Text))
	return hex.EncodeToString(sum[:])
}
