package service

import (
	"context"
	"testing"
	"time"

	auditModel "go-base-agent/internal/biz/audit/model"
	"go-base-agent/internal/biz/audit/repo"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBizChangeLogService_ListAndGet(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	items := []auditModel.BizChangeLog{
		{
			ID:            "1001",
			BizType:       "USER",
			BizId:         "u-1",
			OperationType: "CREATE",
			ActionDesc:    "创建用户",
			Success:       true,
			OperatorID:    "admin",
			OperatorName:  "管理员",
			CreateTime:    now.Add(-time.Minute),
		},
		{
			ID:            "1002",
			BizType:       "USER",
			BizId:         "u-2",
			OperationType: "UPDATE",
			ActionDesc:    "更新用户",
			Success:       false,
			OperatorID:    "admin2",
			OperatorName:  "管理员2",
			ErrorMessage:  "failed",
			CreateTime:    now,
		},
	}
	for i := range items {
		if err := gdb.Create(&items[i]).Error; err != nil {
			t.Fatalf("create item %d: %v", i, err)
		}
	}

	svc := NewBizChangeLogService(repo.NewBizChangeLogRepo(gdb))

	page, total, err := svc.List(context.Background(), BizChangeLogPageReq{BizType: "USER"}, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total 2, got %d", total)
	}
	if len(page) != 2 {
		t.Fatalf("expected 2 records, got %d", len(page))
	}
	if page[0].ID != "1002" {
		t.Fatalf("expected newest record first, got %s", page[0].ID)
	}

	got, err := svc.Get(context.Background(), "1001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "1001" || got.BizType != "USER" || got.OperatorName != "管理员" {
		t.Fatalf("unexpected record: %+v", got)
	}
}
