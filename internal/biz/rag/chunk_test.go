package rag

import (
	"testing"
)

func TestFixedSizeChunker(t *testing.T) {
	f := &FixedSizeChunker{}
	if f.Mode() != ChunkModeFixedSize {
		t.Fatal("unexpected mode")
	}
}

func TestFixedSizeChunker_SingleChunk(t *testing.T) {
	f := &FixedSizeChunker{}
	chunks := f.Chunk("hello", ChunkingOptions{ChunkSize: 100, OverlapSize: 20})
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Content != "hello" {
		t.Fatalf("unexpected content: %s", chunks[0].Content)
	}
	if chunks[0].Index != 0 {
		t.Fatal("first chunk should be index 0")
	}
}

func TestFixedSizeChunker_MultiChunk(t *testing.T) {
	f := &FixedSizeChunker{}
	text := "0123456789" // 10 chars, chunkSize=4, overlap=2 → step=2
	chunks := f.Chunk(text, ChunkingOptions{ChunkSize: 4, OverlapSize: 2, MinChunkSize: 0})

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	// first chunk: 0-4 = "0123", second: 2-6 = "2345", etc.
	if chunks[0].Content != "0123" {
		t.Fatalf("unexpected first chunk: %s", chunks[0].Content)
	}
}

func TestFixedSizeChunker_Empty(t *testing.T) {
	f := &FixedSizeChunker{}
	chunks := f.Chunk("", DefaultChunkingOptions())
	if len(chunks) != 0 {
		t.Fatal("expected empty chunks for empty text")
	}
}

func TestDefaultChunkingOptions(t *testing.T) {
	opts := DefaultChunkingOptions()
	if opts.ChunkSize != 512 {
		t.Fatalf("unexpected chunk size: %d", opts.ChunkSize)
	}
}

func TestNoopChunker(t *testing.T) {
	n := &NoopChunker{}
	chunks := n.Chunk("hello world", DefaultChunkingOptions())
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Content != "hello world" {
		t.Fatalf("unexpected content: %s", chunks[0].Content)
	}
}

func TestNoopChunker_Empty(t *testing.T) {
	n := &NoopChunker{}
	chunks := n.Chunk("   ", DefaultChunkingOptions())
	if len(chunks) != 0 {
		t.Fatal("expected empty for whitespace")
	}
}
