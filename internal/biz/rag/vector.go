package rag

import "context"

// VectorChunk represents a document chunk with its embedding vector.
// Aligns with Java VectorChunk.
type VectorChunk struct {
	ChunkID       string
	DocID         string
	Content       string
	EmbeddingText string
	Embedding     []float32
	Score         float64
	Index         int
	Metadata      map[string]string
}

// VectorStoreService provides vector index CRUD operations.
// Aligns with Java VectorStoreService.
type VectorStoreService interface {
	IndexDocumentChunks(ctx context.Context, collectionName, docID string, chunks []VectorChunk) error
	UpdateChunk(ctx context.Context, collectionName, docID string, chunk VectorChunk) error
	DeleteDocumentVectors(ctx context.Context, collectionName, docID string) error
	DeleteChunkByID(ctx context.Context, collectionName, chunkID string) error
	DeleteChunksByIDs(ctx context.Context, collectionName string, chunkIDs []string) error
}

// VectorSearchService provides vector similarity search operations.
type VectorSearchService interface {
	Search(ctx context.Context, collectionName string, vec []float32, topK int) ([]VectorChunk, error)
}

// VectorSpaceID uniquely identifies a vector space.
type VectorSpaceID struct {
	Name string
}

// VectorSpaceSpec defines the specification for a vector space.
// Aligns with Java VectorSpaceSpec.
type VectorSpaceSpec struct {
	SpaceID   VectorSpaceID
	Remark    string
	Dimension int
}

// VectorStoreAdmin manages vector space lifecycle.
// Aligns with Java VectorStoreAdmin.
type VectorStoreAdmin interface {
	EnsureVectorSpace(ctx context.Context, spec VectorSpaceSpec) error
	VectorSpaceExists(ctx context.Context, spaceID VectorSpaceID) (bool, error)
	DropVectorSpace(ctx context.Context, collectionName string) error
}

// VectorStore combines CRUD, search and admin responsibilities.
type VectorStore interface {
	VectorStoreService
	VectorSearchService
	VectorStoreAdmin
}

// NoopVectorStore implements both VectorStoreService and VectorStoreAdmin as no-ops.
type NoopVectorStore struct{}

func (n *NoopVectorStore) IndexDocumentChunks(ctx context.Context, collectionName, docID string, chunks []VectorChunk) error {
	return nil
}
func (n *NoopVectorStore) UpdateChunk(ctx context.Context, collectionName, docID string, chunk VectorChunk) error {
	return nil
}
func (n *NoopVectorStore) DeleteDocumentVectors(ctx context.Context, collectionName, docID string) error {
	return nil
}
func (n *NoopVectorStore) DeleteChunkByID(ctx context.Context, collectionName, chunkID string) error {
	return nil
}
func (n *NoopVectorStore) DeleteChunksByIDs(ctx context.Context, collectionName string, chunkIDs []string) error {
	return nil
}
func (n *NoopVectorStore) Search(ctx context.Context, collectionName string, vec []float32, topK int) ([]VectorChunk, error) {
	return nil, nil
}
func (n *NoopVectorStore) EnsureVectorSpace(ctx context.Context, spec VectorSpaceSpec) error {
	return nil
}
func (n *NoopVectorStore) VectorSpaceExists(ctx context.Context, spaceID VectorSpaceID) (bool, error) {
	return true, nil
}
func (n *NoopVectorStore) DropVectorSpace(ctx context.Context, collectionName string) error {
	return nil
}
