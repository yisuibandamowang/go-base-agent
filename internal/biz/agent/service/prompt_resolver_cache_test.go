package service

import (
	"context"
	"testing"

	agentModel "go-base-agent/internal/biz/agent/model"
	agentRepo "go-base-agent/internal/biz/agent/repo"
	"go-base-agent/internal/framework/db"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakePromptCache struct {
	prompts map[string]string
	hit     bool
}

func (c *fakePromptCache) Load(context.Context) (map[string]string, bool, error) {
	if !c.hit {
		return nil, false, nil
	}
	return copyPromptMap(c.prompts), true, nil
}

func (c *fakePromptCache) Save(_ context.Context, prompts map[string]string) error {
	c.prompts = copyPromptMap(prompts)
	c.hit = true
	return nil
}

func copyPromptMap(prompts map[string]string) map[string]string {
	copy := make(map[string]string, len(prompts))
	for key, value := range prompts {
		copy[key] = value
	}
	return copy
}

func TestPromptResolverReadsUpdatedRedisPromptAcrossInstances(t *testing.T) {
	repo, gdb := newPromptResolverRepo(t)
	builtin := &agentModel.AgentProfile{
		BaseModel: db.BaseModel{ID: "agent-builtin"},
		Name:      "默认助手",
		Builtin:   1,
		Active:    1,
	}
	if err := gdb.Create(builtin).Error; err != nil {
		t.Fatalf("create builtin agent: %v", err)
	}
	if err := gdb.Create(&agentModel.AgentPrompt{
		BaseModel: db.BaseModel{ID: "prompt-1"},
		AgentID:   builtin.ID,
		SlotKey:   "SYSTEM_CHAT",
		Content:   "旧提示词",
	}).Error; err != nil {
		t.Fatalf("create prompt: %v", err)
	}

	cache := &fakePromptCache{}
	first := NewPromptResolver(repo, ModeWorkflow, cache)
	second := NewPromptResolver(repo, ModeWorkflow, cache)
	if err := first.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh first resolver: %v", err)
	}
	if got := second.Resolve("SYSTEM_CHAT"); got != "旧提示词" {
		t.Fatalf("expected cached old prompt, got %q", got)
	}

	if err := gdb.Model(&agentModel.AgentPrompt{}).
		Where("agent_id = ? AND slot_key = ?", builtin.ID, "SYSTEM_CHAT").
		Update("content", "新提示词").Error; err != nil {
		t.Fatalf("update prompt: %v", err)
	}
	cache.hit = false
	if err := first.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh updated prompt: %v", err)
	}

	if got := second.Resolve("SYSTEM_CHAT"); got != "新提示词" {
		t.Fatalf("expected second resolver to observe updated prompt, got %q", got)
	}
}

func newPromptResolverRepo(t *testing.T) (*agentRepo.AgentRepo, *gorm.DB) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&agentModel.AgentProfile{}, &agentModel.AgentPrompt{}); err != nil {
		t.Fatalf("migrate agent tables: %v", err)
	}
	return agentRepo.NewAgentRepo(gdb), gdb
}
