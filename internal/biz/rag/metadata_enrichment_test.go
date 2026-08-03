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

type fakeChunkContextResolver struct {
	fakeChunkMetadataResolver
	contextIDs []string
	context    []RetrievedChunk
}

func (r *fakeChunkContextResolver) ResolveContextChunks(_ context.Context, ids []string, _ int) ([]RetrievedChunk, error) {
	r.contextIDs = append(r.contextIDs, ids...)
	return r.context, nil
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

func TestMetadataEnrichingRetrieverAppendsAdjacentContextChunks(t *testing.T) {
	resolver := &fakeChunkContextResolver{
		fakeChunkMetadataResolver: fakeChunkMetadataResolver{meta: map[string]ChunkMetadata{
			"chunk-4": {DocID: "doc-1", ChunkIndex: 4, DocName: "收银台诊断.md"},
		}},
		context: []RetrievedChunk{
			{ID: "chunk-4", Text: "支持的查询接口："},
			{ID: "chunk-5", Text: "`/cashier_diagnosis_latest` 查询最近一次诊断快照", Metadata: map[string]string{
				"doc_id":      "doc-1",
				"chunk_index": "5",
				"doc_name":    "收银台诊断.md",
			}},
		},
	}
	retriever := NewMetadataEnrichingRetriever(staticRetriever{chunks: []RetrievedChunk{
		{ID: "chunk-4", Text: "支持的查询接口：", Score: 0.9},
	}}, resolver)

	chunks, err := retriever.Retrieve(context.Background(), "支持哪些接口", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	if got, want := resolver.contextIDs, []string{"chunk-4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("context ids mismatch: got %v, want %v", got, want)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected adjacent chunk to be appended, got %+v", chunks)
	}
	if chunks[0].ID != "chunk-4" || chunks[1].ID != "chunk-5" {
		t.Fatalf("expected original chunk followed by adjacent context, got %+v", chunks)
	}
	if chunks[1].Metadata["chunk_index"] != "5" {
		t.Fatalf("expected adjacent chunk metadata to be preserved, got %+v", chunks[1].Metadata)
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

func TestDBChunkMetadataResolverResolvesAdjacentVectorChunks(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := gdb.Exec(`CREATE TABLE t_knowledge_vector (
		id TEXT PRIMARY KEY,
		collection_name TEXT,
		content TEXT,
		metadata TEXT,
		deleted INTEGER DEFAULT 0
	)`).Error; err != nil {
		t.Fatalf("create vector table: %v", err)
	}
	doc := knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		DocName:   "收银台诊断.md",
		FileURL:   "upload://goknowladge/doc-1",
		FileType:  "md",
		CreatedBy: "tester",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(&doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunks := []knowledgeModel.KnowledgeChunk{
		{KbID: "kb-1", DocID: "doc-1", ChunkIndex: 3, Content: "旧分块，不在向量表", Enabled: 1, CreatedBy: "tester"},
		{KbID: "kb-1", DocID: "doc-1", ChunkIndex: 4, Content: "支持的查询接口：", Enabled: 1, CreatedBy: "tester"},
		{KbID: "kb-1", DocID: "doc-1", ChunkIndex: 5, Content: "`/cashier_diagnosis_latest` 查询最近一次诊断快照", Enabled: 1, CreatedBy: "tester"},
	}
	chunks[0].ID = "chunk-3"
	chunks[1].ID = "chunk-4"
	chunks[2].ID = "chunk-5"
	if err := gdb.Create(&chunks).Error; err != nil {
		t.Fatalf("create chunks: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO t_knowledge_vector (id, collection_name, content, metadata, deleted) VALUES
		('chunk-4', 'goknowladge', '支持的查询接口：', '{"doc_id":"doc-1","chunk_index":"4"}', 0),
		('chunk-5', 'goknowladge', '/cashier_diagnosis_latest 查询最近一次诊断快照', '{"doc_id":"doc-1","chunk_index":"5"}', 0)`).Error; err != nil {
		t.Fatalf("insert vectors: %v", err)
	}

	resolver := NewDBChunkMetadataResolver(gdb)
	contextChunks, err := resolver.ResolveContextChunks(context.Background(), []string{"chunk-4"}, 1)
	if err != nil {
		t.Fatalf("resolve context chunks: %v", err)
	}
	if len(contextChunks) != 2 {
		t.Fatalf("expected current vector chunks only, got %+v", contextChunks)
	}
	if contextChunks[0].ID != "chunk-4" || contextChunks[1].ID != "chunk-5" {
		t.Fatalf("unexpected context chunk order: %+v", contextChunks)
	}
	if contextChunks[1].Metadata["doc_name"] != "收银台诊断.md" || contextChunks[1].Metadata["collection_name"] != "goknowladge" {
		t.Fatalf("expected adjacent metadata, got %+v", contextChunks[1].Metadata)
	}
}
