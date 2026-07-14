package ingestion

import (
	"context"
	"strings"
	"testing"

	"go-base-agent/internal/biz/core/parser"
	"go-base-agent/internal/biz/rag"
)

type capturingVectorStore struct {
	chunks []rag.VectorChunk
}

func (s *capturingVectorStore) IndexDocumentChunks(_ context.Context, _ string, _ string, chunks []rag.VectorChunk) error {
	s.chunks = append([]rag.VectorChunk(nil), chunks...)
	return nil
}

func (s *capturingVectorStore) UpdateChunk(context.Context, string, string, rag.VectorChunk) error {
	return nil
}

func (s *capturingVectorStore) DeleteDocumentVectors(context.Context, string, string) error {
	return nil
}

func (s *capturingVectorStore) DeleteChunkByID(context.Context, string, string) error {
	return nil
}

func (s *capturingVectorStore) DeleteChunksByIDs(context.Context, string, []string) error {
	return nil
}

func TestDefaultEngineUsesStructuredChunks(t *testing.T) {
	store := &capturingVectorStore{}
	engine := NewDefaultEngine(parser.DefaultRegistry(), store, func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, 0, len(texts))
		for range texts {
			out = append(out, []float32{0.1, 0.2})
		}
		return out, nil
	})

	chunks, err := engine.Run(context.Background(), "task-1", "collection_a", "doc-1", []byte("能力,说明\n权益查询,支持\n积分查询,支持"), "text/csv")
	if err != nil {
		t.Fatalf("run engine: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 structured table chunk, got %d", len(chunks))
	}
	if len(store.chunks) != 1 {
		t.Fatalf("expected 1 stored chunk, got %d", len(store.chunks))
	}
	content := chunks[0].Content
	if !containsAll(content, "能力 | 说明", "权益查询 | 支持", "积分查询 | 支持") {
		t.Fatalf("expected full table content, got %q", content)
	}
	if chunks[0].Metadata["block_type"] != string(rag.BlockTable) {
		t.Fatalf("expected table block metadata, got %+v", chunks[0].Metadata)
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
