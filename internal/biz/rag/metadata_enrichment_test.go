package rag

import (
	"context"
	"reflect"
	"testing"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeChunkMetadataResolver struct {
	ids  []string
	meta map[string]ChunkMetadata
}

func (r *fakeChunkMetadataResolver) ResolveChunkMetadata(_ context.Context, ids []string) (map[string]ChunkMetadata, error) {
	r.ids = append(r.ids, ids...)
	return r.meta, nil
}

func TestMetadataEnrichingRetrieverFillsChunkDocumentMetadata(t *testing.T) {
	resolver := &fakeChunkMetadataResolver{meta: map[string]ChunkMetadata{
		"chunk-1": {DocID: "doc-1", ChunkIndex: 3, DocName: "会员说明.md"},
	}}
	retriever := NewMetadataEnrichingRetriever(staticRetriever{chunks: []RetrievedChunk{
		{ID: "chunk-1", Text: "会员等级说明", Score: 0.9, Metadata: map[string]string{"collection_name": "member"}},
		{ID: "chunk-2", Text: "支付说明", Score: 0.8},
	}}, resolver)

	chunks, err := retriever.Retrieve(context.Background(), "会员等级", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	if got, want := resolver.ids, []string{"chunk-1", "chunk-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved ids mismatch: got %v, want %v", got, want)
	}
	if len(chunks) != 2 || chunks[0].ID != "chunk-1" || chunks[1].ID != "chunk-2" {
		t.Fatalf("expected order to be preserved, got %+v", chunks)
	}
	meta := chunks[0].Metadata
	if meta["doc_id"] != "doc-1" || meta["chunk_index"] != "3" || meta["doc_name"] != "会员说明.md" {
		t.Fatalf("expected metadata enrichment, got %+v", meta)
	}
	if chunks[1].Metadata != nil {
		t.Fatalf("expected unmatched chunk metadata to remain nil, got %+v", chunks[1].Metadata)
	}
}

func TestDBChunkMetadataResolverResolvesChunkAndDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	doc := knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		DocName:   "会员规则.md",
		FileURL:   "https://example.com/member.md",
		FileType:  "md",
		CreatedBy: "tester",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk := knowledgeModel.KnowledgeChunk{
		KbID:       "kb-1",
		DocID:      "doc-1",
		ChunkIndex: 4,
		Content:    "会员规则",
		Enabled:    1,
		CreatedBy:  "tester",
	}
	chunk.ID = "chunk-1"
	if err := gdb.Create(&chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	resolver := NewDBChunkMetadataResolver(gdb)
	meta, err := resolver.ResolveChunkMetadata(context.Background(), []string{"chunk-1", "", "chunk-1"})
	if err != nil {
		t.Fatalf("resolve metadata: %v", err)
	}
	got, ok := meta["chunk-1"]
	if !ok {
		t.Fatalf("expected chunk metadata, got %+v", meta)
	}
	if got.DocID != "doc-1" || got.ChunkIndex != 4 || got.DocName != "会员规则.md" {
		t.Fatalf("unexpected chunk metadata: %+v", got)
	}
}
