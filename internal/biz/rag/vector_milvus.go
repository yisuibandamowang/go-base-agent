package rag

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"
)

const (
	milvusFieldID        = "id"
	milvusFieldDocID     = "doc_id"
	milvusFieldContent   = "content"
	milvusFieldMetadata  = "metadata"
	milvusFieldEmbedding = "embedding"
	milvusMaxVarcharLen  = 65535
)

type milvusClientAPI interface {
	HasCollection(context.Context, milvusclient.HasCollectionOption, ...grpc.CallOption) (bool, error)
	CreateCollection(context.Context, milvusclient.CreateCollectionOption, ...grpc.CallOption) error
	CreateIndex(context.Context, milvusclient.CreateIndexOption, ...grpc.CallOption) (*milvusclient.CreateIndexTask, error)
	LoadCollection(context.Context, milvusclient.LoadCollectionOption, ...grpc.CallOption) (milvusclient.LoadTask, error)
	DropCollection(context.Context, milvusclient.DropCollectionOption, ...grpc.CallOption) error
	Upsert(context.Context, milvusclient.UpsertOption, ...grpc.CallOption) (milvusclient.UpsertResult, error)
	Delete(context.Context, milvusclient.DeleteOption, ...grpc.CallOption) (milvusclient.DeleteResult, error)
	Search(context.Context, milvusclient.SearchOption, ...grpc.CallOption) ([]milvusclient.ResultSet, error)
}

var newMilvusClient = func(ctx context.Context, address string) (milvusClientAPI, error) {
	return milvusclient.New(ctx, &milvusclient.ClientConfig{Address: address})
}

// MilvusVectorStore stores vectors in Milvus, using one collection per knowledge base.
type MilvusVectorStore struct {
	client    milvusClientAPI
	dimension int
	metric    entity.MetricType
}

var _ VectorStore = (*MilvusVectorStore)(nil)

// NewMilvusVectorStore creates a Milvus-backed vector store.
func NewMilvusVectorStore(client milvusClientAPI, dimension int, metricType string) *MilvusVectorStore {
	if dimension <= 0 {
		dimension = 1536
	}
	return &MilvusVectorStore{
		client:    client,
		dimension: dimension,
		metric:    normalizeMilvusMetric(metricType),
	}
}

// IndexDocumentChunks upserts document chunks into the Milvus collection.
func (s *MilvusVectorStore) IndexDocumentChunks(ctx context.Context, collectionName, docID string, chunks []VectorChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if err := s.EnsureVectorSpace(ctx, VectorSpaceSpec{
		SpaceID:   VectorSpaceID{Name: collectionName},
		Dimension: s.dimensionForChunks(chunks),
	}); err != nil {
		return err
	}
	option, err := s.upsertOption(collectionName, docID, chunks)
	if err != nil {
		return err
	}
	if _, err := s.client.Upsert(ctx, option); err != nil {
		return fmt.Errorf("milvus upsert chunks: %w", err)
	}
	return nil
}

// UpdateChunk upserts a single chunk into the Milvus collection.
func (s *MilvusVectorStore) UpdateChunk(ctx context.Context, collectionName, docID string, chunk VectorChunk) error {
	return s.IndexDocumentChunks(ctx, collectionName, docID, []VectorChunk{chunk})
}

// DeleteDocumentVectors deletes all vectors that belong to a document.
func (s *MilvusVectorStore) DeleteDocumentVectors(ctx context.Context, collectionName, docID string) error {
	if strings.TrimSpace(docID) == "" {
		return nil
	}
	_, err := s.client.Delete(ctx, milvusclient.NewDeleteOption(collectionName).
		WithExpr(milvusFieldDocID+" == "+strconv.Quote(docID)))
	if err != nil {
		return fmt.Errorf("milvus delete document vectors: %w", err)
	}
	return nil
}

// DeleteChunkByID deletes one chunk vector by primary key.
func (s *MilvusVectorStore) DeleteChunkByID(ctx context.Context, collectionName, chunkID string) error {
	return s.DeleteChunksByIDs(ctx, collectionName, []string{chunkID})
}

// DeleteChunksByIDs deletes chunk vectors by primary key.
func (s *MilvusVectorStore) DeleteChunksByIDs(ctx context.Context, collectionName string, chunkIDs []string) error {
	ids := make([]string, 0, len(chunkIDs))
	for _, id := range chunkIDs {
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := s.client.Delete(ctx, milvusclient.NewDeleteOption(collectionName).
		WithStringIDs(milvusFieldID, ids))
	if err != nil {
		return fmt.Errorf("milvus delete chunks: %w", err)
	}
	return nil
}

// Search performs Milvus vector similarity search.
func (s *MilvusVectorStore) Search(ctx context.Context, collectionName string, vec []float32, topK int) ([]VectorChunk, error) {
	if len(vec) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	results, err := s.client.Search(ctx, milvusclient.NewSearchOption(
		collectionName,
		topK,
		[]entity.Vector{entity.FloatVector(vec)},
	).WithANNSField(milvusFieldEmbedding).WithOutputFields(milvusFieldContent, milvusFieldMetadata, milvusFieldDocID))
	if err != nil {
		return nil, fmt.Errorf("milvus vector search: %w", err)
	}
	return milvusResultSetsToChunks(results), nil
}

// EnsureVectorSpace creates the Milvus collection when it does not exist.
func (s *MilvusVectorStore) EnsureVectorSpace(ctx context.Context, spec VectorSpaceSpec) error {
	name := strings.TrimSpace(spec.SpaceID.Name)
	if name == "" {
		return fmt.Errorf("milvus collection name is empty")
	}
	exists, err := s.VectorSpaceExists(ctx, spec.SpaceID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	dimension := spec.Dimension
	if dimension <= 0 {
		dimension = s.dimension
	}
	schema := entity.NewSchema().
		WithName(name).
		WithDescription(spec.Remark).
		WithField(entity.NewField().WithName(milvusFieldID).WithDataType(entity.FieldTypeVarChar).WithIsPrimaryKey(true).WithMaxLength(128)).
		WithField(entity.NewField().WithName(milvusFieldDocID).WithDataType(entity.FieldTypeVarChar).WithMaxLength(128)).
		WithField(entity.NewField().WithName(milvusFieldContent).WithDataType(entity.FieldTypeVarChar).WithMaxLength(milvusMaxVarcharLen)).
		WithField(entity.NewField().WithName(milvusFieldMetadata).WithDataType(entity.FieldTypeVarChar).WithMaxLength(milvusMaxVarcharLen)).
		WithField(entity.NewField().WithName(milvusFieldEmbedding).WithDataType(entity.FieldTypeFloatVector).WithDim(int64(dimension)))
	if err := s.client.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(name, schema)); err != nil {
		return fmt.Errorf("milvus create collection: %w", err)
	}
	if _, err := s.client.CreateIndex(ctx, milvusclient.NewCreateIndexOption(name, milvusFieldEmbedding, index.NewAutoIndex(s.metric))); err != nil {
		return fmt.Errorf("milvus create index: %w", err)
	}
	if _, err := s.client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name)); err != nil {
		return fmt.Errorf("milvus load collection: %w", err)
	}
	return nil
}

// VectorSpaceExists checks whether the Milvus collection exists.
func (s *MilvusVectorStore) VectorSpaceExists(ctx context.Context, spaceID VectorSpaceID) (bool, error) {
	exists, err := s.client.HasCollection(ctx, milvusclient.NewHasCollectionOption(spaceID.Name))
	if err != nil {
		return false, fmt.Errorf("milvus has collection: %w", err)
	}
	return exists, nil
}

// DropVectorSpace drops the Milvus collection.
func (s *MilvusVectorStore) DropVectorSpace(ctx context.Context, collectionName string) error {
	if strings.TrimSpace(collectionName) == "" {
		return nil
	}
	if err := s.client.DropCollection(ctx, milvusclient.NewDropCollectionOption(collectionName)); err != nil {
		return fmt.Errorf("milvus drop collection: %w", err)
	}
	return nil
}

func (s *MilvusVectorStore) upsertOption(collectionName, docID string, chunks []VectorChunk) (milvusclient.UpsertOption, error) {
	dimension := s.dimensionForChunks(chunks)
	ids := make([]string, 0, len(chunks))
	docIDs := make([]string, 0, len(chunks))
	contents := make([]string, 0, len(chunks))
	metadata := make([]string, 0, len(chunks))
	embeddings := make([][]float32, 0, len(chunks))

	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.ChunkID) == "" {
			return nil, fmt.Errorf("milvus chunk id is empty")
		}
		chunkDocID := firstNonEmpty(chunk.DocID, docID)
		if strings.TrimSpace(chunkDocID) == "" {
			return nil, fmt.Errorf("milvus doc id is empty")
		}
		if len(chunk.Embedding) != dimension {
			return nil, fmt.Errorf("milvus embedding dimension mismatch: chunk %s got %d want %d", chunk.ChunkID, len(chunk.Embedding), dimension)
		}
		ids = append(ids, chunk.ChunkID)
		docIDs = append(docIDs, chunkDocID)
		contents = append(contents, chunk.Content)
		metadata = append(metadata, buildVectorMetadata(chunkDocID, chunk))
		embeddings = append(embeddings, chunk.Embedding)
	}

	return milvusclient.NewColumnBasedInsertOption(collectionName).
		WithVarcharColumn(milvusFieldID, ids).
		WithVarcharColumn(milvusFieldDocID, docIDs).
		WithVarcharColumn(milvusFieldContent, contents).
		WithVarcharColumn(milvusFieldMetadata, metadata).
		WithFloatVectorColumn(milvusFieldEmbedding, dimension, embeddings), nil
}

func (s *MilvusVectorStore) dimensionForChunks(chunks []VectorChunk) int {
	for _, chunk := range chunks {
		if len(chunk.Embedding) > 0 {
			return len(chunk.Embedding)
		}
	}
	return s.dimension
}

func normalizeMilvusMetric(metricType string) entity.MetricType {
	switch strings.ToUpper(strings.TrimSpace(metricType)) {
	case string(entity.L2):
		return entity.L2
	case string(entity.IP):
		return entity.IP
	default:
		return entity.COSINE
	}
}

func milvusResultSetsToChunks(resultSets []milvusclient.ResultSet) []VectorChunk {
	chunks := make([]VectorChunk, 0)
	for _, resultSet := range resultSets {
		if resultSet.Err != nil {
			continue
		}
		contentColumn := resultSet.GetColumn(milvusFieldContent)
		metadataColumn := resultSet.GetColumn(milvusFieldMetadata)
		docIDColumn := resultSet.GetColumn(milvusFieldDocID)
		for i := 0; i < resultSet.Len(); i++ {
			chunkID := milvusColumnString(resultSet.IDs, i)
			metadata := parseVectorMetadata(milvusColumnString(metadataColumn, i))
			docID := firstNonEmpty(metadata["doc_id"], milvusColumnString(docIDColumn, i))
			if docID != "" {
				metadata["doc_id"] = docID
			}
			chunk := VectorChunk{
				ChunkID:  chunkID,
				DocID:    docID,
				Content:  milvusColumnString(contentColumn, i),
				Score:    milvusScore(resultSet.Scores, i),
				Metadata: metadata,
			}
			applyStructuredMetadata(&chunk)
			chunks = append(chunks, chunk)
		}
	}
	return chunks
}

func milvusScore(scores []float32, idx int) float64 {
	if idx < 0 || idx >= len(scores) {
		return 0
	}
	return float64(scores[idx])
}

func milvusColumnString(col column.Column, idx int) string {
	if col == nil {
		return ""
	}
	value, err := col.GetAsString(idx)
	if err != nil {
		return ""
	}
	return value
}
