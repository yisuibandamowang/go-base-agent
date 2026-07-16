package service

import (
	"context"
	"strings"
	"testing"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	knowledgeDto "go-base-agent/internal/biz/knowledge/dto"
	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	knowledgeRepo "go-base-agent/internal/biz/knowledge/repo"
	appctx "go-base-agent/internal/framework/context"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestKnowledgeBaseService_RecordsAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&knowledgeModel.KnowledgeBase{}, &auditModel.BizChangeLog{}); err != nil {
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
