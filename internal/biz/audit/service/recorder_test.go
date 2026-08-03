package service

import (
	"context"
	"strings"
	"testing"

	auditModel "go-base-agent/internal/biz/audit/model"
	"go-base-agent/internal/biz/audit/repo"
	appctx "go-base-agent/internal/framework/context"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBizChangeLogService_Record(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewBizChangeLogService(repo.NewBizChangeLogRepo(gdb))
	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "op-1",
		Username: "管理员",
		Role:     "admin",
	})

	if err := svc.Record(ctx, RecordReq{
		BizType:       BizTypeUser,
		BizID:         "user-1",
		OperationType: OperationCreate,
		ActionDesc:    "创建用户：alice",
		AfterSnapshot: map[string]any{"id": "user-1", "username": "alice"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	var item auditModel.BizChangeLog
	if err := gdb.First(&item).Error; err != nil {
		t.Fatalf("load record: %v", err)
	}
	if item.BizType != BizTypeUser || item.BizId != "user-1" || item.OperationType != OperationCreate {
		t.Fatalf("unexpected audit record: %+v", item)
	}
	if item.OperatorID != "op-1" || item.OperatorName != "管理员" || item.OperatorRole != "admin" {
		t.Fatalf("unexpected operator: %+v", item)
	}
	if !item.Success {
		t.Fatal("expected success audit record")
	}
	if !strings.Contains(item.AfterSnapshot, `"username":"alice"`) {
		t.Fatalf("expected after snapshot to include username, got %q", item.AfterSnapshot)
	}
}

func TestBizChangeLogService_RecordAutoMigratesMissingTable(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	svc := NewBizChangeLogService(repo.NewBizChangeLogRepo(gdb))
	if err := svc.Record(context.Background(), RecordReq{
		BizType:       BizTypeKnowledgeDocument,
		BizID:         "doc-1",
		OperationType: OperationDelete,
		ActionDesc:    "删除文档：guide.md",
	}); err != nil {
		t.Fatalf("record without pre-migration: %v", err)
	}

	if !gdb.Migrator().HasTable(&auditModel.BizChangeLog{}) {
		t.Fatal("expected biz change log table to be created automatically")
	}
	var count int64
	if err := gdb.Model(&auditModel.BizChangeLog{}).Count(&count).Error; err != nil {
		t.Fatalf("count audit records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one audit record, got %d", count)
	}
}

func TestMustJSONHandlesEmptyValues(t *testing.T) {
	if got := mustJSON(nil); got != "null" {
		t.Fatalf("expected nil to serialize to null, got %q", got)
	}
	if got := mustJSON(""); got != `""` {
		t.Fatalf("expected empty string to serialize as JSON string, got %q", got)
	}
	if got := mustJSON(`{"id":"1"}`); got != `{"id":"1"}` {
		t.Fatalf("expected valid JSON string to pass through, got %q", got)
	}
}
