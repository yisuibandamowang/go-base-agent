package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	knowledgeDto "go-base-agent/internal/biz/knowledge/dto"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/biz/rag"
	appctx "go-base-agent/internal/framework/context"
	"go-base-agent/internal/framework/mq"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type capturingVectorSpaceCleaner struct {
	ensureCalls []string
	dropCalls   []string
}

func (c *capturingVectorSpaceCleaner) EnsureVectorSpace(_ context.Context, spec rag.VectorSpaceSpec) error {
	c.ensureCalls = append(c.ensureCalls, spec.SpaceID.Name)
	return nil
}

func (c *capturingVectorSpaceCleaner) DropVectorSpace(_ context.Context, collectionName string) error {
	c.dropCalls = append(c.dropCalls, collectionName)
	return nil
}

func TestKnowledgeBaseService_RecordsAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	created, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "go 语言知识库",
		EmbeddingModel: "qwen-emb-8b",
		CollectionName: "go_knowledge",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}

	updated, err := svc.Update(ctx, created.ID, knowledgeDto.UpdateKnowledgeBaseReq{
		Name:           "go 语言知识库 v2",
		EmbeddingModel: "qwen-emb-8b",
		CollectionName: "go_knowledge_v2",
	}, "admin-1")
	if err != nil {
		t.Fatalf("update knowledge base: %v", err)
	}

	if err := svc.Delete(ctx, updated.ID); err != nil {
		t.Fatalf("delete knowledge base: %v", err)
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeKnowledgeBase, created.ID).
		Order("create_time ASC").
		Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationCreate || !strings.Contains(logs[0].AfterSnapshot, "go 语言知识库") {
		t.Fatalf("unexpected create audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationUpdate ||
		!strings.Contains(logs[1].BeforeSnapshot, "go 语言知识库") ||
		!strings.Contains(logs[1].AfterSnapshot, "go 语言知识库 v2") {
		t.Fatalf("unexpected update audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationDelete || !strings.Contains(logs[2].BeforeSnapshot, "go 语言知识库 v2") {
		t.Fatalf("unexpected delete audit log: %+v", logs[2])
	}
}

func TestKnowledgeBaseService_DeleteBlocksWhenDocumentsExist(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	kb, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "有文档的知识库",
		EmbeddingModel: "qwen-emb-8b",
		CollectionName: "kb_with_docs",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeDocument{
		KbID:      kb.ID,
		DocName:   "doc-1",
		FileURL:   "https://example.com/doc-1",
		FileType:  "pdf",
		Status:    "success",
		CreatedBy: "admin-1",
		UpdatedBy: "admin-1",
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}

	if err := svc.Delete(ctx, kb.ID); err == nil {
		t.Fatal("expected delete to be blocked while documents still exist")
	}

	var activeCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeBase{}).
		Where("id = ? AND deleted = 0", kb.ID).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count active kb: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected knowledge base to remain active, got %d", activeCount)
	}
}

func TestKnowledgeBaseService_DeleteDropsVectorSpace(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	vecStore := &capturingVectorSpaceCleaner{}
	svc.SetVectorStore(vecStore)

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	kb, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "清理向量的知识库",
		EmbeddingModel: "qwen-emb-8b",
		CollectionName: "kb_cleanup",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	if err := svc.Delete(ctx, kb.ID); err != nil {
		t.Fatalf("delete knowledge base: %v", err)
	}
	if len(vecStore.dropCalls) != 1 || vecStore.dropCalls[0] != "kb_cleanup" {
		t.Fatalf("expected vector space drop for kb_cleanup, got %+v", vecStore.dropCalls)
	}
}

func TestKnowledgeBaseService_CreateEnsuresVectorSpace(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	vecStore := &capturingVectorSpaceCleaner{}
	svc.SetVectorStore(vecStore)

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	kb, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "创建时确保向量空间",
		EmbeddingModel: "qwen-emb-8b",
		CollectionName: "kb_ensure",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	if kb == nil {
		t.Fatal("expected knowledge base response")
	}
	if len(vecStore.ensureCalls) != 1 || vecStore.ensureCalls[0] != "kb_ensure" {
		t.Fatalf("expected vector space ensure for kb_ensure, got %+v", vecStore.ensureCalls)
	}
}

func TestKnowledgeBaseService_ListFiltersByNameOrdersByUpdateTimeAndCountsDocuments(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now()
	kbs := []knowledgeModel.KnowledgeBase{
		{Name: "会员知识库", EmbeddingModel: "emb-1", CollectionName: "member_kb", CreatedBy: "admin"},
		{Name: "会员规则库", EmbeddingModel: "emb-1", CollectionName: "rule_kb", CreatedBy: "admin"},
		{Name: "工单知识库", EmbeddingModel: "emb-1", CollectionName: "ticket_kb", CreatedBy: "admin"},
	}
	for i := range kbs {
		if err := gdb.Create(&kbs[i]).Error; err != nil {
			t.Fatalf("seed kb %d: %v", i, err)
		}
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeBase{}).Where("id = ?", kbs[0].ID).
		Updates(map[string]any{"update_time": now.Add(-2 * time.Hour)}).Error; err != nil {
		t.Fatalf("update time member: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeBase{}).Where("id = ?", kbs[1].ID).
		Updates(map[string]any{"update_time": now}).Error; err != nil {
		t.Fatalf("update time rule: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeDocument{
		KbID: kbs[1].ID, DocName: "doc-1", FileURL: "file://doc-1", FileType: "md", Status: "success", CreatedBy: "admin",
	}).Error; err != nil {
		t.Fatalf("seed active document: %v", err)
	}
	deletedDoc := knowledgeModel.KnowledgeDocument{
		KbID: kbs[1].ID, DocName: "doc-2", FileURL: "file://doc-2", FileType: "md", Status: "success", CreatedBy: "admin",
	}
	if err := gdb.Create(&deletedDoc).Error; err != nil {
		t.Fatalf("seed deleted document: %v", err)
	}
	if err := gdb.Model(&knowledgeModel.KnowledgeDocument{}).Where("id = ?", deletedDoc.ID).
		Update("deleted", 1).Error; err != nil {
		t.Fatalf("mark deleted document: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	items, total, err := svc.List(context.Background(), 1, 10, "会员")
	if err != nil {
		t.Fatalf("list knowledge bases: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 matched knowledge bases, got %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 records, got %d", len(items))
	}
	if items[0].ID != kbs[1].ID || items[1].ID != kbs[0].ID {
		t.Fatalf("expected update_time desc order, got %+v", items)
	}
	if items[0].DocumentCount != 1 {
		t.Fatalf("expected active document count 1, got %+v", items[0])
	}
}

func TestKnowledgeBaseService_UpdateRenamesWithoutClearingExistingFields(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	kb := knowledgeModel.KnowledgeBase{Name: "原知识库", EmbeddingModel: "emb-1", CollectionName: "kb_origin", CreatedBy: "admin"}
	if err := gdb.Create(&kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	updated, err := svc.Update(context.Background(), kb.ID, knowledgeDto.UpdateKnowledgeBaseReq{Name: "新知识库"}, "admin")
	if err != nil {
		t.Fatalf("rename knowledge base: %v", err)
	}
	if updated.Name != "新知识库" || updated.EmbeddingModel != "emb-1" || updated.CollectionName != "kb_origin" {
		t.Fatalf("expected rename to preserve existing fields, got %+v", updated)
	}

	if _, err := svc.Update(context.Background(), kb.ID, knowledgeDto.UpdateKnowledgeBaseReq{Name: "   "}, "admin"); err == nil || !strings.Contains(err.Error(), "知识库名称不能为空") {
		t.Fatalf("expected blank name validation, got %v", err)
	}
}

func TestKnowledgeBaseService_UpdateIgnoresCollectionName(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	kb := knowledgeModel.KnowledgeBase{Name: "原知识库", EmbeddingModel: "emb-1", CollectionName: "kb_origin", CreatedBy: "admin"}
	if err := gdb.Create(&kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	updated, err := svc.Update(context.Background(), kb.ID, knowledgeDto.UpdateKnowledgeBaseReq{
		Name:           "新知识库",
		CollectionName: "kb_new",
	}, "admin")
	if err != nil {
		t.Fatalf("update knowledge base: %v", err)
	}
	if updated.CollectionName != "kb_origin" {
		t.Fatalf("expected update to preserve collection name, got %+v", updated)
	}
}

func TestKnowledgeBaseService_CreateRejectsWhitespaceEquivalentName(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	if _, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "知 识 库 A",
		EmbeddingModel: "emb-1",
		CollectionName: "kb-a",
	}, "admin-1"); err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}

	if _, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "知识库A",
		EmbeddingModel: "emb-1",
		CollectionName: "kb-b",
	}, "admin-1"); err == nil || !strings.Contains(err.Error(), "知识库名称已存在") {
		t.Fatalf("expected whitespace-equivalent name to be rejected, got %v", err)
	}
}

func TestKnowledgeBaseService_UpdateBlocksEmbeddingModelChangeAfterChunkedDocuments(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	kb := knowledgeModel.KnowledgeBase{Name: "会员知识库", EmbeddingModel: "emb-1", CollectionName: "member_kb", CreatedBy: "admin"}
	if err := gdb.Create(&kb).Error; err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	if err := gdb.Create(&knowledgeModel.KnowledgeDocument{
		KbID: kb.ID, DocName: "doc-1", FileURL: "file://doc-1", FileType: "md", Status: "success", ChunkCount: 1, CreatedBy: "admin",
	}).Error; err != nil {
		t.Fatalf("seed chunked document: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	_, err = svc.Update(context.Background(), kb.ID, knowledgeDto.UpdateKnowledgeBaseReq{
		Name:           "会员知识库",
		EmbeddingModel: "emb-2",
	}, "admin")
	if err == nil || !strings.Contains(err.Error(), "不允许修改嵌入模型") {
		t.Fatalf("expected embedding model change to be blocked, got %v", err)
	}
}

func TestKnowledgeBaseService_DeletePublishesCleanupEventWhenMQEnabled(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	producer := &capturingKnowledgeMQProducer{}
	vecStore := &capturingVectorSpaceCleaner{}
	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	svc.SetVectorStore(vecStore)
	svc.SetMQProducer(producer, true)
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	kb, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "MQ 清理知识库",
		EmbeddingModel: "qwen-emb-8b",
		CollectionName: "kb_mq_cleanup",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	if err := svc.Delete(ctx, kb.ID); err != nil {
		t.Fatalf("delete knowledge base: %v", err)
	}
	if len(vecStore.dropCalls) != 0 {
		t.Fatalf("expected MQ path not to drop vector space inline, got %+v", vecStore.dropCalls)
	}
	if len(producer.messages) != 1 {
		t.Fatalf("expected one cleanup event, got %+v", producer.messages)
	}
	msg := producer.messages[0]
	if msg.Topic != KnowledgeBaseCleanupTopic || msg.Keys != kb.ID || msg.BizDesc != "知识库删除清理" {
		t.Fatalf("unexpected mq message: %+v", msg)
	}
	var event KnowledgeBaseCleanupEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		t.Fatalf("decode cleanup event: %v", err)
	}
	if event.KBID != kb.ID || event.CollectionName != "kb_mq_cleanup" || event.Operator != "管理员" {
		t.Fatalf("unexpected cleanup event: %+v", event)
	}
}

func TestKnowledgeBaseService_DeleteRollsBackWhenMQSendFails(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &knowledgeModel.KnowledgeDocument{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewKnowledgeBaseService(knowledgeRepo.NewKnowledgeBaseRepo(gdb))
	svc.SetMQProducer(failingKnowledgeMQProducer{}, true)
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	kb, err := svc.Create(ctx, knowledgeDto.CreateKnowledgeBaseReq{
		Name:           "回滚删除的知识库",
		EmbeddingModel: "qwen-emb-8b",
		CollectionName: "kb_mq_fail",
	}, "admin-1")
	if err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}
	if err := svc.Delete(ctx, kb.ID); err == nil {
		t.Fatal("expected delete to fail when mq send fails")
	}

	var activeCount int64
	if err := gdb.Model(&knowledgeModel.KnowledgeBase{}).
		Where("id = ? AND deleted = 0", kb.ID).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count active kb: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected knowledge base delete to roll back, got %d", activeCount)
	}
}

type capturingKnowledgeMQProducer struct {
	messages []mq.Message
}

func (p *capturingKnowledgeMQProducer) Send(ctx context.Context, msg mq.Message) (*mq.SendResult, error) {
	p.messages = append(p.messages, msg)
	return &mq.SendResult{MsgID: "msg-1", Status: "SEND_OK"}, nil
}

func (p *capturingKnowledgeMQProducer) SendInTransaction(ctx context.Context, msg mq.Message, executor mq.TransactionExecutor) (*mq.SendResult, error) {
	p.messages = append(p.messages, msg)
	if executor != nil {
		if err := executor(ctx, msg); err != nil {
			return nil, err
		}
	}
	return &mq.SendResult{MsgID: "msg-1", Status: "SEND_OK"}, nil
}

func (p *capturingKnowledgeMQProducer) RegisterTransactionChecker(topic string, checker mq.TransactionChecker) {
}

type failingKnowledgeMQProducer struct{}

func (failingKnowledgeMQProducer) Send(context.Context, mq.Message) (*mq.SendResult, error) {
	return nil, fmt.Errorf("send failed")
}

func (failingKnowledgeMQProducer) SendInTransaction(context.Context, mq.Message, mq.TransactionExecutor) (*mq.SendResult, error) {
	return nil, fmt.Errorf("send failed")
}

func (failingKnowledgeMQProducer) RegisterTransactionChecker(string, mq.TransactionChecker) {}
