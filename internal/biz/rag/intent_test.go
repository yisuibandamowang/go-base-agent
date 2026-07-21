package rag

import (
	"context"
	"strings"
	"testing"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/framework/db"
)

type fakeIntentNodeLister struct {
	nodes []intentModel.IntentNode
	err   error
}

func (l fakeIntentNodeLister) ListAll(ctx context.Context) ([]intentModel.IntentNode, error) {
	return l.nodes, l.err
}

func TestIntentResolverClassifiesLeafNodesAndGroupsKinds(t *testing.T) {
	resolver := NewIntentResolver(fakeIntentNodeLister{nodes: []intentModel.IntentNode{
		{BaseModel: db.BaseModel{ID: "root"}, IntentCode: "member", Name: "会员系统", Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-kb"}, IntentCode: "member_points", ParentCode: "member", Name: "积分查询", Description: "会员积分余额和明细", Examples: "积分怎么查", CollectionName: "member_kb", Kind: int16(IntentKindKB), Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-mcp"}, IntentCode: "member_profile", ParentCode: "member", Name: "会员画像", Description: "查询实时会员等级", McpToolID: "member_profile", Kind: int16(IntentKindMCP), Enabled: 1},
	}}, IntentResolverOptions{MinScore: 0.1, MaxIntents: 3})

	subIntents, err := resolver.ResolveQuestions(context.Background(), []string{"帮我查会员积分和等级"})
	if err != nil {
		t.Fatalf("resolve intents: %v", err)
	}
	if len(subIntents) != 1 {
		t.Fatalf("expected one sub intent, got %+v", subIntents)
	}
	if len(subIntents[0].NodeScores) != 2 {
		t.Fatalf("expected kb and mcp candidates, got %+v", subIntents[0].NodeScores)
	}
	if got := subIntents[0].TopLeafID(); got != "leaf-kb" {
		t.Fatalf("expected top leaf id leaf-kb, got %q", got)
	}

	group := MergeIntentGroup(subIntents)
	if len(group.KBIntents) != 1 || group.KBIntents[0].Node.ID != "leaf-kb" {
		t.Fatalf("expected one KB intent, got %+v", group.KBIntents)
	}
	if len(group.MCPIntents) != 1 || group.MCPIntents[0].Node.ID != "leaf-mcp" {
		t.Fatalf("expected one MCP intent, got %+v", group.MCPIntents)
	}
}

func TestIntentGuidanceServicePromptsOnAmbiguousScores(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{
		Enabled:             true,
		AmbiguityScoreRatio: 0.8,
		AmbiguityMargin:     0.15,
		MaxOptions:          3,
	})
	decision := guide.DetectAmbiguity("会员怎么查", []SubQuestionIntent{{
		SubQuestion: "会员怎么查",
		NodeScores: []NodeScore{
			{Node: IntentNode{ID: "a", Name: "会员等级"}, Score: 0.9},
			{Node: IntentNode{ID: "b", Name: "会员积分"}, Score: 0.82},
		},
	}})

	if decision.Action != GuidanceActionPrompt {
		t.Fatalf("expected prompt decision, got %+v", decision)
	}
	if !strings.Contains(decision.Prompt, "会员等级") || !strings.Contains(decision.Prompt, "会员积分") {
		t.Fatalf("expected prompt to include candidates, got %q", decision.Prompt)
	}
}
