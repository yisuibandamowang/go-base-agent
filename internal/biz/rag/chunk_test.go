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

	chunks := chunker.ChunkBlocks(blocks, ChunkingOptions{ChunkSize: 1000, OverlapSize: 0})
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
		{Type: BlockTable, Provenance: Provenance{SourceFile: "会员.xlsx", SheetName: "权益表"}, Headers: []string{"能力"}, Rows: [][]string{{"权益查询"}}},
	}, ChunkingOptions{ChunkSize: -1})
	if len(chunks) != 1 {
		t.Fatalf("expected whole document chunk, got %d", len(chunks))
	}
	if !containsAll(chunks[0].Content, "# 标题", "权益查询") {
		t.Fatalf("unexpected whole document content: %q", chunks[0].Content)
	}
	if chunks[0].Provenance.SourceFile != "会员.xlsx" || chunks[0].Provenance.SheetName != "权益表" {
		t.Fatalf("expected whole document provenance, got %+v", chunks[0].Provenance)
	}
	if chunks[0].Metadata["source_file"] != "会员.xlsx" || chunks[0].Metadata["sheet_name"] != "权益表" {
		t.Fatalf("expected whole document provenance metadata, got %+v", chunks[0].Metadata)
	}
}

func TestStructureAwareChunkerAddsHeadingOutlineToFollowingChunks(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{Type: BlockHeading, Level: 1, Content: "会员中心"},
		{Type: BlockHeading, Level: 2, Content: "权益查询"},
		{Type: BlockParagraph, Content: "支持查询会员权益。"},
		{Type: BlockHeading, Level: 2, Content: "积分查询"},
		{Type: BlockTable, Headers: []string{"能力", "说明"}, Rows: [][]string{{"积分", "支持"}}},
	}, ChunkingOptions{ChunkSize: 1000, OverlapSize: 0})
	if len(chunks) != 2 {
		t.Fatalf("expected heading blocks to only annotate following chunks, got %+v", chunks)
	}
	if got := chunks[0].Metadata["outline_path"]; got != "会员中心 > 权益查询" {
		t.Fatalf("expected paragraph outline path, got %q in %+v", got, chunks[0].Metadata)
	}
	if got := chunks[1].Metadata["outline_path"]; got != "会员中心 > 积分查询" {
		t.Fatalf("expected table outline path, got %q in %+v", got, chunks[1].Metadata)
	}
}

func TestStructureAwareChunkerPacksAdjacentTextBlocks(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{Type: BlockParagraph, Content: "第一段说明。"},
		{Type: BlockImage, Caption: "架构图", Description: "会员中心架构图", Asset: AssetRef{PublicURL: "https://example.com/a.png"}},
		{Type: BlockParagraph, Content: "第二段说明。"},
	}, ChunkingOptions{ChunkSize: 1000, OverlapSize: 0})
	if len(chunks) != 1 {
		t.Fatalf("expected adjacent mergeable blocks to be packed, got %+v", chunks)
	}
	if !containsAll(chunks[0].Content, "第一段说明。", "会员中心架构图", "![架构图](https://example.com/a.png)", "第二段说明。") {
		t.Fatalf("packed chunk lost content: %q", chunks[0].Content)
	}
	if chunks[0].Metadata["block_type"] != "mixed" {
		t.Fatalf("expected mixed packed chunk metadata, got %+v", chunks[0].Metadata)
	}
}

func TestStructureAwareChunkerSplitsLongListsByItemsPerChunk(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{Type: BlockList, Ordered: true, Items: []string{
			"能力一", "能力二", "能力三", "能力四", "能力五",
		}},
	}, ChunkingOptions{ChunkSize: 14, OverlapSize: 0, MaxListItems: 3, ListItemsPerChunk: 2})
	if len(chunks) != 3 {
		t.Fatalf("expected long list to split into 3 chunks, got %+v", chunks)
	}
	if chunks[0].Content != "1. 能力一\n2. 能力二" {
		t.Fatalf("unexpected first list chunk: %q", chunks[0].Content)
	}
	if chunks[1].Content != "3. 能力三\n4. 能力四" {
		t.Fatalf("unexpected second list chunk: %q", chunks[1].Content)
	}
	if chunks[2].Content != "5. 能力五" {
		t.Fatalf("unexpected third list chunk: %q", chunks[2].Content)
	}
	for _, chunk := range chunks {
		if chunk.BlockType != string(BlockList) || chunk.Metadata["block_type"] != string(BlockList) {
			t.Fatalf("expected list block type, got %+v", chunk)
		}
	}
}

func TestStructureAwareChunkerCarriesStructuredChunkPayload(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{Type: BlockHeading, Level: 1, Content: "会员中心"},
		{Type: BlockParagraph, Content: "查看下面架构图。"},
		{
			Type:        BlockImage,
			Caption:     "架构图",
			Description: "会员中心架构图",
			Asset:       AssetRef{PublicURL: "https://example.com/a.png", Mime: "image/png", SourceBlockID: "img-1"},
		},
	}, ChunkingOptions{ChunkSize: 1000, OverlapSize: 0})
	if len(chunks) != 1 {
		t.Fatalf("expected packed chunk, got %+v", chunks)
	}
	chunk := chunks[0]
	if chunk.BlockType != "mixed" || chunk.Metadata["block_type"] != "mixed" {
		t.Fatalf("expected block type to be carried on struct and metadata, got %+v", chunk)
	}
	if len(chunk.OutlinePath) != 1 || chunk.OutlinePath[0] != "会员中心" || chunk.Metadata["outline_path"] != "会员中心" {
		t.Fatalf("expected outline path on struct and metadata, got %+v", chunk)
	}
	if len(chunk.Assets) != 1 || chunk.Assets[0].PublicURL != "https://example.com/a.png" {
		t.Fatalf("expected image asset to be carried, got %+v", chunk.Assets)
	}
	if len(chunk.SourceBlockIDs) != 2 || chunk.SourceBlockIDs[0] == "" || chunk.SourceBlockIDs[1] != "img-1" {
		t.Fatalf("expected paragraph fallback and image source block ids to be carried, got %+v", chunk.SourceBlockIDs)
	}
	if strings.Contains(chunk.EmbeddingText, "https://example.com/a.png") || !strings.Contains(chunk.EmbeddingText, "会员中心架构图") {
		t.Fatalf("expected merged embedding text to keep image description without URL noise, got %q", chunk.EmbeddingText)
	}
}

func TestStructureAwareChunkerCarriesSourceBlockIDs(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{ID: "p-1", Type: BlockParagraph, Content: "段落说明。"},
		{ID: "list-1", Type: BlockList, Items: []string{"能力一", "能力二"}},
		{ID: "table-1", Type: BlockTable, Headers: []string{"能力"}, Rows: [][]string{{"权益"}}},
	}, ChunkingOptions{ChunkSize: 1000, OverlapSize: 0})
	if len(chunks) != 2 {
		t.Fatalf("expected paragraph/list packed plus table chunk, got %+v", chunks)
	}
	if got := strings.Join(chunks[0].SourceBlockIDs, ","); got != "p-1,list-1" {
		t.Fatalf("expected packed source block ids, got %+v", chunks[0].SourceBlockIDs)
	}
	if got := chunks[0].Metadata["source_block_ids"]; got != "p-1,list-1" {
		t.Fatalf("expected packed metadata source ids, got %q", got)
	}
	if got := strings.Join(chunks[1].SourceBlockIDs, ","); got != "table-1" {
		t.Fatalf("expected table source block id, got %+v", chunks[1].SourceBlockIDs)
	}
}

func TestStructureAwareChunkerCarriesProvenance(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{
			ID: "table-1", Type: BlockTable,
			Provenance: Provenance{SourceFile: "会员.xlsx", SheetName: "权益表"},
			Headers:    []string{"能力", "说明"},
			Rows:       [][]string{{"权益查询", "支持"}},
		},
	}, ChunkingOptions{ChunkSize: 1000, OverlapSize: 0})
	if len(chunks) != 1 {
		t.Fatalf("expected table chunk, got %+v", chunks)
	}
	chunk := chunks[0]
	if chunk.Provenance.SourceFile != "会员.xlsx" || chunk.Provenance.SheetName != "权益表" {
		t.Fatalf("expected provenance to be carried, got %+v", chunk.Provenance)
	}
	if !strings.Contains(chunk.SectionContext, "sheet=权益表") || !strings.Contains(chunk.Metadata["section_context"], "sheet=权益表") {
		t.Fatalf("expected section context to include sheet name, got struct=%q metadata=%q", chunk.SectionContext, chunk.Metadata["section_context"])
	}
	if chunk.Metadata["source_file"] != "会员.xlsx" || chunk.Metadata["sheet_name"] != "权益表" {
		t.Fatalf("expected provenance metadata, got %+v", chunk.Metadata)
	}
}

func TestStructureAwareChunkerGeneratesFallbackSourceBlockIDs(t *testing.T) {
	chunker := &StructureAwareChunker{}
	chunks := chunker.ChunkBlocks([]Block{
		{Type: BlockParagraph, Content: "段落说明。"},
		{Type: BlockTable, Headers: []string{"能力"}, Rows: [][]string{{"权益"}}},
	}, ChunkingOptions{ChunkSize: 1000, OverlapSize: 0})
	if len(chunks) != 2 {
		t.Fatalf("expected paragraph and table chunks, got %+v", chunks)
	}
	if len(chunks[0].SourceBlockIDs) != 1 || chunks[0].SourceBlockIDs[0] == "" {
		t.Fatalf("expected fallback paragraph source id, got %+v", chunks[0].SourceBlockIDs)
	}
	if len(chunks[1].SourceBlockIDs) != 1 || chunks[1].SourceBlockIDs[0] == "" {
		t.Fatalf("expected fallback table source id, got %+v", chunks[1].SourceBlockIDs)
	}
	if chunks[0].SourceBlockIDs[0] == chunks[1].SourceBlockIDs[0] {
		t.Fatalf("expected fallback source ids to be distinct, got %+v and %+v", chunks[0].SourceBlockIDs, chunks[1].SourceBlockIDs)
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
