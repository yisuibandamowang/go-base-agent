package rag

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type pgVectorRow struct {
	ID             string    `gorm:"column:id;primaryKey;type:varchar(20)"`
	CollectionName string    `gorm:"column:collection_name;type:varchar(64);not null;index:idx_kv_collection_name"`
	Content        string    `gorm:"column:content;type:text"`
	Metadata       string    `gorm:"column:metadata;type:jsonb"`
	Embedding      string    `gorm:"column:embedding;type:vector(1536)"`
	CreateTime     time.Time `gorm:"column:create_time;autoCreateTime"`
}

func (pgVectorRow) TableName() string { return "t_knowledge_vector" }

// PgVectorStore 基于 pgvector 的向量存储实现。
type PgVectorStore struct {
	db *gorm.DB
}

// NewPgVectorStore 创建 PgVectorStore。
func NewPgVectorStore(db *gorm.DB) *PgVectorStore {
	return &PgVectorStore{db: db}
}

// IndexDocumentChunks 批量写入分块向量。
func (s *PgVectorStore) IndexDocumentChunks(ctx context.Context, collectionName, docID string, chunks []VectorChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	rows := make([]pgVectorRow, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, pgVectorRow{
			ID:             c.ChunkID,
			CollectionName: collectionName,
			Content:        c.Content,
			Metadata:       fmt.Sprintf(`{"doc_id":"%s","index":%d}`, docID, c.Index),
			Embedding:      vecToString(c.Embedding),
		})
	}
	return s.db.WithContext(ctx).Create(rows).Error
}

// UpdateChunk 更新单个分块向量。
func (s *PgVectorStore) UpdateChunk(ctx context.Context, collectionName, docID string, chunk VectorChunk) error {
	return s.db.WithContext(ctx).Model(&pgVectorRow{}).
		Where("id = ?", chunk.ChunkID).
		Updates(map[string]interface{}{
			"content":   chunk.Content,
			"embedding": vecToString(chunk.Embedding),
		}).Error
}

// DeleteDocumentVectors 删除文档的所有向量。
func (s *PgVectorStore) DeleteDocumentVectors(ctx context.Context, collectionName, docID string) error {
	return s.db.WithContext(ctx).
		Where("collection_name = ? AND metadata::jsonb->>'doc_id' = ?", collectionName, docID).
		Delete(&pgVectorRow{}).Error
}

// DeleteChunkByID 删除单个分块向量。
func (s *PgVectorStore) DeleteChunkByID(ctx context.Context, collectionName, chunkID string) error {
	return s.db.WithContext(ctx).
		Where("id = ? AND collection_name = ?", chunkID, collectionName).
		Delete(&pgVectorRow{}).Error
}

// DeleteChunksByIDs 批量删除分块向量。
func (s *PgVectorStore) DeleteChunksByIDs(ctx context.Context, collectionName string, chunkIDs []string) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("id IN ? AND collection_name = ?", chunkIDs, collectionName).
		Delete(&pgVectorRow{}).Error
}

// Search 向量相似度搜索。
func (s *PgVectorStore) Search(ctx context.Context, collectionName string, vec []float32, topK int) ([]VectorChunk, error) {
	var rows []pgVectorRow
	err := s.db.WithContext(ctx).Raw(
		`SELECT * FROM t_knowledge_vector 
		 WHERE collection_name = ? 
		 ORDER BY embedding <=> ? 
		 LIMIT ?`,
		collectionName, vecToString(vec), topK,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}
	chunks := make([]VectorChunk, 0, len(rows))
	for _, r := range rows {
		chunks = append(chunks, VectorChunk{
			ChunkID:   r.ID,
			Content:   r.Content,
			Embedding: stringToVec(r.Embedding),
		})
	}
	return chunks, nil
}

func vecToString(vec []float32) string {
	if len(vec) == 0 {
		return "[]"
	}
	s := "["
	for i, v := range vec {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%f", v)
	}
	s += "]"
	return s
}

func stringToVec(s string) []float32 {
	var result []float32
	_ = result
	return nil
}
