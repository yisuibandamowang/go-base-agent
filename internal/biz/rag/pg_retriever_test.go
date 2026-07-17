package rag

import (
	"context"
	"errors"
	"reflect"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
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

type recordingVectorSearcher struct {
	collections []string
	results     []VectorChunk
}

func (s *recordingVectorSearcher) Search(_ context.Context, collectionName string, _ []float32, _ int) ([]VectorChunk, error) {
	s.collections = append(s.collections, collectionName)
	return s.results, nil
}

func TestPgRetriever_EmbedsQuestionWithEachKnowledgeBaseModel(t *testing.T) {
	emb := &recordingEmbeddingService{}
	searcher := &recordingVectorSearcher{}
	retriever := &PgRetriever{
		vectorSearch: searcher,
		emb:          emb,
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

	wantCollections := []string{"collection_a", "collection_b"}
	if !reflect.DeepEqual(searcher.collections, wantCollections) {
		t.Fatalf("collections mismatch: got %v, want %v", searcher.collections, wantCollections)
	}
}

func TestPgRetriever_SkipsKnowledgeBaseWhenEmbeddingFails(t *testing.T) {
	emb := &recordingEmbeddingService{
		failures: map[string]error{
			"bad-emb": errors.New("embedding HTTP 401: invalid token"),
		},
	}
	searcher := &recordingVectorSearcher{}
	retriever := &PgRetriever{
		vectorSearch: searcher,
		emb:          emb,
		kbRepo: fakeKnowledgeBaseLister{kbs: []knowledgeModel.KnowledgeBase{
			{Name: "bad-kb", EmbeddingModel: "bad-emb", CollectionName: "collection_bad"},
			{Name: "good-kb", EmbeddingModel: "good-emb", CollectionName: "collection_good"},
		}},
		topK: 10,
	}

	_, err := retriever.Retrieve(context.Background(), "question", 2)
	if err != nil {
		t.Fatalf("expected retriever to skip failed knowledge base, got %v", err)
	}

	want := []string{"bad-emb", "good-emb"}
	if !reflect.DeepEqual(emb.modelIDs, want) {
		t.Fatalf("embedding models mismatch: got %v, want %v", emb.modelIDs, want)
	}

	wantCollections := []string{"collection_good"}
	if !reflect.DeepEqual(searcher.collections, wantCollections) {
		t.Fatalf("collections mismatch: got %v, want %v", searcher.collections, wantCollections)
	}
}

func TestPgRetriever_AddsKnowledgeBaseMetadataFromVectorSearch(t *testing.T) {
	emb := &recordingEmbeddingService{}
	searcher := &recordingVectorSearcher{
		results: []VectorChunk{
			{
				ChunkID: "chunk-1",
				Content: "当前会员Agent支持错误排查能力",
				Metadata: map[string]string{
					"doc_id":     "doc-1",
					"doc_name":   "会员Agent说明.md",
					"source_url": "https://example.com/member-agent.md",
				},
			},
		},
	}
	kb := knowledgeModel.KnowledgeBase{
		Name:           "会员知识库",
		EmbeddingModel: "emb-1536",
		CollectionName: "member_agent",
	}
	kb.ID = "kb-1"
	retriever := &PgRetriever{
		vectorSearch: searcher,
		emb:          emb,
		kbRepo:       fakeKnowledgeBaseLister{kbs: []knowledgeModel.KnowledgeBase{kb}},
		topK:         10,
	}

	chunks, err := retriever.Retrieve(context.Background(), "会员Agent能力", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %+v", chunks)
	}
	meta := chunks[0].Metadata
	if meta["kb_name"] != "会员知识库" || meta["doc_name"] != "会员Agent说明.md" || meta["source_url"] != "https://example.com/member-agent.md" {
		t.Fatalf("unexpected metadata: %+v", meta)
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

func TestMetadataWithSourcesUsesDocumentNameFromDatabase(t *testing.T) {
	kb := knowledgeModel.KnowledgeBase{
		Name:           "go 语言知识库",
		CollectionName: "goknowladge",
	}
	kb.ID = "kb-1"

	meta := metadataWithSources(
		map[string]string{"doc_id": "2075677715192614912"},
		kb,
		"会员智能问答Agent当前支持能力.md",
		"https://example.com/member-agent.md",
		"",
	)

	if meta["doc_name"] != "会员智能问答Agent当前支持能力.md" {
		t.Fatalf("expected document name from database, got %+v", meta)
	}
	if meta["source_url"] != "https://example.com/member-agent.md" {
		t.Fatalf("expected source url from database, got %+v", meta)
	}
}
