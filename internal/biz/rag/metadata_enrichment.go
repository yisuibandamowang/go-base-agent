package rag

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/framework/db"

	"gorm.io/gorm"
)

// ChunkMetadata contains document ownership for a retrieved chunk.
type ChunkMetadata struct {
	DocID      string
	ChunkIndex int
	DocName    string
}

// ChunkMetadataResolver resolves chunk metadata by chunk IDs.
type ChunkMetadataResolver interface {
	ResolveChunkMetadata(ctx context.Context, ids []string) (map[string]ChunkMetadata, error)
}

// MetadataEnrichingRetriever fills document metadata after retrieval/rerank.
type MetadataEnrichingRetriever struct {
	base     Retriever
	resolver ChunkMetadataResolver
}

// NewMetadataEnrichingRetriever creates a retriever that enriches final chunks with document metadata.
func NewMetadataEnrichingRetriever(base Retriever, resolver ChunkMetadataResolver) *MetadataEnrichingRetriever {
	return &MetadataEnrichingRetriever{base: base, resolver: resolver}
}

// Retrieve runs the base retriever and enriches chunk metadata without changing order.
func (r *MetadataEnrichingRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	if r == nil || r.base == nil {
		return nil, nil
	}
	chunks, err := r.base.Retrieve(ctx, question, topK)
	if err != nil || r.resolver == nil || len(chunks) == 0 {
		return chunks, err
	}
	return r.enrich(ctx, chunks)
}

// RetrieveWithContext preserves richer search context when the base retriever supports it.
func (r *MetadataEnrichingRetriever) RetrieveWithContext(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error) {
	if r == nil || r.base == nil {
		return nil, nil
	}
	var (
		chunks []RetrievedChunk
		err    error
	)
	if intentAware, ok := r.base.(IntentAwareRetriever); ok {
		chunks, err = intentAware.RetrieveWithContext(ctx, sc)
	} else {
		chunks, err = r.base.Retrieve(ctx, firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion), sc.TopK)
	}
	if err != nil || r.resolver == nil || len(chunks) == 0 {
		return chunks, err
	}
	return r.enrich(ctx, chunks)
}

func (r *MetadataEnrichingRetriever) enrich(ctx context.Context, chunks []RetrievedChunk) ([]RetrievedChunk, error) {
	ids := make([]string, 0, len(chunks))
	seen := make(map[string]bool, len(chunks))
	for _, chunk := range chunks {
		id := strings.TrimSpace(chunk.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return chunks, nil
	}
	metaByID, err := r.resolver.ResolveChunkMetadata(ctx, ids)
	if err != nil || len(metaByID) == 0 {
		return chunks, nil
	}
	for i := range chunks {
		meta, ok := metaByID[chunks[i].ID]
		if !ok {
			continue
		}
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = make(map[string]string)
		}
		chunks[i].Metadata["doc_id"] = meta.DocID
		chunks[i].Metadata["chunk_index"] = strconv.Itoa(meta.ChunkIndex)
		chunks[i].Metadata["index"] = strconv.Itoa(meta.ChunkIndex)
		if strings.TrimSpace(meta.DocName) != "" {
			chunks[i].Metadata["doc_name"] = meta.DocName
		}
	}
	return chunks, nil
}

// DBChunkMetadataResolver resolves chunk ownership from knowledge chunk and document tables.
type DBChunkMetadataResolver struct {
	db *gorm.DB
}

// NewDBChunkMetadataResolver creates a database-backed chunk metadata resolver.
func NewDBChunkMetadataResolver(gdb *gorm.DB) *DBChunkMetadataResolver {
	return &DBChunkMetadataResolver{db: gdb}
}

// ResolveChunkMetadata resolves chunk metadata from t_knowledge_chunk and t_knowledge_document.
func (r *DBChunkMetadataResolver) ResolveChunkMetadata(ctx context.Context, ids []string) (map[string]ChunkMetadata, error) {
	if r == nil || r.db == nil || len(ids) == 0 {
		return nil, nil
	}
	distinctIDs := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		distinctIDs = append(distinctIDs, id)
	}
	if len(distinctIDs) == 0 {
		return nil, nil
	}

	var chunks []knowledgeModel.KnowledgeChunk
	if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
		Where("id IN ?", distinctIDs).
		Find(&chunks).Error; err != nil {
		return nil, fmt.Errorf("resolve chunk metadata: %w", err)
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	docIDs := make([]string, 0, len(chunks))
	docSeen := make(map[string]bool, len(chunks))
	for _, chunk := range chunks {
		if chunk.DocID == "" || docSeen[chunk.DocID] {
			continue
		}
		docSeen[chunk.DocID] = true
		docIDs = append(docIDs, chunk.DocID)
	}
	docNameByID := make(map[string]string, len(docIDs))
	if len(docIDs) > 0 {
		var docs []knowledgeModel.KnowledgeDocument
		if err := r.db.WithContext(ctx).Scopes(db.NotDeletedScope()).
			Where("id IN ?", docIDs).
			Find(&docs).Error; err != nil {
			return nil, fmt.Errorf("resolve chunk documents: %w", err)
		}
		for _, doc := range docs {
			docNameByID[doc.ID] = doc.DocName
		}
	}

	result := make(map[string]ChunkMetadata, len(chunks))
	for _, chunk := range chunks {
		result[chunk.ID] = ChunkMetadata{
			DocID:      chunk.DocID,
			ChunkIndex: chunk.ChunkIndex,
			DocName:    docNameByID[chunk.DocID],
		}
	}
	return result, nil
}
