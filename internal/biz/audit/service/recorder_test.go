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
