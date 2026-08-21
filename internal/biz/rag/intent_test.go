package rag

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	intentModel "go-base-agent/internal/biz/intent_tree/model"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/infra/chat"
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

func TestIntentResolverUsesLLMClassifierWhenConfigured(t *testing.T) {
	resolver := NewIntentResolver(fakeIntentNodeLister{nodes: []intentModel.IntentNode{
		{BaseModel: db.BaseModel{ID: "root"}, IntentCode: "member", Name: "会员系统", Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-kb"}, IntentCode: "member_points", ParentCode: "member", Name: "积分查询", Description: "会员积分余额和明细", Kind: int16(IntentKindKB), Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-mcp"}, IntentCode: "member_profile", ParentCode: "member", Name: "会员画像", Description: "查询实时会员等级", McpToolID: "member_profile", Kind: int16(IntentKindMCP), Enabled: 1},
	}}, IntentResolverOptions{MinScore: 0.1, MaxIntents: 3})
	resolver.SetLLMService(&fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			if len(req.Messages) != 2 {
				t.Fatalf("expected system and user messages, got %+v", req.Messages)
			}
			if !strings.Contains(req.Messages[0].Content, "id=leaf-mcp") || !strings.Contains(req.Messages[0].Content, "type=MCP") {
				t.Fatalf("expected prompt to include MCP leaf details, got %q", req.Messages[0].Content)
			}
			return `[{"id":"leaf-mcp","score":0.91,"reason":"需要实时会员画像"}]`, nil
		},
	})

	subIntents, err := resolver.ResolveQuestions(context.Background(), []string{"帮我查会员等级"})
	if err != nil {
		t.Fatalf("resolve intents: %v", err)
	}
	if len(subIntents) != 1 || len(subIntents[0].NodeScores) != 1 {
		t.Fatalf("expected one llm intent, got %+v", subIntents)
	}
	if got := subIntents[0].NodeScores[0].Node.ID; got != "leaf-mcp" {
		t.Fatalf("expected LLM-selected MCP intent, got %q", got)
	}
	if got := subIntents[0].NodeScores[0].Score; got != 0.91 {
		t.Fatalf("expected LLM score 0.91, got %.2f", got)
	}
}

func TestBuildIntentClassifierPromptUnwrapsJSONExamples(t *testing.T) {
	prompt := buildIntentClassifierPrompt([]IntentNode{{
		ID:          "sys-feedback",
		IntentCode:  "sys-feedback",
		Name:        "评价反馈",
		Description: "用户对上一轮回答做出评价",
		Examples:    `["回答得不错","你答错了"]`,
		Kind:        IntentKindSystem,
	}}, nil)

	if !strings.Contains(prompt, "examples=回答得不错 / 你答错了") {
		t.Fatalf("expected JSON examples to be unwrapped, got %q", prompt)
	}
	if strings.Contains(prompt, `examples=["`) {
		t.Fatalf("JSON syntax should not leak into classifier examples, got %q", prompt)
	}
}

func TestBuildIntentClassifierPromptIncludesInteractionRoutingRules(t *testing.T) {
	prompt := buildIntentClassifierPrompt([]IntentNode{{
		ID:          "sys-feedback",
		IntentCode:  "sys-feedback",
		Name:        "评价反馈",
		Description: "用户对上一轮回答做出评价",
		Kind:        IntentKindSystem,
	}}, nil)

	for _, fragment := range []string{
		"交互导向",
		"只在 type=SYSTEM 节点中选择",
		"交际行为一致时按强匹配打分",
	} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("expected interaction routing rule %q in prompt, got %q", fragment, prompt)
		}
	}
}

func TestIntentResolverFallsBackToHeuristicWhenLLMFails(t *testing.T) {
	resolver := NewIntentResolver(fakeIntentNodeLister{nodes: []intentModel.IntentNode{
		{BaseModel: db.BaseModel{ID: "root"}, IntentCode: "member", Name: "会员系统", Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-kb"}, IntentCode: "member_points", ParentCode: "member", Name: "积分查询", Description: "会员积分余额和明细", Examples: "积分怎么查", CollectionName: "member_kb", Kind: int16(IntentKindKB), Enabled: 1},
	}}, IntentResolverOptions{MinScore: 0.1, MaxIntents: 3})
	resolver.SetLLMService(&fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			return "not json", nil
		},
	})

	subIntents, err := resolver.ResolveQuestions(context.Background(), []string{"积分怎么查"})
	if err != nil {
		t.Fatalf("resolve intents: %v", err)
	}
	if len(subIntents) != 1 || len(subIntents[0].NodeScores) != 1 {
		t.Fatalf("expected heuristic fallback intent, got %+v", subIntents)
	}
	if got := subIntents[0].TopLeafID(); got != "leaf-kb" {
		t.Fatalf("expected heuristic fallback leaf-kb, got %q", got)
	}
}

func TestCapTotalIntentsKeepsTopPerSubQuestion(t *testing.T) {
	subIntents := []SubQuestionIntent{
		{SubQuestion: "q1", NodeScores: []NodeScore{
			{Node: IntentNode{ID: "a1"}, Score: 0.9},
			{Node: IntentNode{ID: "a2"}, Score: 0.8},
		}},
		{SubQuestion: "q2", NodeScores: []NodeScore{
			{Node: IntentNode{ID: "b1"}, Score: 0.7},
			{Node: IntentNode{ID: "b2"}, Score: 0.6},
		}},
		{SubQuestion: "q3", NodeScores: []NodeScore{
			{Node: IntentNode{ID: "c1"}, Score: 0.5},
			{Node: IntentNode{ID: "c2"}, Score: 0.4},
		}},
	}

	capped := capTotalIntents(subIntents, 4)

	if got := totalNodeScores(capped); got != 4 {
		t.Fatalf("expected total 4 intents, got %d in %+v", got, capped)
	}
	for i, want := range []string{"a1", "b1", "c1"} {
		if len(capped[i].NodeScores) == 0 || capped[i].NodeScores[0].Node.ID != want {
			t.Fatalf("expected sub question %d to keep top %s, got %+v", i, want, capped[i].NodeScores)
		}
	}
	if len(capped[0].NodeScores) != 2 || capped[0].NodeScores[1].Node.ID != "a2" {
		t.Fatalf("expected remaining slot to keep highest additional a2, got %+v", capped[0].NodeScores)
	}
}

func totalNodeScores(subIntents []SubQuestionIntent) int {
	total := 0
	for _, subIntent := range subIntents {
		total += len(subIntent.NodeScores)
	}
	return total
}

func TestIntentGuidanceServicePromptsOnAmbiguousScores(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{
		Enabled:             true,
		AmbiguityScoreRatio: 0.8,
		AmbiguityMargin:     0.15,
		MaxOptions:          3,
	})
	decision := guide.DetectAmbiguity(context.Background(), "会员怎么查", []SubQuestionIntent{{
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

func TestIntentGuidanceServiceSkipsWhenQuestionContainsDomainName(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{
		Enabled:             true,
		AmbiguityScoreRatio: 0.8,
		AmbiguityMargin:     0.15,
		MaxOptions:          3,
	})
	guide.SetIntentNodeLister(fakeIntentNodeLister{nodes: []intentModel.IntentNode{
		{BaseModel: db.BaseModel{ID: "domain"}, IntentCode: "member", Name: "会员系统", Level: 0, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "cat-a"}, IntentCode: "member_level", ParentCode: "member", Name: "等级", Level: 1, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "cat-b"}, IntentCode: "member_points", ParentCode: "member", Name: "积分", Level: 1, Enabled: 1},
	}})

	decision := guide.DetectAmbiguity(context.Background(), "会员系统怎么查", []SubQuestionIntent{{
		SubQuestion: "会员系统怎么查",
		NodeScores: []NodeScore{
			{Node: IntentNode{ID: "topic-a", IntentCode: "member_level_detail", Name: "会员等级", ParentCode: "member_level", Level: 2, Kind: IntentKindKB}, Score: 0.9},
			{Node: IntentNode{ID: "topic-b", IntentCode: "member_points_detail", Name: "会员积分", ParentCode: "member_points", Level: 2, Kind: IntentKindKB}, Score: 0.82},
		},
	}})

	if decision.Action != GuidanceActionNone {
		t.Fatalf("expected domain name to skip guidance, got %+v", decision)
	}
}

func TestIntentGuidanceServiceUsesCheckerForBorderlineRatio(t *testing.T) {
	checker := &recordingAmbiguityChecker{ambiguous: true}
	guide := NewIntentGuidanceService(GuidanceOptions{
		Enabled:             true,
		AmbiguityScoreRatio: 0.8,
		AmbiguityMargin:     0.15,
		MaxOptions:          3,
	})
	guide.SetAmbiguityChecker(checker)

	decision := guide.DetectAmbiguity(context.Background(), "会员怎么查", []SubQuestionIntent{{
		SubQuestion: "会员怎么查",
		NodeScores: []NodeScore{
			{Node: IntentNode{ID: "a", Name: "会员等级", Kind: IntentKindKB}, Score: 0.9},
			{Node: IntentNode{ID: "b", Name: "会员积分", Kind: IntentKindKB}, Score: 0.7},
		},
	}})

	if checker.calls != 1 {
		t.Fatalf("expected checker to be called once, got %d", checker.calls)
	}
	if decision.Action != GuidanceActionPrompt {
		t.Fatalf("expected checker-confirmed ambiguity to prompt, got %+v", decision)
	}
}

func TestIntentGuidanceServiceDefaultsMaxOptionsLikeJava(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{Enabled: true})

	decision := guide.DetectAmbiguity(context.Background(), "会员怎么查", []SubQuestionIntent{{
		SubQuestion: "会员怎么查",
		NodeScores: []NodeScore{
			{Node: IntentNode{ID: "a", Name: "候选1", Kind: IntentKindKB}, Score: 0.97},
			{Node: IntentNode{ID: "b", Name: "候选2", Kind: IntentKindKB}, Score: 0.96},
			{Node: IntentNode{ID: "c", Name: "候选3", Kind: IntentKindKB}, Score: 0.95},
			{Node: IntentNode{ID: "d", Name: "候选4", Kind: IntentKindKB}, Score: 0.94},
			{Node: IntentNode{ID: "e", Name: "候选5", Kind: IntentKindKB}, Score: 0.93},
			{Node: IntentNode{ID: "f", Name: "候选6", Kind: IntentKindKB}, Score: 0.92},
			{Node: IntentNode{ID: "g", Name: "候选7", Kind: IntentKindKB}, Score: 0.91},
		},
	}})

	if decision.Action != GuidanceActionPrompt {
		t.Fatalf("expected prompt decision, got %+v", decision)
	}
	if !strings.Contains(decision.Prompt, "候选6") || strings.Contains(decision.Prompt, "候选7") {
		t.Fatalf("expected Java default max options 6, got %q", decision.Prompt)
	}
}

func TestLLMAmbiguityCheckerParsesAmbiguousFlag(t *testing.T) {
	checker := NewLLMAmbiguityChecker(&fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			return `{"ambiguous":false,"reason":"用户已明确"}`, nil
		},
	})

	if checker.CheckAmbiguity(context.Background(), "会员等级怎么查", []NodeScore{
		{Node: IntentNode{ID: "a", Name: "会员等级"}, Score: 0.9},
		{Node: IntentNode{ID: "b", Name: "会员积分"}, Score: 0.7},
	}) {
		t.Fatal("expected ambiguous=false to skip guidance")
	}
}

type recordingAmbiguityChecker struct {
	ambiguous bool
	calls     int
}

func (c *recordingAmbiguityChecker) CheckAmbiguity(ctx context.Context, question string, ranked []NodeScore) bool {
	c.calls++
	return c.ambiguous
}

// TestIntentResolver_ResolvesQuestionsConcurrently 验证多个子问题的意图分类并行执行：
// 串行总耗时约为单次分类耗时之和，并行时应显著小于该值。
func TestIntentResolver_ResolvesQuestionsConcurrently(t *testing.T) {
	nodes := []intentModel.IntentNode{
		{BaseModel: db.BaseModel{ID: "root"}, IntentCode: "member", Name: "会员系统", Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-1"}, IntentCode: "member_points", ParentCode: "member", Name: "积分查询", Kind: int16(IntentKindKB), Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-2"}, IntentCode: "member_level", ParentCode: "member", Name: "等级查询", Kind: int16(IntentKindKB), Enabled: 1},
		{BaseModel: db.BaseModel{ID: "leaf-3"}, IntentCode: "member_profile", ParentCode: "member", Name: "会员画像", Kind: int16(IntentKindKB), Enabled: 1},
	}

	var mu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	// 每次 LLM 分类耗时 100ms；3 个子问题串行至少 300ms，并行应接近 100ms
	slowLLM := &fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return `[{"id":"leaf-1","score":0.9}]`, nil
		},
	}
	resolver := NewIntentResolver(fakeIntentNodeLister{nodes: nodes}, IntentResolverOptions{MinScore: 0.1, MaxIntents: 5})
	resolver.SetLLMService(slowLLM)

	questions := []string{"问题一", "问题二", "问题三"}
	start := time.Now()
	resolved, err := resolver.ResolveQuestions(context.Background(), questions)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ResolveQuestions: %v", err)
	}
	if len(resolved) != 3 {
		t.Fatalf("expected 3 sub intents, got %d", len(resolved))
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("expected concurrent classification, took %v (serial would be >=300ms)", elapsed)
	}
	if maxInFlight < 2 {
		t.Fatalf("expected at least 2 concurrent classifications, max in-flight=%d", maxInFlight)
	}
}
