package rag

import (
	"context"
	"strings"
	"testing"
)

func TestNoopVectorStoreService(t *testing.T) {
	s := &NoopVectorStore{}

	err := s.IndexDocumentChunks(context.Background(), "test", "doc-1", []VectorChunk{
		{ChunkID: "c1", Content: "hello", Embedding: []float32{1.0}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = s.UpdateChunk(context.Background(), "test", "doc-1", VectorChunk{ChunkID: "c1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = s.DeleteDocumentVectors(context.Background(), "test", "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = s.DeleteChunkByID(context.Background(), "test", "c1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = s.DeleteChunksByIDs(context.Background(), "test", []string{"c1", "c2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNoopVectorStoreAdmin(t *testing.T) {
	s := &NoopVectorStore{}

	err := s.EnsureVectorSpace(context.Background(), VectorSpaceSpec{
		SpaceID:   VectorSpaceID{Name: "test"},
		Dimension: 1536,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exists, err := s.VectorSpaceExists(context.Background(), VectorSpaceID{Name: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("noop should return true")
	}

	err = s.DropVectorSpace(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildVectorMetadataIncludesChunkMetadata(t *testing.T) {
	meta := buildVectorMetadata("doc-1", VectorChunk{
		Index: 3,
		Metadata: map[string]string{
			"doc_name":   "会员Agent说明.md",
			"source_url": "https://example.com/member-agent.md",
			"line_start": "10",
			"line_end":   "16",
		},
	})

	for _, want := range []string{
		`"doc_id":"doc-1"`,
		`"index":3`,
		`"doc_name":"会员Agent说明.md"`,
		`"source_url":"https://example.com/member-agent.md"`,
		`"line_start":"10"`,
		`"line_end":"16"`,
	} {
		if !strings.Contains(meta, want) {
			t.Fatalf("expected metadata to contain %s, got %s", want, meta)
		}
	}
}

func TestBuildVectorMetadataIncludesStructuredChunkFields(t *testing.T) {
	meta := buildVectorMetadata("doc-1", VectorChunk{
		Index:          2,
		BlockType:      "image",
		OutlinePath:    []string{"会员中心", "权益查询"},
		Provenance:     Provenance{SourceFile: "会员.xlsx", SheetName: "权益表"},
		SectionContext: "caption=架构图",
		SourceBlockIDs: []string{"img-1", "img-2"},
		Assets: []AssetRef{
			{PublicURL: "https://example.com/a.png", Mime: "image/png", SourceBlockID: "img-1"},
		},
	})

	for _, want := range []string{
		`"block_type":"image"`,
		`"outline_path":"会员中心 > 权益查询"`,
		`"source_file":"会员.xlsx"`,
		`"sheet_name":"权益表"`,
		`"section_context":"caption=架构图"`,
		`"source_block_ids":"img-1,img-2"`,
		`"asset_urls":"https://example.com/a.png"`,
	} {
		if !strings.Contains(meta, want) {
			t.Fatalf("expected metadata to contain %s, got %s", want, meta)
		}
	}
}

func TestApplyStructuredMetadataRestoresProvenance(t *testing.T) {
	chunk := VectorChunk{Metadata: map[string]string{
		"block_type":       "table",
		"outline_path":     "会员中心 > 权益查询",
		"source_file":      "会员.xlsx",
		"sheet_name":       "权益表",
		"section_context":  "sheet=权益表",
		"source_block_ids": "table-1,table-2",
	}}

	applyStructuredMetadata(&chunk)

	if chunk.Provenance.SourceFile != "会员.xlsx" || chunk.Provenance.SheetName != "权益表" {
		t.Fatalf("expected provenance read-back, got %+v", chunk.Provenance)
	}
	if chunk.BlockType != "table" || len(chunk.OutlinePath) != 2 || chunk.OutlinePath[1] != "权益查询" {
		t.Fatalf("expected structured metadata read-back, got %+v", chunk)
	}
	if len(chunk.SourceBlockIDs) != 2 || chunk.SourceBlockIDs[1] != "table-2" {
		t.Fatalf("expected source block ids read-back, got %+v", chunk.SourceBlockIDs)
	}
}

func TestStringToVecParsesPgVectorLiteral(t *testing.T) {
	got := stringToVec("[0.100000, -2.5,3]")
	want := []float32{0.1, -2.5, 3}
	if len(got) != len(want) {
		t.Fatalf("expected %d values, got %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: expected %v, got %v", i, want[i], got[i])
		}
	}
}

func TestStringToVecInvalidLiteral(t *testing.T) {
	if got := stringToVec("[0.1,not-a-number]"); got != nil {
		t.Fatalf("expected nil for invalid vector literal, got %+v", got)
	}
}

func TestPgVectorSearchTuningStatementsMatchJavaDefaults(t *testing.T) {
	statements := pgVectorSearchTuningStatements()
	want := []string{
		"SET hnsw.ef_search = 200",
		"SET hnsw.iterative_scan = relaxed_order",
	}
	if len(statements) != len(want) {
		t.Fatalf("expected %d tuning statements, got %v", len(want), statements)
	}
	for i := range want {
		if statements[i] != want[i] {
			t.Fatalf("statement %d mismatch: got %q, want %q", i, statements[i], want[i])
		}
	}
}
