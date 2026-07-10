package service

import (
	"context"
	"errors"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/rag"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type fakeKnowledgeBaseFinder struct {
	kb *knowledgeModel.KnowledgeBase
}

func (f fakeKnowledgeBaseFinder) FindByID(context.Context, string) (*knowledgeModel.KnowledgeBase, error) {
	return f.kb, nil
}

type failingVectorStore struct {
	err error
}

func (s failingVectorStore) DeleteDocumentVectors(context.Context, string, string) error {
	return nil
}

func (s failingVectorStore) IndexDocumentChunks(context.Context, string, string, []rag.VectorChunk) error {
	return s.err
}

func TestDocumentService_PersistChunksAndVectorsReturnsVectorError(t *testing.T) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 user=postgres dbname=ragent sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open dry-run db: %v", err)
	}

	vectorErr := errors.New("vector insert failed")
	svc := &DocumentService{
		db: gdb,
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			CollectionName: "collection_a",
		}},
		vecStore: failingVectorStore{err: vectorErr},
	}

	_, err = svc.persistChunksAndVectors(context.Background(), &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		CreatedBy: "user-1",
	}, []rag.VectorChunk{
		{ChunkID: "chunk-1", Content: "content", Embedding: []float32{0.1, 0.2}, Index: 0},
	})

	if !errors.Is(err, vectorErr) {
		t.Fatalf("expected vector error, got %v", err)
	}
}
