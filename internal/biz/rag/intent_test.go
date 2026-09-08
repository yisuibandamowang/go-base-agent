package rag

import (
	"context"
	"fmt"
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

// TestNewIntentResolverDefaultsToJavaConstants 验证未显式传参时意图准入阈值与数量上限
// 使用 Java RAGConstant 的固定值（0.35 / 3），而非检索通道的 min-intent-score。
func TestNewIntentResolverDefaultsToJavaConstants(t *testing.T) {
	resolver := NewIntentResolver(fakeIntentNodeLister{}, IntentResolverOptions{})
	if resolver.opts.MinScore != IntentMinScore || resolver.opts.MaxIntents != MaxIntentCount {
		t.Fatalf("expected defaults %v/%d, got %v/%d", IntentMinScore, MaxIntentCount, resolver.opts.MinScore, resolver.opts.MaxIntents)
	}
	if IntentMinScore != 0.35 || MaxIntentCount != 3 {
		t.Fatalf("expected java constants 0.35/3, got %v/%d", IntentMinScore, MaxIntentCount)
	}
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

func TestBuildIntentClassifierPromptRendersJavaClassifierTemplate(t *testing.T) {
	prompt := buildIntentClassifierPrompt([]IntentNode{{
		ID:          "biz-oa-security",
		IntentCode:  "biz-oa-security",
		Name:        "数据安全",
		Description: "OA 系统数据安全要求",
		Kind:        IntentKindKB,
	}}, nil)

	// 对齐 Java intent-classifier.st 的完整规则段落，模板缺失时这些断言会提示内联兜底已偏离。
	for _, fragment := range []string{
		"# 角色定义",
		"# 核心判断流程",
		"歧义引导式问答",
		"最多 3 个",
		"# 评分标准",
		"0.4-0.7 的多项候选",
		"# 输出规范",
		"# 分类列表",
		"id=biz-oa-security",
		"path=数据安全",
	} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("expected java classifier template fragment %q in prompt, got %q", fragment, prompt)
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

func TestIntentGuidanceServicePromptsOnSameLeafNameAcrossSystems(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{Enabled: true, MaxOptions: 6})
	guide.SetIntentNodeLister(fakeIntentNodeLister{nodes: []intentModel.IntentNode{
		{BaseModel: db.BaseModel{ID: "1"}, IntentCode: "group", Name: "集团信息化", Level: 0, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "2"}, IntentCode: "oa", ParentCode: "group", Name: "OA系统", Level: 1, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "3"}, IntentCode: "ins", ParentCode: "group", Name: "保险系统", Level: 1, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "4"}, IntentCode: "oa_security", ParentCode: "oa", Name: "数据安全", Level: 2, Kind: int16(IntentKindKB), Enabled: 1},
		{BaseModel: db.BaseModel{ID: "5"}, IntentCode: "ins_security", ParentCode: "ins", Name: "数据安全", Level: 2, Kind: int16(IntentKindKB), Enabled: 1},
	}})
	guide.SetAmbiguityChecker(&recordingAmbiguityChecker{ambiguous: true})

	decision := guide.DetectAmbiguity(context.Background(), "数据安全方案有哪些", []SubQuestionIntent{{
		SubQuestion: "数据安全方案有哪些",
		NodeScores: []NodeScore{
			{Node: IntentNode{ID: "oa_security", ParentCode: "oa", Name: "数据安全", Kind: IntentKindKB}, Score: 0.62},
			{Node: IntentNode{ID: "ins_security", ParentCode: "ins", Name: "数据安全", Kind: IntentKindKB}, Score: 0.60},
		},
	}})

	if decision.Action != GuidanceActionPrompt {
		t.Fatalf("expected prompt decision, got %+v", decision)
	}
	for _, fragment := range []string{
		"关于数据安全",
		"1) 集团信息化 > OA系统 > 数据安全",
		"2) 集团信息化 > 保险系统 > 数据安全",
		"请回复数字选择（可多选，如 1,2）",
	} {
		if !strings.Contains(decision.Prompt, fragment) {
			t.Fatalf("expected guidance prompt to contain %q, got %q", fragment, decision.Prompt)
		}
	}
}

func TestIntentGuidanceServiceSkipsWhenNoPathNameConflict(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{Enabled: true, MaxOptions: 6})
	guide.SetAmbiguityChecker(&recordingAmbiguityChecker{ambiguous: true})

	// 分数接近但叶子名称与中间节点名称均不同，不构成路径重名，不应触发澄清。
	decision := guide.DetectAmbiguity(context.Background(), "会员怎么查", []SubQuestionIntent{{
		SubQuestion: "会员怎么查",
		NodeScores: []NodeScore{
			{Node: IntentNode{ID: "a", Name: "会员等级", Kind: IntentKindKB}, Score: 0.9},
			{Node: IntentNode{ID: "b", Name: "会员积分", Kind: IntentKindKB}, Score: 0.82},
		},
	}})

	if decision.Action != GuidanceActionNone {
		t.Fatalf("expected no conflict without path name collision, got %+v", decision)
	}
}

func TestIntentGuidanceServiceMidPathConflictRequiresQuestionMention(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{Enabled: true, MaxOptions: 6})
	guide.SetIntentNodeLister(fakeIntentNodeLister{nodes: []intentModel.IntentNode{
		{BaseModel: db.BaseModel{ID: "1"}, IntentCode: "group", Name: "集团信息化", Level: 0, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "2"}, IntentCode: "oa", ParentCode: "group", Name: "OA系统", Level: 1, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "3"}, IntentCode: "ins", ParentCode: "group", Name: "保险系统", Level: 1, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "4"}, IntentCode: "oa_rule", ParentCode: "oa", Name: "数据制度", Level: 2, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "5"}, IntentCode: "ins_rule", ParentCode: "ins", Name: "数据制度", Level: 2, Enabled: 1},
		{BaseModel: db.BaseModel{ID: "6"}, IntentCode: "oa_rule_doc", ParentCode: "oa_rule", Name: "制度文档", Level: 3, Kind: int16(IntentKindKB), Enabled: 1},
		{BaseModel: db.BaseModel{ID: "7"}, IntentCode: "ins_rule_doc", ParentCode: "ins_rule", Name: "制度清单", Level: 3, Kind: int16(IntentKindKB), Enabled: 1},
	}})
	checker := &recordingAmbiguityChecker{ambiguous: true}
	guide.SetAmbiguityChecker(checker)

	scores := []NodeScore{
		{Node: IntentNode{ID: "oa_rule_doc", ParentCode: "oa_rule", Name: "制度文档", Kind: IntentKindKB}, Score: 0.72},
		{Node: IntentNode{ID: "ins_rule_doc", ParentCode: "ins_rule", Name: "制度清单", Kind: IntentKindKB}, Score: 0.70},
	}

	// 中间节点「数据制度」重名，但问题没有提到该名称，用户并不在这个岔路口上。
	decision := guide.DetectAmbiguity(context.Background(), "制度文档包含什么", []SubQuestionIntent{{
		SubQuestion: "制度文档包含什么",
		NodeScores:  scores,
	}})
	if decision.Action != GuidanceActionNone {
		t.Fatalf("expected mid-path conflict to require question mention, got %+v", decision)
	}
	if checker.calls != 0 {
		t.Fatalf("expected no checker call without mention, got %d", checker.calls)
	}

	// 问题明确提到「数据制度」时，中间节点重名升级为冲突并交由 LLM 确认。
	decision = guide.DetectAmbiguity(context.Background(), "数据制度有哪些要求", []SubQuestionIntent{{
		SubQuestion: "数据制度有哪些要求",
		NodeScores:  scores,
	}})
	if decision.Action != GuidanceActionPrompt {
		t.Fatalf("expected prompt decision when question mentions mid-path name, got %+v", decision)
	}
	if !strings.Contains(decision.Prompt, "关于数据制度") {
		t.Fatalf("expected topic to be the conflicting mid-path name, got %q", decision.Prompt)
	}
}

func TestIntentGuidanceServiceRequiresLLMConfirmation(t *testing.T) {
	checker := &recordingAmbiguityChecker{ambiguous: false}
	guide := NewIntentGuidanceService(GuidanceOptions{Enabled: true, MaxOptions: 6})
	guide.SetAmbiguityChecker(checker)

	decision := guide.DetectAmbiguity(context.Background(), "数据安全方案有哪些", []SubQuestionIntent{{
		SubQuestion: "数据安全方案有哪些",
		NodeScores: []NodeScore{
			{Node: IntentNode{ID: "a", Name: "数据安全", Kind: IntentKindKB}, Score: 0.9},
			{Node: IntentNode{ID: "b", Name: "数据安全", Kind: IntentKindKB}, Score: 0.88},
		},
	}})

	if checker.calls != 1 {
		t.Fatalf("expected checker to be called once, got %d", checker.calls)
	}
	if decision.Action != GuidanceActionNone {
		t.Fatalf("expected LLM-rejected ambiguity to skip clarification, got %+v", decision)
	}
}

func TestIntentGuidanceServiceDefaultsMaxOptionsLikeJava(t *testing.T) {
	guide := NewIntentGuidanceService(GuidanceOptions{Enabled: true})
	guide.SetAmbiguityChecker(&recordingAmbiguityChecker{ambiguous: true})

	scores := make([]NodeScore, 0, 7)
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		scores = append(scores, NodeScore{Node: IntentNode{ID: id, Name: "候选", Kind: IntentKindKB}, Score: 0.9})
	}
	decision := guide.DetectAmbiguity(context.Background(), "候选是什么", []SubQuestionIntent{{
		SubQuestion: "候选是什么",
		NodeScores:  scores,
	}})

	if decision.Action != GuidanceActionPrompt {
		t.Fatalf("expected prompt decision, got %+v", decision)
	}
	if got := strings.Count(decision.Prompt, ") "); got != 6 {
		t.Fatalf("expected Java default max options 6, got %d options in %q", got, decision.Prompt)
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

// TestLLMAmbiguityCheckerFailsOpenOnErrors 验证调用失败、非法 JSON、缺 ambiguous 字段时
// 一律放行检索（返回 false），对齐 Java AmbiguityLLMChecker 的降级方向。
func TestLLMAmbiguityCheckerFailsOpenOnErrors(t *testing.T) {
	ranked := []NodeScore{
		{Node: IntentNode{ID: "a", Name: "数据安全"}, Score: 0.9},
		{Node: IntentNode{ID: "b", Name: "数据安全"}, Score: 0.88},
	}

	failing := NewLLMAmbiguityChecker(&fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			return "", fmt.Errorf("llm unavailable")
		},
	})
	if failing.CheckAmbiguity(context.Background(), "数据安全方案", ranked) {
		t.Fatal("expected llm error to fail open")
	}

	invalidJSON := NewLLMAmbiguityChecker(&fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			return "not json", nil
		},
	})
	if invalidJSON.CheckAmbiguity(context.Background(), "数据安全方案", ranked) {
		t.Fatal("expected invalid json to fail open")
	}

	missingField := NewLLMAmbiguityChecker(&fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			return `{"reason":"缺少判定字段"}`, nil
		},
	})
	if missingField.CheckAmbiguity(context.Background(), "数据安全方案", ranked) {
		t.Fatal("expected missing ambiguous field to fail open")
	}
}

// TestLLMAmbiguityCheckerRendersJavaTemplate 验证歧义确认提示词渲染了 Java
// guidance-ambiguity-check.st 的完整判定规则。
func TestLLMAmbiguityCheckerRendersJavaTemplate(t *testing.T) {
	var captured string
	checker := NewLLMAmbiguityChecker(&fakeLLMService{
		chatFn: func(ctx context.Context, req chat.Request) (string, error) {
			captured = req.Messages[0].Content
			return `{"ambiguous":true,"reason":"候选路径同名"}`, nil
		},
	})

	if !checker.CheckAmbiguity(context.Background(), "数据安全方案", []NodeScore{
		{Node: IntentNode{ID: "oa", Name: "数据安全", FullPath: "集团信息化 > OA系统 > 数据安全"}, Score: 0.62},
	}) {
		t.Fatal("expected ambiguous=true to require clarification")
	}
	for _, fragment := range []string{
		"用户问题：数据安全方案",
		"完整路径: 集团信息化 > OA系统 > 数据安全",
		"判定规则：",
		"拿不准时返回 false",
		`{"ambiguous": true/false`,
	} {
		if !strings.Contains(captured, fragment) {
			t.Fatalf("expected ambiguity check prompt to contain %q, got %q", fragment, captured)
		}
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
