package rag

import (
	"context"
	"errors"
	"testing"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/framework/db"
)

type fakeIntentNodeCache struct {
	loadNodes  []intentModel.IntentNode
	loadHit    bool
	loadErr    error
	loadCalls  int
	saveNodes  []intentModel.IntentNode
	saveErr    error
	saveCalls  int
	clearCalls int
}

func (c *fakeIntentNodeCache) LoadNodes(ctx context.Context) ([]intentModel.IntentNode, bool, error) {
	c.loadCalls++
	if c.loadErr != nil {
		return nil, false, c.loadErr
	}
	return append([]intentModel.IntentNode(nil), c.loadNodes...), c.loadHit, nil
}

func (c *fakeIntentNodeCache) SaveNodes(ctx context.Context, nodes []intentModel.IntentNode) error {
	c.saveCalls++
	c.saveNodes = append([]intentModel.IntentNode(nil), nodes...)
	return c.saveErr
}

func (c *fakeIntentNodeCache) ClearNodes(ctx context.Context) error {
	c.clearCalls++
	return nil
}

func TestCachedIntentNodeLister_UsesCacheBeforeLister(t *testing.T) {
	cache := &fakeIntentNodeCache{
		loadNodes: []intentModel.IntentNode{
			{BaseModel: db.BaseModel{ID: "cached"}, IntentCode: "cached", Name: "缓存节点", Enabled: 1},
		},
		loadHit: true,
	}
	lister := fakeIntentNodeLister{
		nodes: []intentModel.IntentNode{
			{BaseModel: db.BaseModel{ID: "db"}, IntentCode: "db", Name: "数据库节点", Enabled: 1},
		},
	}
	cached := NewCachedIntentNodeLister(lister, cache)

	nodes, err := cached.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list cached nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "cached" {
		t.Fatalf("expected cached node, got %+v", nodes)
	}
	if cache.loadCalls != 1 || cache.saveCalls != 0 {
		t.Fatalf("unexpected cache calls: %+v", cache)
	}
}

func TestCachedIntentNodeLister_SavesCacheAfterListerLoad(t *testing.T) {
	cache := &fakeIntentNodeCache{}
	lister := fakeIntentNodeLister{
		nodes: []intentModel.IntentNode{
			{BaseModel: db.BaseModel{ID: "db"}, IntentCode: "db", Name: "数据库节点", Enabled: 1},
		},
	}
	cached := NewCachedIntentNodeLister(lister, cache)

	nodes, err := cached.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list db nodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "db" {
		t.Fatalf("expected db node, got %+v", nodes)
	}
	if cache.loadCalls != 1 || cache.saveCalls != 1 {
		t.Fatalf("unexpected cache calls: %+v", cache)
	}
	if len(cache.saveNodes) != 1 || cache.saveNodes[0].ID != "db" {
		t.Fatalf("expected db nodes to be saved, got %+v", cache.saveNodes)
	}
}

func TestCachedIntentNodeLister_FallsBackWhenCacheFails(t *testing.T) {
	cache := &fakeIntentNodeCache{
		loadErr: errors.New("redis down"),
		saveErr: errors.New("redis still down"),
	}
	lister := fakeIntentNodeLister{
		nodes: []intentModel.IntentNode{
			{BaseModel: db.BaseModel{ID: "db"}, IntentCode: "db", Name: "数据库节点", Enabled: 1},
		},
	}
	cached := NewCachedIntentNodeLister(lister, cache)

	nodes, err := cached.ListAll(context.Background())
	if err != nil {
		t.Fatalf("expected cache failure to fallback to lister, got %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "db" {
		t.Fatalf("expected db node after cache failure, got %+v", nodes)
	}
	if cache.loadCalls != 1 || cache.saveCalls != 1 {
		t.Fatalf("unexpected cache calls: %+v", cache)
	}
}
