package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

var _ VectorStore = (*PgVectorStore)(nil)

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
	chunkIDs := make([]string, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, pgVectorRow{
			ID:             c.ChunkID,
			CollectionName: collectionName,
			Content:        c.Content,
			Metadata:       buildVectorMetadata(docID, c),
			Embedding:      vecToString(c.Embedding),
		})
		chunkIDs = append(chunkIDs, c.ChunkID)
	}
	if err := s.db.WithContext(ctx).Create(rows).Error; err != nil {
		return err
	}
	s.refreshSearchVectors(ctx, chunkIDs)
	return nil
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
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"collection_name", "content", "metadata", "embedding"}),
	}).Create(&row).Error; err != nil {
		return err
	}
	s.refreshSearchVectors(ctx, []string{chunk.ChunkID})
	return nil
}

func (s *PgVectorStore) refreshSearchVectors(ctx context.Context, chunkIDs []string) {
	if s == nil || s.db == nil || len(chunkIDs) == 0 {
		return
	}
	sql, args := refreshSearchVectorSQL(chunkIDs)
	if strings.TrimSpace(sql) == "" {
		return
	}
	if err := s.db.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		slog.Warn("refresh knowledge vector search_vector failed", "err", err)
	}
}

func refreshSearchVectorSQL(chunkIDs []string) (string, []any) {
	if len(chunkIDs) == 0 {
		return "", nil
	}
	return `UPDATE t_knowledge_vector AS v
	        SET search_vector =
	            setweight(to_tsvector('jiebacfg', COALESCE(d.doc_name, '')), 'A') ||
	            setweight(to_tsvector('jiebacfg', COALESCE(v.content, '')), 'D')
	        FROM t_knowledge_document AS d
	        WHERE d.id = v.metadata::jsonb->>'doc_id'
	          AND v.id IN ?`, []any{chunkIDs}
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

// EnsureVectorSpace keeps pgvector compatible with the VectorStore admin API.
func (s *PgVectorStore) EnsureVectorSpace(ctx context.Context, spec VectorSpaceSpec) error {
	return nil
}

// VectorSpaceExists returns true for pgvector logical collections.
func (s *PgVectorStore) VectorSpaceExists(ctx context.Context, spaceID VectorSpaceID) (bool, error) {
	return true, nil
}

// DropVectorSpace deletes all vectors under one logical pgvector collection.
func (s *PgVectorStore) DropVectorSpace(ctx context.Context, collectionName string) error {
	if collectionName == "" {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("collection_name = ?", collectionName).
		Delete(&pgVectorRow{}).Error
}

// Search 向量相似度搜索。
func (s *PgVectorStore) Search(ctx context.Context, collectionName string, vec []float32, topK int) ([]VectorChunk, error) {
	return s.searchCollections(ctx, []string{collectionName}, vec, topK)
}

// SearchCollections performs one vector search across multiple pgvector logical collections.
func (s *PgVectorStore) SearchCollections(ctx context.Context, collectionNames []string, vec []float32, topK int) ([]VectorChunk, error) {
	return s.searchCollections(ctx, collectionNames, vec, topK)
}

func (s *PgVectorStore) searchCollections(ctx context.Context, collectionNames []string, vec []float32, topK int) ([]VectorChunk, error) {
	collections := make([]string, 0, len(collectionNames))
	for _, collectionName := range collectionNames {
		collectionName = strings.TrimSpace(collectionName)
		if collectionName != "" {
			collections = append(collections, collectionName)
		}
	}
	if len(collections) == 0 {
		return nil, nil
	}
	var rows []searchRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		applyPgVectorSearchTuning(ctx, tx)
		return tx.Raw(
			`SELECT id, collection_name, content, metadata, embedding, 1 - (embedding <=> ?) AS score FROM t_knowledge_vector
			 WHERE collection_name IN ?
			   AND deleted = 0
			 ORDER BY embedding <=> ? 
			 LIMIT ?`,
			vecToString(vec), collections, vecToString(vec), topK,
		).Scan(&rows).Error
	})
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}
	return pgVectorRowsToChunks(rows), nil
}

type searchRow struct {
	ID             string  `gorm:"column:id"`
	CollectionName string  `gorm:"column:collection_name"`
	Content        string  `gorm:"column:content"`
	Metadata       string  `gorm:"column:metadata"`
	Embedding      string  `gorm:"column:embedding"`
	Score          float64 `gorm:"column:score"`
}

func pgVectorSearchTuningStatements() []string {
	return []string{
		"SET hnsw.ef_search = 200",
		"SET hnsw.iterative_scan = relaxed_order",
	}
}

func applyPgVectorSearchTuning(ctx context.Context, tx *gorm.DB) {
	if tx == nil {
		return
	}
	for _, statement := range pgVectorSearchTuningStatements() {
		_ = tx.WithContext(ctx).Exec(statement).Error
	}
}

func pgVectorRowsToChunks(rows []searchRow) []VectorChunk {
	chunks := make([]VectorChunk, 0, len(rows))
	for _, r := range rows {
		chunk := VectorChunk{
			ChunkID:   r.ID,
			Content:   r.Content,
			Embedding: stringToVec(r.Embedding),
			Score:     r.Score,
			Metadata:  parseVectorMetadata(r.Metadata),
		}
		if chunk.Metadata == nil {
			chunk.Metadata = make(map[string]string)
		}
		chunk.Metadata["collection_name"] = r.CollectionName
		applyStructuredMetadata(&chunk)
		chunks = append(chunks, chunk)
	}
	return chunks
}

func buildVectorMetadata(docID string, chunk VectorChunk) string {
	meta := make(map[string]interface{}, len(chunk.Metadata)+2)
	meta["doc_id"] = docID
	meta["index"] = chunk.Index
	for k, v := range chunk.Metadata {
		meta[k] = v
	}
	if strings.TrimSpace(chunk.BlockType) != "" {
		meta["block_type"] = chunk.BlockType
	}
	if len(chunk.OutlinePath) > 0 {
		meta["outline_path"] = outlinePathString(chunk.OutlinePath)
	}
	if strings.TrimSpace(chunk.SectionContext) != "" {
		meta["section_context"] = chunk.SectionContext
	}
	if strings.TrimSpace(chunk.Provenance.SourceFile) != "" {
		meta["source_file"] = chunk.Provenance.SourceFile
	}
	if strings.TrimSpace(chunk.Provenance.SheetName) != "" {
		meta["sheet_name"] = chunk.Provenance.SheetName
	}
	if len(chunk.SourceBlockIDs) > 0 {
		meta["source_block_ids"] = strings.Join(appendUniqueStrings(nil, chunk.SourceBlockIDs...), ",")
	}
	if urls := assetURLs(chunk.Assets); len(urls) > 0 {
		meta["asset_urls"] = strings.Join(urls, ",")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(meta)
	if err != nil {
		return fmt.Sprintf(`{"doc_id":"%s","index":%d}`, docID, chunk.Index)
	}
	return strings.TrimSpace(buf.String())
}

func applyStructuredMetadata(chunk *VectorChunk) {
	if chunk == nil || chunk.Metadata == nil {
		return
	}
	if chunk.BlockType == "" {
		chunk.BlockType = chunk.Metadata["block_type"]
	}
	if len(chunk.OutlinePath) == 0 {
		chunk.OutlinePath = splitOutlinePath(chunk.Metadata["outline_path"])
	}
	if chunk.SectionContext == "" {
		chunk.SectionContext = chunk.Metadata["section_context"]
	}
	if strings.TrimSpace(chunk.Provenance.SourceFile) == "" {
		chunk.Provenance.SourceFile = chunk.Metadata["source_file"]
	}
	if strings.TrimSpace(chunk.Provenance.SheetName) == "" {
		chunk.Provenance.SheetName = chunk.Metadata["sheet_name"]
	}
	if len(chunk.SourceBlockIDs) == 0 {
		chunk.SourceBlockIDs = splitCSVMetadata(chunk.Metadata["source_block_ids"])
	}
}

func assetURLs(assets []AssetRef) []string {
	if len(assets) == 0 {
		return nil
	}
	urls := make([]string, 0, len(assets))
	for _, asset := range assets {
		if strings.TrimSpace(asset.PublicURL) != "" {
			urls = appendUniqueStrings(urls, asset.PublicURL)
		}
	}
	return urls
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
