package service

import (
	"context"
	"strings"
	"testing"

	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/biz/intent_tree/dto"
	"go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/biz/intent_tree/repo"
	appctx "go-base-agent/internal/framework/context"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIntentService_RecordsNodeAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IntentNode{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}

	svc := NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	created, err := svc.CreateNode(ctx, dto.CreateIntentReq{
		KbID:       "kb-1",
		IntentCode: "member.query",
		Name:       "会员查询",
		Level:      1,
		Enabled:    1,
	}, "admin-1")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	updatedName := "会员信息查询"
	updatedEnabled := int16(0)
	updated, err := svc.UpdateNode(ctx, created.ID, dto.UpdateIntentReq{
		Name:    &updatedName,
		Enabled: &updatedEnabled,
	}, "admin-1")
	if err != nil {
		t.Fatalf("update node: %v", err)
	}

	if err := svc.ToggleNode(ctx, updated.ID, 1); err != nil {
		t.Fatalf("toggle node: %v", err)
	}
	if err := svc.DeleteNode(ctx, updated.ID); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeIntentTree, created.ID).
		Order("create_time ASC").
		Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 4 {
		t.Fatalf("expected 4 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationCreate || !strings.Contains(logs[0].AfterSnapshot, "会员查询") {
		t.Fatalf("unexpected create audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationUpdate ||
		!strings.Contains(logs[1].BeforeSnapshot, "会员查询") ||
		!strings.Contains(logs[1].AfterSnapshot, updatedName) {
		t.Fatalf("unexpected update audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationEnable ||
		!strings.Contains(logs[2].BeforeSnapshot, `"enabled":0`) ||
		!strings.Contains(logs[2].AfterSnapshot, `"enabled":1`) {
		t.Fatalf("unexpected toggle audit log: %+v", logs[2])
	}
	if logs[3].OperationType != auditService.OperationDelete || !strings.Contains(logs[3].BeforeSnapshot, updatedName) {
		t.Fatalf("unexpected delete audit log: %+v", logs[3])
	}
}

func TestIntentService_RecordsBatchNodeAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&model.IntentNode{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}

	svc := NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})

	first, err := svc.CreateNode(ctx, dto.CreateIntentReq{
		IntentCode: "member.query",
		Name:       "会员查询",
		Level:      1,
		Enabled:    0,
	}, "admin-1")
	if err != nil {
		t.Fatalf("create first node: %v", err)
	}
	second, err := svc.CreateNode(ctx, dto.CreateIntentReq{
		IntentCode: "member.benefit",
		Name:       "权益查询",
		Level:      1,
		Enabled:    0,
	}, "admin-1")
	if err != nil {
		t.Fatalf("create second node: %v", err)
	}
	if err := gdb.Model(&model.IntentNode{}).Where("id IN ?", []string{first.ID, second.ID}).Updates(map[string]any{"enabled": 0}).Error; err != nil {
		t.Fatalf("reset enabled state: %v", err)
	}

	if err := svc.BatchToggleNodes(ctx, []string{first.ID, second.ID}, 1); err != nil {
		t.Fatalf("batch enable nodes: %v", err)
	}
	if err := svc.BatchDeleteNodes(ctx, []string{first.ID}); err != nil {
		t.Fatalf("batch delete nodes: %v", err)
	}
	if err := svc.BatchToggleNodes(ctx, []string{"missing-id"}, 0); err == nil {
		t.Fatal("expected missing id error")
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND operation_type IN ?", auditService.BizTypeIntentTree, []string{
		auditService.OperationEnable,
		auditService.OperationDelete,
	}).Order("create_time ASC").Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 batch audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationEnable ||
		!strings.Contains(logs[0].BeforeSnapshot, `"enabled":0`) ||
		!strings.Contains(logs[0].AfterSnapshot, `"enabled":1`) {
		t.Fatalf("unexpected first enable audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationEnable ||
		!strings.Contains(logs[1].BeforeSnapshot, `"enabled":0`) ||
		!strings.Contains(logs[1].AfterSnapshot, `"enabled":1`) {
		t.Fatalf("unexpected second enable audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationDelete || !strings.Contains(logs[2].BeforeSnapshot, "会员查询") {
		t.Fatalf("unexpected delete audit log: %+v", logs[2])
	}
}
