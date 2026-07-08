package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"

	"gorm.io/gorm"
)

// DocumentService 文档管理业务逻辑层。
type DocumentService struct {
	docRepo   *repo.KnowledgeDocumentRepo
	chunkRepo *repo.KnowledgeChunkRepo
	kbRepo    *repo.KnowledgeBaseRepo
}

// NewDocumentService 创建 DocumentService。
func NewDocumentService(docRepo *repo.KnowledgeDocumentRepo, chunkRepo *repo.KnowledgeChunkRepo, kbRepo *repo.KnowledgeBaseRepo) *DocumentService {
	return &DocumentService{docRepo: docRepo, chunkRepo: chunkRepo, kbRepo: kbRepo}
}

// CreateDocument 创建文档记录，状态为 pending，等待 pipeline 处理。
func (s *DocumentService) CreateDocument(ctx context.Context, kbID string, req dto.CreateDocumentReq, userID string) (*dto.DocumentResp, error) {
	if _, err := s.kbRepo.FindByID(ctx, kbID); err != nil {
		return nil, fmt.Errorf("知识库不存在")
	}
	doc := &model.KnowledgeDocument{
		KbID:       kbID,
		DocName:    req.DocName,
		FileURL:    req.FileURL,
		FileType:   req.FileType,
		FileSize:   req.FileSize,
		SourceType: req.SourceType,
		Status:     "pending",
		CreatedBy:  userID,
	}
	doc.CreateTime = time.Now()
	doc.UpdateTime = time.Now()

	if req.ChunkStrategy != "" {
		doc.ProcessMode = "chunk"
		doc.ChunkStrategy = req.ChunkStrategy
		doc.ChunkConfig = req.ChunkConfig
	}

	if err := s.docRepo.Create(ctx, doc); err != nil {
		return nil, fmt.Errorf("failed to create document: %w", err)
	}
	return s.docToResp(doc), nil
}

// GetDocument 查询文档详情。
func (s *DocumentService) GetDocument(ctx context.Context, id string) (*dto.DocumentResp, error) {
	doc, err := s.docRepo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to get document: %w", err)
	}
	return s.docToResp(doc), nil
}

// UpdateDocument 更新文档。
func (s *DocumentService) UpdateDocument(ctx context.Context, id string, req dto.UpdateDocumentReq, userID string) (*dto.DocumentResp, error) {
	doc, err := s.docRepo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to find document for update: %w", err)
	}

	doc.DocName = req.DocName
	if req.Enabled != nil {
		doc.Enabled = *req.Enabled
	}
	if req.ChunkStrategy != "" {
		doc.ChunkStrategy = req.ChunkStrategy
	}
	if req.ChunkConfig != "" {
		doc.ChunkConfig = req.ChunkConfig
	}
	doc.UpdatedBy = userID
	doc.UpdateTime = time.Now()

	if err := s.docRepo.Update(ctx, doc); err != nil {
		return nil, fmt.Errorf("failed to update document: %w", err)
	}
	return s.docToResp(doc), nil
}

// DeleteDocument 软删除文档及其分块。
func (s *DocumentService) DeleteDocument(ctx context.Context, id string) error {
	if _, err := s.docRepo.FindByID(ctx, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return fmt.Errorf("failed to find document for delete: %w", err)
	}
	if err := s.chunkRepo.DeleteByDocID(ctx, id); err != nil {
		return fmt.Errorf("failed to delete chunks: %w", err)
	}
	return s.docRepo.SoftDelete(ctx, id)
}

// ListDocumentsByKB 按知识库分页查询文档列表。
func (s *DocumentService) ListDocumentsByKB(ctx context.Context, kbID string, page, size int) ([]dto.DocumentResp, int64, error) {
	docs, total, err := s.docRepo.ListByKB(ctx, kbID, page, size)
	if err != nil {
		return nil, 0, err
	}
	resps := make([]dto.DocumentResp, 0, len(docs))
	for i := range docs {
		resps = append(resps, *s.docToResp(&docs[i]))
	}
	return resps, total, nil
}

// SearchDocuments 搜索文档。
func (s *DocumentService) SearchDocuments(ctx context.Context, keyword string, page, size int) ([]dto.DocumentResp, int64, error) {
	docs, total, err := s.docRepo.SearchDocs(ctx, keyword, page, size)
	if err != nil {
		return nil, 0, err
	}
	resps := make([]dto.DocumentResp, 0, len(docs))
	for i := range docs {
		resps = append(resps, *s.docToResp(&docs[i]))
	}
	return resps, total, nil
}

// ListChunks 按文档 ID 分页查询分块列表。
func (s *DocumentService) ListChunks(ctx context.Context, docID string, page, size int) ([]dto.ChunkResp, int64, error) {
	chunks, total, err := s.chunkRepo.ListByDoc(ctx, docID, page, size)
	if err != nil {
		return nil, 0, err
	}
	resps := make([]dto.ChunkResp, 0, len(chunks))
	for i := range chunks {
		resps = append(resps, *s.chunkToResp(&chunks[i]))
	}
	return resps, total, nil
}

// UpdateChunk 更新分块内容。
func (s *DocumentService) UpdateChunk(ctx context.Context, id string, req dto.UpdateChunkReq, userID string) (*dto.ChunkResp, error) {
	chunk, err := s.chunkRepo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to find chunk for update: %w", err)
	}
	chunk.Content = req.Content
	chunk.UpdatedBy = userID
	chunk.UpdateTime = time.Now()
	if err := s.chunkRepo.Update(ctx, chunk); err != nil {
		return nil, fmt.Errorf("failed to update chunk: %w", err)
	}
	return s.chunkToResp(chunk), nil
}

// DeleteChunk 软删除分块。
func (s *DocumentService) DeleteChunk(ctx context.Context, id string) error {
	if _, err := s.chunkRepo.FindByID(ctx, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return fmt.Errorf("failed to find chunk for delete: %w", err)
	}
	return s.chunkRepo.SoftDelete(ctx, id)
}

// ToggleChunk 切换单个分块启用状态。
func (s *DocumentService) ToggleChunk(ctx context.Context, id string, enabled int16) error {
	return s.chunkRepo.UpdateEnabled(ctx, id, enabled)
}

// BatchToggleChunks 批量切换分块启用状态。
func (s *DocumentService) BatchToggleChunks(ctx context.Context, docID string, ids []string, enabled int16) error {
	return s.chunkRepo.BatchUpdateEnabled(ctx, docID, ids, enabled)
}

func (s *DocumentService) docToResp(doc *model.KnowledgeDocument) *dto.DocumentResp {
	return &dto.DocumentResp{
		ID:              doc.ID,
		KbID:            doc.KbID,
		DocName:         doc.DocName,
		Enabled:         doc.Enabled,
		ChunkCount:      doc.ChunkCount,
		FileURL:         doc.FileURL,
		FileType:        doc.FileType,
		FileSize:        doc.FileSize,
		ProcessMode:     doc.ProcessMode,
		Status:          doc.Status,
		SourceType:      doc.SourceType,
		SourceLocation:  doc.SourceLocation,
		ScheduleEnabled: doc.ScheduleEnabled,
		ScheduleCron:    doc.ScheduleCron,
		ChunkStrategy:   doc.ChunkStrategy,
		ChunkConfig:     doc.ChunkConfig,
		PipelineID:      doc.PipelineID,
		CreatedBy:       doc.CreatedBy,
		UpdatedBy:       doc.UpdatedBy,
		CreateTime:      doc.CreateTime.Format(time.RFC3339),
		UpdateTime:      doc.UpdateTime.Format(time.RFC3339),
	}
}

func (s *DocumentService) chunkToResp(c *model.KnowledgeChunk) *dto.ChunkResp {
	return &dto.ChunkResp{
		ID:          c.ID,
		DocID:       c.DocID,
		KbID:        c.KbID,
		ChunkIndex:  c.ChunkIndex,
		Content:     c.Content,
		ContentHash: c.ContentHash,
		CharCount:   c.CharCount,
		TokenCount:  c.TokenCount,
		Enabled:     c.Enabled,
		CreatedBy:   c.CreatedBy,
		UpdatedBy:   c.UpdatedBy,
		CreateTime:  c.CreateTime.Format(time.RFC3339),
		UpdateTime:  c.UpdateTime.Format(time.RFC3339),
	}
}
