package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
			Metadata:       buildVectorMetadata(docID, c),
			Embedding:      vecToString(c.Embedding),
		})
	}
	return s.db.WithContext(ctx).Create(rows).Error
}

// UpdateChunk 更新单个分块向量。
func (s *PgVectorStore) UpdateChunk(ctx context.Context, collectionName, docID string, chunk VectorChunk) error {
	row := pgVectorRow{
		ID:             chunk.ChunkID,
		CollectionName: collectionName,
		Content:        chunk.Content,
		Metadata:       buildVectorMetadata(docID, chunk),
		Embedding:      vecToString(chunk.Embedding),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"collection_name", "content", "metadata", "embedding"}),
	}).Create(&row).Error
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
			Metadata:  parseVectorMetadata(r.Metadata),
		})
	}
	return chunks, nil
}

func buildVectorMetadata(docID string, chunk VectorChunk) string {
	meta := make(map[string]interface{}, len(chunk.Metadata)+2)
	meta["doc_id"] = docID
	meta["index"] = chunk.Index
	for k, v := range chunk.Metadata {
		meta[k] = v
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Sprintf(`{"doc_id":"%s","index":%d}`, docID, chunk.Index)
	}
	return string(data)
}

func parseVectorMetadata(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil
	}
	result := make(map[string]string, len(meta))
	for k, v := range meta {
		switch val := v.(type) {
		case string:
			result[k] = val
		case float64:
			result[k] = strconv.FormatInt(int64(val), 10)
		case bool:
			result[k] = strconv.FormatBool(val)
		default:
			result[k] = fmt.Sprint(val)
		}
	}
	return result
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
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]float32, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil
		}
		result = append(result, float32(value))
	}
	return result
}
