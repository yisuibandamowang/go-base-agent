package rag

import (
	"context"
	"fmt"
	"log/slog"

	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/infra/embedding"

	"gorm.io/gorm"
)

// PgRetriever implements Retriever using pgvector similarity search against
// all enabled knowledge bases.
// Replaces NoopRetriever.
type PgRetriever struct {
	vectorDB *gorm.DB
	emb      embedding.Service
	kbRepo   *repo.KnowledgeBaseRepo
	topK     int
}

// NewPgRetriever creates a PgRetriever.
func NewPgRetriever(
	vectorDB *gorm.DB,
	emb embedding.Service,
	kbRepo *repo.KnowledgeBaseRepo,
	topK int,
) *PgRetriever {
	if topK <= 0 {
		topK = 10
	}
	return &PgRetriever{
		vectorDB: vectorDB,
		emb:      emb,
		kbRepo:   kbRepo,
		topK:     topK,
	}
}

// Retrieve performs pgvector similarity search across all knowledge bases.
func (r *PgRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	if topK <= 0 {
		topK = r.topK
	}
	vec, err := r.emb.Embed(ctx, question)
	if err != nil {
		return nil, fmt.Errorf("embed question failed: %w", err)
	}
	kbs, _, err := r.kbRepo.List(ctx, 1, 100)
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	if len(kbs) == 0 {
		return nil, nil
	}

	type row struct {
		ID      string  `gorm:"column:id"`
		Content string  `gorm:"column:content"`
		Score   float64 `gorm:"column:score"`
	}
	var allChunks []RetrievedChunk
	vecStr := vecToString(vec)

	for _, kb := range kbs {
		var rows []row
		err := r.vectorDB.WithContext(ctx).Raw(
			`SELECT id, content, 1 - (embedding <=> ?) AS score
			 FROM t_knowledge_vector
			 WHERE collection_name = ?
			 ORDER BY embedding <=> ?
			 LIMIT ?`,
			vecStr, kb.CollectionName, vecStr, topK,
		).Scan(&rows).Error
		if err != nil {
			slog.Warn("pg retriever: vector search failed", "kb", kb.Name, "err", err)
			continue
		}
		for _, row := range rows {
			allChunks = append(allChunks, RetrievedChunk{
				ID:    row.ID,
				Text:  row.Content,
				Score: row.Score,
			})
		}
	}
	return allChunks, nil
}
