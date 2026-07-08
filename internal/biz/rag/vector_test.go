package rag

import (
	"context"
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
