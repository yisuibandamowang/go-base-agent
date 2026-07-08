package rag

import (
	"context"
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
	ChannelHybrid         SearchChannelType = "HYBRID"
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
		chunks []RetrievedChunk
		err    error
	}
	results := make([]result, len(e.channels))

	for i, ch := range e.channels {
		if !ch.IsEnabled(sc) {
			continue
		}
		start := time.Now()
		r, err := ch.Search(ctx, sc)
		if err != nil {
			results[i].err = err
			continue
		}
		r.LatencyMs = time.Since(start).Milliseconds()
		results[i].chunks = r.Chunks
	}

	var allChunks []RetrievedChunk
	var allResults []SearchChannelResult
	for _, r := range results {
		if r.err == nil && len(r.chunks) > 0 {
			allChunks = append(allChunks, r.chunks...)
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
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		deduped = append(deduped, c)
	}
	return deduped
}
