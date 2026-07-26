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

func TestAdminService_ProtectsDefaultAdminUser(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defaultAdmin := &userModel.User{Username: "admin", Password: "pwd", Role: "admin"}
	if err := gdb.Create(defaultAdmin).Error; err != nil {
		t.Fatalf("seed default admin: %v", err)
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, adminDto.CreateUserReq{
		Username: " ADMIN ",
		Password: "secret",
		Role:     "user",
	}); err == nil || !strings.Contains(err.Error(), "默认管理员用户名不可用") {
		t.Fatalf("expected default admin username create to be rejected, got %v", err)
	}

	newName := "root"
	if _, err := svc.UpdateUser(ctx, defaultAdmin.ID, adminDto.UpdateUserReq{
		Username: &newName,
	}); err == nil || !strings.Contains(err.Error(), "默认管理员不允许修改或删除") {
		t.Fatalf("expected default admin update to be rejected, got %v", err)
	}

	if err := svc.DeleteUser(ctx, defaultAdmin.ID); err == nil || !strings.Contains(err.Error(), "默认管理员不允许修改或删除") {
		t.Fatalf("expected default admin delete to be rejected, got %v", err)
	}
}

func TestAdminService_UserRoleAndUsernameValidationAlignsJava(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	alice := &userModel.User{Username: "alice", Password: "pwd", Role: "user"}
	bob := &userModel.User{Username: "bob", Password: "pwd", Role: "user"}
	if err := gdb.Create(alice).Error; err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	if err := gdb.Create(bob).Error; err != nil {
		t.Fatalf("seed bob: %v", err)
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, adminDto.CreateUserReq{
		Username: "charlie",
		Password: "pwd",
		Role:     "manager",
	}); err == nil || !strings.Contains(err.Error(), "角色类型不合法") {
		t.Fatalf("expected invalid create role to be rejected, got %v", err)
	}

	if _, err := svc.CreateUser(ctx, adminDto.CreateUserReq{
		Username: " alice ",
		Password: "pwd",
		Role:     "user",
	}); err == nil || !strings.Contains(err.Error(), "用户名已存在") {
		t.Fatalf("expected duplicate create username to be rejected, got %v", err)
	}

	invalidRole := "manager"
	if _, err := svc.UpdateUser(ctx, bob.ID, adminDto.UpdateUserReq{
		Role: &invalidRole,
	}); err == nil || !strings.Contains(err.Error(), "角色类型不合法") {
		t.Fatalf("expected invalid update role to be rejected, got %v", err)
	}

	duplicateName := "alice"
	if _, err := svc.UpdateUser(ctx, bob.ID, adminDto.UpdateUserReq{
		Username: &duplicateName,
	}); err == nil || !strings.Contains(err.Error(), "用户名已存在") {
		t.Fatalf("expected duplicate update username to be rejected, got %v", err)
	}
}

func TestAdminService_GetDashboardSupportsWindowOverviewShape(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&userModel.User{}, &conversationModel.Conversation{}, &conversationModel.Message{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{"t_knowledge_base", "t_knowledge_document", "t_knowledge_chunk", "t_knowledge_vector"} {
		if err := gdb.Exec("CREATE TABLE " + table + " (id text primary key, deleted integer default 0, create_time datetime)").Error; err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
	}

	now := time.Now()
	prevWindow := now.Add(-25 * time.Hour)
	inWindow := now.Add(-30 * time.Minute)
	if err := gdb.Create(&userModel.User{Username: "u1", Password: "pwd", Role: "user"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, conv := range []conversationModel.Conversation{
		{ConversationID: "conv-current", UserID: "u1", Title: "current", LastTime: inWindow},
		{ConversationID: "conv-prev", UserID: "u1", Title: "prev", LastTime: prevWindow},
	} {
		if err := gdb.Create(&conv).Error; err != nil {
			t.Fatalf("seed conversation: %v", err)
		}
	}
	if err := gdb.Model(&conversationModel.Conversation{}).Where("conversation_id = ?", "conv-current").
		Updates(map[string]any{"create_time": inWindow, "update_time": inWindow}).Error; err != nil {
		t.Fatalf("update current conversation time: %v", err)
	}
	if err := gdb.Model(&conversationModel.Conversation{}).Where("conversation_id = ?", "conv-prev").
		Updates(map[string]any{"create_time": prevWindow, "update_time": prevWindow}).Error; err != nil {
		t.Fatalf("update prev conversation time: %v", err)
	}
	for _, msg := range []conversationModel.Message{
		{ConversationID: "conv-current", UserID: "u1", Role: "user", Content: "current"},
		{ConversationID: "conv-prev", UserID: "u1", Role: "user", Content: "prev"},
	} {
		if err := gdb.Create(&msg).Error; err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
	if err := gdb.Model(&conversationModel.Message{}).Where("conversation_id = ?", "conv-current").
		Updates(map[string]any{"create_time": inWindow, "update_time": inWindow}).Error; err != nil {
		t.Fatalf("update current message time: %v", err)
	}
	if err := gdb.Model(&conversationModel.Message{}).Where("conversation_id = ?", "conv-prev").
		Updates(map[string]any{"create_time": prevWindow, "update_time": prevWindow}).Error; err != nil {
		t.Fatalf("update prev message time: %v", err)
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	resp, err := svc.GetDashboard(context.Background(), "24h")
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	if resp.Window != "24h" || resp.CompareWindow != "prev_24h" || resp.UpdatedAt == 0 {
		t.Fatalf("unexpected dashboard metadata: %+v", resp)
	}
	if resp.Kpis == nil || resp.Kpis.Sessions24h.Value != 1 || resp.Kpis.Sessions24h.Delta != 0 {
		t.Fatalf("unexpected session kpis: %+v", resp.Kpis)
	}
	if resp.Kpis.TotalSessions.Value != 2 || resp.ConversationCount != 2 {
		t.Fatalf("expected total sessions to keep old and new fields, got %+v", resp)
	}
}

func TestAdminService_GetPerformanceSupportsWindowMetrics(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &rag.TraceRunRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now()
	inWindow := now.Add(-30 * time.Minute)
	end := inWindow.Add(time.Second)
	old := now.Add(-48 * time.Hour)
	noDocReply := "未检索到与问题相关的文档内容。"
	for _, msg := range []conversationModel.Message{
		{ConversationID: "conv-perf", UserID: "u1", Role: "assistant", Content: noDocReply},
		{ConversationID: "conv-old", UserID: "u1", Role: "assistant", Content: noDocReply},
	} {
		if err := gdb.Create(&msg).Error; err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
	if err := gdb.Model(&conversationModel.Message{}).Where("conversation_id = ?", "conv-perf").
		Updates(map[string]any{"create_time": inWindow, "update_time": inWindow}).Error; err != nil {
		t.Fatalf("update current message time: %v", err)
	}
	if err := gdb.Model(&conversationModel.Message{}).Where("conversation_id = ?", "conv-old").
		Updates(map[string]any{"create_time": old, "update_time": old}).Error; err != nil {
		t.Fatalf("update old message time: %v", err)
	}
	for _, run := range []rag.TraceRunRecord{
		{ID: "run-fast", TraceID: "trace-fast", Status: "SUCCESS", StartTime: &inWindow, EndTime: &end, DurationMs: 1000, Deleted: 0},
		{ID: "run-slow", TraceID: "trace-slow", Status: "SUCCESS", StartTime: &inWindow, EndTime: &end, DurationMs: 21000, Deleted: 0},
		{ID: "run-error", TraceID: "trace-error", Status: "ERROR", StartTime: &inWindow, EndTime: &end, DurationMs: 0, Deleted: 0},
		{ID: "run-old", TraceID: "trace-old", Status: "ERROR", StartTime: &old, EndTime: &old, DurationMs: 0, Deleted: 0},
	} {
		if err := gdb.Create(&run).Error; err != nil {
			t.Fatalf("seed trace run: %v", err)
		}
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	resp, err := svc.GetPerformance(context.Background(), "24h")
	if err != nil {
		t.Fatalf("get performance: %v", err)
	}
	if resp.Window != "24h" || resp.AvgLatencyMs != 11000 || resp.P95LatencyMs != 21000 {
		t.Fatalf("unexpected latency metrics: %+v", resp)
	}
	if resp.SuccessRate != 66.7 || resp.ErrorRate != 33.3 || resp.NoDocRate != 100 || resp.SlowRate != 50 {
		t.Fatalf("unexpected rate metrics: %+v", resp)
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

func TestAdminService_SampleQuestionCreateTrimsAndRejectsBlankQuestion(t *testing.T) {
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
		Title:       "  标题  ",
		Description: "  描述  ",
		Question:    "  怎么开通会员？  ",
	})
	if err != nil {
		t.Fatalf("create sample question: %v", err)
	}
	if created.Title != "标题" || created.Description != "描述" || created.Question != "怎么开通会员？" {
		t.Fatalf("expected trimmed sample question, got %+v", created)
	}

	if _, err := svc.CreateSampleQuestion(ctx, adminDto.CreateSampleQuestionReq{
		Question: "   ",
	}); err == nil || !strings.Contains(err.Error(), "示例问题内容不能为空") {
		t.Fatalf("expected blank question validation, got %v", err)
	}
}

func TestAdminService_SampleQuestionListUsesKeywordAndUpdateOrder(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.AutoMigrate(&adminModel.SampleQuestion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now()
	records := []adminModel.SampleQuestion{
		{Title: "A", Description: "会员权益", Question: "如何开通会员？"},
		{Title: "B", Description: "普通", Question: "如何续费会员？"},
		{Title: "C", Description: "普通", Question: "如何查看积分？"},
	}
	for i := range records {
		if err := gdb.Create(&records[i]).Error; err != nil {
			t.Fatalf("seed sample question %d: %v", i, err)
		}
	}
	if err := gdb.Model(&adminModel.SampleQuestion{}).Where("id = ?", records[0].ID).
		Updates(map[string]any{"update_time": now.Add(-2 * time.Hour)}).Error; err != nil {
		t.Fatalf("update time a: %v", err)
	}
	if err := gdb.Model(&adminModel.SampleQuestion{}).Where("id = ?", records[1].ID).
		Updates(map[string]any{"update_time": now}).Error; err != nil {
		t.Fatalf("update time b: %v", err)
	}
	if err := gdb.Model(&adminModel.SampleQuestion{}).Where("id = ?", records[2].ID).
		Updates(map[string]any{"update_time": now.Add(-1 * time.Hour)}).Error; err != nil {
		t.Fatalf("update time c: %v", err)
	}

	svc := NewAdminService(adminRepo.NewAdminRepo(gdb), adminRepo.NewSampleQuestionRepo(gdb), gdb)
	items, total, err := svc.ListSampleQuestions(context.Background(), 1, 2, "会员")
	if err != nil {
		t.Fatalf("list sample questions: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 matched questions, got %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 records on first page, got %d", len(items))
	}
	if items[0].ID != records[1].ID || items[1].ID != records[0].ID {
		t.Fatalf("expected update_time desc ordering, got %+v", items)
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
