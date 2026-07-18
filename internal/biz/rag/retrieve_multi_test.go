package rag

import (
	"context"
	"testing"
)

type testChannel struct {
	name     string
	priority int
	enabled  bool
	typ      SearchChannelType
	chunks   []RetrievedChunk
}

func (c *testChannel) Name() string                     { return c.name }
func (c *testChannel) Priority() int                    { return c.priority }
func (c *testChannel) IsEnabled(ctx SearchContext) bool { return c.enabled }
func (c *testChannel) Type() SearchChannelType          { return c.typ }
func (c *testChannel) Search(ctx context.Context, sc SearchContext) (SearchChannelResult, error) {
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
