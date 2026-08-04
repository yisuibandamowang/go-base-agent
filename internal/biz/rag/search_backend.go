package rag

import (
	"context"
	"fmt"
	"strings"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"

	"gorm.io/gorm"
)

// KnowledgeSearchBackend provides lexical and fallback retrieval capabilities.
type KnowledgeSearchBackend interface {
	ListKnowledgeBases(ctx context.Context) ([]knowledgeModel.KnowledgeBase, error)
	SearchKeywordChunks(ctx context.Context, kb knowledgeModel.KnowledgeBase, query string, topK int) ([]RetrievedChunk, error)
	SearchRecentChunks(ctx context.Context, collectionName string, topK int) ([]RetrievedChunk, error)
	MatchIntentCollections(ctx context.Context, query string, limit int) ([]string, error)
}

type PgKnowledgeSearchBackend struct {
	vectorDB *gorm.DB
	kbRepo   knowledgeBaseLister
}

var _ KnowledgeSearchBackend = (*PgKnowledgeSearchBackend)(nil)

// NewPgKnowledgeSearchBackend creates a backend that keeps PG-specific query logic out of the business layer.
func NewPgKnowledgeSearchBackend(vectorDB *gorm.DB, kbRepo knowledgeBaseLister) *PgKnowledgeSearchBackend {
	return &PgKnowledgeSearchBackend{vectorDB: vectorDB, kbRepo: kbRepo}
}

// NewKnowledgeSearchBackend creates the business-neutral search backend alias.
func NewKnowledgeSearchBackend(vectorDB *gorm.DB, kbRepo knowledgeBaseLister) *PgKnowledgeSearchBackend {
	return NewPgKnowledgeSearchBackend(vectorDB, kbRepo)
}

// ListKnowledgeBases returns all known knowledge bases.
func (b *PgKnowledgeSearchBackend) ListKnowledgeBases(ctx context.Context) ([]knowledgeModel.KnowledgeBase, error) {
	if b == nil || b.kbRepo == nil {
		return nil, nil
	}
	kbs, _, err := b.kbRepo.List(ctx, 1, 100, "")
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	return kbs, nil
}

// SearchKeywordChunks performs simple PostgreSQL content keyword recall.
func (b *PgKnowledgeSearchBackend) SearchKeywordChunks(ctx context.Context, kb knowledgeModel.KnowledgeBase, query string, topK int) ([]RetrievedChunk, error) {
	if b == nil || b.vectorDB == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	type row struct {
		ID             string  `gorm:"column:id"`
		Content        string  `gorm:"column:content"`
		Metadata       string  `gorm:"column:metadata"`
		DocName        string  `gorm:"column:doc_name"`
		SourceLocation string  `gorm:"column:source_location"`
		FileURL        string  `gorm:"column:file_url"`
		Score          float64 `gorm:"column:score"`
	}
	rows := make([]row, 0)
	err := b.vectorDB.WithContext(ctx).Raw(
		`SELECT v.id, v.content, v.metadata, d.doc_name, d.source_location, d.file_url, 0.5 AS score
		 FROM t_knowledge_vector v
		 LEFT JOIN t_knowledge_document d
		   ON d.id = v.metadata::jsonb->>'doc_id'
		  AND d.deleted = 0
		 WHERE v.collection_name = ?
		   AND LOWER(v.content) LIKE LOWER(?)
		 ORDER BY v.create_time DESC
		 LIMIT ?`,
		kb.CollectionName, "%"+strings.TrimSpace(query)+"%", topK,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search keyword chunks: %w", err)
	}
	chunks := make([]RetrievedChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, RetrievedChunk{
			ID:       row.ID,
			Text:     row.Content,
			Score:    row.Score,
			Metadata: metadataWithSources(parseVectorMetadata(row.Metadata), knowledgeModel.KnowledgeBase(kb), row.DocName, row.SourceLocation, row.FileURL),
		})
	}
	return chunks, nil
}

// SearchRecentChunks returns the newest chunks in a collection as a fallback path.
func (b *PgKnowledgeSearchBackend) SearchRecentChunks(ctx context.Context, collectionName string, topK int) ([]RetrievedChunk, error) {
	if b == nil || b.vectorDB == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	type row struct {
		ID             string  `gorm:"column:id"`
		Content        string  `gorm:"column:content"`
		Metadata       string  `gorm:"column:metadata"`
		DocName        string  `gorm:"column:doc_name"`
		SourceLocation string  `gorm:"column:source_location"`
		FileURL        string  `gorm:"column:file_url"`
		Score          float64 `gorm:"column:score"`
	}
	rows := make([]row, 0)
	err := b.vectorDB.WithContext(ctx).Raw(
		`SELECT v.id, v.content, v.metadata, d.doc_name, d.source_location, d.file_url, 0.7 AS score
		 FROM t_knowledge_vector v
		 LEFT JOIN t_knowledge_document d
		   ON d.id = v.metadata::jsonb->>'doc_id'
		  AND d.deleted = 0
		 WHERE v.collection_name = ?
		 ORDER BY v.create_time DESC
		 LIMIT ?`,
		collectionName, topK,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search recent chunks: %w", err)
	}
	chunks := make([]RetrievedChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, RetrievedChunk{
			ID:       row.ID,
			Text:     row.Content,
			Score:    row.Score,
			Metadata: metadataWithSources(parseVectorMetadata(row.Metadata), knowledgeModel.KnowledgeBase{CollectionName: collectionName}, row.DocName, row.SourceLocation, row.FileURL),
		})
	}
	return chunks, nil
}

// MatchIntentCollections returns collections whose intent node content matches the query.
func (b *PgKnowledgeSearchBackend) MatchIntentCollections(ctx context.Context, query string, limit int) ([]string, error) {
	if b == nil || b.vectorDB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	like := "%" + strings.TrimSpace(query) + "%"
	type row struct {
		CollectionName string `gorm:"column:collection_name"`
	}
	rows := make([]row, 0)
	err := b.vectorDB.WithContext(ctx).Raw(
		`SELECT DISTINCT collection_name
		 FROM t_intent_node
		 WHERE deleted = 0
		   AND enabled = 1
		   AND collection_name <> ''
		   AND (
		     LOWER(name) LIKE LOWER(?)
		     OR LOWER(description) LIKE LOWER(?)
		     OR LOWER(examples) LIKE LOWER(?)
		   )
		 ORDER BY collection_name ASC
		 LIMIT ?`,
		like, like, like, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("match intent collections: %w", err)
	}
	collections := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.CollectionName) != "" {
			collections = append(collections, row.CollectionName)
		}
	}
	return collections, nil
}
