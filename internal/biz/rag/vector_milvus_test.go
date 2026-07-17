package rag

import (
	"context"
	"testing"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"
)

type fakeMilvusClient struct {
	hasCollection bool
	created       int
	loaded        int
	upserted      int
	deleted       int
	searchResults []milvusclient.ResultSet
}

func (c *fakeMilvusClient) HasCollection(context.Context, milvusclient.HasCollectionOption, ...grpc.CallOption) (bool, error) {
	return c.hasCollection, nil
}

func (c *fakeMilvusClient) CreateCollection(context.Context, milvusclient.CreateCollectionOption, ...grpc.CallOption) error {
	c.created++
	c.hasCollection = true
	return nil
}

func (c *fakeMilvusClient) CreateIndex(context.Context, milvusclient.CreateIndexOption, ...grpc.CallOption) (*milvusclient.CreateIndexTask, error) {
	return nil, nil
}

func (c *fakeMilvusClient) LoadCollection(context.Context, milvusclient.LoadCollectionOption, ...grpc.CallOption) (milvusclient.LoadTask, error) {
	c.loaded++
	return milvusclient.LoadTask{}, nil
}

func (c *fakeMilvusClient) DropCollection(context.Context, milvusclient.DropCollectionOption, ...grpc.CallOption) error {
	c.hasCollection = false
	return nil
}

func (c *fakeMilvusClient) Upsert(context.Context, milvusclient.UpsertOption, ...grpc.CallOption) (milvusclient.UpsertResult, error) {
	c.upserted++
	return milvusclient.UpsertResult{}, nil
}

func (c *fakeMilvusClient) Delete(context.Context, milvusclient.DeleteOption, ...grpc.CallOption) (milvusclient.DeleteResult, error) {
	c.deleted++
	return milvusclient.DeleteResult{}, nil
}

func (c *fakeMilvusClient) Search(context.Context, milvusclient.SearchOption, ...grpc.CallOption) ([]milvusclient.ResultSet, error) {
	return c.searchResults, nil
}

func TestNewVectorStore_DefaultsToPgVector(t *testing.T) {
	store, err := NewVectorStore(context.Background(), "", "", nil, 1536, "COSINE")
	if err != nil {
		t.Fatalf("new vector store: %v", err)
	}
	if _, ok := store.(*PgVectorStore); !ok {
		t.Fatalf("expected PgVectorStore, got %T", store)
	}
}

func TestNewVectorStore_SelectsMilvus(t *testing.T) {
	oldNewMilvusClient := newMilvusClient
	defer func() { newMilvusClient = oldNewMilvusClient }()
	newMilvusClient = func(context.Context, string) (milvusClientAPI, error) {
		return &fakeMilvusClient{hasCollection: true}, nil
	}

	store, err := NewVectorStore(context.Background(), "milvus", "127.0.0.1:19530", nil, 1536, "COSINE")
	if err != nil {
		t.Fatalf("new vector store: %v", err)
	}
	if _, ok := store.(*MilvusVectorStore); !ok {
		t.Fatalf("expected MilvusVectorStore, got %T", store)
	}
}

func TestMilvusVectorStore_IndexDocumentChunksCreatesCollectionAndUpserts(t *testing.T) {
	client := &fakeMilvusClient{}
	store := NewMilvusVectorStore(client, 3, "COSINE")

	err := store.IndexDocumentChunks(context.Background(), "member_agent", "doc-1", []VectorChunk{
		{
			ChunkID:   "chunk-1",
			Content:   "当前会员Agent支持错误排查能力",
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  map[string]string{"doc_name": "会员Agent说明.md"},
		},
	})
	if err != nil {
		t.Fatalf("index chunks: %v", err)
	}
	if client.created != 1 || client.loaded != 1 || client.upserted != 1 {
		t.Fatalf("unexpected client calls: created=%d loaded=%d upserted=%d", client.created, client.loaded, client.upserted)
	}
}

func TestMilvusVectorStore_SearchMapsMetadata(t *testing.T) {
	client := &fakeMilvusClient{
		hasCollection: true,
		searchResults: []milvusclient.ResultSet{
			{
				ResultCount: 1,
				IDs:         column.NewColumnVarChar("id", []string{"chunk-1"}),
				Fields: milvusclient.DataSet{
					column.NewColumnVarChar("content", []string{"当前会员Agent支持错误排查能力"}),
					column.NewColumnVarChar("metadata", []string{`{"doc_id":"doc-1","doc_name":"会员Agent说明.md","source_url":"https://example.com/member-agent.md"}`}),
				},
				Scores: []float32{0.92},
			},
		},
	}
	store := NewMilvusVectorStore(client, 3, "COSINE")

	chunks, err := store.Search(context.Background(), "member_agent", []float32{0.1, 0.2, 0.3}, 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %+v", chunks)
	}
	if chunks[0].ChunkID != "chunk-1" || chunks[0].DocID != "doc-1" || chunks[0].Content == "" || chunks[0].Metadata["doc_name"] != "会员Agent说明.md" {
		t.Fatalf("unexpected chunk: %+v", chunks[0])
	}
}
