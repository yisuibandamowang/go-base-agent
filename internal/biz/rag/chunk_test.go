package rag

import (
	"strings"
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

func TestStructureAwareChunkerKeepsTableTogether(t *testing.T) {
	chunker := &StructureAwareChunker{}
	blocks := []Block{
		{Type: BlockHeading, Level: 1, Content: "会员 Agent"},
		{Type: BlockTable, Headers: []string{"能力", "说明"}, Rows: [][]string{{"权益查询", "支持"}, {"积分查询", "支持"}}},
		{Type: BlockParagraph, Content: "错误排查能力。"},
	}

	chunks := chunker.ChunkBlocks(blocks, ChunkingOptions{ChunkSize: 30, OverlapSize: 0})
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %+v", chunks)
	}
	tableCount := 0
	for _, chunk := range chunks {
		if chunk.Metadata["block_type"] == string(BlockTable) {
			tableCount++
			if !containsAll(chunk.Content, "能力 | 说明", "权益查询 | 支持", "积分查询 | 支持") {
				t.Fatalf("table chunk lost content: %q", chunk.Content)
			}
		}
	}
	if tableCount != 1 {
		t.Fatalf("expected exactly one table chunk, got %d in %+v", tableCount, chunks)
	}
}

func TestStructureAwareChunkerWholeDocumentSentinel(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{Type: BlockHeading, Level: 1, Content: "标题"},
		{Type: BlockParagraph, Content: "正文"},
	}, ChunkingOptions{ChunkSize: -1})
	if len(chunks) != 1 {
		t.Fatalf("expected whole document chunk, got %d", len(chunks))
	}
	if !containsAll(chunks[0].Content, "# 标题", "正文") {
		t.Fatalf("unexpected whole document content: %q", chunks[0].Content)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
