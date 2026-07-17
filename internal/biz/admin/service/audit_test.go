package service

import (
	"context"
	"strings"
	"testing"
	"time"

	adminDto "go-base-agent/internal/biz/admin/dto"
	adminModel "go-base-agent/internal/biz/admin/model"
	adminRepo "go-base-agent/internal/biz/admin/repo"
	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/rag"
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

func TestAdminService_GetTrendsSupportsQualityMetric(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &rag.TraceRunRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now()
	start := now.Add(-30 * time.Minute)
	end := now.Add(-29 * time.Minute)
	noDocReply := "未检索到与问题相关的文档内容。"
	if err := gdb.Create(&conversationModel.Message{
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        noDocReply,
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := gdb.Model(&conversationModel.Message{}).Where("conversation_id = ?", "conv-1").
		Updates(map[string]any{"create_time": start, "update_time": start}).Error; err != nil {
		t.Fatalf("update message time: %v", err)
	}
	for _, run := range []rag.TraceRunRecord{
		{ID: "run-1", TraceID: "trace-1", Status: "SUCCESS", StartTime: &start, EndTime: &end, DurationMs: 1000, Deleted: 0},
		{ID: "run-2", TraceID: "trace-2", Status: "ERROR", StartTime: &start, EndTime: &end, DurationMs: 1000, Deleted: 0},
	} {
		if err := gdb.Create(&run).Error; err != nil {
			t.Fatalf("seed trace run: %v", err)
		}
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	resp, err := svc.GetTrends(context.Background(), "quality", "24h", "hour")
	if err != nil {
		t.Fatalf("get trends: %v", err)
	}
	if resp.Metric != "quality" || resp.Window != "24h" || resp.Granularity != "hour" {
		t.Fatalf("unexpected trend metadata: %+v", resp)
	}
	if len(resp.Series) != 2 {
		t.Fatalf("expected quality to return 2 series, got %+v", resp.Series)
	}
	if resp.Series[0].Name != "错误率" || resp.Series[1].Name != "无知识率" {
		t.Fatalf("unexpected quality series names: %+v", resp.Series)
	}
	if len(resp.Series[0].Data) == 0 || len(resp.Series[1].Data) == 0 {
		t.Fatalf("expected non-empty trend points: %+v", resp.Series)
	}
}

func TestAdminService_GetTrendsBucketsMessageMetricByHour(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	messageTime := time.Now().Add(-30 * time.Minute).Truncate(time.Hour).Add(10 * time.Minute)
	if err := gdb.Create(&conversationModel.Message{
		ConversationID: "conv-hour",
		UserID:         "user-hour",
		Role:           "user",
		Content:        "hello",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := gdb.Model(&conversationModel.Message{}).Where("conversation_id = ?", "conv-hour").
		Updates(map[string]any{"create_time": messageTime, "update_time": messageTime}).Error; err != nil {
		t.Fatalf("update message time: %v", err)
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	resp, err := svc.GetTrends(context.Background(), "messages", "24h", "hour")
	if err != nil {
		t.Fatalf("get trends: %v", err)
	}
	if len(resp.Series) != 1 {
		t.Fatalf("expected one message series, got %+v", resp.Series)
	}
	bucketTs := messageTime.Truncate(time.Hour).UnixMilli()
	for _, point := range resp.Series[0].Data {
		if point.Ts == bucketTs {
			if point.Value != 1 {
				t.Fatalf("expected message bucket value 1, got %+v", point)
			}
			return
		}
	}
	t.Fatalf("expected bucket %d in trend points: %+v", bucketTs, resp.Series[0].Data)
}
