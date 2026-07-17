package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	ingestionDto "go-base-agent/internal/biz/ingestion/dto"
	knowledgeDto "go-base-agent/internal/biz/knowledge/dto"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/rag"
	appctx "go-base-agent/internal/framework/context"

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

type fakeKnowledgeBaseFinder struct {
	kb *knowledgeModel.KnowledgeBase
}

func (f fakeKnowledgeBaseFinder) FindByID(context.Context, string) (*knowledgeModel.KnowledgeBase, error) {
	return f.kb, nil
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

type capturingVectorStore struct {
	chunks []rag.VectorChunk
}

func (s *capturingVectorStore) DeleteDocumentVectors(context.Context, string, string) error {
	return nil
}

func (s *capturingVectorStore) IndexDocumentChunks(_ context.Context, _ string, _ string, chunks []rag.VectorChunk) error {
	s.chunks = append([]rag.VectorChunk(nil), chunks...)
	return nil
}

type fakeIngestionTaskStarter struct {
	req    ingestionDto.CreateTaskReq
	userID string
	resp   *ingestionDto.IngestionResultResp
	err    error
}

func (f *fakeIngestionTaskStarter) Create(ctx context.Context, req ingestionDto.CreateTaskReq, userID string) (*ingestionDto.IngestionResultResp, error) {
	f.req = req
	f.userID = userID
	return f.resp, f.err
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

func TestDocumentService_CreateDocumentStoresPipelineMode(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
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
		ChunkStrategy: "pipeline",
		ChunkConfig:   `{"pipelineId":"pipe-1"}`,
	}, "user-1")
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	if resp.ProcessMode != "pipeline" || resp.PipelineID != "pipe-1" {
		t.Fatalf("expected pipeline mode and id, got %+v", resp)
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
	if len(vecStore.chunks) != 1 || vecStore.chunks[0].Metadata["doc_name"] != "会员Agent说明.md" {
		t.Fatalf("unexpected vector chunks: %+v", vecStore.chunks)
	}
	var updated knowledgeModel.KnowledgeDocument
	if err := gdb.First(&updated, "id = ?", "doc-1").Error; err != nil {
		t.Fatalf("find updated doc: %v", err)
	}
	if updated.Status != "success" || updated.ChunkCount != 1 {
		t.Fatalf("expected completed doc, got status=%s chunks=%d", updated.Status, updated.ChunkCount)
	}
}

func TestDocumentService_RecordsAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &auditModel.BizChangeLog{}); err != nil {
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
		DocName:       updatedName,
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
	updated, err := svc.UpdateChunk(ctx, created.ID, knowledgeDto.UpdateChunkReq{
		Content: updatedContent,
	}, "admin-1")
	if err != nil {
		t.Fatalf("update chunk: %v", err)
	}

	if err := svc.DeleteChunk(ctx, updated.ID); err != nil {
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

	if err := svc.ToggleDocument(ctx, doc.ID, 0); err != nil {
		t.Fatalf("toggle document: %v", err)
	}
	if err := svc.ToggleChunk(ctx, chunkA.ID, 0); err != nil {
		t.Fatalf("toggle chunk: %v", err)
	}
	if err := svc.BatchToggleChunks(ctx, doc.ID, []string{chunkB.ID}, 1); err != nil {
		t.Fatalf("batch toggle chunks: %v", err)
	}

	var docLogs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeKnowledgeDocument, doc.ID).
		Find(&docLogs).Error; err != nil {
		t.Fatalf("load document audit logs: %v", err)
	}
	if len(docLogs) != 1 || docLogs[0].OperationType != auditService.OperationDisable ||
		!strings.Contains(docLogs[0].BeforeSnapshot, `"enabled":1`) ||
		!strings.Contains(docLogs[0].AfterSnapshot, `"enabled":0`) {
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

	if len(vecStore.chunks) != 2 {
		t.Fatalf("expected 2 vector chunks, got %d", len(vecStore.chunks))
	}
	seen := make(map[string]bool)
	for _, c := range vecStore.chunks {
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

	if len(vecStore.chunks) != 1 {
		t.Fatalf("expected 1 vector chunk, got %d", len(vecStore.chunks))
	}
	meta := vecStore.chunks[0].Metadata
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
