package service

import (
	"context"
	"strings"
	"testing"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/ingestion/dto"
	"go-base-agent/internal/biz/ingestion/model"
	"go-base-agent/internal/biz/ingestion/repo"
	appctx "go-base-agent/internal/framework/context"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPipelineService_RecordsAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IngestionPipeline{}, &model.IngestionPipelineNode{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}

	svc := NewPipelineService(repo.NewPipelineRepo(gdb), gdb)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	created, err := svc.Create(ctx, dto.CreatePipelineReq{
		Name:        "默认摄取流水线",
		Description: "用于文档入库",
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "fetch", NodeType: "fetcher", NextNodeID: "index"},
			{NodeID: "index", NodeType: "indexer"},
		},
	}, "admin-1")
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	updatedDesc := "用于会员文档入库"
	updated, err := svc.Update(ctx, created.ID, dto.UpdatePipelineReq{
		Name:        "会员摄取流水线",
		Description: &updatedDesc,
		Nodes: []dto.PipelineNodeReq{
			{NodeID: "fetch", NodeType: "fetcher", NextNodeID: "chunk"},
			{NodeID: "chunk", NodeType: "chunker", NextNodeID: "index"},
			{NodeID: "index", NodeType: "indexer"},
		},
	}, "admin-1")
	if err != nil {
		t.Fatalf("update pipeline: %v", err)
	}

	if err := svc.Delete(ctx, updated.ID); err != nil {
		t.Fatalf("delete pipeline: %v", err)
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeIngestionPipeline, created.ID).
		Order("create_time ASC").
		Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationCreate || !strings.Contains(logs[0].AfterSnapshot, "默认摄取流水线") {
		t.Fatalf("unexpected create audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationUpdate ||
		!strings.Contains(logs[1].BeforeSnapshot, "默认摄取流水线") ||
		!strings.Contains(logs[1].AfterSnapshot, "会员摄取流水线") ||
		!strings.Contains(logs[1].AfterSnapshot, "chunk") {
		t.Fatalf("unexpected update audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationDelete || !strings.Contains(logs[2].BeforeSnapshot, "会员摄取流水线") {
		t.Fatalf("unexpected delete audit log: %+v", logs[2])
	}
}
