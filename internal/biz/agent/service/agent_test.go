package service

import (
	"context"
	"testing"

	agentDto "go-base-agent/internal/biz/agent/dto"
	agentModel "go-base-agent/internal/biz/agent/model"
	agentRepo "go-base-agent/internal/biz/agent/repo"
	auditModel "go-base-agent/internal/biz/audit/model"
	auditRepo "go-base-agent/internal/biz/audit/repo"
	auditService "go-base-agent/internal/biz/audit/service"
	"go-base-agent/internal/framework/db"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAgentServiceListAndPromptFallback(t *testing.T) {
	svc, gdb := newAgentServiceTest(t, ModeWorkflow)

	builtin := &agentModel.AgentProfile{
		BaseModel:   db.BaseModel{ID: "agent-builtin"},
		Name:        "默认助手",
		Description: "系统默认人设",
		Avatar:      "orbit-indigo",
		Builtin:     1,
		Active:      1,
	}
	custom := &agentModel.AgentProfile{
		BaseModel:   db.BaseModel{ID: "agent-custom"},
		Name:        "客服助手",
		Description: "面向客服",
		Avatar:      "orbit-green",
		Builtin:     0,
		Active:      0,
	}
	if err := gdb.Create(builtin).Error; err != nil {
		t.Fatalf("create builtin: %v", err)
	}
	if err := gdb.Create(custom).Error; err != nil {
		t.Fatalf("create custom: %v", err)
	}
	mustCreatePrompt(t, gdb, "prompt-1", builtin.ID, "SYSTEM_CHAT", "builtin chat")
	mustCreatePrompt(t, gdb, "prompt-2", builtin.ID, "CONVERSATION_SUMMARY", "builtin summary {summary_max_chars}")
	mustCreatePrompt(t, gdb, "prompt-3", custom.ID, "SYSTEM_CHAT", "custom chat")
	mustCreatePrompt(t, gdb, "prompt-4", custom.ID, "AGENT_MAIN", "agent only")

	resp, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if resp.Mode != ModeWorkflow {
		t.Fatalf("unexpected mode: %s", resp.Mode)
	}
	if resp.EffectiveSlotTotal != 6 {
		t.Fatalf("unexpected effective slot total: %d", resp.EffectiveSlotTotal)
	}
	if len(resp.Agents) != 2 {
		t.Fatalf("unexpected agent count: %d", len(resp.Agents))
	}
	if !resp.Agents[0].Builtin || resp.Agents[0].Name != "默认助手" {
		t.Fatalf("builtin should be first: %+v", resp.Agents[0])
	}
	if resp.Agents[1].EffectiveSlots != 1 || resp.Agents[1].InactiveSlots != 1 {
		t.Fatalf("unexpected custom slot counts: %+v", resp.Agents[1])
	}

	cfg, err := svc.LoadPrompts(context.Background(), custom.ID)
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	if cfg.DefaultAgentName != "默认助手" {
		t.Fatalf("unexpected default agent name: %s", cfg.DefaultAgentName)
	}
	if len(cfg.Slots) != 7 {
		t.Fatalf("unexpected slot count: %d", len(cfg.Slots))
	}
	if got := cfg.Slots[0].Content; got != "custom chat" {
		t.Fatalf("unexpected system chat content: %q", got)
	}
	if cfg.Slots[3].Effective {
		t.Fatalf("agent main should be inactive in workflow mode")
	}
	if got, err := svc.DefaultPrompt(context.Background(), "SYSTEM_CHAT"); err != nil || got != "builtin chat" {
		t.Fatalf("default prompt = %q, err=%v", got, err)
	}
}

func TestAgentServiceCreateUpdateDeleteActivateAndPromptValidation(t *testing.T) {
	svc, gdb := newAgentServiceTest(t, ModeWorkflow)

	builtin := &agentModel.AgentProfile{
		BaseModel:   db.BaseModel{ID: "agent-builtin"},
		Name:        "默认助手",
		Description: "系统默认人设",
		Avatar:      "orbit-indigo",
		Builtin:     1,
		Active:      1,
	}
	if err := gdb.Create(builtin).Error; err != nil {
		t.Fatalf("create builtin: %v", err)
	}

	id, err := svc.Create(context.Background(), agentDto.CreateAgentProfileReq{
		Name:        "  客服助手  ",
		Description: "  面向客服  ",
		Avatar:      "  orbit-green  ",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if id == "" {
		t.Fatal("expected created id")
	}

	dupID, err := svc.Create(context.Background(), agentDto.CreateAgentProfileReq{Name: "客服助手"})
	if err == nil || dupID != "" {
		t.Fatalf("expected duplicate name error, got id=%q err=%v", dupID, err)
	}

	if _, err := svc.Update(context.Background(), id, agentDto.UpdateAgentProfileReq{
		Name:        ptrString("  客服助手-新版  "),
		Description: ptrString(""),
		Avatar:      ptrString("orbit-blue"),
	}); err != nil {
		t.Fatalf("update agent: %v", err)
	}

	created, err := svc.repo.FindProfileByID(context.Background(), id)
	if err != nil {
		t.Fatalf("load updated agent: %v", err)
	}
	if created.Name != "客服助手-新版" || created.Description != "" || created.Avatar != "orbit-blue" {
		t.Fatalf("unexpected updated profile: %+v", created)
	}

	if err := svc.SavePrompt(context.Background(), id, "RECOMMENDED_QUESTIONS", agentDto.SaveAgentPromptReq{
		Content: "只给一个问题：{question}",
	}); err == nil {
		t.Fatal("expected placeholder validation failure")
	}

	if err := svc.SavePrompt(context.Background(), id, "RECOMMENDED_QUESTIONS", agentDto.SaveAgentPromptReq{
		Content: "问题：{question}；答案：{answer}；片段：{chunks}；数量：{count}",
	}); err != nil {
		t.Fatalf("save prompt: %v", err)
	}
	if err := svc.SavePrompt(context.Background(), id, "RECOMMENDED_QUESTIONS", agentDto.SaveAgentPromptReq{Content: ""}); err != nil {
		t.Fatalf("clear prompt: %v", err)
	}

	if err := svc.Activate(context.Background(), id); err != nil {
		t.Fatalf("activate agent: %v", err)
	}
	if err := svc.Activate(context.Background(), builtin.ID); err != nil {
		t.Fatalf("reactivate builtin: %v", err)
	}
	activeProfiles, err := svc.repo.ListProfiles(context.Background())
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	var activeCount int
	for _, profile := range activeProfiles {
		if profile.Active == 1 {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected one active profile, got %d", activeCount)
	}

	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	deleted, err := svc.repo.FindProfileByID(context.Background(), id)
	if err != nil {
		t.Fatalf("find deleted agent: %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected soft deleted profile to be hidden, got %+v", deleted)
	}
}

func newAgentServiceTest(t *testing.T, mode string) (*AgentService, *gorm.DB) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&agentModel.AgentProfile{}, &agentModel.AgentPrompt{}, &auditModel.BizChangeLog{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	svc := NewAgentService(agentRepo.NewAgentRepo(gdb), mode)
	svc.SetAuditRecorder(auditService.NewBizChangeLogService(auditRepo.NewBizChangeLogRepo(gdb)))
	return svc, gdb
}

func mustCreatePrompt(t *testing.T, gdb *gorm.DB, id, agentID, slotKey, content string) {
	t.Helper()
	if err := gdb.Create(&agentModel.AgentPrompt{
		BaseModel: db.BaseModel{ID: id},
		AgentID:   agentID,
		SlotKey:   slotKey,
		Content:   content,
	}).Error; err != nil {
		t.Fatalf("create prompt %s: %v", slotKey, err)
	}
}

func ptrString(value string) *string {
	return &value
}
