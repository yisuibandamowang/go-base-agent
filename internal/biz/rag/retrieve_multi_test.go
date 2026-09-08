package rag

import (
	"context"
	"testing"
	"time"
)

type testChannel struct {
	name     string
	priority int
	enabled  bool
	typ      SearchChannelType
	chunks   []RetrievedChunk
	delay    time.Duration
}

func (c *testChannel) Name() string                     { return c.name }
func (c *testChannel) Priority() int                    { return c.priority }
func (c *testChannel) IsEnabled(ctx SearchContext) bool { return c.enabled }
func (c *testChannel) Type() SearchChannelType          { return c.typ }
func (c *testChannel) Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return SearchChannelResult{
		ChannelType: c.typ,
		ChannelName: c.name,
		Chunks:      c.chunks,
	}, nil
}

func TestMultiChannelRetrieval_Basic(t *testing.T) {
	channels := []SearchChannel{
		&testChannel{name: "global", priority: 1, enabled: true, typ: ChannelVectorGlobal, chunks: []RetrievedChunk{
			{ID: "c1", Text: "hello", Score: 0.9},
		}},
		&testChannel{name: "keyword", priority: 2, enabled: true, typ: ChannelKeyword, chunks: []RetrievedChunk{
			{ID: "c2", Text: "world", Score: 0.8},
		}},
	}

	engine := NewMultiChannelRetrievalEngine(channels, []SearchResultPostProcessor{&DedupPostProcessor{}})
	chunks, err := engine.Retrieve(context.Background(), SearchContext{OriginalQuestion: "test", TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
}

func TestMultiChannelRetrieval_Disabled(t *testing.T) {
	channels := []SearchChannel{
		&testChannel{name: "off", priority: 1, enabled: false, typ: ChannelVectorGlobal},
		&testChannel{name: "on", priority: 2, enabled: true, typ: ChannelKeyword, chunks: []RetrievedChunk{
			{ID: "c1", Text: "only", Score: 0.5},
		}},
	}

	engine := NewMultiChannelRetrievalEngine(channels, nil)
	chunks, err := engine.Retrieve(context.Background(), SearchContext{OriginalQuestion: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk from enabled channel, got %d", len(chunks))
	}
}

func TestDedupPostProcessor(t *testing.T) {
	d := &DedupPostProcessor{}
	chunks := []RetrievedChunk{
		{ID: "a", Text: "first"},
		{ID: "b", Text: "second"},
		{ID: "a", Text: "duplicate"},
	}
	result := d.Process(chunks, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 after dedup, got %d", len(result))
	}
	if result[0].Text != "first" || result[1].Text != "second" {
		t.Fatal("unexpected dedup result")
	}
}

func TestDedupPostProcessorKeepsHighestScoreForDuplicateChunk(t *testing.T) {
	d := &DedupPostProcessor{}
	chunks := []RetrievedChunk{
		{ID: "a", Text: "intent hit", Score: 0.2},
		{ID: "b", Text: "keyword hit", Score: 0.8},
		{ID: "a", Text: "vector hit", Score: 0.95},
	}
	results := []SearchChannelResult{
		{ChannelType: ChannelIntentDirected, Chunks: []RetrievedChunk{chunks[0]}},
		{ChannelType: ChannelKeyword, Chunks: []RetrievedChunk{chunks[1]}},
		{ChannelType: ChannelVectorGlobal, Chunks: []RetrievedChunk{chunks[2]}},
	}

	result := d.Process(chunks, results)
	if len(result) != 2 {
		t.Fatalf("expected 2 after dedup, got %+v", result)
	}
	if result[0].ID != "a" || result[0].Text != "vector hit" || result[0].Score != 0.95 {
		t.Fatalf("expected duplicate chunk to keep highest score while preserving position, got %+v", result)
	}
}

func TestDedupPostProcessorUsesTextFallback(t *testing.T) {
	d := &DedupPostProcessor{}
	chunks := []RetrievedChunk{
		{Text: "same text"},
		{Text: "same text"},
	}

	result := d.Process(chunks, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 after dedup fallback, got %d", len(result))
	}
}

func TestFusionPostProcessor_RRFReordersByCrossChannelHits(t *testing.T) {
	pp := NewFusionPostProcessor(60)
	chunks := []RetrievedChunk{
		{ID: "a", Text: "alpha"},
		{ID: "b", Text: "beta"},
		{ID: "c", Text: "gamma"},
	}
	results := []SearchChannelResult{
		{Chunks: []RetrievedChunk{{ID: "a", Text: "alpha"}, {ID: "b", Text: "beta"}}},
		{Chunks: []RetrievedChunk{{ID: "b", Text: "beta"}, {ID: "c", Text: "gamma"}}},
	}

	result := pp.Process(chunks, results)
	if len(result) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(result))
	}
	if result[0].ID != "b" || result[1].ID != "a" || result[2].ID != "c" {
		t.Fatalf("unexpected RRF order: %+v", result)
	}
	if !(result[0].Score > result[1].Score && result[1].Score > result[2].Score) {
		t.Fatalf("unexpected RRF scores: %+v", result)
	}
}

func TestFusionPostProcessor_UsesConfiguredChannelWeights(t *testing.T) {
	pp := NewFusionPostProcessorWithWeights(60, 0, FusionChannelWeights{
		Vector:  1,
		Keyword: 3,
	})
	chunks := []RetrievedChunk{
		{ID: "a", Text: "alpha"},
		{ID: "b", Text: "beta"},
	}
	results := []SearchChannelResult{
		{ChannelType: ChannelVectorGlobal, Chunks: []RetrievedChunk{{ID: "a"}, {ID: "b"}}},
		{ChannelType: ChannelKeyword, Chunks: []RetrievedChunk{{ID: "b"}, {ID: "a"}}},
	}

	result := pp.Process(chunks, results)
	if result[0].ID != "b" {
		t.Fatalf("expected keyword weight to promote b, got %+v", result)
	}
	if result[0].Score <= result[1].Score {
		t.Fatalf("expected weighted RRF scores to be ordered, got %+v", result)
	}
}

func TestFusionPostProcessor_TruncatesRerankCandidates(t *testing.T) {
	pp := NewFusionPostProcessorWithLimit(60, 2)
	chunks := []RetrievedChunk{
		{ID: "a", Text: "alpha"},
		{ID: "b", Text: "beta"},
		{ID: "c", Text: "gamma"},
		{ID: "d", Text: "delta"},
	}
	results := []SearchChannelResult{
		{Chunks: []RetrievedChunk{{ID: "a", Text: "alpha"}, {ID: "b", Text: "beta"}, {ID: "c", Text: "gamma"}, {ID: "d", Text: "delta"}}},
		{Chunks: []RetrievedChunk{{ID: "b", Text: "beta"}, {ID: "a", Text: "alpha"}, {ID: "d", Text: "delta"}, {ID: "c", Text: "gamma"}}},
	}

	result := pp.Process(chunks, results)
	if len(result) != 2 {
		t.Fatalf("expected candidates to be truncated to 2, got %+v", result)
	}
	if result[0].ID != "a" || result[1].ID != "b" {
		t.Fatalf("expected highest RRF candidates to be retained, got %+v", result)
	}
}

func TestFusionPostProcessor_SingleChannelKeepsOriginalScoresAndTruncates(t *testing.T) {
	pp := NewFusionPostProcessorWithLimit(60, 2)
	chunks := []RetrievedChunk{
		{ID: "a", Text: "alpha", Score: 0.9},
		{ID: "b", Text: "beta", Score: 0.7},
		{ID: "c", Text: "gamma", Score: 0.5},
	}
	results := []SearchChannelResult{{Chunks: chunks}}

	result := pp.Process(chunks, results)
	if len(result) != 2 {
		t.Fatalf("expected candidates to be truncated to 2, got %+v", result)
	}
	if result[0].ID != "a" || result[0].Score != 0.9 || result[1].ID != "b" || result[1].Score != 0.7 {
		t.Fatalf("expected single channel scores/order to be preserved, got %+v", result)
	}
}

func TestMultiChannelRetrieverImplementsRetriever(t *testing.T) {
	channels := []SearchChannel{
		&testChannel{name: "vector", priority: 1, enabled: true, typ: ChannelVectorGlobal, chunks: []RetrievedChunk{
			{ID: "chunk-1", Text: "vector result"},
		}},
		&testChannel{name: "keyword", priority: 2, enabled: true, typ: ChannelKeyword, chunks: []RetrievedChunk{
			{ID: "chunk-2", Text: "keyword result"},
		}},
	}
	retriever := NewMultiChannelRetriever(
		NewMultiChannelRetrievalEngine(channels, []SearchResultPostProcessor{&DedupPostProcessor{}}),
	)

	chunks, err := retriever.Retrieve(context.Background(), "会员Agent能力", 5)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected two chunks, got %+v", chunks)
	}
}

func TestMultiChannelRetrievalEngineRetrieveWithResultTracksDirectedScope(t *testing.T) {
	engine := NewMultiChannelRetrievalEngine([]SearchChannel{
		&testChannel{name: "scope", enabled: true, typ: ChannelIntentDirected},
	}, nil)
	engine.SetRetrievalScopeOptions(0.6, 0.4)

	result, err := engine.RetrieveWithResult(context.Background(), SearchContext{
		OriginalQuestion: "会员等级规则",
		Intents: []SubQuestionIntent{{NodeScores: []NodeScore{{
			Node:  IntentNode{ID: "intent-member", Kind: IntentKindKB, CollectionName: "member"},
			Score: 0.92,
		}}}},
	})
	if err != nil {
		t.Fatalf("retrieve with result: %v", err)
	}
	if _, ok := result.DirectedIntentIDs["intent-member"]; !ok {
		t.Fatalf("expected high-confidence KB intent to be marked directed, got %v", result.DirectedIntentIDs)
	}

	globalResult, err := engine.RetrieveWithResult(context.Background(), SearchContext{
		OriginalQuestion: "会员等级规则",
		Intents: []SubQuestionIntent{{NodeScores: []NodeScore{{
			Node:  IntentNode{ID: "intent-member", Kind: IntentKindKB, CollectionName: "member"},
			Score: 0.45,
		}}}},
	})
	if err != nil {
		t.Fatalf("global fallback retrieve with result: %v", err)
	}
	if len(globalResult.DirectedIntentIDs) != 0 {
		t.Fatalf("low-confidence retrieval should be global fallback, got %v", globalResult.DirectedIntentIDs)
	}
}

func TestMultiChannelRetrievalEngine_UsesFusionPostProcessor(t *testing.T) {
	channels := []SearchChannel{
		&testChannel{name: "keyword", priority: 1, enabled: true, typ: ChannelKeyword, chunks: []RetrievedChunk{
			{ID: "a", Text: "alpha"},
			{ID: "b", Text: "beta"},
		}},
		&testChannel{name: "vector", priority: 2, enabled: true, typ: ChannelVectorGlobal, chunks: []RetrievedChunk{
			{ID: "b", Text: "beta"},
			{ID: "c", Text: "gamma"},
		}},
	}

	engine := NewMultiChannelRetrievalEngine(channels, []SearchResultPostProcessor{
		&DedupPostProcessor{},
		NewFusionPostProcessor(60),
	})

	chunks, err := engine.Retrieve(context.Background(), SearchContext{OriginalQuestion: "test", TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %+v", chunks)
	}
	if chunks[0].ID != "b" || chunks[1].ID != "a" || chunks[2].ID != "c" {
		t.Fatalf("expected fused order b,a,c, got %+v", chunks)
	}
}

func TestMultiChannelRetrievalEngine_RunsChannelsConcurrently(t *testing.T) {
	channels := []SearchChannel{
		&testChannel{
			name:     "slow-1",
			priority: 1,
			enabled:  true,
			typ:      ChannelKeyword,
			chunks:   []RetrievedChunk{{ID: "a", Text: "alpha"}},
			delay:    120 * time.Millisecond,
		},
		&testChannel{
			name:     "slow-2",
			priority: 2,
			enabled:  true,
			typ:      ChannelVectorGlobal,
			chunks:   []RetrievedChunk{{ID: "b", Text: "beta"}},
			delay:    120 * time.Millisecond,
		},
	}

	start := time.Now()
	engine := NewMultiChannelRetrievalEngine(channels, nil)
	chunks, err := engine.Retrieve(context.Background(), SearchContext{OriginalQuestion: "test", TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %+v", chunks)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("expected concurrent retrieval to finish quickly, got %s", elapsed)
	}
}

func TestMultiChannelRetrievalEngine_EnforcesChannelTimeout(t *testing.T) {
	channels := []SearchChannel{
		&testChannel{
			name:     "fast",
			priority: 1,
			enabled:  true,
			typ:      ChannelKeyword,
			chunks:   []RetrievedChunk{{ID: "fast", Text: "fast"}},
		},
		&testChannel{
			name:     "slow",
			priority: 2,
			enabled:  true,
			typ:      ChannelGraph,
			chunks:   []RetrievedChunk{{ID: "slow", Text: "slow"}},
			delay:    300 * time.Millisecond,
		},
	}

	engine := NewMultiChannelRetrievalEngine(channels, nil)
	engine.SetChannelTimeout(80 * time.Millisecond)

	start := time.Now()
	chunks, err := engine.Retrieve(context.Background(), SearchContext{OriginalQuestion: "test", TopK: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 || chunks[0].ID != "fast" {
		t.Fatalf("expected timeout to drop slow channel only, got %+v", chunks)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("expected timeout to finish quickly, got %s", elapsed)
	}
}

func TestFusionPostProcessor_MarksChannelAttributionOnChunks(t *testing.T) {
	// 对齐 Java ChannelAttribution：融合阶段按 chunk key 反查通道并标记归属，供 Rerank 存活率日志使用
	pp := NewFusionPostProcessor(60)
	chunks := []RetrievedChunk{
		{ID: "a", Text: "alpha"},
		{ID: "b", Text: "beta"},
	}
	results := []SearchChannelResult{
		{ChannelType: ChannelVectorGlobal, Chunks: []RetrievedChunk{{ID: "a", Text: "alpha"}}},
		{ChannelType: ChannelKeyword, Chunks: []RetrievedChunk{{ID: "a", Text: "alpha"}, {ID: "b", Text: "beta"}}},
	}

	result := pp.Process(chunks, results)
	byID := make(map[string]RetrievedChunk, len(result))
	for _, chunk := range result {
		byID[chunk.ID] = chunk
	}
	if byID["a"].Metadata["retrieval_channel"] != string(ChannelVectorGlobal) {
		t.Fatalf("expected vector attribution on a, got %+v", byID["a"].Metadata)
	}
	if byID["b"].Metadata["retrieval_channel"] != string(ChannelKeyword) {
		t.Fatalf("expected keyword attribution on b, got %+v", byID["b"].Metadata)
	}
}

func TestFormatChannelCountsRendersChineseLabels(t *testing.T) {
	counts := map[string]int{
		string(ChannelKeyword): 6,
		string(ChannelGraph):   8,
	}
	if got := formatChannelCounts(counts); got != "图谱=8 关键词=6" {
		t.Fatalf("unexpected formatted counts: %q", got)
	}
	if got := formatChannelCounts(nil); got != "无" {
		t.Fatalf("expected empty marker, got %q", got)
	}
}
