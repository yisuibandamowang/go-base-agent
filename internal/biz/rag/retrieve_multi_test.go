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
