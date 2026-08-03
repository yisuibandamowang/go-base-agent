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

// ChunkContextResolver resolves nearby chunks that should be included as prompt context.
type ChunkContextResolver interface {
	ResolveContextChunks(ctx context.Context, ids []string, window int) ([]RetrievedChunk, error)
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
	if contextResolver, ok := r.resolver.(ChunkContextResolver); ok {
		contextChunks, err := contextResolver.ResolveContextChunks(ctx, ids, 1)
		if err == nil && len(contextChunks) > 0 {
			chunks = appendMissingContextChunks(chunks, contextChunks)
		}
	}
	return chunks, nil
}

func appendMissingContextChunks(chunks []RetrievedChunk, contextChunks []RetrievedChunk) []RetrievedChunk {
	seen := make(map[string]bool, len(chunks)+len(contextChunks))
	for _, chunk := range chunks {
		seen[chunkKey(chunk)] = true
	}
	for _, chunk := range contextChunks {
		key := chunkKey(chunk)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		chunks = append(chunks, chunk)
	}
	return chunks
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

// ResolveContextChunks resolves active adjacent chunks from the same documents as the hit chunks.
func (r *DBChunkMetadataResolver) ResolveContextChunks(ctx context.Context, ids []string, window int) ([]RetrievedChunk, error) {
	if r == nil || r.db == nil || len(ids) == 0 {
		return nil, nil
	}
	if window < 0 {
		window = 0
	}
	metaByID, err := r.ResolveChunkMetadata(ctx, ids)
	if err != nil || len(metaByID) == 0 {
		return nil, err
	}

	conditions := make([]string, 0, len(metaByID))
	args := make([]any, 0, len(metaByID)*3)
	seenRanges := make(map[string]bool, len(metaByID))
	for _, meta := range metaByID {
		docID := strings.TrimSpace(meta.DocID)
		if docID == "" {
			continue
		}
		start := meta.ChunkIndex - window
		if start < 0 {
			start = 0
		}
		end := meta.ChunkIndex + window
		rangeKey := docID + ":" + strconv.Itoa(start) + ":" + strconv.Itoa(end)
		if seenRanges[rangeKey] {
			continue
		}
		seenRanges[rangeKey] = true
		conditions = append(conditions, "(c.doc_id = ? AND c.chunk_index BETWEEN ? AND ?)")
		args = append(args, docID, start, end)
	}
	if len(conditions) == 0 {
		return nil, nil
	}

	type row struct {
		ID         string `gorm:"column:id"`
		DocID      string `gorm:"column:doc_id"`
		ChunkIndex int    `gorm:"column:chunk_index"`
		Content    string `gorm:"column:content"`
		Metadata   string `gorm:"column:metadata"`
		DocName    string `gorm:"column:doc_name"`
		Collection string `gorm:"column:collection_name"`
	}
	rows := make([]row, 0)
	sql := `SELECT c.id, c.doc_id, c.chunk_index, c.content, v.metadata, d.doc_name, v.collection_name
		FROM t_knowledge_chunk c
		JOIN t_knowledge_vector v
		  ON v.id = c.id
		 AND v.deleted = 0
		LEFT JOIN t_knowledge_document d
		  ON d.id = c.doc_id
		 AND d.deleted = 0
		WHERE c.deleted = 0
		  AND c.enabled = 1
		  AND (` + strings.Join(conditions, " OR ") + `)
		ORDER BY c.doc_id ASC, c.chunk_index ASC, c.id ASC`
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("resolve context chunks: %w", err)
	}

	chunks := make([]RetrievedChunk, 0, len(rows))
	seenIndex := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := row.DocID + ":" + strconv.Itoa(row.ChunkIndex)
		if seenIndex[key] {
			continue
		}
		seenIndex[key] = true
		meta := parseVectorMetadata(row.Metadata)
		if meta == nil {
			meta = make(map[string]string)
		}
		meta["doc_id"] = row.DocID
		meta["chunk_index"] = strconv.Itoa(row.ChunkIndex)
		meta["index"] = strconv.Itoa(row.ChunkIndex)
		if strings.TrimSpace(row.DocName) != "" {
			meta["doc_name"] = row.DocName
		}
		if strings.TrimSpace(row.Collection) != "" {
			meta["collection_name"] = row.Collection
		}
		chunks = append(chunks, RetrievedChunk{
			ID:       row.ID,
			Text:     row.Content,
			Metadata: meta,
		})
	}
	return chunks, nil
}
