package service

import (
	"context"
	"strings"
	"testing"

	adminDto "go-base-agent/internal/biz/admin/dto"
	adminModel "go-base-agent/internal/biz/admin/model"
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

func TestAdminService_SampleQuestionRecordsAuditLogs(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&adminModel.SampleQuestion{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))

	ctx := appctx.WithUser(context.Background(), &appctx.LoginUser{
		UserID:   "admin-1",
		Username: "管理员",
		Role:     "admin",
	})
	created, err := svc.CreateSampleQuestion(ctx, adminDto.CreateSampleQuestionReq{
		Title:    "标题",
		Question: "如何开通会员？",
	})
	if err != nil {
		t.Fatalf("create sample question: %v", err)
	}
	updatedQuestion := "如何续费会员？"
	if _, err := svc.UpdateSampleQuestion(ctx, created.ID, adminDto.UpdateSampleQuestionReq{
		Question: &updatedQuestion,
	}); err != nil {
		t.Fatalf("update sample question: %v", err)
	}
	if err := svc.DeleteSampleQuestion(ctx, created.ID); err != nil {
		t.Fatalf("delete sample question: %v", err)
	}

	var logs []auditModel.BizChangeLog
	if err := gdb.Where("biz_type = ? AND biz_id = ?", auditService.BizTypeSampleQuestion, created.ID).
		Order("create_time ASC").
		Find(&logs).Error; err != nil {
		t.Fatalf("load audit logs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 audit logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].OperationType != auditService.OperationCreate || !strings.Contains(logs[0].AfterSnapshot, "如何开通会员") {
		t.Fatalf("unexpected create audit log: %+v", logs[0])
	}
	if logs[1].OperationType != auditService.OperationUpdate ||
		!strings.Contains(logs[1].BeforeSnapshot, "如何开通会员") ||
		!strings.Contains(logs[1].AfterSnapshot, "如何续费会员") {
		t.Fatalf("unexpected update audit log: %+v", logs[1])
	}
	if logs[2].OperationType != auditService.OperationDelete || !strings.Contains(logs[2].BeforeSnapshot, "如何续费会员") {
		t.Fatalf("unexpected delete audit log: %+v", logs[2])
	}
}
