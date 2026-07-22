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

type recordingQueryTermMappingCache struct {
	clearCalls  int
	clearDomain string
}

func (c *recordingQueryTermMappingCache) LoadMappings(ctx context.Context, domain string) ([]intentModel.QueryTermMapping, bool, error) {
	return nil, false, nil
}

func (c *recordingQueryTermMappingCache) SaveMappings(ctx context.Context, domain string, mappings []intentModel.QueryTermMapping) error {
	return nil
}

func (c *recordingQueryTermMappingCache) ClearMappings(ctx context.Context, domain string) error {
	c.clearCalls++
	c.clearDomain = domain
	return nil
}

func TestIntentService_ClearsQueryTermMappingCacheOnMutation(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&intentModel.QueryTermMapping{}); err != nil {
		t.Fatalf("migrate query term mappings: %v", err)
	}

	svc := NewIntentService(repo.NewIntentRepo(gdb), repo.NewTermMappingRepo(gdb), gdb)
	cache := &recordingQueryTermMappingCache{}
	svc.SetQueryTermMappingCacheManager(cache)

	created, err := svc.CreateTermMapping(context.Background(), dto.CreateTermMappingReq{
		Domain:     "membership",
		SourceTerm: "VIP",
		TargetTerm: "会员",
		MatchType:  1,
		Priority:   100,
		Enabled:    1,
	}, "user-1")
	if err != nil {
		t.Fatalf("create term mapping: %v", err)
	}

	target := "贵宾"
	if _, err := svc.UpdateTermMapping(context.Background(), created.ID, dto.UpdateTermMappingReq{
		TargetTerm: &target,
	}, "user-1"); err != nil {
		t.Fatalf("update term mapping: %v", err)
	}
	if err := svc.DeleteTermMapping(context.Background(), created.ID); err != nil {
		t.Fatalf("delete term mapping: %v", err)
	}

	if cache.clearCalls != 3 {
		t.Fatalf("expected three cache clears, got %d", cache.clearCalls)
	}
	if cache.clearDomain != "membership" {
		t.Fatalf("expected membership cache domain, got %q", cache.clearDomain)
	}
}
