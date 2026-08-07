package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/core/parser"
	ingestionDto "go-base-agent/internal/biz/ingestion/dto"
	ingestionModel "go-base-agent/internal/biz/ingestion/model"
	ingestionService "go-base-agent/internal/biz/ingestion/service"
	knowledgeDto "go-base-agent/internal/biz/knowledge/dto"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/rag"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/infra/chat"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeFileReader struct {
	data []byte
}

func (f fakeFileReader) Read(string) ([]byte, error) {
	return f.data, nil
}

type capturingDeletableFileReader struct {
	deleteErr     error
	deletedDocIDs []string
}

func (f *capturingDeletableFileReader) Read(string) ([]byte, error) {
	return nil, nil
}

func (f *capturingDeletableFileReader) Delete(_ context.Context, docID string) error {
	f.deletedDocIDs = append(f.deletedDocIDs, docID)
	return f.deleteErr
}

type fakeEmbeddingService struct{}

func (f fakeEmbeddingService) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (f fakeEmbeddingService) EmbedWithModel(context.Context, string, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (f fakeEmbeddingService) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func (f fakeEmbeddingService) EmbedBatchWithModel(context.Context, []string, string) ([][]float32, error) {
	return nil, nil
}

func (f fakeEmbeddingService) Dimension() int {
	return 2
}

func testSHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

type fakeKnowledgeBaseFinder struct {
	kb *knowledgeModel.KnowledgeBase
}

func (f fakeKnowledgeBaseFinder) FindByID(context.Context, string) (*knowledgeModel.KnowledgeBase, error) {
	return f.kb, nil
}

func newDocumentServiceTestContext(t *testing.T) (*gorm.DB, *knowledgeModel.KnowledgeBase, *DocumentService) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo:    knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     &capturingVectorStore{},
		fileStore:    fakeFileReader{},
	}
	return gdb, kb, svc
}

func TestDocumentService_PreviewDocumentRejectsNonMarkdown(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:      kb.ID,
		DocName:   "会员说明.pdf",
		FileURL:   "upload://会员说明.pdf",
		FileType:  "pdf",
		Status:    "success",
		CreatedBy: "tester",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeChunk{
		BaseModel:  db.BaseModel{ID: "chunk-1"},
		KbID:       kb.ID,
		DocID:      doc.ID,
		Content:    "分块内容",
		ChunkIndex: 1,
		CreatedBy:  "tester",
	}).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
	}

	_, err := svc.PreviewDocument(context.Background(), doc.ID)
	if err == nil || !strings.Contains(err.Error(), "仅支持预览 markdown 格式文档") {
		t.Fatalf("expected markdown-only preview error, got %v", err)
	}
}

func TestDocumentService_PreviewDocumentReadsMarkdownOriginalFile(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	svc.fileStore = fakeFileReader{data: []byte("# 原始 Markdown\n会员权益说明")}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:      kb.ID,
		DocName:   "会员说明.md",
		FileURL:   "upload://会员说明.md",
		FileType:  "markdown",
		Status:    "success",
		CreatedBy: "tester",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeChunk{
		BaseModel:  db.BaseModel{ID: "chunk-1"},
		KbID:       kb.ID,
		DocID:      doc.ID,
		Content:    "分块内容不应作为预览",
		ChunkIndex: 1,
		CreatedBy:  "tester",
	}).Error; err != nil {
		t.Fatalf("seed chunk: %v", err)
	}

	content, err := svc.PreviewDocument(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("preview document: %v", err)
	}
	if content != "# 原始 Markdown\n会员权益说明" {
		t.Fatalf("expected original markdown file content, got %q", content)
	}
}

func TestDocumentService_ListDocumentsMarksChunksEdited(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	ctx := context.Background()
	now := time.Now().Add(-time.Hour).Truncate(time.Second)
	editedDoc := &knowledgeModel.KnowledgeDocument{
		BaseModel: db.BaseModel{
			CreateTime: now,
			UpdateTime: now,
		},
		KbID:      kb.ID,
		DocName:   "edited.md",
		FileURL:   "upload://edited.md",
		FileType:  "md",
		Status:    "success",
		Enabled:   1,
		CreatedBy: "tester",
		UpdatedBy: "tester",
	}
	cleanDoc := &knowledgeModel.KnowledgeDocument{
		BaseModel: db.BaseModel{
			CreateTime: now,
			UpdateTime: now,
		},
		KbID:      kb.ID,
		DocName:   "clean.md",
		FileURL:   "upload://clean.md",
		FileType:  "md",
		Status:    "success",
		Enabled:   1,
		CreatedBy: "tester",
		UpdatedBy: "tester",
	}
	if err := gdb.Create(editedDoc).Error; err != nil {
		t.Fatalf("create edited doc: %v", err)
	}
	if err := gdb.Create(cleanDoc).Error; err != nil {
		t.Fatalf("create clean doc: %v", err)
	}
	editedChunk := &knowledgeModel.KnowledgeChunk{
		BaseModel: db.BaseModel{
			CreateTime: now,
			UpdateTime: now.Add(2 * time.Second),
		},
		KbID:       kb.ID,
		DocID:      editedDoc.ID,
		ChunkIndex: 0,
		Content:    "edited chunk",
		Enabled:    1,
		CreatedBy:  "tester",
	}
	cleanChunk := &knowledgeModel.KnowledgeChunk{
		BaseModel: db.BaseModel{
			CreateTime: now,
			UpdateTime: now,
		},
		KbID:       kb.ID,
		DocID:      cleanDoc.ID,
		ChunkIndex: 0,
		Content:    "clean chunk",
		Enabled:    1,
		CreatedBy:  "tester",
	}
	if err := gdb.Create(editedChunk).Error; err != nil {
		t.Fatalf("create edited chunk: %v", err)
	}
	if err := gdb.Create(cleanChunk).Error; err != nil {
		t.Fatalf("create clean chunk: %v", err)
	}

	records, total, err := svc.ListDocumentsByKB(ctx, kb.ID, 1, 10, "", "")
	if err != nil {
		t.Fatalf("list docs: %v", err)
	}
	if total != 2 || len(records) != 2 {
		t.Fatalf("expected two docs, got total=%d records=%d", total, len(records))
	}
	editedByID := map[string]bool{}
	for _, record := range records {
		if record.ChunksEdited == nil {
			t.Fatalf("expected chunksEdited to be populated for %s", record.DocName)
		}
		editedByID[record.ID] = *record.ChunksEdited
	}
	if !editedByID[editedDoc.ID] {
		t.Fatalf("expected edited doc to be marked chunksEdited")
	}
	if editedByID[cleanDoc.ID] {
		t.Fatalf("expected clean doc not to be marked chunksEdited")
	}
}

type failingVectorStore struct {
	err error
}

func (s failingVectorStore) DeleteDocumentVectors(context.Context, string, string) error {
	return nil
}

func (s failingVectorStore) IndexDocumentChunks(context.Context, string, string, []rag.VectorChunk) error {
	return s.err
}

func (s failingVectorStore) UpdateChunk(context.Context, string, string, rag.VectorChunk) error {
	return nil
}

func (s failingVectorStore) DeleteChunkByID(context.Context, string, string) error {
	return nil
}

func (s failingVectorStore) DeleteChunksByIDs(context.Context, string, []string) error {
	return nil
}

type capturingVectorStore struct {
	deletedDocCalls []string
	indexedChunks   []rag.VectorChunk
	updatedChunks   []rag.VectorChunk
	deletedChunkIDs []string
}

func (s *capturingVectorStore) DeleteDocumentVectors(_ context.Context, collectionName, docID string) error {
	s.deletedDocCalls = append(s.deletedDocCalls, collectionName+"|"+docID)
	return nil
}

func (s *capturingVectorStore) IndexDocumentChunks(_ context.Context, _ string, _ string, chunks []rag.VectorChunk) error {
	s.indexedChunks = append([]rag.VectorChunk(nil), chunks...)
	return nil
}

func (s *capturingVectorStore) UpdateChunk(_ context.Context, _ string, _ string, chunk rag.VectorChunk) error {
	s.updatedChunks = append(s.updatedChunks, chunk)
	return nil
}

func (s *capturingVectorStore) DeleteChunkByID(_ context.Context, _ string, chunkID string) error {
	s.deletedChunkIDs = append(s.deletedChunkIDs, chunkID)
	return nil
}

func (s *capturingVectorStore) DeleteChunksByIDs(_ context.Context, _ string, chunkIDs []string) error {
	s.deletedChunkIDs = append(s.deletedChunkIDs, chunkIDs...)
	return nil
}

type fakeIngestionTaskStarter struct {
	req    ingestionDto.CreateTaskReq
	userID string
	resp   *ingestionDto.IngestionResultResp
	err    error
	delay  time.Duration
}

func (f *fakeIngestionTaskStarter) Create(ctx context.Context, req ingestionDto.CreateTaskReq, userID string) (*ingestionDto.IngestionResultResp, error) {
	f.req = req
	f.userID = userID
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.resp, f.err
}

type fakeIngestionPipelineGetter struct {
	err error
}

func (f fakeIngestionPipelineGetter) Get(context.Context, string) (*ingestionDto.PipelineResp, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &ingestionDto.PipelineResp{ID: "pipe-1", Name: "默认流水线"}, nil
}

func TestDocumentService_StartChunkPublishesMQEventWhenEnabled(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate document table: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeDocument{
		BaseModel: db.BaseModel{ID: "doc-1"},
		KbID:      "kb-1",
		DocName:   "doc.md",
		FileURL:   "upload://doc.md",
		FileType:  "md",
		Status:    "pending",
		CreatedBy: "user-1",
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}

	producer := &capturingKnowledgeMQProducer{}
	svc := &DocumentService{
		docRepo: knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:      gdb,
	}
	svc.SetMQProducer(producer, true)

	if err := svc.StartChunk(context.Background(), "doc-1", "operator-1"); err != nil {
		t.Fatalf("start chunk: %v", err)
	}

	var doc knowledgeModel.KnowledgeDocument
	if err := gdb.First(&doc, "id = ?", "doc-1").Error; err != nil {
		t.Fatalf("load document: %v", err)
	}
	if doc.Status != "running" || doc.UpdatedBy != "operator-1" {
		t.Fatalf("expected document to be marked running by operator, got status=%s updatedBy=%s", doc.Status, doc.UpdatedBy)
	}
	if len(producer.messages) != 1 {
		t.Fatalf("expected one chunk event, got %+v", producer.messages)
	}
	msg := producer.messages[0]
	if msg.Topic != KnowledgeDocumentChunkTopic || msg.Keys != "doc-1" || msg.BizDesc != "文档分块" {
		t.Fatalf("unexpected mq message: %+v", msg)
	}
	var event KnowledgeDocumentChunkEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		t.Fatalf("decode chunk event: %v", err)
	}
	if event.DocID != "doc-1" || event.Operator != "operator-1" {
		t.Fatalf("unexpected chunk event: %+v", event)
	}
}

func TestDocumentService_PersistChunksAndVectorsReturnsVectorError(t *testing.T) {
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 user=postgres dbname=ragent sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open dry-run db: %v", err)
	}

	vectorErr := errors.New("vector insert failed")
	svc := &DocumentService{
		db: gdb,
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			CollectionName: "collection_a",
		}},
		vecStore: failingVectorStore{err: vectorErr},
	}

	_, err = svc.persistChunksAndVectors(context.Background(), &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		CreatedBy: "user-1",
	}, []rag.VectorChunk{
		{ChunkID: "chunk-1", Content: "content", Embedding: []float32{0.1, 0.2}, Index: 0},
	})

	if !errors.Is(err, vectorErr) {
		t.Fatalf("expected vector error, got %v", err)
	}
}

func TestDocumentService_RunPipelineProcessCreatesIngestionTask(t *testing.T) {
	starter := &fakeIngestionTaskStarter{resp: &ingestionDto.IngestionResultResp{
		TaskID:     "task-1",
		PipelineID: "pipe-1",
		Status:     "completed",
		ChunkCount: 3,
		Message:    "OK",
	}}
	svc := &DocumentService{}
	svc.SetIngestionTaskStarter(starter)

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:           "kb-1",
		DocName:        "会员Agent说明.md",
		SourceType:     "url",
		SourceLocation: "https://example.com/member-agent.md",
		PipelineID:     "pipe-1",
		CreatedBy:      "user-1",
	}
	doc.ID = "doc-1"

	result, err := svc.runPipelineProcess(context.Background(), doc)
	if err != nil {
		t.Fatalf("run pipeline process: %v", err)
	}
	if result.TaskID != "task-1" || result.ChunkCount != 3 {
		t.Fatalf("unexpected ingestion result: %+v", result)
	}
	if starter.userID != "user-1" {
		t.Fatalf("expected user-1, got %q", starter.userID)
	}
	if starter.req.PipelineID != "pipe-1" {
		t.Fatalf("expected pipeline id pipe-1, got %q", starter.req.PipelineID)
	}
	if starter.req.Source.Type != "url" || starter.req.Source.Location != "https://example.com/member-agent.md" || starter.req.Source.FileName != "会员Agent说明.md" {
		t.Fatalf("unexpected source request: %+v", starter.req.Source)
	}
	if starter.req.Metadata["docId"] != "doc-1" || starter.req.Metadata["kbId"] != "kb-1" {
		t.Fatalf("unexpected metadata: %+v", starter.req.Metadata)
	}
}

func TestDocumentService_ExecuteChunkRecordsPipelineDuration(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeDocumentChunkLog{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:        "kb-1",
		DocName:     "会员Agent说明.md",
		FileType:    "md",
		ProcessMode: "pipeline",
		PipelineID:  "pipe-1",
		Status:      "running",
		CreatedBy:   "user-1",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	starter := &fakeIngestionTaskStarter{
		delay: 5 * time.Millisecond,
		resp: &ingestionDto.IngestionResultResp{
			TaskID:     "task-1",
			PipelineID: "pipe-1",
			Status:     "completed",
			ChunkCount: 2,
		},
	}
	svc := &DocumentService{
		docRepo: knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:      gdb,
	}
	svc.SetIngestionTaskStarter(starter)

	if err := svc.executeChunk(context.Background(), doc.ID); err != nil {
		t.Fatalf("execute pipeline chunk: %v", err)
	}
	logs, _, err := svc.GetChunkLogs(context.Background(), doc.ID, 1, 10)
	if err != nil {
		t.Fatalf("get chunk logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one chunk log, got %d", len(logs))
	}
	if logs[0].ChunkDuration <= 0 {
		t.Fatalf("expected pipeline execution duration to be recorded as chunkDuration, got %+v", logs[0])
	}
	if logs[0].OtherDuration >= logs[0].TotalDuration {
		t.Fatalf("expected pipeline other duration to exclude chunk duration, got %+v", logs[0])
	}
}

func TestDocumentService_CreateDocumentStoresPipelineMode(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "kb", EmbeddingModel: "emb", CollectionName: "kb_collection", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	svc := NewDocumentService(
		knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		gdb,
		nil,
		nil,
		nil,
	)

	resp, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:       "会员Agent说明.md",
		FileURL:       "upload://会员Agent说明.md",
		FileType:      "md",
		SourceType:    "local_file",
		ChunkStrategy: "pipeline",
		ChunkConfig:   `{"pipelineId":"pipe-1"}`,
	}, "user-1")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if resp.ProcessMode != "pipeline" || resp.PipelineID != "pipe-1" {
		t.Fatalf("expected pipeline mode and id, got %+v", resp)
	}
	if resp.SourceType != "file" {
		t.Fatalf("expected local_file source type to normalize to file, got %+v", resp)
	}
}

func TestDocumentService_CreateDocumentRejectsInvalidProcessMode(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)

	_, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:     "会员Agent说明.md",
		FileURL:     "upload://会员Agent说明.md",
		FileType:    "md",
		SourceType:  "file",
		ProcessMode: "legacy",
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "不支持的处理模式") {
		t.Fatalf("expected unsupported process mode rejection, got %v", err)
	}

	var count int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no document created, got %d", count)
	}
}

func TestDocumentService_CreateDocumentRejectsInvalidChunkConfig(t *testing.T) {
	_, kb, svc := newDocumentServiceTestContext(t)

	_, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:       "会员Agent说明.md",
		FileURL:       "upload://会员Agent说明.md",
		FileType:      "md",
		SourceType:    "file",
		ProcessMode:   "chunk",
		ChunkStrategy: "fixed_size",
		ChunkConfig:   `{"chunkSize":512}`,
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "分块参数缺少必要字段: overlapSize") {
		t.Fatalf("expected missing chunk config field rejection, got %v", err)
	}

	_, err = svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:       "会员Agent说明.md",
		FileURL:       "upload://会员Agent说明.md",
		FileType:      "md",
		SourceType:    "file",
		ProcessMode:   "chunk",
		ChunkStrategy: "fixed_size",
		ChunkConfig:   `{bad json}`,
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "分块参数JSON格式不合法") {
		t.Fatalf("expected invalid chunk config json rejection, got %v", err)
	}
}

func TestDocumentService_UpdateDocumentValidatesProcessModeAndChunkConfig(t *testing.T) {
	_, kb, svc := newDocumentServiceTestContext(t)
	created, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:    "会员Agent说明.md",
		FileURL:    "upload://会员Agent说明.md",
		FileType:   "md",
		SourceType: "file",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	_, err = svc.UpdateDocument(context.Background(), created.ID, knowledgeDto.UpdateDocumentReq{
		DocName:     ptrString("会员Agent说明.md"),
		ProcessMode: "pipeline",
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "使用Pipeline模式时，必须指定Pipeline ID") {
		t.Fatalf("expected missing pipeline id rejection, got %v", err)
	}

	_, err = svc.UpdateDocument(context.Background(), created.ID, knowledgeDto.UpdateDocumentReq{
		DocName:       ptrString("会员Agent说明.md"),
		ProcessMode:   "chunk",
		ChunkStrategy: "structure_aware",
		ChunkConfig:   `{"targetChars":1400}`,
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "分块参数缺少必要字段: overlapChars") {
		t.Fatalf("expected missing structure aware field rejection, got %v", err)
	}
}

func TestDocumentService_ValidatesPipelineExists(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	svc.SetIngestionPipelineGetter(fakeIngestionPipelineGetter{err: errors.New("not found")})

	_, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:     "会员Agent说明.md",
		FileURL:     "upload://会员Agent说明.md",
		FileType:    "md",
		SourceType:  "file",
		ProcessMode: "pipeline",
		PipelineID:  "missing-pipe",
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "指定的Pipeline不存在: missing-pipe") {
		t.Fatalf("expected missing pipeline rejection, got %v", err)
	}

	var count int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no document created, got %d", count)
	}

	svc.SetIngestionPipelineGetter(fakeIngestionPipelineGetter{})
	created, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:     "会员Agent说明.md",
		FileURL:     "upload://会员Agent说明.md",
		FileType:    "md",
		SourceType:  "file",
		ProcessMode: "pipeline",
		PipelineID:  "pipe-1",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create with existing pipeline: %v", err)
	}

	svc.SetIngestionPipelineGetter(fakeIngestionPipelineGetter{err: errors.New("not found")})
	_, err = svc.UpdateDocument(context.Background(), created.ID, knowledgeDto.UpdateDocumentReq{
		DocName:     ptrString("会员Agent说明.md"),
		ProcessMode: "pipeline",
		PipelineID:  "missing-pipe",
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "指定的Pipeline不存在: missing-pipe") {
		t.Fatalf("expected update missing pipeline rejection, got %v", err)
	}
}

func TestDocumentService_CreateDocumentRejectsUnsupportedFileType(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	svc.SetParserRegistry(parser.DefaultRegistry())

	_, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:    "installer.exe",
		FileURL:    "upload://installer.exe",
		FileType:   "exe",
		SourceType: "file",
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "暂不支持的文件类型") {
		t.Fatalf("expected unsupported file type rejection, got %v", err)
	}

	var count int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no document created, got %d", count)
	}
}

func TestDocumentService_ExecuteIngestionTaskPersistsChunksAndVectors(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent说明.md",
		FileType:   "md",
		Status:     "running",
		CreatedBy:  "user-1",
		SourceType: "file",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}

	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		docRepo:  knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:       gdb,
		kbRepo:   fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{EmbeddingModel: "emb-1", CollectionName: "collection_a"}},
		emb:      fakeEmbeddingService{},
		vecStore: vecStore,
		fileStore: fakeFileReader{data: []byte(`会员 Agent 支持权益查询。
会员 Agent 支持积分查询。`)},
	}

	count, err := svc.ExecuteIngestionTask(context.Background(), ingestionDto.CreateTaskReq{
		PipelineID: "pipe-1",
		Metadata:   map[string]any{"docId": "doc-1"},
	})
	if err != nil {
		t.Fatalf("execute ingestion task: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 chunk, got %d", count)
	}
	if len(vecStore.indexedChunks) != 1 || vecStore.indexedChunks[0].Metadata["doc_name"] != "会员Agent说明.md" {
		t.Fatalf("unexpected vector chunks: %+v", vecStore.indexedChunks)
	}
	var updated knowledgeModel.KnowledgeDocument
	if err := gdb.First(&updated, "id = ?", "doc-1").Error; err != nil {
		t.Fatalf("find updated doc: %v", err)
	}
	if updated.Status != "success" || updated.ChunkCount != 1 {
		t.Fatalf("expected completed doc, got status=%s chunks=%d", updated.Status, updated.ChunkCount)
	}
}

func TestDocumentService_ExecuteIngestionPipelineTaskRunsCoreNodes(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent说明.md",
		FileType:   "md",
		Status:     "running",
		CreatedBy:  "user-1",
		SourceType: "file",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}

	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:        gdb,
		kbRepo:    fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{EmbeddingModel: "emb-1", CollectionName: "collection_a"}},
		emb:       pipelineEmbeddingService{},
		vecStore:  vecStore,
		fileStore: fakeFileReader{data: []byte("# 会员 Agent\n支持权益查询。\n支持积分查询。")},
	}

	result, err := svc.ExecuteIngestionPipelineTask(context.Background(), ingestionDto.CreateTaskReq{
		PipelineID: "pipe-1",
		Source: ingestionDto.DocumentSourceReq{
			Type:     "file",
			FileName: "会员Agent说明.md",
		},
		Metadata: map[string]any{"docId": "doc-1"},
	}, []ingestionModel.IngestionPipelineNode{
		{PipelineID: "pipe-1", NodeID: "parser", NodeType: "parser", NextNodeID: "chunker"},
		{PipelineID: "pipe-1", NodeID: "chunker", NodeType: "chunker", NextNodeID: "indexer", SettingsJSON: `{"chunkSize":128}`},
		{PipelineID: "pipe-1", NodeID: "indexer", NodeType: "indexer"},
	})
	if err != nil {
		t.Fatalf("execute pipeline task: %v", err)
	}
	if result.ChunkCount == 0 {
		t.Fatalf("expected chunks to be persisted, got %+v", result)
	}
	nodeByID := make(map[string]ingestionService.TaskNodeExecutionResult, len(result.Nodes))
	for _, node := range result.Nodes {
		nodeByID[node.NodeID] = node
	}
	parserNode := nodeByID["parser"]
	if parserNode.Output["rawText"] == nil || !strings.Contains(fmt.Sprint(parserNode.Output["rawText"]), "会员 Agent") {
		t.Fatalf("expected parser output to contain raw text, got %+v", parserNode.Output)
	}
	chunkerNode := nodeByID["chunker"]
	if chunkerNode.Output["chunkCount"] == nil || fmt.Sprint(chunkerNode.Output["chunkCount"]) == "0" {
		t.Fatalf("expected chunker output to contain chunk count, got %+v", chunkerNode.Output)
	}
	indexerNode := nodeByID["indexer"]
	if indexerNode.Output["chunks"] == nil {
		t.Fatalf("expected indexer output to include embedded chunks, got %+v", indexerNode.Output)
	}
	if len(vecStore.indexedChunks) != result.ChunkCount {
		t.Fatalf("expected vectors persisted after pipeline execution, got %d vectors for %d chunks", len(vecStore.indexedChunks), result.ChunkCount)
	}
}

func TestDocumentService_ExecuteIngestionPipelineTaskReturnsKeywordsAndQuestionsMetadata(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent说明.md",
		FileType:   "md",
		Status:     "running",
		CreatedBy:  "user-1",
		SourceType: "file",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:        gdb,
		kbRepo:    fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{EmbeddingModel: "emb-1", CollectionName: "collection_a"}},
		emb:       pipelineEmbeddingService{},
		vecStore:  &capturingVectorStore{},
		fileStore: fakeFileReader{data: []byte("# 会员 Agent\n支持权益查询。\n支持积分查询。")},
		llm:       &pipelineLLMService{responses: []string{`["会员","积分"]`, `["会员权益怎么查?"]`}},
	}

	result, err := svc.ExecuteIngestionPipelineTask(context.Background(), ingestionDto.CreateTaskReq{
		PipelineID: "pipe-1",
		Source: ingestionDto.DocumentSourceReq{
			Type:     "file",
			FileName: "会员Agent说明.md",
		},
		Metadata: map[string]any{"docId": "doc-1"},
	}, []ingestionModel.IngestionPipelineNode{
		{PipelineID: "pipe-1", NodeID: "parser", NodeType: "parser", NextNodeID: "enhancer"},
		{PipelineID: "pipe-1", NodeID: "enhancer", NodeType: "enhancer", NextNodeID: "chunker", SettingsJSON: `{"tasks":[{"type":"keywords"},{"type":"questions"}]}`},
		{PipelineID: "pipe-1", NodeID: "chunker", NodeType: "chunker", NextNodeID: "indexer", SettingsJSON: `{"chunkSize":128}`},
		{PipelineID: "pipe-1", NodeID: "indexer", NodeType: "indexer"},
	})
	if err != nil {
		t.Fatalf("execute pipeline task: %v", err)
	}
	keywords, ok := result.Metadata["keywords"].([]string)
	if !ok || len(keywords) != 2 || keywords[0] != "会员" {
		t.Fatalf("expected keywords metadata, got %+v", result.Metadata)
	}
	questions, ok := result.Metadata["questions"].([]string)
	if !ok || len(questions) != 1 || questions[0] != "会员权益怎么查?" {
		t.Fatalf("expected questions metadata, got %+v", result.Metadata)
	}
}

func TestDocumentService_ExecuteIngestionPipelineTaskPrefersRequestRawBytes(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent说明.md",
		FileType:   "md",
		Status:     "running",
		CreatedBy:  "user-1",
		SourceType: "file",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:        gdb,
		kbRepo:    fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{EmbeddingModel: "emb-1", CollectionName: "collection_a"}},
		emb:       pipelineEmbeddingService{},
		vecStore:  &capturingVectorStore{},
		fileStore: fakeFileReader{data: []byte("# 旧内容\n不应该被解析。")},
	}

	result, err := svc.ExecuteIngestionPipelineTask(context.Background(), ingestionDto.CreateTaskReq{
		PipelineID: "pipe-1",
		Source: ingestionDto.DocumentSourceReq{
			Type:     "file",
			FileName: "会员Agent说明.md",
		},
		Metadata: map[string]any{"docId": "doc-1"},
		RawBytes: []byte("# 上传内容\n应该被解析。"),
		MimeType: "text/markdown",
	}, []ingestionModel.IngestionPipelineNode{
		{PipelineID: "pipe-1", NodeID: "parser", NodeType: "parser", NextNodeID: "chunker"},
		{PipelineID: "pipe-1", NodeID: "chunker", NodeType: "chunker", SettingsJSON: `{"chunkSize":128}`},
	})
	if err != nil {
		t.Fatalf("execute pipeline task: %v", err)
	}
	nodeByID := make(map[string]ingestionService.TaskNodeExecutionResult, len(result.Nodes))
	for _, node := range result.Nodes {
		nodeByID[node.NodeID] = node
	}
	if !strings.Contains(fmt.Sprint(nodeByID["parser"].Output["rawText"]), "上传内容") {
		t.Fatalf("expected parser to use request raw bytes, got %+v", result.Nodes)
	}
}

func TestDocumentService_ExecuteIngestionPipelineTaskMarksConditionSkippedNode(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent说明.md",
		FileType:   "md",
		Status:     "running",
		CreatedBy:  "user-1",
		SourceType: "file",
	}
	doc.ID = "doc-1"
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:        gdb,
		kbRepo:    fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{EmbeddingModel: "emb-1", CollectionName: "collection_a"}},
		emb:       pipelineEmbeddingService{},
		vecStore:  &capturingVectorStore{},
		fileStore: fakeFileReader{data: []byte("# 会员 Agent\n支持权益查询。")},
	}

	result, err := svc.ExecuteIngestionPipelineTask(context.Background(), ingestionDto.CreateTaskReq{
		PipelineID: "pipe-1",
		Source:     ingestionDto.DocumentSourceReq{Type: "file", FileName: "会员Agent说明.md"},
		Metadata:   map[string]any{"docId": "doc-1"},
	}, []ingestionModel.IngestionPipelineNode{
		{PipelineID: "pipe-1", NodeID: "parser", NodeType: "parser", NextNodeID: "enhancer"},
		{PipelineID: "pipe-1", NodeID: "enhancer", NodeType: "enhancer", ConditionJSON: `false`, NextNodeID: "chunker"},
		{PipelineID: "pipe-1", NodeID: "chunker", NodeType: "chunker", SettingsJSON: `{"chunkSize":128}`},
	})
	if err != nil {
		t.Fatalf("execute pipeline task: %v", err)
	}
	nodeByID := make(map[string]ingestionService.TaskNodeExecutionResult, len(result.Nodes))
	for _, node := range result.Nodes {
		nodeByID[node.NodeID] = node
	}
	if nodeByID["enhancer"].Status != "success" || nodeByID["enhancer"].Message != "Skipped: 条件未满足" {
		t.Fatalf("expected skipped enhancer log to be carried in result, got %+v", nodeByID["enhancer"])
	}
}

type pipelineEmbeddingService struct{}

func (pipelineEmbeddingService) Embed(context.Context, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (pipelineEmbeddingService) EmbedWithModel(context.Context, string, string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (pipelineEmbeddingService) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(texts))
	for i := range texts {
		vectors = append(vectors, []float32{float32(i) + 0.1, 0.2})
	}
	return vectors, nil
}

func (pipelineEmbeddingService) EmbedBatchWithModel(ctx context.Context, texts []string, modelID string) ([][]float32, error) {
	return pipelineEmbeddingService{}.EmbedBatch(ctx, texts)
}

func (pipelineEmbeddingService) Dimension() int {
	return 2
}

type pipelineLLMService struct {
	responses []string
	calls     int
}

func (s *pipelineLLMService) Chat(context.Context, chat.Request) (string, error) {
	return s.next(), nil
}

func (s *pipelineLLMService) ChatWithModel(context.Context, chat.Request, string) (string, error) {
	return s.next(), nil
}

func (s *pipelineLLMService) StreamChat(context.Context, chat.Request, chat.StreamCallback) (chat.StreamHandle, error) {
	panic("unused")
}

func (s *pipelineLLMService) next() string {
	if s.calls >= len(s.responses) {
		return ""
	}
	resp := s.responses[s.calls]
	s.calls++
	return resp
}

func TestDocumentService_RecordsAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentChunkLog{},
		&auditModel.BizChangeLog{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  &capturingVectorStore{},
		fileStore: fakeFileReader{},
	}
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	created, err := svc.CreateDocument(ctx, kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:        "会员Agent说明.md",
		FileURL:        "https://example.com/member-agent.md",
		FileType:       "md",
		SourceType:     "url",
		SourceLocation: "https://example.com/member-agent.md",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	updatedName := "会员Agent能力说明.md"
	updated, err := svc.UpdateDocument(ctx, created.ID, knowledgeDto.UpdateDocumentReq{
		DocName:       ptrString(updatedName),
		ChunkStrategy: "paragraph",
		ChunkConfig:   `{"maxChars":1000}`,
	}, "admin-1")
	if err != nil {
		t.Fatalf("update document: %v", err)
	}

	if err := svc.DeleteDocument(ctx, updated.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeKnowledgeDocument, created.ID).
		Order("create_time ASC").
		Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationCreate || !strings.Contains(logs[0].AfterSnapshot, "会员Agent说明.md") {
		t.Fatalf("unexpected create audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationUpdate ||
		!strings.Contains(logs[1].BeforeSnapshot, "会员Agent说明.md") ||
		!strings.Contains(logs[1].AfterSnapshot, updatedName) {
		t.Fatalf("unexpected update audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationDelete || !strings.Contains(logs[2].BeforeSnapshot, updatedName) {
		t.Fatalf("unexpected delete audit log: %+v", logs[2])
	}
}

func TestDocumentService_CreateDocumentDisablesScheduleForFileSource(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}

	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     &capturingVectorStore{},
		fileStore:    fakeFileReader{},
	}

	created, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:         "本地文件.md",
		FileURL:         "upload://本地文件.md",
		FileType:        "md",
		SourceType:      "file",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if created.ScheduleEnabled != 0 || created.ScheduleCron != "" {
		t.Fatalf("expected schedule disabled for file source, got %+v", created)
	}

	var count int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocumentSchedule{}).Where("doc_id = ?", created.ID).Count(&count).Error; err != nil {
		t.Fatalf("count schedule: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no schedule for file source, got %d", count)
	}
}

func TestDocumentService_CreateDocumentKeepsScheduleForURLSource(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}

	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     &capturingVectorStore{},
		fileStore:    fakeFileReader{},
	}

	created, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:         "会员Agent说明.md",
		FileURL:         "https://example.com/member-agent.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if created.ScheduleEnabled != 1 || created.ScheduleCron != "@every 1h" {
		t.Fatalf("expected schedule kept for url source, got %+v", created)
	}

	var count int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocumentSchedule{}).Where("doc_id = ?", created.ID).Count(&count).Error; err != nil {
		t.Fatalf("count schedule: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one schedule for url source, got %d", count)
	}
}

func TestDocumentService_StartChunkSkipsInternalURLFolderNode(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	vecStore := svc.vecStore.(*capturingVectorStore)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:               kb.ID,
		DocName:            "目录节点.md",
		FileURL:            "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=folder",
		FileType:           "md",
		SourceType:         "internal_url",
		SourceLocation:     "https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=368231",
		CanonicalSourceKey: InternalURLCanonicalSourceKey("https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=folder"),
		SourceRootKey:      InternalURLCanonicalSourceKey("https://geelib.qihoo.net/geelib/knowledge/doc?spaceId=5&docId=368231"),
		SourceNodeType:     "folder",
		Status:             "pending",
		CreatedBy:          "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create folder doc: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeChunk{
		BaseModel:  db.BaseModel{ID: "old-folder-chunk"},
		KbID:       kb.ID,
		DocID:      doc.ID,
		ChunkIndex: 0,
		Content:    "旧正文",
		Enabled:    1,
		CreatedBy:  "admin-1",
	}).Error; err != nil {
		t.Fatalf("create stale folder chunk: %v", err)
	}

	if err := svc.RunChunkNow(context.Background(), doc.ID, "admin-1"); err != nil {
		t.Fatalf("run chunk now for folder: %v", err)
	}
	var stored knowledgeModel.KnowledgeDocument
	if err := gdb.First(&stored, "id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find folder doc: %v", err)
	}
	if stored.Status != "success" || stored.ChunkCount != 0 {
		t.Fatalf("expected folder chunk skipped with success status, got %+v", stored)
	}
	var chunkCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeChunk{}).Where("doc_id = ? AND deleted = 0", doc.ID).Count(&chunkCount).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("expected no active chunks for folder, got %d", chunkCount)
	}
	if len(vecStore.deletedDocCalls) != 1 || vecStore.deletedDocCalls[0] != kb.CollectionName+"|"+doc.ID {
		t.Fatalf("expected folder vectors to be deleted, got %+v", vecStore.deletedDocCalls)
	}
}

func TestDocumentService_CreateDocumentRejectsEnabledScheduleWithoutCron(t *testing.T) {
	_, kb, svc := newDocumentServiceTestContext(t)

	_, err := svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:         "会员Agent说明.md",
		FileURL:         "https://example.com/member-agent.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "定时表达式不能为空") {
		t.Fatalf("expected missing schedule cron rejection, got %v", err)
	}
}

func TestDocumentService_CreateDocumentRejectsTooShortScheduleCron(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}

	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     &capturingVectorStore{},
		fileStore:    fakeFileReader{},
	}

	_, err = svc.CreateDocument(context.Background(), kb.ID, knowledgeDto.CreateDocumentReq{
		DocName:         "会员Agent说明.md",
		FileURL:         "https://example.com/member-agent.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 30s",
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "定时周期不能小于") {
		t.Fatalf("expected too-short cron rejection, got %v", err)
	}
}

func TestDocumentService_UpdateDocumentRejectsTooShortScheduleCron(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            kb.ID,
		DocName:         "会员Agent说明.md",
		FileURL:         "https://example.com/member-agent.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     &capturingVectorStore{},
		fileStore:    fakeFileReader{},
	}

	_, err = svc.UpdateDocument(context.Background(), doc.ID, knowledgeDto.UpdateDocumentReq{
		DocName:         ptrString("会员Agent说明.md"),
		ScheduleEnabled: ptrInt16(1),
		ScheduleCron:    "@every 30s",
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "定时周期不能小于") {
		t.Fatalf("expected too-short cron rejection on update, got %v", err)
	}
}

func TestDocumentService_UpdateDocumentRejectsEnabledScheduleWithoutCron(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:           kb.ID,
		DocName:        "会员Agent说明.md",
		FileURL:        "https://example.com/member-agent.md",
		FileType:       "md",
		SourceType:     "url",
		SourceLocation: "https://example.com/member-agent.md",
		Status:         "success",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}

	_, err := svc.UpdateDocument(context.Background(), doc.ID, knowledgeDto.UpdateDocumentReq{
		DocName:         ptrString("会员Agent说明.md"),
		ScheduleEnabled: ptrInt16(1),
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "启用定时拉取时必须设置定时表达式") {
		t.Fatalf("expected missing update schedule cron rejection, got %v", err)
	}
}

func TestDocumentService_UpdateDocumentRejectsRunningDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "admin-1"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       kb.ID,
		DocName:    "会员Agent说明.md",
		FileURL:    "https://example.com/member-agent.md",
		FileType:   "md",
		SourceType: "url",
		Status:     "running",
		CreatedBy:  "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
	}

	_, err = svc.UpdateDocument(context.Background(), doc.ID, knowledgeDto.UpdateDocumentReq{
		DocName: ptrString("新文档名.md"),
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "正在分块中") {
		t.Fatalf("expected running document rejection on update, got %v", err)
	}
}

func TestDocumentService_UpdateDocumentRejectsEmptyName(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "admin-1"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", FileURL: "https://example.com/member-agent.md", FileType: "md", SourceType: "url", Status: "success", CreatedBy: "admin-1"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	svc := &DocumentService{
		docRepo: knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:      gdb,
	}

	_, err = svc.UpdateDocument(context.Background(), doc.ID, knowledgeDto.UpdateDocumentReq{
		DocName: ptrString("   "),
	}, "admin-1")
	if err == nil || !strings.Contains(err.Error(), "文档名称不能为空") {
		t.Fatalf("expected empty name rejection, got %v", err)
	}
}

func TestDocumentService_UpdateDocumentPersistsScheduleFields(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "admin-1"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:           kb.ID,
		DocName:        "会员Agent说明.md",
		FileURL:        "https://example.com/old.md",
		FileType:       "md",
		SourceType:     "url",
		SourceLocation: "https://example.com/old.md",
		Status:         "success",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     &capturingVectorStore{},
		fileStore:    fakeFileReader{},
	}

	updated, err := svc.UpdateDocument(context.Background(), doc.ID, knowledgeDto.UpdateDocumentReq{
		DocName:         ptrString("会员Agent能力说明.md"),
		SourceLocation:  "https://example.com/new.md",
		ScheduleEnabled: ptrInt16(1),
		ScheduleCron:    "@every 1h",
	}, "admin-1")
	if err != nil {
		t.Fatalf("update document: %v", err)
	}
	if updated.SourceLocation != "https://example.com/new.md" || updated.ScheduleEnabled != 1 || updated.ScheduleCron != "@every 1h" {
		t.Fatalf("unexpected updated response: %+v", updated)
	}
	var stored knowledgeModel.KnowledgeDocument
	if err := gdb.First(&stored, "id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find stored document: %v", err)
	}
	if stored.SourceLocation != "https://example.com/new.md" || stored.ScheduleEnabled != 1 || stored.ScheduleCron != "@every 1h" {
		t.Fatalf("schedule fields were not persisted: %+v", stored)
	}
	var schedule knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&schedule, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule: %v", err)
	}
	if schedule.Enabled != 1 || schedule.CronExpr != "@every 1h" || schedule.NextRunTime == nil {
		t.Fatalf("schedule not synced: %+v", schedule)
	}
}

func TestDocumentService_UpdateDocumentRollsBackWhenScheduleSyncFails(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "admin-1"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            kb.ID,
		DocName:         "会员Agent说明.md",
		FileURL:         "https://example.com/old.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/old.md",
		Status:          "success",
		ScheduleEnabled: 0,
		ScheduleCron:    "",
		CreatedBy:       "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := gdb.Migrator().DropTable(&knowledgeModel.KnowledgeDocumentSchedule{}); err != nil {
		t.Fatalf("drop schedule table: %v", err)
	}
	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     &capturingVectorStore{},
		fileStore:    fakeFileReader{},
	}

	_, err = svc.UpdateDocument(context.Background(), doc.ID, knowledgeDto.UpdateDocumentReq{
		DocName:         ptrString("会员Agent能力说明.md"),
		SourceLocation:  "https://example.com/new.md",
		ScheduleEnabled: ptrInt16(1),
		ScheduleCron:    "@every 1h",
	}, "admin-1")
	if err == nil {
		t.Fatalf("expected schedule sync failure")
	}

	var stored knowledgeModel.KnowledgeDocument
	if err := gdb.First(&stored, "id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find stored document: %v", err)
	}
	if stored.DocName != "会员Agent说明.md" ||
		stored.SourceLocation != "https://example.com/old.md" ||
		stored.ScheduleEnabled != 0 ||
		stored.ScheduleCron != "" {
		t.Fatalf("document update should have been rolled back, got %+v", stored)
	}
}

func TestDocumentService_RecordsChunkAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:      kb.ID,
		DocName:   "会员Agent说明.md",
		FileType:  "md",
		Status:    "success",
		CreatedBy: "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
	}
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	created, err := svc.CreateChunk(ctx, doc.ID, knowledgeDto.CreateChunkReq{
		Content: "第一段内容",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	updatedContent := "更新后的内容"
	updated, err := svc.UpdateChunk(ctx, doc.ID, created.ID, knowledgeDto.UpdateChunkReq{
		Content: updatedContent,
	}, "admin-1")
	if err != nil {
		t.Fatalf("update chunk: %v", err)
	}

	if err := svc.DeleteChunk(ctx, doc.ID, updated.ID); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeKnowledgeChunk, created.ID).
		Order("create_time ASC").
		Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationCreate || !strings.Contains(logs[0].AfterSnapshot, "第一段内容") {
		t.Fatalf("unexpected create audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationUpdate ||
		!strings.Contains(logs[1].BeforeSnapshot, "第一段内容") ||
		!strings.Contains(logs[1].AfterSnapshot, updatedContent) {
		t.Fatalf("unexpected update audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationDelete || !strings.Contains(logs[2].BeforeSnapshot, updatedContent) {
		t.Fatalf("unexpected delete audit log: %+v", logs[2])
	}
}

func TestDocumentService_RecordsToggleAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	kb := &knowledgeModel.KnowledgeBase{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "collection_a",
		CreatedBy:      "admin-1",
	}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:      kb.ID,
		DocName:   "会员Agent说明.md",
		Enabled:   1,
		FileType:  "md",
		Status:    "success",
		CreatedBy: "admin-1",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunkA := &knowledgeModel.KnowledgeChunk{
		KbID:       kb.ID,
		DocID:      doc.ID,
		ChunkIndex: 0,
		Content:    "第一段内容",
		Enabled:    1,
		CreatedBy:  "admin-1",
	}
	chunkB := &knowledgeModel.KnowledgeChunk{
		KbID:       kb.ID,
		DocID:      doc.ID,
		ChunkIndex: 1,
		Content:    "第二段内容",
		Enabled:    0,
		CreatedBy:  "admin-1",
	}
	if err := gdb.Create(chunkA).Error; err != nil {
		t.Fatalf("create chunk a: %v", err)
	}
	if err := gdb.Create(chunkB).Error; err != nil {
		t.Fatalf("create chunk b: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeChunk{}).Where("id = ?", chunkB.ID).Update("enabled", 0).Error; err != nil {
		t.Fatalf("disable chunk b: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
	}
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	if err := svc.ToggleChunk(ctx, doc.ID, chunkA.ID, 0); err != nil {
		t.Fatalf("toggle chunk: %v", err)
	}
	if err := svc.BatchToggleChunks(ctx, doc.ID, []string{chunkB.ID}, 1); err != nil {
		t.Fatalf("batch toggle chunks: %v", err)
	}
	if err := svc.ToggleDocument(ctx, doc.ID, 0); err != nil {
		t.Fatalf("toggle document: %v", err)
	}

	var docLogs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeKnowledgeDocument, doc.ID).
		Find(&docLogs).Error; err != nil {
		t.Fatalf("load document audit logs: %v", err)
	}
	if len(docLogs) != 1 || docLogs[0].OperationType != auditService.OperationDisable ||
		!strings.Contains(docLogs[0].BeforeSnapshot, `"enabled":true`) ||
		!strings.Contains(docLogs[0].AfterSnapshot, `"enabled":false`) {
		t.Fatalf("unexpected document toggle audit logs: %+v", docLogs)
	}

	var chunkLogs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id IN ?", auditService.BizTypeKnowledgeChunk, []string{chunkA.ID, chunkB.ID}).
		Order("create_time ASC").
		Find(&chunkLogs).Error; err != nil {
		t.Fatalf("load chunk audit logs: %v", err)
	}
	if len(chunkLogs) != 2 {
		t.Fatalf("expected 2 chunk audit logs, got %d: %+v", len(chunkLogs), chunkLogs)
	}
	if chunkLogs[0].OperationType != auditService.OperationDisable ||
		!strings.Contains(chunkLogs[0].BeforeSnapshot, `"enabled":1`) ||
		!strings.Contains(chunkLogs[0].AfterSnapshot, `"enabled":0`) {
		t.Fatalf("unexpected single chunk toggle audit log: %+v", chunkLogs[0])
	}
	if chunkLogs[1].OperationType != auditService.OperationEnable ||
		!strings.Contains(chunkLogs[1].BeforeSnapshot, `"enabled":0`) ||
		!strings.Contains(chunkLogs[1].AfterSnapshot, `"enabled":1`) {
		t.Fatalf("unexpected batch chunk toggle audit log: %+v", chunkLogs[1])
	}
}

func TestDocumentService_DeleteDocumentRemovesScheduleAndExecRecords(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentChunkLog{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
		&knowledgeModel.KnowledgeDocumentScheduleExec{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            kb.ID,
		DocName:         "会员Agent说明.md",
		FileType:        "md",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		Status:          "success",
		CreatedBy:       "tester",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        kb.ID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(time.Now().Add(time.Hour)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	exec := &knowledgeModel.KnowledgeDocumentScheduleExec{
		ScheduleID: schedule.ID,
		DocID:      doc.ID,
		KbID:       kb.ID,
		Status:     "success",
	}
	if err := gdb.Create(exec).Error; err != nil {
		t.Fatalf("create schedule exec: %v", err)
	}

	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:           gdb,
	}
	if err := svc.DeleteDocument(context.Background(), doc.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	var scheduleCount, execCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeDocumentSchedule{}).Where("doc_id = ?", doc.ID).Count(&scheduleCount).Error; err != nil {
		t.Fatalf("count schedule: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocumentScheduleExec{}).Where("doc_id = ?", doc.ID).Count(&execCount).Error; err != nil {
		t.Fatalf("count schedule exec: %v", err)
	}
	if scheduleCount != 0 || execCount != 0 {
		t.Fatalf("expected schedule and exec removed, got schedule=%d exec=%d", scheduleCount, execCount)
	}
}

func TestDocumentService_DeleteDocumentRemovesChunksLogsAndVectors(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentChunkLog{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	log := &knowledgeModel.KnowledgeDocumentChunkLog{DocID: doc.ID, Status: "success"}
	if err := gdb.Create(log).Error; err != nil {
		t.Fatalf("create chunk log: %v", err)
	}
	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		vecStore:  vecStore,
	}

	if err := svc.DeleteDocument(context.Background(), doc.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	var activeChunks, logCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeChunk{}).Where("doc_id = ? AND deleted = 0", doc.ID).Count(&activeChunks).Error; err != nil {
		t.Fatalf("count active chunks: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocumentChunkLog{}).Where("doc_id = ?", doc.ID).Count(&logCount).Error; err != nil {
		t.Fatalf("count chunk logs: %v", err)
	}
	if activeChunks != 0 || logCount != 0 {
		t.Fatalf("expected chunks and logs removed, got chunks=%d logs=%d", activeChunks, logCount)
	}
	if len(vecStore.deletedDocCalls) != 1 || vecStore.deletedDocCalls[0] != "collection_a|"+doc.ID {
		t.Fatalf("expected document vectors deleted, got %+v", vecStore.deletedDocCalls)
	}
}

func TestDocumentService_DeleteDocumentDeletesStoredFileQuietly(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeDocumentChunkLog{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	fileStore := &capturingDeletableFileReader{deleteErr: errors.New("delete failed")}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		fileStore: fileStore,
	}

	if err := svc.DeleteDocument(context.Background(), doc.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	if len(fileStore.deletedDocIDs) != 1 || fileStore.deletedDocIDs[0] != doc.ID {
		t.Fatalf("expected stored file delete invoked, got %+v", fileStore.deletedDocIDs)
	}
}

func TestDocumentService_GetChunkLogsReturnsPipelineNameAndOtherDuration(t *testing.T) {
	gdb, _, svc := newDocumentServiceTestContext(t)
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocumentChunkLog{}); err != nil {
		t.Fatalf("migrate chunk log: %v", err)
	}
	svc.SetIngestionPipelineGetter(fakeIngestionPipelineGetter{})
	now := time.Now()
	if err := gdb.Create(&knowledgeModel.KnowledgeDocumentChunkLog{
		DocID:           "doc-1",
		Status:          "success",
		ProcessMode:     "chunk",
		ExtractDuration: 10,
		ChunkDuration:   20,
		EmbedDuration:   30,
		PersistDuration: 15,
		TotalDuration:   100,
		CreateTime:      now,
	}).Error; err != nil {
		t.Fatalf("create chunk log: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeDocumentChunkLog{
		DocID:           "doc-1",
		Status:          "success",
		ProcessMode:     "pipeline",
		PipelineID:      "pipe-1",
		ChunkDuration:   20,
		PersistDuration: 15,
		TotalDuration:   100,
		CreateTime:      now.Add(time.Second),
	}).Error; err != nil {
		t.Fatalf("create pipeline log: %v", err)
	}

	logs, total, err := svc.GetChunkLogs(context.Background(), "doc-1", 1, 10)
	if err != nil {
		t.Fatalf("get chunk logs: %v", err)
	}
	if total != 2 || len(logs) != 2 {
		t.Fatalf("expected 2 logs, total=%d len=%d", total, len(logs))
	}
	if logs[0].PipelineName != "默认流水线" || logs[0].OtherDuration != 65 {
		t.Fatalf("expected pipeline name and pipeline other duration, got %+v", logs[0])
	}
	if logs[1].PipelineName != "" || logs[1].OtherDuration != 25 {
		t.Fatalf("expected chunk other duration, got %+v", logs[1])
	}
}

func TestDocumentService_DeleteDocumentRejectsRunningDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", FileType: "md", Status: "running", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}

	svc := &DocumentService{
		docRepo: knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:      gdb,
	}
	err = svc.DeleteDocument(context.Background(), doc.ID)
	if err == nil || !strings.Contains(err.Error(), "正在分块中") {
		t.Fatalf("expected running document rejection on delete, got %v", err)
	}
}

func TestDocumentService_ToggleDocumentSyncsVectorsAndChunkStates(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 0, FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk1 := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 0, CreatedBy: "tester"}
	chunk2 := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 1, Content: "第二段内容", Enabled: 0, CreatedBy: "tester"}
	if err := gdb.Create(chunk1).Error; err != nil {
		t.Fatalf("create chunk1: %v", err)
	}
	if err := gdb.Create(chunk2).Error; err != nil {
		t.Fatalf("create chunk2: %v", err)
	}
	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  vecStore,
	}
	if err := svc.ToggleDocument(context.Background(), doc.ID, 1); err != nil {
		t.Fatalf("enable document: %v", err)
	}
	if len(vecStore.deletedDocCalls) != 1 || len(vecStore.indexedChunks) != 2 {
		t.Fatalf("expected vector rebuild on enable, got %+v %+v", vecStore.deletedDocCalls, vecStore.indexedChunks)
	}
	var enabledChunks int64
	if err := gdb.Model(&knowledgeModel.KnowledgeChunk{}).Where("doc_id = ? AND enabled = 1 AND deleted = 0", doc.ID).Count(&enabledChunks).Error; err != nil {
		t.Fatalf("count enabled chunks: %v", err)
	}
	if enabledChunks != 2 {
		t.Fatalf("expected chunks enabled after document enable, got %d", enabledChunks)
	}
	if err := svc.ToggleDocument(context.Background(), doc.ID, 0); err != nil {
		t.Fatalf("disable document: %v", err)
	}
	if len(vecStore.deletedDocCalls) != 2 {
		t.Fatalf("expected vectors deleted on disable, got %+v", vecStore.deletedDocCalls)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeChunk{}).Where("doc_id = ? AND enabled = 0 AND deleted = 0", doc.ID).Count(&enabledChunks).Error; err != nil {
		t.Fatalf("count disabled chunks: %v", err)
	}
	if enabledChunks != 2 {
		t.Fatalf("expected chunks disabled after document disable, got %d", enabledChunks)
	}
}

func TestDocumentService_ToggleDocumentRejectsRunningDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 1, FileType: "md", Status: "running", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}

	svc := &DocumentService{
		docRepo: knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		db:      gdb,
	}
	err = svc.ToggleDocument(context.Background(), doc.ID, 0)
	if err == nil || !strings.Contains(err.Error(), "正在分块中") {
		t.Fatalf("expected running document rejection, got %v", err)
	}
}

func TestDocumentService_ToggleDocumentSyncsExistingScheduleWithoutDeletingIt(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(
		&knowledgeModel.KnowledgeBase{},
		&knowledgeModel.KnowledgeDocument{},
		&knowledgeModel.KnowledgeChunk{},
		&knowledgeModel.KnowledgeDocumentSchedule{},
	); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:            kb.ID,
		DocName:         "会员Agent说明.md",
		Enabled:         1,
		FileType:        "md",
		Status:          "success",
		SourceType:      "url",
		SourceLocation:  "https://example.com/member-agent.md",
		ScheduleEnabled: 1,
		ScheduleCron:    "@every 1h",
		CreatedBy:       "tester",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	schedule := &knowledgeModel.KnowledgeDocumentSchedule{
		DocID:       doc.ID,
		KbID:        kb.ID,
		CronExpr:    "@every 1h",
		Enabled:     1,
		NextRunTime: ptrTime(time.Now().Add(time.Hour)),
	}
	if err := gdb.Create(schedule).Error; err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		docRepo:      knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo:    knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:       knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		scheduleRepo: knowledgeRepo.NewKnowledgeDocumentScheduleRepo(gdb),
		db:           gdb,
		emb:          fakeEmbeddingService{},
		vecStore:     vecStore,
	}
	if err := svc.ToggleDocument(context.Background(), doc.ID, 0); err != nil {
		t.Fatalf("disable document: %v", err)
	}
	var updated knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&updated, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find schedule: %v", err)
	}
	if updated.Enabled != 0 || updated.NextRunTime != nil {
		t.Fatalf("expected existing schedule to remain and be disabled, got %+v", updated)
	}
	if updated.CronExpr != "@every 1h" {
		t.Fatalf("expected cron to be retained, got %+v", updated)
	}
	if len(vecStore.deletedDocCalls) != 1 {
		t.Fatalf("expected document vectors deleted on disable, got %+v", vecStore.deletedDocCalls)
	}
	if err := svc.ToggleDocument(context.Background(), doc.ID, 1); err != nil {
		t.Fatalf("enable document: %v", err)
	}
	var reenabled knowledgeModel.KnowledgeDocumentSchedule
	if err := gdb.First(&reenabled, "doc_id = ?", doc.ID).Error; err != nil {
		t.Fatalf("find reenabled schedule: %v", err)
	}
	if reenabled.Enabled != 1 || reenabled.NextRunTime == nil {
		t.Fatalf("expected existing schedule to be reenabled, got %+v", reenabled)
	}
}

func TestDocumentService_ToggleChunkRequiresEnabledDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 0, FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Where("id = ?", doc.ID).Update("enabled", 0).Error; err != nil {
		t.Fatalf("disable doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 0, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  &capturingVectorStore{},
	}
	err = svc.ToggleChunk(context.Background(), doc.ID, chunk.ID, 1)
	if err == nil || !strings.Contains(err.Error(), "文档未启用") {
		t.Fatalf("expected document enabled validation error, got %v", err)
	}
}

func TestDocumentService_BatchToggleChunksRequiresEnabledDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 0, FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Where("id = ?", doc.ID).Update("enabled", 0).Error; err != nil {
		t.Fatalf("disable doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 0, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  &capturingVectorStore{},
	}
	err = svc.BatchToggleChunks(context.Background(), doc.ID, []string{chunk.ID}, 1)
	if err == nil || !strings.Contains(err.Error(), "文档未启用") {
		t.Fatalf("expected document enabled validation error, got %v", err)
	}
}

func TestDocumentService_ToggleChunkRejectsRunningDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 1, FileType: "md", Status: "running", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
	}
	err = svc.ToggleChunk(context.Background(), doc.ID, chunk.ID, 0)
	if err == nil || !strings.Contains(err.Error(), "文档正在分块处理中") {
		t.Fatalf("expected running document validation error, got %v", err)
	}
}

func TestDocumentService_BatchToggleChunksRejectsRunningDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 1, FileType: "md", Status: "running", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
	}
	err = svc.BatchToggleChunks(context.Background(), doc.ID, []string{chunk.ID}, 0)
	if err == nil || !strings.Contains(err.Error(), "文档正在分块处理中") {
		t.Fatalf("expected running document validation error, got %v", err)
	}
}

func TestDocumentService_BatchToggleChunksRejectsDuplicateState(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 1, FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
	}
	err = svc.BatchToggleChunks(context.Background(), doc.ID, []string{chunk.ID}, 1)
	if err == nil || !strings.Contains(err.Error(), "所有 Chunk 已全部启用，无需重复操作") {
		t.Fatalf("expected duplicate state validation error, got %v", err)
	}
}

func TestDocumentService_ToggleChunkSyncsVectors(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 1, FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: doc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 0, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  vecStore,
	}
	if err := svc.ToggleChunk(context.Background(), doc.ID, chunk.ID, 1); err != nil {
		t.Fatalf("enable chunk: %v", err)
	}
	if len(vecStore.updatedChunks) != 1 || vecStore.updatedChunks[0].ChunkID != chunk.ID {
		t.Fatalf("expected vector update on enable, got %+v", vecStore.updatedChunks)
	}
	if err := svc.ToggleChunk(context.Background(), doc.ID, chunk.ID, 0); err != nil {
		t.Fatalf("disable chunk: %v", err)
	}
	if len(vecStore.deletedChunkIDs) != 1 || vecStore.deletedChunkIDs[0] != chunk.ID {
		t.Fatalf("expected vector delete on disable, got %+v", vecStore.deletedChunkIDs)
	}
}

func TestDocumentService_CreateChunkRejectsRunningOrDisabledDocument(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	runningDoc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "running.md", Enabled: 1, FileType: "md", Status: "running", CreatedBy: "tester"}
	if err := gdb.Create(runningDoc).Error; err != nil {
		t.Fatalf("create running doc: %v", err)
	}
	disabledDoc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "disabled.md", Enabled: 0, FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(disabledDoc).Error; err != nil {
		t.Fatalf("create disabled doc: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Where("id = ?", disabledDoc.ID).Update("enabled", 0).Error; err != nil {
		t.Fatalf("disable document: %v", err)
	}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  &capturingVectorStore{},
	}

	if _, err := svc.CreateChunk(context.Background(), runningDoc.ID, knowledgeDto.CreateChunkReq{Content: "新增内容"}, "tester"); err == nil || !strings.Contains(err.Error(), "文档正在分块处理中") {
		t.Fatalf("expected running document create chunk to fail, got %v", err)
	}
	if _, err := svc.CreateChunk(context.Background(), disabledDoc.ID, knowledgeDto.CreateChunkReq{Content: "新增内容"}, "tester"); err == nil || !strings.Contains(err.Error(), "文档未启用") {
		t.Fatalf("expected disabled document create chunk to fail, got %v", err)
	}
}

func TestDocumentService_CreateUpdateDeleteChunkSyncsVectorAndDocumentCount(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	doc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "会员Agent说明.md", Enabled: 1, FileType: "md", Status: "success", ChunkCount: 0, CreatedBy: "tester"}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  vecStore,
	}

	index := 7
	created, err := svc.CreateChunk(context.Background(), doc.ID, knowledgeDto.CreateChunkReq{
		ChunkID: "manual-chunk-1",
		Index:   &index,
		Content: "  第一段 内容  ",
	}, "tester")
	if err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	if created.ID != "manual-chunk-1" || created.ChunkIndex != 7 || created.Content != "  第一段 内容  " {
		t.Fatalf("expected Java create chunk fields preserving content, got %+v", created)
	}
	if len(vecStore.indexedChunks) != 1 || vecStore.indexedChunks[0].ChunkID != created.ID {
		t.Fatalf("expected vector index on create, got %+v", vecStore.indexedChunks)
	}
	var storedDoc knowledgeModel.KnowledgeDocument
	if err := gdb.First(&storedDoc, "id = ?", doc.ID).Error; err != nil {
		t.Fatalf("load doc after create: %v", err)
	}
	if storedDoc.ChunkCount != 1 {
		t.Fatalf("expected chunk count 1 after create, got %d", storedDoc.ChunkCount)
	}

	updated, err := svc.UpdateChunk(context.Background(), doc.ID, created.ID, knowledgeDto.UpdateChunkReq{
		Content: "  更新 后 内容  ",
	}, "tester")
	if err != nil {
		t.Fatalf("update chunk: %v", err)
	}
	if updated.ContentHash == created.ContentHash || updated.Content != "  更新 后 内容  " || updated.CharCount != len([]rune("  更新 后 内容  ")) || updated.TokenCount != 3 {
		t.Fatalf("expected refreshed content metadata, got %+v", updated)
	}
	if len(vecStore.updatedChunks) != 1 || vecStore.updatedChunks[0].Content != "  更新 后 内容  " {
		t.Fatalf("expected vector update on content update, got %+v", vecStore.updatedChunks)
	}

	if err := svc.DeleteChunk(context.Background(), doc.ID, created.ID); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}
	if len(vecStore.deletedChunkIDs) != 1 || vecStore.deletedChunkIDs[0] != created.ID {
		t.Fatalf("expected vector delete on chunk delete, got %+v", vecStore.deletedChunkIDs)
	}
	if err := gdb.First(&storedDoc, "id = ?", doc.ID).Error; err != nil {
		t.Fatalf("load doc after delete: %v", err)
	}
	if storedDoc.ChunkCount != 0 {
		t.Fatalf("expected chunk count 0 after delete, got %d", storedDoc.ChunkCount)
	}
}

func TestDocumentService_ChunkDocumentStoresSourceLocationMetadata(t *testing.T) {
	_, kb, svc := newDocumentServiceTestContext(t)
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:          kb.ID,
		DocName:       "source.txt",
		FileType:      "txt",
		Status:        "success",
		ChunkStrategy: "fixed_size",
		ChunkConfig:   `{"chunkSize":4,"overlapSize":2}`,
		CreatedBy:     "tester",
	}

	chunks := svc.chunkDocument(context.Background(), doc, nil, "abcdefgh")
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if chunks[0].SourceStartOffset != 0 || chunks[0].SourceEndOffset != 4 || chunks[0].CoreStartOffset != 0 || chunks[0].CoreEndOffset != 2 {
		t.Fatalf("unexpected first chunk source location: %+v", chunks[0])
	}
	if chunks[1].SourceStartOffset != 2 || chunks[1].SourceEndOffset != 6 || chunks[1].CoreStartOffset != 2 || chunks[1].CoreEndOffset != 4 {
		t.Fatalf("unexpected overlap chunk source location: %+v", chunks[1])
	}
	if chunks[0].SourceVersion != testSHA256Hex("abcdefgh") || chunks[0].SourceHash != testSHA256Hex("abcd") || chunks[0].ChunkConfigHash == "" {
		t.Fatalf("expected source hashes to be stored, got %+v", chunks[0])
	}
}

func TestDocumentService_UpdateChunkWithOverlapRebuildsDocumentVectors(t *testing.T) {
	gdb, kb, svc := newDocumentServiceTestContext(t)
	sourceText := "abcdefgh"
	svc.fileStore = fakeFileReader{data: []byte(sourceText)}
	vecStore := &capturingVectorStore{}
	svc.vecStore = vecStore

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:          kb.ID,
		DocName:       "source.txt",
		FileURL:       "upload://source.txt",
		FileType:      "txt",
		Enabled:       1,
		Status:        "success",
		ChunkStrategy: "fixed_size",
		ChunkConfig:   `{"chunkSize":4,"overlapSize":2}`,
		ChunkCount:    3,
		CreatedBy:     "tester",
	}
	if err := gdb.Create(doc).Error; err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunks := svc.chunkDocument(context.Background(), doc, nil, sourceText)
	if err := gdb.Create(chunks).Error; err != nil {
		t.Fatalf("create chunks: %v", err)
	}

	updated, err := svc.UpdateChunk(context.Background(), doc.ID, chunks[1].ID, knowledgeDto.UpdateChunkReq{
		Content: "cXef",
	}, "tester")
	if err != nil {
		t.Fatalf("update overlap chunk: %v", err)
	}
	if updated.ID == chunks[1].ID {
		t.Fatalf("expected overlap update to return rebuilt chunk, got original id %s", updated.ID)
	}
	if len(vecStore.updatedChunks) != 0 {
		t.Fatalf("expected no single chunk vector update, got %+v", vecStore.updatedChunks)
	}
	if len(vecStore.deletedDocCalls) != 1 || len(vecStore.indexedChunks) != 3 {
		t.Fatalf("expected full vector rebuild, deleted=%+v indexed=%+v", vecStore.deletedDocCalls, vecStore.indexedChunks)
	}

	var stored []knowledgeModel.KnowledgeChunk
	if err := gdb.Where("doc_id = ? AND deleted = 0", doc.ID).Order("chunk_index ASC").Find(&stored).Error; err != nil {
		t.Fatalf("load rebuilt chunks: %v", err)
	}
	gotContents := make([]string, 0, len(stored))
	for _, chunk := range stored {
		gotContents = append(gotContents, chunk.Content)
		if chunk.SourceVersion != testSHA256Hex("abcXefgh") || chunk.ChunkConfigHash == "" {
			t.Fatalf("expected rebuilt chunk source metadata, got %+v", chunk)
		}
	}
	if strings.Join(gotContents, "|") != "abcX|cXef|efgh" {
		t.Fatalf("unexpected rebuilt chunk contents: %v", gotContents)
	}
}

func TestDocumentService_UpdateDeleteChunkRejectsRunningDocumentAndWrongDoc(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	runningDoc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "running.md", Enabled: 1, FileType: "md", Status: "running", CreatedBy: "tester"}
	otherDoc := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "other.md", Enabled: 1, FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(runningDoc).Error; err != nil {
		t.Fatalf("create running doc: %v", err)
	}
	if err := gdb.Create(otherDoc).Error; err != nil {
		t.Fatalf("create other doc: %v", err)
	}
	chunk := &knowledgeModel.KnowledgeChunk{KbID: kb.ID, DocID: runningDoc.ID, ChunkIndex: 0, Content: "第一段内容", Enabled: 1, CreatedBy: "tester"}
	if err := gdb.Create(chunk).Error; err != nil {
		t.Fatalf("create chunk: %v", err)
	}
	svc := &DocumentService{
		docRepo:   knowledgeRepo.NewKnowledgeDocumentRepo(gdb),
		chunkRepo: knowledgeRepo.NewKnowledgeChunkRepo(gdb),
		kbRepo:    knowledgeRepo.NewKnowledgeBaseRepo(gdb),
		db:        gdb,
		emb:       fakeEmbeddingService{},
		vecStore:  &capturingVectorStore{},
	}

	if _, err := svc.UpdateChunk(context.Background(), runningDoc.ID, chunk.ID, knowledgeDto.UpdateChunkReq{Content: "更新内容"}, "tester"); err == nil || !strings.Contains(err.Error(), "文档正在分块处理中") {
		t.Fatalf("expected running document update chunk to fail, got %v", err)
	}
	if err := svc.DeleteChunk(context.Background(), runningDoc.ID, chunk.ID); err == nil || !strings.Contains(err.Error(), "文档正在分块处理中") {
		t.Fatalf("expected running document delete chunk to fail, got %v", err)
	}
	if _, err := svc.UpdateChunk(context.Background(), otherDoc.ID, chunk.ID, knowledgeDto.UpdateChunkReq{Content: "更新内容"}, "tester"); err == nil || !strings.Contains(err.Error(), "不属于该文档") {
		t.Fatalf("expected wrong doc update chunk to fail, got %v", err)
	}
	if err := svc.DeleteChunk(context.Background(), otherDoc.ID, chunk.ID); err == nil || !strings.Contains(err.Error(), "不属于该文档") {
		t.Fatalf("expected wrong doc delete chunk to fail, got %v", err)
	}
}

func TestDocumentService_SearchDocumentsOrdersByUpdateTime(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}
	kb := &knowledgeModel.KnowledgeBase{Name: "知识库A", EmbeddingModel: "emb-1", CollectionName: "collection_a", CreatedBy: "tester"}
	if err := gdb.Create(kb).Error; err != nil {
		t.Fatalf("create kb: %v", err)
	}
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	doc1 := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "老文档Agent.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	doc2 := &knowledgeModel.KnowledgeDocument{KbID: kb.ID, DocName: "新文档Agent.md", FileType: "md", Status: "success", CreatedBy: "tester"}
	if err := gdb.Create(doc1).Error; err != nil {
		t.Fatalf("create doc1: %v", err)
	}
	if err := gdb.Create(doc2).Error; err != nil {
		t.Fatalf("create doc2: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Where("id = ?", doc1.ID).Updates(map[string]any{"update_time": newer, "create_time": older}).Error; err != nil {
		t.Fatalf("update doc1 time: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Where("id = ?", doc2.ID).Updates(map[string]any{"update_time": older, "create_time": newer}).Error; err != nil {
		t.Fatalf("update doc2 time: %v", err)
	}
	svc := NewDocumentService(knowledgeRepo.NewKnowledgeDocumentRepo(gdb), knowledgeRepo.NewKnowledgeChunkRepo(gdb), knowledgeRepo.NewKnowledgeBaseRepo(gdb), gdb, fakeEmbeddingService{}, &capturingVectorStore{}, nil)
	records, err := svc.SearchDocuments(context.Background(), "Agent", 8)
	if err != nil {
		t.Fatalf("search documents: %v", err)
	}
	if len(records) != 2 || records[0].DocName != "老文档Agent.md" || records[1].DocName != "新文档Agent.md" {
		t.Fatalf("expected update_time desc order, got %+v", records)
	}
}

func TestDocumentService_PersistChunksAndVectorsUsesPersistedChunkIDs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		db: gdb,
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			CollectionName: "collection_a",
		}},
		vecStore: vecStore,
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:      "kb-1",
		CreatedBy: "user-1",
	}
	doc.ID = "doc-1"

	_, err = svc.persistChunksAndVectors(context.Background(), doc, []rag.VectorChunk{
		{Content: "content 1", Embedding: []float32{0.1, 0.2}, Index: 0},
		{Content: "content 2", Embedding: []float32{0.3, 0.4}, Index: 1},
	})
	if err != nil {
		t.Fatalf("persist chunks and vectors: %v", err)
	}

	if len(vecStore.indexedChunks) != 2 {
		t.Fatalf("expected 2 vector chunks, got %d", len(vecStore.indexedChunks))
	}
	seen := make(map[string]bool)
	for _, c := range vecStore.indexedChunks {
		if c.ChunkID == "" {
			t.Fatal("expected vector chunk ID to be populated from persisted chunk ID")
		}
		if seen[c.ChunkID] {
			t.Fatalf("expected unique vector chunk IDs, got duplicate %q", c.ChunkID)
		}
		seen[c.ChunkID] = true
	}
}

func TestDocumentService_PersistChunksAndVectorsCompletesDocumentMetadata(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeDocument{}, &knowledgeModel.KnowledgeChunk{}); err != nil {
		t.Fatalf("migrate knowledge tables: %v", err)
	}

	vecStore := &capturingVectorStore{}
	svc := &DocumentService{
		db: gdb,
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			CollectionName: "collection_a",
		}},
		vecStore: vecStore,
	}

	doc := &knowledgeModel.KnowledgeDocument{
		KbID:           "kb-1",
		DocName:        "会员智能问答Agent当前支持能力.md",
		SourceType:     "url",
		SourceLocation: "https://example.com/member-agent.md",
		CreatedBy:      "user-1",
	}
	doc.ID = "doc-1"

	_, err = svc.persistChunksAndVectors(context.Background(), doc, []rag.VectorChunk{
		{Content: "content", Embedding: []float32{0.1, 0.2}, Index: 0},
	})
	if err != nil {
		t.Fatalf("persist chunks and vectors: %v", err)
	}

	if len(vecStore.indexedChunks) != 1 {
		t.Fatalf("expected 1 vector chunk, got %d", len(vecStore.indexedChunks))
	}
	meta := vecStore.indexedChunks[0].Metadata
	if meta["doc_id"] != "doc-1" {
		t.Fatalf("expected doc_id metadata, got %+v", meta)
	}
	if meta["doc_name"] != "会员智能问答Agent当前支持能力.md" {
		t.Fatalf("expected doc_name metadata, got %+v", meta)
	}
	if meta["source_type"] != "url" {
		t.Fatalf("expected source_type metadata, got %+v", meta)
	}
	if meta["source_url"] != "https://example.com/member-agent.md" {
		t.Fatalf("expected source_url metadata, got %+v", meta)
	}
}

func TestDocumentService_RunChunkProcessBuildsSourceMetadata(t *testing.T) {
	svc := &DocumentService{
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			EmbeddingModel: "emb-1",
		}},
		emb:       fakeEmbeddingService{},
		fileStore: fakeFileReader{data: []byte("第一行\n第二行\n第三行")},
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent说明.md",
		FileURL:    "https://example.com/member-agent.md",
		SourceType: "url",
	}
	doc.ID = "doc-1"

	_, _, _, chunks, err := svc.runChunkProcess(context.Background(), doc)
	if err != nil {
		t.Fatalf("run chunk process: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	meta := chunks[0].Metadata
	if meta["doc_id"] != "doc-1" {
		t.Fatalf("expected doc_id metadata, got %q", meta["doc_id"])
	}
	if meta["doc_name"] != "会员Agent说明.md" {
		t.Fatalf("expected doc_name metadata, got %q", meta["doc_name"])
	}
	if meta["source_url"] != "https://example.com/member-agent.md" {
		t.Fatalf("expected source_url metadata, got %q", meta["source_url"])
	}
	if meta["page_start"] != "1" || meta["line_start"] != "1" || meta["line_end"] == "" {
		t.Fatalf("expected page/line metadata, got %+v", meta)
	}
}

func TestDocumentService_RunChunkProcessUsesDocumentParser(t *testing.T) {
	svc := &DocumentService{
		kbRepo: fakeKnowledgeBaseFinder{kb: &knowledgeModel.KnowledgeBase{
			EmbeddingModel: "emb-1",
		}},
		emb:       fakeEmbeddingService{},
		fileStore: fakeFileReader{data: []byte("能力,说明\n权益查询,支持\n积分查询,支持")},
	}
	doc := &knowledgeModel.KnowledgeDocument{
		KbID:       "kb-1",
		DocName:    "会员Agent能力.csv",
		FileType:   "csv",
		SourceType: "file",
	}
	doc.ID = "doc-1"

	_, _, _, chunks, err := svc.runChunkProcess(context.Background(), doc)
	if err != nil {
		t.Fatalf("run chunk process: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Content, "能力 | 说明") || !strings.Contains(chunks[0].Content, "权益查询 | 支持") {
		t.Fatalf("expected parsed table content, got %q", chunks[0].Content)
	}
}

func ptrInt16(v int16) *int16 {
	return &v
}

func ptrString(v string) *string {
	return &v
}
