package service

import (
	"context"
	"strings"
	"testing"

	adminDto "go-base-agent/internal/biz/admin/dto"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	userModel "go-base-agent/internal/biz/user/model"
	appctx "go-base-agent/internal/framework/context"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAdminService_CreateUserRecordsAuditLog(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})
	resp, err := svc.CreateUser(ctx, adminDto.CreateUserReq{
		Username: "alice",
		Password: "secret",
		Role:     "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	var log auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeUser, resp.ID).First(&log).Error; err != nil {
		t.Fatalf("load audit log: %v", err)
	}
	if log.OperationType != auditService.OperationCreate || log.OperatorID != "admin-1" {
		t.Fatalf("unexpected audit log: %+v", log)
	}
	if !strings.Contains(log.ActionDesc, "alice") || !strings.Contains(log.AfterSnapshot, `"username":"alice"`) {
		t.Fatalf("unexpected audit payload: %+v", log)
	}
}
