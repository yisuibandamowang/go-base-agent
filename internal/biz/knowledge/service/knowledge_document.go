package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/core/parser"
	ingestionDto "go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/knowledge/dto"
	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/embedding"

	"gorm.io/gorm"
)

// DocumentService 文档管理业务逻辑层。
type DocumentService struct {
	docRepo       *repo.KnowledgeDocumentRepo
	chunkRepo     *repo.KnowledgeChunkRepo
	kbRepo        knowledgeBaseFinder
	db            *gorm.DB
	emb           embedding.Service
	vecStore      vectorStore
	fileStore     FileReader
	ingestion     ingestionTaskStarter
	auditRecorder *auditService.BizChangeLogService
}

type knowledgeBaseFinder interface {
	FindByID(ctx context.Context, id string) (*model.KnowledgeBase, error)
}

type vectorStore interface {
	DeleteDocumentVectors(ctx context.Context, collectionName, docID string) error
	IndexDocumentChunks(ctx context.Context, collectionName, docID string, chunks []rag.VectorChunk) error
}

// FileReader reads file content for chunk processing.
type FileReader interface {
	Read(docID string) ([]byte, error)
}

type ingestionTaskStarter interface {
	Create(ctx context.Context, req ingestionDto.CreateTaskReq, userID string) (*ingestionDto.IngestionResultResp, error)
}

// NewDocumentService 创建 DocumentService。
func NewDocumentService(
	docRepo *repo.KnowledgeDocumentRepo,
	chunkRepo *repo.KnowledgeChunkRepo,
	kbRepo *repo.KnowledgeBaseRepo,
	db *gorm.DB,
	emb embedding.Service,
	vecStore *rag.PgVectorStore,
	fileStore FileReader,
) *DocumentService {
	return &DocumentService{
		docRepo:   docRepo,
		chunkRepo: chunkRepo,
		kbRepo:    kbRepo,
		db:        db,
		emb:       emb,
		vecStore:  vecStore,
		fileStore: fileStore,
	}
}

// SetIngestionTaskStarter sets the optional ingestion task service for pipeline-mode documents.
func (s *DocumentService) SetIngestionTaskStarter(starter ingestionTaskStarter) {
	s.ingestion = starter
}

// SetAuditRecorder 设置审计日志记录器。
func (s *DocumentService) SetAuditRecorder(recorder *auditService.BizChangeLogService) {
	s.auditRecorder = recorder
}

// CreateDocument 创建文档记录，状态为 pending。
func (s *DocumentService) CreateDocument(ctx context.Context, kbID string, req dto.CreateDocumentReq, userID string) (*dto.DocumentResp, error) {
	if _, err := s.kbRepo.FindByID(ctx, kbID); err != nil {
		return nil, fmt.Errorf("知识库不存在")
	}
	doc := &model.KnowledgeDocument{
		KbID:           kbID,
		DocName:        req.DocName,
		FileURL:        req.FileURL,
		FileType:       req.FileType,
		FileSize:       req.FileSize,
		SourceType:     req.SourceType,
		Status:         "pending",
		CreatedBy:      userID,
		SourceLocation: req.SourceLocation,
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
	resp := s.docToResp(doc)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeKnowledgeDocument,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建文档：" + resp.DocName,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// StartChunk 开始文档分块处理：状态校验 → 更新为 running → 异步执行分块任务。
// 对齐 Java KnowledgeDocumentServiceImpl.startChunk。
func (s *DocumentService) StartChunk(ctx context.Context, docID string, userID string) error {
	doc, err := s.docRepo.FindByID(ctx, docID)
	if err != nil {
		return fmt.Errorf("文档不存在")
	}
	if doc.Status == "running" {
		return fmt.Errorf("文档分块操作正在进行中，请稍后再试")
	}
	// CAS 更新状态：仅当非 running 时更新
	result := s.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Where("id = ? AND status != ?", docID, "running").
		Updates(map[string]interface{}{
			"status":      "running",
			"updated_by":  userID,
			"update_time": time.Now(),
		})
	if result.RowsAffected == 0 {
		return fmt.Errorf("文档分块操作正在进行中，请稍后再试")
	}
	// 异步执行分块任务
	go s.executeChunk(docID)
	return nil
}

// executeChunk 执行分块任务（对应 Java runChunkTask）。
func (s *DocumentService) executeChunk(docID string) {
	ctx := context.Background()
	startTime := time.Now()

	doc, err := s.docRepo.FindByID(ctx, docID)
	if err != nil {
		slog.Error("chunk task: document not found", "docId", docID, "err", err)
		return
	}

	// 1. 创建分块日志
	chunkLog := &model.KnowledgeDocumentChunkLog{
		DocID:         docID,
		Status:        "running",
		ProcessMode:   doc.ProcessMode,
		ChunkStrategy: doc.ChunkStrategy,
		PipelineID:    doc.PipelineID,
	}
	now := time.Now()
	chunkLog.StartTime = &now
	chunkLog.CreateTime = now
	chunkLog.UpdateTime = now
	if err := s.db.Create(chunkLog).Error; err != nil {
		slog.Error("chunk task: create log failed", "docId", docID, "err", err)
	}

	var chunkResults []rag.VectorChunk
	var extractDuration, chunkDuration, embedDuration, persistDuration int64

	// 2. 执行分块处理
	if doc.ProcessMode == "pipeline" {
		result, err := s.runPipelineProcess(ctx, doc)
		if err != nil {
			slog.Error("chunk task: pipeline process failed", "docId", docID, "err", err)
			s.markChunkFailed(ctx, docID)
			s.updateChunkLog(chunkLog.ID, "failed", 0, 0, 0, 0, 0, time.Since(startTime).Milliseconds(), err.Error())
			return
		}
		s.markPipelineCompleted(ctx, docID, result.ChunkCount)
		s.updateChunkLog(chunkLog.ID, "success", result.ChunkCount, 0, 0, 0, 0, time.Since(startTime).Milliseconds(), result.Message)
		slog.Info("chunk task: pipeline completed", "docId", docID, "taskId", result.TaskID, "chunks", result.ChunkCount)
		return
	}
	extractDuration, chunkDuration, embedDuration, chunkResults, err = s.runChunkProcess(ctx, doc)
	if err != nil {
		slog.Error("chunk task: process failed", "docId", docID, "err", err)
		s.markChunkFailed(ctx, docID)
		s.updateChunkLog(chunkLog.ID, "failed", 0, extractDuration, chunkDuration, embedDuration, 0, time.Since(startTime).Milliseconds(), err.Error())
		return
	}

	// 3. 持久化分块和向量（事务）
	persistStart := time.Now()
	savedCount, err := s.persistChunksAndVectors(ctx, doc, chunkResults)
	persistDuration = time.Since(persistStart).Milliseconds()
	if err != nil {
		slog.Error("chunk task: persist failed", "docId", docID, "err", err)
		s.markChunkFailed(ctx, docID)
		s.updateChunkLog(chunkLog.ID, "failed", 0, extractDuration, chunkDuration, embedDuration, persistDuration, time.Since(startTime).Milliseconds(), err.Error())
		return
	}

	// 4. 更新分块日志为成功
	s.updateChunkLog(chunkLog.ID, "success", savedCount, extractDuration, chunkDuration, embedDuration, persistDuration, time.Since(startTime).Milliseconds(), "")
	slog.Info("chunk task: completed", "docId", docID, "chunks", savedCount)
}

func (s *DocumentService) runPipelineProcess(ctx context.Context, doc *model.KnowledgeDocument) (*ingestionDto.IngestionResultResp, error) {
	if s.ingestion == nil {
		return nil, fmt.Errorf("pipeline ingestion service not configured")
	}
	if strings.TrimSpace(doc.PipelineID) == "" {
		return nil, fmt.Errorf("pipeline id is empty")
	}
	result, err := s.ingestion.Create(ctx, ingestionDto.CreateTaskReq{
		PipelineID: doc.PipelineID,
		Source: ingestionDto.DocumentSourceReq{
			Type:     firstNonEmpty(doc.SourceType, doc.FileType, "file"),
			Location: firstNonEmpty(documentSourceURL(doc), doc.SourceLocation, doc.FileURL),
			FileName: doc.DocName,
		},
		Metadata: map[string]any{
			"docId": doc.ID,
			"kbId":  doc.KbID,
		},
	}, doc.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("create ingestion task: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("create ingestion task: empty result")
	}
	status := strings.ToLower(strings.TrimSpace(result.Status))
	if status != "" && status != "completed" && status != "success" {
		return nil, fmt.Errorf("ingestion task %s status %s", result.TaskID, result.Status)
	}
	return result, nil
}

// ExecuteIngestionTask 执行 pipeline 任务对应的实际文档入库动作。
func (s *DocumentService) ExecuteIngestionTask(ctx context.Context, req ingestionDto.CreateTaskReq) (int, error) {
	docID, _ := req.Metadata["docId"].(string)
	if strings.TrimSpace(docID) == "" {
		return 0, fmt.Errorf("ingestion metadata docId is empty")
	}
	doc, err := s.docRepo.FindByID(ctx, docID)
	if err != nil {
		return 0, fmt.Errorf("document not found: %w", err)
	}
	_, _, _, chunks, err := s.runChunkProcess(ctx, doc)
	if err != nil {
		return 0, err
	}
	savedCount, err := s.persistChunksAndVectors(ctx, doc, chunks)
	if err != nil {
		return 0, err
	}
	return savedCount, nil
}

// runChunkProcess 执行分块处理：Extract → Chunk → Embed
// 对齐 Java KnowledgeDocumentServiceImpl.runChunkProcess
func (s *DocumentService) runChunkProcess(ctx context.Context, doc *model.KnowledgeDocument) (int64, int64, int64, []rag.VectorChunk, error) {
	extractStart := time.Now()

	// 1. 读取文件内容
	fileBytes, err := s.fileStore.Read(doc.ID)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("读取文件内容失败: %w", err)
	}
	if len(fileBytes) == 0 {
		return 0, 0, 0, nil, fmt.Errorf("文件内容为空")
	}

	// 2. 文档解析：按 MIME/文件类型选择解析器，保留结构化 blocks
	parsed, err := parser.DefaultRegistry().Parse(ctx, fileBytes, detectDocumentMIME(doc))
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("文件解析失败: %w", err)
	}
	text := sanitizeText(rag.RenderBlocks(parsed.Blocks))
	if text == "" {
		return 0, 0, 0, nil, fmt.Errorf("文件无有效文本内容")
	}
	extractDuration := time.Since(extractStart).Milliseconds()

	// 3. 分块处理
	chunkStart := time.Now()
	chunks := s.chunkDocument(ctx, doc, parsed.Blocks, text)
	chunkDuration := time.Since(chunkStart).Milliseconds()

	// 4. 向量化
	embedStart := time.Now()
	kb, err := s.kbRepo.FindByID(ctx, doc.KbID)
	if err != nil {
		return extractDuration, chunkDuration, 0, nil, fmt.Errorf("知识库不存在")
	}
	vecChunks := make([]rag.VectorChunk, 0, len(chunks))
	lineRanges := lineRangesForChunks(text, chunks)
	for i, c := range chunks {
		vec, embErr := s.emb.EmbedWithModel(ctx, c.Content, kb.EmbeddingModel)
		if embErr != nil {
			slog.Warn("chunk task: embed failed", "chunkId", c.ID, "err", embErr)
			continue
		}
		lineRange := lineRanges[i]
		vecChunks = append(vecChunks, rag.VectorChunk{
			ChunkID:       c.ID,
			DocID:         doc.ID,
			Content:       c.Content,
			Embedding:     vec,
			EmbeddingText: c.Content,
			Index:         c.ChunkIndex,
			Metadata: map[string]string{
				"doc_id":      doc.ID,
				"doc_name":    doc.DocName,
				"source_type": doc.SourceType,
				"source_url":  documentSourceURL(doc),
				"page_start":  "1",
				"page_end":    "1",
				"line_start":  fmt.Sprintf("%d", lineRange.start),
				"line_end":    fmt.Sprintf("%d", lineRange.end),
				"chunk_index": fmt.Sprintf("%d", c.ChunkIndex),
			},
		})
	}
	if len(vecChunks) == 0 {
		return extractDuration, chunkDuration, 0, nil, fmt.Errorf("所有分块向量化失败")
	}
	embedDuration := time.Since(embedStart).Milliseconds()

	return extractDuration, chunkDuration, embedDuration, vecChunks, nil
}

// chunkText splits text into chunks using simple paragraph-based splitting.
func (s *DocumentService) chunkDocument(ctx context.Context, doc *model.KnowledgeDocument, blocks []rag.Block, fallbackText string) []*model.KnowledgeChunk {
	opts := chunkingOptionsForDocument(doc)
	var chunkTexts []string
	if len(blocks) > 0 {
		chunker := &rag.StructureAwareChunker{}
		vecChunks := chunker.ChunkBlocks(blocks, opts)
		chunkTexts = make([]string, 0, len(vecChunks))
		for _, chunk := range vecChunks {
			if strings.TrimSpace(chunk.Content) != "" {
				chunkTexts = append(chunkTexts, chunk.Content)
			}
		}
	}
	if len(chunkTexts) == 0 {
		chunkTexts = splitToChunks(fallbackText, opts.ChunkSize, opts.OverlapSize)
	}
	result := make([]*model.KnowledgeChunk, 0, len(chunkTexts))
	for i, content := range chunkTexts {
		c := &model.KnowledgeChunk{
			KbID:       doc.KbID,
			DocID:      doc.ID,
			ChunkIndex: i,
			Content:    content,
			CharCount:  len([]rune(content)),
			Enabled:    1,
			CreatedBy:  doc.CreatedBy,
		}
		c.CreateTime = time.Now()
		c.UpdateTime = time.Now()
		result = append(result, c)
	}
	_ = ctx
	return result
}

func chunkingOptionsForDocument(doc *model.KnowledgeDocument) rag.ChunkingOptions {
	opts := rag.DefaultChunkingOptions()
	if strings.TrimSpace(doc.ChunkConfig) == "" {
		return opts
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(doc.ChunkConfig), &raw); err != nil {
		return opts
	}
	if v := intFromChunkConfig(raw, "chunkSize", "size", "targetChars"); v > 0 {
		opts.ChunkSize = v
	}
	if v := intFromChunkConfig(raw, "overlapSize", "overlap"); v >= 0 {
		opts.OverlapSize = v
	}
	return opts
}

func intFromChunkConfig(raw map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch v := value.(type) {
			case float64:
				return int(v)
			case int:
				return v
			case string:
				if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					return parsed
				}
			}
		}
	}
	return -1
}

func detectDocumentMIME(doc *model.KnowledgeDocument) string {
	fileType := strings.ToLower(strings.TrimSpace(doc.FileType))
	switch fileType {
	case "pdf":
		return "application/pdf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "csv":
		return "text/csv"
	case "md", "markdown":
		return "text/markdown"
	case "html", "htm":
		return "text/html"
	case "txt", "text":
		return "text/plain"
	case "json":
		return "application/json"
	default:
		ext := strings.ToLower(filepath.Ext(doc.DocName))
		switch ext {
		case ".pdf":
			return "application/pdf"
		case ".docx":
			return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case ".xlsx":
			return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		case ".csv":
			return "text/csv"
		case ".md", ".markdown":
			return "text/markdown"
		case ".html", ".htm":
			return "text/html"
		case ".json":
			return "application/json"
		default:
			return "text/plain"
		}
	}
}

func splitToChunks(text string, size, overlap int) []string {
	runes := []rune(text)
	if len(runes) <= size {
		return []string{text}
	}
	var chunks []string
	step := size - overlap
	if step <= 0 {
		step = size
	}
	for i := 0; i < len(runes); i += step {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}

type lineRange struct {
	start int
	end   int
}

func lineRangesForChunks(text string, chunks []*model.KnowledgeChunk) []lineRange {
	ranges := make([]lineRange, 0, len(chunks))
	searchFrom := 0
	for _, chunk := range chunks {
		idx := strings.Index(text[searchFrom:], chunk.Content)
		if idx >= 0 {
			idx += searchFrom
		} else {
			idx = strings.Index(text, chunk.Content)
		}
		if idx < 0 {
			idx = 0
		}
		startLine := strings.Count(text[:idx], "\n") + 1
		endLine := startLine + strings.Count(chunk.Content, "\n")
		ranges = append(ranges, lineRange{start: startLine, end: endLine})
		if idx+1 > searchFrom {
			searchFrom = idx + 1
		}
	}
	return ranges
}

func documentSourceURL(doc *model.KnowledgeDocument) string {
	if strings.HasPrefix(doc.SourceLocation, "http://") || strings.HasPrefix(doc.SourceLocation, "https://") {
		return doc.SourceLocation
	}
	if strings.HasPrefix(doc.FileURL, "http://") || strings.HasPrefix(doc.FileURL, "https://") {
		return doc.FileURL
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// persistChunksAndVectors 原子性写入分块和向量。
func (s *DocumentService) persistChunksAndVectors(ctx context.Context, doc *model.KnowledgeDocument, vecChunks []rag.VectorChunk) (int, error) {
	kb, err := s.kbRepo.FindByID(ctx, doc.KbID)
	if err != nil {
		return 0, fmt.Errorf("知识库不存在: %w", err)
	}

	// 构建分块记录
	chunks := make([]*model.KnowledgeChunk, 0, len(vecChunks))
	for _, vc := range vecChunks {
		chunks = append(chunks, &model.KnowledgeChunk{
			KbID:       doc.KbID,
			DocID:      doc.ID,
			ChunkIndex: vc.Index,
			Content:    vc.Content,
			CharCount:  len([]rune(vc.Content)),
			Enabled:    1,
			CreatedBy:  doc.CreatedBy,
		})
	}

	// 事务：删除旧分块 + 创建新分块 + 删除旧向量 + 写入新向量 + 更新文档状态
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 删除旧分块
		if err := tx.Model(&model.KnowledgeChunk{}).
			Where("doc_id = ? AND deleted = 0", doc.ID).
			Updates(map[string]interface{}{"deleted": 1, "update_time": time.Now()}).Error; err != nil {
			return err
		}
		// 创建新分块
		for _, c := range chunks {
			c.CreateTime = time.Now()
			c.UpdateTime = time.Now()
			if err := tx.Create(c).Error; err != nil {
				return err
			}
		}
		// 更新文档状态
		return tx.Model(&model.KnowledgeDocument{}).Where("id = ?", doc.ID).
			Updates(map[string]interface{}{
				"status":      "success",
				"chunk_count": len(chunks),
				"update_time": time.Now(),
			}).Error
	})
	if err != nil {
		return 0, fmt.Errorf("persist chunks: %w", err)
	}

	for i := range vecChunks {
		if chunks[i].ID == "" {
			return 0, fmt.Errorf("persist chunks: generated chunk id is empty at index %d", i)
		}
		vecChunks[i].ChunkID = chunks[i].ID
		completeVectorChunkMetadata(doc, &vecChunks[i])
	}

	// 向量写入（事务外，避免长事务）
	_ = s.vecStore.DeleteDocumentVectors(ctx, kb.CollectionName, doc.ID)
	if err := s.vecStore.IndexDocumentChunks(ctx, kb.CollectionName, doc.ID, vecChunks); err != nil {
		slog.Error("persist vectors failed", "docId", doc.ID, "err", err)
		return 0, fmt.Errorf("persist vectors: %w", err)
	}

	return len(chunks), nil
}

func completeVectorChunkMetadata(doc *model.KnowledgeDocument, chunk *rag.VectorChunk) {
	if chunk.Metadata == nil {
		chunk.Metadata = make(map[string]string)
	}
	chunk.Metadata["doc_id"] = doc.ID
	chunk.Metadata["doc_name"] = doc.DocName
	chunk.Metadata["source_type"] = doc.SourceType
	if sourceURL := documentSourceURL(doc); sourceURL != "" {
		chunk.Metadata["source_url"] = sourceURL
	}
	chunk.Metadata["chunk_index"] = fmt.Sprintf("%d", chunk.Index)
}

func (s *DocumentService) markChunkFailed(ctx context.Context, docID string) {
	s.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).Where("id = ?", docID).
		Updates(map[string]interface{}{"status": "failed", "update_time": time.Now()})
}

func (s *DocumentService) markPipelineCompleted(ctx context.Context, docID string, chunkCount int) {
	s.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).Where("id = ?", docID).
		Updates(map[string]interface{}{"status": "success", "chunk_count": chunkCount, "update_time": time.Now()})
}

func (s *DocumentService) updateChunkLog(logID, status string, chunkCount int, extractDuration, chunkDuration, embedDuration, persistDuration, totalDuration int64, errorMsg string) {
	now := time.Now()
	updates := map[string]interface{}{
		"status":           status,
		"chunk_count":      chunkCount,
		"extract_duration": extractDuration,
		"chunk_duration":   chunkDuration,
		"embed_duration":   embedDuration,
		"persist_duration": persistDuration,
		"total_duration":   totalDuration,
		"end_time":         now,
		"update_time":      now,
	}
	if errorMsg != "" {
		updates["error_message"] = errorMsg
	}
	s.db.Model(&model.KnowledgeDocumentChunkLog{}).Where("id = ?", logID).Updates(updates)
}

// --- 原有 CRUD 方法 ---

// GetDocument 查询文档详情。
func (s *DocumentService) GetDocument(ctx context.Context, id string) (*dto.DocumentResp, error) {
	doc, err := s.docRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("document not found: %w", err)
	}
	return s.docToResp(doc), nil
}

// ListDocumentsByKB 按知识库分页查询文档。
func (s *DocumentService) ListDocumentsByKB(ctx context.Context, kbID string, page, size int) ([]dto.DocumentResp, int64, error) {
	docs, total, err := s.docRepo.ListByKB(ctx, kbID, page, size)
	if err != nil {
		return nil, 0, err
	}
	records := make([]dto.DocumentResp, 0, len(docs))
	for _, d := range docs {
		records = append(records, *s.docToResp(&d))
	}
	return records, total, nil
}

// UpdateDocument 更新文档。
func (s *DocumentService) UpdateDocument(ctx context.Context, id string, req dto.UpdateDocumentReq, userID string) (*dto.DocumentResp, error) {
	doc, err := s.docRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("document not found: %w", err)
	}
	before := s.docToResp(doc)
	doc.DocName = req.DocName
	doc.UpdatedBy = userID
	if req.ChunkStrategy != "" {
		doc.ChunkStrategy = req.ChunkStrategy
		doc.ChunkConfig = req.ChunkConfig
	}
	if err := s.docRepo.Update(ctx, doc); err != nil {
		return nil, fmt.Errorf("update document: %w", err)
	}
	resp := s.docToResp(doc)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeDocument,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新文档：" + resp.DocName,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// DeleteDocument 软删除文档。
func (s *DocumentService) DeleteDocument(ctx context.Context, id string) error {
	doc, err := s.docRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	before := s.docToResp(doc)
	if err := s.docRepo.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeDocument,
		BizID:          id,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除文档：" + before.DocName,
		BeforeSnapshot: before,
	})
	return nil
}

// ToggleDocument 切换文档启用状态。
func (s *DocumentService) ToggleDocument(ctx context.Context, id string, enabled int16) error {
	doc, err := s.docRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	before := s.docToResp(doc)
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&model.KnowledgeDocument{}).
		Where("id = ? AND deleted = 0", id).
		Updates(map[string]interface{}{"enabled": enabled, "update_time": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	doc.Enabled = enabled
	doc.UpdateTime = now
	after := s.docToResp(doc)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeDocument,
		BizID:          id,
		OperationType:  operationForEnabled(enabled),
		ActionDesc:     "切换文档状态：" + after.DocName,
		BeforeSnapshot: before,
		AfterSnapshot:  after,
	})
	return nil
}

// SearchDocuments 搜索文档。
func (s *DocumentService) SearchDocuments(ctx context.Context, keyword string, page, size int) ([]dto.DocumentResp, int64, error) {
	docs, total, err := s.docRepo.SearchDocs(ctx, keyword, page, size)
	if err != nil {
		return nil, 0, err
	}
	records := make([]dto.DocumentResp, 0, len(docs))
	for _, d := range docs {
		records = append(records, *s.docToResp(&d))
	}
	return records, total, nil
}

// ListChunks 查询文档分块列表。
func (s *DocumentService) ListChunks(ctx context.Context, docID string, page, size int) ([]dto.ChunkResp, int64, error) {
	chunks, total, err := s.chunkRepo.ListByDoc(ctx, docID, page, size)
	if err != nil {
		return nil, 0, err
	}
	records := make([]dto.ChunkResp, 0, len(chunks))
	for _, c := range chunks {
		records = append(records, *s.chunkToResp(&c))
	}
	return records, total, nil
}

// CreateChunk 手工创建文档分块。
func (s *DocumentService) CreateChunk(ctx context.Context, docID string, req dto.CreateChunkReq, userID string) (*dto.ChunkResp, error) {
	doc, err := s.docRepo.FindByID(ctx, docID)
	if err != nil {
		return nil, fmt.Errorf("document not found: %w", err)
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, fmt.Errorf("分块内容不能为空")
	}
	var nextIndex int
	if err := s.db.WithContext(ctx).Model(&model.KnowledgeChunk{}).
		Where("doc_id = ? AND deleted = 0", docID).
		Select("COALESCE(MAX(chunk_index), -1) + 1").
		Scan(&nextIndex).Error; err != nil {
		return nil, fmt.Errorf("查询分块序号失败: %w", err)
	}
	hash := sha256.Sum256([]byte(content))
	chunk := &model.KnowledgeChunk{
		KbID:        doc.KbID,
		DocID:       doc.ID,
		ChunkIndex:  nextIndex,
		Content:     content,
		ContentHash: fmt.Sprintf("%x", hash[:]),
		CharCount:   len([]rune(content)),
		TokenCount:  len(strings.Fields(content)),
		Enabled:     1,
		CreatedBy:   userID,
		UpdatedBy:   userID,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(chunk).Error; err != nil {
			return err
		}
		return tx.Model(&model.KnowledgeDocument{}).Where("id = ?", doc.ID).
			Updates(map[string]interface{}{
				"chunk_count": gorm.Expr("chunk_count + 1"),
				"status":      "success",
				"update_time": time.Now(),
			}).Error
	}); err != nil {
		return nil, fmt.Errorf("create chunk: %w", err)
	}
	resp := s.chunkToResp(chunk)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:       auditService.BizTypeKnowledgeChunk,
		BizID:         resp.ID,
		OperationType: auditService.OperationCreate,
		ActionDesc:    "创建分块：" + doc.DocName,
		AfterSnapshot: resp,
	})
	return resp, nil
}

// UpdateChunk 更新分块内容。
func (s *DocumentService) UpdateChunk(ctx context.Context, chunkID string, req dto.UpdateChunkReq, userID string) (*dto.ChunkResp, error) {
	chunk, err := s.chunkRepo.FindByID(ctx, chunkID)
	if err != nil {
		return nil, err
	}
	before := s.chunkToResp(chunk)
	chunk.Content = req.Content
	chunk.UpdatedBy = userID
	if err := s.chunkRepo.Update(ctx, chunk); err != nil {
		return nil, fmt.Errorf("update chunk: %w", err)
	}
	resp := s.chunkToResp(chunk)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeChunk,
		BizID:          resp.ID,
		OperationType:  auditService.OperationUpdate,
		ActionDesc:     "更新分块：" + resp.DocID,
		BeforeSnapshot: before,
		AfterSnapshot:  resp,
	})
	return resp, nil
}

// DeleteChunk 软删除分块。
func (s *DocumentService) DeleteChunk(ctx context.Context, chunkID string) error {
	chunk, err := s.chunkRepo.FindByID(ctx, chunkID)
	if err != nil {
		return err
	}
	before := s.chunkToResp(chunk)
	if err := s.chunkRepo.SoftDelete(ctx, chunkID); err != nil {
		return err
	}
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeChunk,
		BizID:          chunkID,
		OperationType:  auditService.OperationDelete,
		ActionDesc:     "删除分块：" + before.DocID,
		BeforeSnapshot: before,
	})
	return nil
}

// ToggleChunk 切换分块启用状态。
func (s *DocumentService) ToggleChunk(ctx context.Context, chunkID string, enabled int16) error {
	chunk, err := s.chunkRepo.FindByID(ctx, chunkID)
	if err != nil {
		return err
	}
	before := s.chunkToResp(chunk)
	if err := s.chunkRepo.UpdateEnabled(ctx, chunkID, enabled); err != nil {
		return err
	}
	chunk.Enabled = enabled
	after := s.chunkToResp(chunk)
	s.recordAudit(ctx, auditService.RecordReq{
		BizType:        auditService.BizTypeKnowledgeChunk,
		BizID:          chunkID,
		OperationType:  operationForEnabled(enabled),
		ActionDesc:     "切换分块状态：" + after.DocID,
		BeforeSnapshot: before,
		AfterSnapshot:  after,
	})
	return nil
}

// BatchToggleChunks 批量切换分块启用状态。
func (s *DocumentService) BatchToggleChunks(ctx context.Context, docID string, ids []string, enabled int16) error {
	if len(ids) == 0 {
		return nil
	}
	var chunks []model.KnowledgeChunk
	if err := s.db.WithContext(ctx).
		Where("doc_id = ? AND id IN ? AND deleted = 0", docID, ids).
		Order("chunk_index ASC").
		Find(&chunks).Error; err != nil {
		return err
	}
	if err := s.chunkRepo.BatchUpdateEnabled(ctx, docID, ids, enabled); err != nil {
		return err
	}
	for i := range chunks {
		before := s.chunkToResp(&chunks[i])
		chunks[i].Enabled = enabled
		chunks[i].UpdateTime = time.Now()
		after := s.chunkToResp(&chunks[i])
		s.recordAudit(ctx, auditService.RecordReq{
			BizType:        auditService.BizTypeKnowledgeChunk,
			BizID:          chunks[i].ID,
			OperationType:  operationForEnabled(enabled),
			ActionDesc:     "批量切换分块状态：" + docID,
			BeforeSnapshot: before,
			AfterSnapshot:  after,
		})
	}
	return nil
}

// GetChunkLogs 查询文档分块日志。
func (s *DocumentService) GetChunkLogs(ctx context.Context, docID string, page, size int) ([]model.KnowledgeDocumentChunkLog, int64, error) {
	var logs []model.KnowledgeDocumentChunkLog
	var total int64
	q := s.db.WithContext(ctx).Model(&model.KnowledgeDocumentChunkLog{}).
		Where("doc_id = ?", docID)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("create_time DESC").Limit(size).Offset((page - 1) * size).Find(&logs).Error; err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

// PreviewDocument returns the full text of a document by concatenating all its chunks.
func (s *DocumentService) PreviewDocument(ctx context.Context, docID string) (string, error) {
	chunks, _, err := s.chunkRepo.ListByDoc(ctx, docID, 1, 500)
	if err != nil {
		return "", fmt.Errorf("查询分块失败: %w", err)
	}
	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString(c.Content)
	}
	return sb.String(), nil
}

// --- helpers ---

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

func (s *DocumentService) recordAudit(ctx context.Context, req auditService.RecordReq) {
	if s.auditRecorder == nil {
		return
	}
	if err := s.auditRecorder.Record(ctx, req); err != nil {
		slog.Warn("audit record failed", "err", err, "biz_type", req.BizType, "biz_id", req.BizID)
	}
}

func operationForEnabled(enabled int16) string {
	if enabled == 1 {
		return auditService.OperationEnable
	}
	return auditService.OperationDisable
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

// sanitizeText removes null bytes and non-printable characters from text content.
func sanitizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == 0 {
			continue // skip null bytes
		}
		if r == '\t' || r == '\n' || r == '\r' || (r >= ' ' && r <= '~') || r > 127 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
