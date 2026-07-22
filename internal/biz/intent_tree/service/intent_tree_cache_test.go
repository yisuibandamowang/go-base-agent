package service

import (
	"context"
	"testing"

	"go-base-agent/internal/biz/intent_tree/dto"
	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/biz/intent_tree/repo"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type recordingIntentNodeCache struct {
	clearCalls int
}

func (c *recordingIntentNodeCache) LoadNodes(ctx context.Context) ([]intentModel.IntentNode, bool, error) {
	return nil, false, nil
}

func (c *recordingIntentNodeCache) SaveNodes(ctx context.Context, nodes []intentModel.IntentNode) error {
	return nil
}

func (c *recordingIntentNodeCache) ClearNodes(ctx context.Context) error {
	c.clearCalls++
	return nil
}

func TestIntentService_ClearsIntentTreeCacheOnNodeMutation(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&intentModel.IntentNode{}); err != nil {
		t.Fatalf("migrate intent nodes: %v", err)
	}

	svc := NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb)
	cache := &recordingIntentNodeCache{}
	svc.SetIntentNodeCacheManager(cache)

	created, err := svc.CreateNode(context.Background(), dto.CreateIntentReq{
		IntentCode: "member",
		Name:       "会员系统",
		Level:      0,
		Enabled:    1,
	}, "user-1")
	if err != nil {
		t.Fatalf("create intent node: %v", err)
	}

	name := "会员中心"
	if _, err := svc.UpdateNode(context.Background(), created.ID, dto.UpdateIntentReq{
		Name: &name,
	}, "user-1"); err != nil {
		t.Fatalf("update intent node: %v", err)
	}
	if err := svc.ToggleNode(context.Background(), created.ID, 0); err != nil {
		t.Fatalf("toggle intent node: %v", err)
	}
	if err := svc.DeleteNode(context.Background(), created.ID); err != nil {
		t.Fatalf("delete intent node: %v", err)
	}

	if cache.clearCalls != 4 {
		t.Fatalf("expected four cache clears, got %d", cache.clearCalls)
	}
}
