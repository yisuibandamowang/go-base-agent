package rag

import (
	"context"
	"errors"
	"reflect"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type recordingEmbeddingService struct {
	modelIDs []string
	failures map[string]error
}

func (s *recordingEmbeddingService) Embed(context.Context, string) ([]float32, error) {
	s.modelIDs = append(s.modelIDs, "")
	return []float32{0.1, 0.2, 0.3}, nil
}

func (s *recordingEmbeddingService) EmbedWithModel(_ context.Context, _ string, modelID string) ([]float32, error) {
	s.modelIDs = append(s.modelIDs, modelID)
	if err := s.failures[modelID]; err != nil {
		return nil, err
	}
	return []float32{0.1, 0.2, 0.3}, nil
}

func (s *recordingEmbeddingService) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func (s *recordingEmbeddingService) EmbedBatchWithModel(context.Context, []string, string) ([][]float32, error) {
	return nil, nil
}

func (s *recordingEmbeddingService) Dimension() int {
	return 3
}

type fakeKnowledgeBaseLister struct {
	kbs []knowledgeModel.KnowledgeBase
}

func (l fakeKnowledgeBaseLister) List(context.Context, int, int) ([]knowledgeModel.KnowledgeBase, int64, error) {
	return l.kbs, int64(len(l.kbs)), nil
}

func TestPgRetriever_EmbedsQuestionWithEachKnowledgeBaseModel(t *testing.T) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 user=postgres dbname=ragent sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open dry-run db: %v", err)
	}

	emb := &recordingEmbeddingService{}
	retriever := &PgRetriever{
		vectorDB: gdb,
		emb:      emb,
		kbRepo: fakeKnowledgeBaseLister{kbs: []knowledgeModel.KnowledgeBase{
			{Name: "kb-1", EmbeddingModel: "emb-1536-a", CollectionName: "collection_a"},
			{Name: "kb-2", EmbeddingModel: "emb-1536-b", CollectionName: "collection_b"},
		}},
		topK: 10,
	}

	_, _ = retriever.Retrieve(context.Background(), "question", 2)

	want := []string{"emb-1536-a", "emb-1536-b"}
	if !reflect.DeepEqual(emb.modelIDs, want) {
		t.Fatalf("embedding models mismatch: got %v, want %v", emb.modelIDs, want)
	}
}

func TestPgRetriever_SkipsKnowledgeBaseWhenEmbeddingFails(t *testing.T) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 user=postgres dbname=ragent sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open dry-run db: %v", err)
	}

	emb := &recordingEmbeddingService{
		failures: map[string]error{
			"bad-emb": errors.New("embedding HTTP 401: invalid token"),
		},
	}
	retriever := &PgRetriever{
		vectorDB: gdb,
		emb:      emb,
		kbRepo: fakeKnowledgeBaseLister{kbs: []knowledgeModel.KnowledgeBase{
			{Name: "bad-kb", EmbeddingModel: "bad-emb", CollectionName: "collection_bad"},
			{Name: "good-kb", EmbeddingModel: "good-emb", CollectionName: "collection_good"},
		}},
		topK: 10,
	}

	_, err = retriever.Retrieve(context.Background(), "question", 2)
	if err != nil {
		t.Fatalf("expected retriever to skip failed knowledge base, got %v", err)
	}

	want := []string{"bad-emb", "good-emb"}
	if !reflect.DeepEqual(emb.modelIDs, want) {
		t.Fatalf("embedding models mismatch: got %v, want %v", emb.modelIDs, want)
	}
}

func TestMetadataWithKnowledgeBaseAddsSource(t *testing.T) {
	kb := knowledgeModel.KnowledgeBase{
		Name:           "go 语言知识库",
		CollectionName: "goknowladge",
	}
	kb.ID = "kb-1"

	meta := metadataWithKnowledgeBase(map[string]string{"doc_name": "会员Agent说明.md"}, kb)

	if meta["kb_id"] != "kb-1" {
		t.Fatalf("expected kb_id, got %+v", meta)
	}
	if meta["kb_name"] != "go 语言知识库" {
		t.Fatalf("expected kb_name, got %+v", meta)
	}
	if meta["collection_name"] != "goknowladge" {
		t.Fatalf("expected collection_name, got %+v", meta)
	}
	if meta["doc_name"] != "会员Agent说明.md" {
		t.Fatalf("expected existing doc metadata to be preserved, got %+v", meta)
	}
}
