package rag

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// GraphSyncingVectorStore decorates a vector store and best-effort syncs LightRAG graph data.
type GraphSyncingVectorStore struct {
	delegate VectorStore
	sync     GraphSyncClient
}

var _ VectorStore = (*GraphSyncingVectorStore)(nil)

// NewGraphSyncingVectorStore wraps a vector store with graph sync behavior.
func NewGraphSyncingVectorStore(delegate VectorStore, sync GraphSyncClient) *GraphSyncingVectorStore {
	return &GraphSyncingVectorStore{delegate: delegate, sync: sync}
}

// IndexDocumentChunks writes vectors first and then best-effort syncs the graph document.
func (s *GraphSyncingVectorStore) IndexDocumentChunks(ctx context.Context, collectionName, docID string, chunks []VectorChunk) error {
	if s == nil || s.delegate == nil {
		return fmt.Errorf("vector store is nil")
	}
	if err := s.delegate.IndexDocumentChunks(ctx, collectionName, docID, chunks); err != nil {
		return err
	}
	if s.sync != nil {
		if err := s.sync.InsertText(ctx, concatVectorChunkText(chunks), EncodeGraphFileSource(collectionName, docID)); err != nil {
			slog.Warn("graph sync insert skipped", "collection", collectionName, "doc_id", docID, "err", err)
		}
	}
	return nil
}

// UpdateChunk delegates chunk updates without graph sync.
func (s *GraphSyncingVectorStore) UpdateChunk(ctx context.Context, collectionName, docID string, chunk VectorChunk) error {
	if s == nil || s.delegate == nil {
		return fmt.Errorf("vector store is nil")
	}
	return s.delegate.UpdateChunk(ctx, collectionName, docID, chunk)
}

// DeleteDocumentVectors deletes vectors first and then best-effort syncs the graph deletion.
func (s *GraphSyncingVectorStore) DeleteDocumentVectors(ctx context.Context, collectionName, docID string) error {
	if s == nil || s.delegate == nil {
		return fmt.Errorf("vector store is nil")
	}
	if err := s.delegate.DeleteDocumentVectors(ctx, collectionName, docID); err != nil {
		return err
	}
	if s.sync != nil {
		if err := s.sync.DeleteByDoc(ctx, docID); err != nil {
			slog.Warn("graph sync delete skipped", "collection", collectionName, "doc_id", docID, "err", err)
		}
	}
	return nil
}

// DeleteChunkByID delegates single chunk deletion without graph sync.
func (s *GraphSyncingVectorStore) DeleteChunkByID(ctx context.Context, collectionName, chunkID string) error {
	if s == nil || s.delegate == nil {
		return fmt.Errorf("vector store is nil")
	}
	return s.delegate.DeleteChunkByID(ctx, collectionName, chunkID)
}

// DeleteChunksByIDs delegates batch chunk deletion without graph sync.
func (s *GraphSyncingVectorStore) DeleteChunksByIDs(ctx context.Context, collectionName string, chunkIDs []string) error {
	if s == nil || s.delegate == nil {
		return fmt.Errorf("vector store is nil")
	}
	return s.delegate.DeleteChunksByIDs(ctx, collectionName, chunkIDs)
}

// Search delegates vector search.
func (s *GraphSyncingVectorStore) Search(ctx context.Context, collectionName string, vec []float32, topK int) ([]VectorChunk, error) {
	if s == nil || s.delegate == nil {
		return nil, fmt.Errorf("vector store is nil")
	}
	return s.delegate.Search(ctx, collectionName, vec, topK)
}

// EnsureVectorSpace delegates vector space creation.
func (s *GraphSyncingVectorStore) EnsureVectorSpace(ctx context.Context, spec VectorSpaceSpec) error {
	if s == nil || s.delegate == nil {
		return fmt.Errorf("vector store is nil")
	}
	return s.delegate.EnsureVectorSpace(ctx, spec)
}

// VectorSpaceExists delegates vector space existence checks.
func (s *GraphSyncingVectorStore) VectorSpaceExists(ctx context.Context, spaceID VectorSpaceID) (bool, error) {
	if s == nil || s.delegate == nil {
		return false, fmt.Errorf("vector store is nil")
	}
	return s.delegate.VectorSpaceExists(ctx, spaceID)
}

// DropVectorSpace delegates vector space deletion.
func (s *GraphSyncingVectorStore) DropVectorSpace(ctx context.Context, collectionName string) error {
	if s == nil || s.delegate == nil {
		return fmt.Errorf("vector store is nil")
	}
	return s.delegate.DropVectorSpace(ctx, collectionName)
}

func concatVectorChunkText(chunks []VectorChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.Content) == "" {
			continue
		}
		texts = append(texts, strings.TrimSpace(chunk.Content))
	}
	return strings.Join(texts, "\n\n")
}
