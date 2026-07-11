package service

import (
	"context"
	"errors"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/rag"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeFileReader struct {
	data []byte
}

func (f fakeFileReader) Read(string) ([]byte, error) {
	return f.data, nil
}

type fakeEmbeddingService struct{}

func (f fakeEmbeddingService) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (f fakeEmbeddingService) EmbedWithModel(context.Context, string, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (f fakeEmbeddingService) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func (f fakeEmbeddingService) EmbedBatchWithModel(context.Context, []string, string) ([][]float32, error) {
	return nil, nil
}

func (f fakeEmbeddingService) Dimension() int {
	return 2
}

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

type capturingVectorStore struct {
	chunks []rag.VectorChunk
}

func (s *capturingVectorStore) DeleteDocumentVectors(context.Context, string, string) error {
	return nil
}

func (s *capturingVectorStore) IndexDocumentChunks(_ context.Context, _ string, _ string, chunks []rag.VectorChunk) error {
	s.chunks = append([]rag.VectorChunk(nil), chunks...)
	return nil
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

func TestDocumentService_PersistChunksAndVectorsUsesPersistedChunkIDs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		db: gdb,
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			CollectionName: "collection_a",
		}},
		vecStore: vecStore,
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		CreatedBy: "user-1",
	}
	doc.ID = "doc-1"

	_, err = svc.persistChunksAndVectors(context.Background(), doc, []rag.VectorChunk{
		{Content: "content 1", Embedding: []float32{0.1, 0.2}, Index: 0},
		{Content: "content 2", Embedding: []float32{0.3, 0.4}, Index: 1},
	})
	if err != nil {
		t.Fatalf("persist chunks and vectors: %v", err)
	}

	if len(vecStore.chunks) != 2 {
		t.Fatalf("expected 2 vector chunks, got %d", len(vecStore.chunks))
	}
	seen := make(map[string]bool)
	for _, c := range vecStore.chunks {
		if c.ChunkID == "" {
			t.Fatal("expected vector chunk ID to be populated from persisted chunk ID")
		}
		if seen[c.ChunkID] {
			t.Fatalf("expected unique vector chunk IDs, got duplicate %q", c.ChunkID)
		}
		seen[c.ChunkID] = true
	}
}

func TestDocumentService_PersistChunksAndVectorsCompletesDocumentMetadata(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		db: gdb,
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			CollectionName: "collection_a",
		}},
		vecStore: vecStore,
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:           "kb-1",
		DocName:        "会员智能问答Agent当前支持能力.md",
		SourceType:     "url",
		SourceLocation: "https://example.com/member-agent.md",
		CreatedBy:      "user-1",
	}
	doc.ID = "doc-1"

	_, err = svc.persistChunksAndVectors(context.Background(), doc, []rag.VectorChunk{
		{Content: "content", Embedding: []float32{0.1, 0.2}, Index: 0},
	})
	if err != nil {
		t.Fatalf("persist chunks and vectors: %v", err)
	}

	if len(vecStore.chunks) != 1 {
		t.Fatalf("expected 1 vector chunk, got %d", len(vecStore.chunks))
	}
	meta := vecStore.chunks[0].Metadata
	if meta["doc_id"] != "doc-1" {
		t.Fatalf("expected doc_id metadata, got %+v", meta)
	}
	if meta["doc_name"] != "会员智能问答Agent当前支持能力.md" {
		t.Fatalf("expected doc_name metadata, got %+v", meta)
	}
	if meta["source_type"] != "url" {
		t.Fatalf("expected source_type metadata, got %+v", meta)
	}
	if meta["source_url"] != "https://example.com/member-agent.md" {
		t.Fatalf("expected source_url metadata, got %+v", meta)
	}
}

func TestDocumentService_RunChunkProcessBuildsSourceMetadata(t *testing.T) {
	svc := &DocumentService{
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			EmbeddingModel: "emb-1",
		}},
		emb:       fakeEmbeddingService{},
		fileStore: fakeFileReader{data: []byte("第一行\n第二行\n第三行")},
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent说明.md",
		FileURL:    "https://example.com/member-agent.md",
		SourceType: "url",
	}
	doc.ID = "doc-1"

	_, _, _, chunks, err := svc.runChunkProcess(context.Background(), doc)
	if err != nil {
		t.Fatalf("run chunk process: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	meta := chunks[0].Metadata
	if meta["doc_id"] != "doc-1" {
		t.Fatalf("expected doc_id metadata, got %q", meta["doc_id"])
	}
	if meta["doc_name"] != "会员Agent说明.md" {
		t.Fatalf("expected doc_name metadata, got %q", meta["doc_name"])
	}
	if meta["source_url"] != "https://example.com/member-agent.md" {
		t.Fatalf("expected source_url metadata, got %q", meta["source_url"])
	}
	if meta["page_start"] != "1" || meta["line_start"] != "1" || meta["line_end"] != "3" {
		t.Fatalf("expected page/line metadata, got %+v", meta)
	}
}
