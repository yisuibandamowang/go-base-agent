package rag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-base-agent/internal/infra/chat"
)

type stubRuntimePromptResolver struct {
	prompts map[string]string
}

func (r *stubRuntimePromptResolver) Resolve(slotKey string) string {
	if r == nil {
		return ""
	}
	return r.prompts[strings.ToUpper(strings.TrimSpace(slotKey))]
}

func (r *stubRuntimePromptResolver) Render(slotKey string, data any) (string, error) {
	return r.Resolve(slotKey), nil
}

func TestDefaultPromptBuilder_Basic(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{Question: "你好"})

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != chat.RoleSystem {
		t.Fatal("first should be system")
	}
	if req.Messages[0].Content == "" {
		t.Fatal("system prompt should not be empty")
	}
	if req.Messages[1].Role != chat.RoleUser {
		t.Fatal("second should be user")
	}
	if req.Messages[1].Content != "你好" {
		t.Fatalf("unexpected user content: %s", req.Messages[1].Content)
	}
}

func TestDefaultPromptBuilder_WithKbContext(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:  "什么是RAG",
		KbContext: "RAG是检索增强生成技术。",
	})

	content := req.Messages[1].Content
	if !strings.Contains(content, "RAG是检索增强生成技术") {
		t.Fatal("kb context should be in user message")
	}
	if !strings.Contains(content, "只能依据以下知识库内容回答") {
		t.Fatal("prompt should constrain the model to knowledge base content")
	}
	if !strings.Contains(content, "只命中文档标题、目录或链接") {
		t.Fatal("prompt should tell the model not to infer from title-only matches")
	}
	if !strings.Contains(content, "什么是RAG") {
		t.Fatal("question should be in user message")
	}
	if !strings.Contains(req.Messages[0].Content, "[N](#cite-N)") {
		t.Fatal("system prompt should include citation rules for kb context")
	}
}

func TestDefaultPromptBuilder_WrapsEvidenceAndQuestionLikeJavaPromptService(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:   "查询订单状态",
		KbContext:  `<content source="订单手册">订单助手支持订单状态查询。</content>`,
		McpContext: "<data>\n工具：order_status\n订单 123 当前状态为已发货\n</data>",
	})

	content := req.Messages[1].Content
	for _, want := range []string{
		"<tool-data>\n<data>\n工具：order_status\n订单 123 当前状态为已发货\n</data>\n</tool-data>",
		"<documents>\n<content source=\"订单手册\">订单助手支持订单状态查询。</content>\n</documents>",
		"<question>查询订单状态</question>",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, content)
		}
	}
}

func TestDefaultPromptBuilder_WrapsMultipleSubQuestionsLikeJavaPromptService(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:     "会员和积分怎么查？",
		SubQuestions: []string{"会员等级怎么查？", "积分明细怎么查？"},
		KbContext:    `<content source="会员手册">会员与积分说明</content>`,
	})

	content := req.Messages[1].Content
	if !strings.Contains(content, "<questions>\n1. 会员等级怎么查？\n2. 积分明细怎么查？\n</questions>") {
		t.Fatalf("expected prompt to contain wrapped sub questions, got %q", content)
	}
	if strings.Contains(content, "<question>会员和积分怎么查？</question>") {
		t.Fatalf("expected multi-question prompt to use sub questions instead of single original question, got %q", content)
	}
}

func TestDefaultPromptBuilder_WithoutEvidenceKeepsOriginalQuestion(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:     "会员和积分怎么查？",
		SubQuestions: []string{"会员等级怎么查？", "积分明细怎么查？"},
	})

	content := req.Messages[1].Content
	if content != "会员和积分怎么查？" {
		t.Fatalf("expected prompt without evidence to keep original question, got %q", content)
	}
}

func TestDefaultPromptBuilder_WithMcpContext(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:   "查询订单状态",
		KbContext:  "订单助手支持订单状态查询。",
		McpContext: "工具：order_status\n结果：订单 123 当前状态为已发货。",
	})

	content := req.Messages[1].Content
	if !strings.Contains(content, "MCP工具结果") {
		t.Fatal("prompt should include MCP context section")
	}
	if !strings.Contains(content, "订单 123 当前状态为已发货") {
		t.Fatal("mcp context should be in user message")
	}
	if !strings.Contains(content, "查询订单状态") {
		t.Fatal("question should be in user message")
	}
}

func TestDefaultPromptBuilder_SelectsScenePromptSlots(t *testing.T) {
	resolver := &stubRuntimePromptResolver{prompts: map[string]string{
		"KB_ANSWER":    "kb scene prompt",
		"MCP_ANSWER":   "mcp scene prompt",
		"MIXED_ANSWER": "mixed scene prompt",
		"SYSTEM_CHAT":  "fallback prompt",
	}}

	tests := []struct {
		name       string
		ctx        PromptContext
		wantSystem string
	}{
		{
			name: "kb only",
			ctx: PromptContext{
				Question:  "什么是RAG",
				KbContext: "RAG是检索增强生成技术。",
			},
			wantSystem: "kb scene prompt",
		},
		{
			name: "mcp only",
			ctx: PromptContext{
				Question:   "查订单状态",
				McpContext: "工具：order_status",
			},
			wantSystem: "mcp scene prompt",
		},
		{
			name: "mixed",
			ctx: PromptContext{
				Question:   "查订单并结合知识库",
				KbContext:  "订单手册",
				McpContext: "工具：order_status",
			},
			wantSystem: "mixed scene prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewDefaultPromptBuilder(resolver)
			req := b.Build(tt.ctx)
			if !strings.Contains(req.Messages[0].Content, tt.wantSystem) {
				t.Fatalf("unexpected system prompt: %q", req.Messages[0].Content)
			}
		})
	}
}

func TestDefaultPromptBuilder_UsesAgentMainPromptInAgentMode(t *testing.T) {
	resolver := &stubRuntimePromptResolver{prompts: map[string]string{
		"AGENT_MAIN":  "agent main prompt",
		"SYSTEM_CHAT": "fallback prompt",
	}}
	b := NewDefaultPromptBuilder(resolver)
	b.SetEngineMode("agent")

	req := b.Build(PromptContext{
		Question:   "查询订单状态",
		KbContext:  "订单手册支持订单状态查询。",
		McpContext: "工具：order_status",
	})

	if !strings.Contains(req.Messages[0].Content, "agent main prompt") {
		t.Fatalf("expected agent mode to use AGENT_MAIN prompt, got %q", req.Messages[0].Content)
	}
	if strings.Contains(req.Messages[0].Content, "fallback prompt") {
		t.Fatalf("expected agent mode not to fall back to SYSTEM_CHAT when AGENT_MAIN exists, got %q", req.Messages[0].Content)
	}
}

func TestDefaultPromptBuilder_WithMcpOnlyContextUsesToolDataWithoutDocuments(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:   "查询天气",
		McpContext: "<data>\n工具：weather_query\n北京 今日晴\n</data>",
	})

	content := req.Messages[1].Content
	if !strings.Contains(content, "<tool-data>\n<data>\n工具：weather_query\n北京 今日晴\n</data>\n</tool-data>") {
		t.Fatalf("expected mcp-only prompt to include tool-data evidence, got %q", content)
	}
	if strings.Contains(content, "<documents>") {
		t.Fatalf("expected mcp-only prompt not to include documents evidence, got %q", content)
	}
	if !strings.Contains(content, "<question>查询天气</question>") {
		t.Fatalf("expected mcp-only prompt to wrap question, got %q", content)
	}
	if strings.Contains(req.Messages[0].Content, "[N](#cite-N)") {
		t.Fatalf("expected mcp-only system prompt not to include citation rules, got %q", req.Messages[0].Content)
	}
}

func TestDefaultPromptBuilder_WithHistory(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question: "继续",
		History: []chat.Message{
			chat.NewUserMessage("上一条用户消息"),
			chat.NewAssistantMessage("上一条助手消息"),
		},
	})

	if len(req.Messages) != 4 {
		t.Fatalf("expected 4 messages (system + 2 history + user), got %d", len(req.Messages))
	}
	if req.Messages[1].Role != chat.RoleUser {
		t.Fatal("history[0] should be user")
	}
	if req.Messages[2].Role != chat.RoleAssistant {
		t.Fatal("history[1] should be assistant")
	}
}

func TestDefaultPromptBuilder_CustomSystemPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	sysFile := filepath.Join(tmpDir, "custom_system.txt")
	os.WriteFile(sysFile, []byte("你是一个专业的客服助手。"), 0o644)

	b := NewPromptBuilder(tmpDir, "custom_system.txt")
	req := b.Build(PromptContext{Question: "退款"})

	if req.Messages[0].Content != "你是一个专业的客服助手。" {
		t.Fatalf("unexpected system prompt: %s", req.Messages[0].Content)
	}
}

// TestDeriveIntentAttributionByCollection 验证按库推导意图归属：
// chunk 的 collection 属于某命中意图的绑定库即归属该意图，多归属与无归属均正确。
// 对齐 Java KnowledgeRetrievalResult.deriveAttribution。
func TestDeriveIntentAttributionByCollection(t *testing.T) {
	kbIntents := []NodeScore{
		{Node: IntentNode{ID: "intent-ins", Kind: IntentKindKB, CollectionNames: []string{"insurance"}}, Score: 0.9},
		{Node: IntentNode{ID: "intent-claims", Kind: IntentKindKB, CollectionNames: []string{"claims", "insurance"}}, Score: 0.8},
		{Node: IntentNode{ID: "intent-oa", Kind: IntentKindKB, CollectionName: "oa"}, Score: 0.7},
	}
	chunks := []RetrievedChunk{
		{ID: "c1", Metadata: map[string]string{"collection_name": "insurance"}},
		{ID: "c2", Metadata: map[string]string{"collection_name": "oa"}},
		{ID: "c3", Metadata: map[string]string{"collection_name": "unknown-lib"}},
	}

	got := DeriveIntentAttribution(chunks, kbIntents)
	// insurance 库被两个意图绑定：确定性多归属
	for _, id := range []string{"intent-ins", "intent-claims", "intent-oa"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("expected intent %q to be attributed, got %v", id, got)
		}
	}
	// 未命中任何绑定库的 chunk 不产生归属
	if _, ok := got["intent-other"]; ok {
		t.Fatalf("unexpected attribution: %v", got)
	}

	// 无 chunk 时整体无归属
	empty := DeriveIntentAttribution(nil, kbIntents)
	if len(empty) != 0 {
		t.Fatalf("expected empty attribution, got %v", empty)
	}
}

// TestSingleKbIntentPromptTemplateUsesAttribution 验证只有一个有证据归属的 KB 意图时
// 使用该意图的 PromptTemplate 作为系统提示词。对齐 Java planPrompt 的 eligible 过滤。
func TestSingleKbIntentPromptTemplateUsesAttribution(t *testing.T) {
	builder := NewDefaultPromptBuilder()
	ctx := PromptContext{
		Question:  "问题",
		KbContext: "证据内容",
		KbIntents: []NodeScore{
			{Node: IntentNode{ID: "intent-hit", Kind: IntentKindKB, CollectionName: "kb-a", PromptTemplate: "你是理赔专员，只回答理赔相关问题。"}, Score: 0.9},
			{Node: IntentNode{ID: "intent-miss", Kind: IntentKindKB, CollectionName: "kb-b", PromptTemplate: "你是 OA 助手。"}, Score: 0.8},
		},
		// 只有 intent-hit 有证据归属（检索未命中 kb-b）
		EligibleIntentIds: map[string]struct{}{"intent-hit": {}},
	}

	req := builder.Build(ctx)
	if len(req.Messages) == 0 || req.Messages[0].Role != chat.RoleSystem {
		t.Fatalf("expected system message, got %+v", req.Messages)
	}
	// KB 场景会追加引用规则，系统提示词应以意图模板开头
	if !strings.HasPrefix(req.Messages[0].Content, "你是理赔专员，只回答理赔相关问题。") {
		t.Fatalf("expected attributed intent template at head, got %q", req.Messages[0].Content)
	}
	if strings.Contains(req.Messages[0].Content, "OA 助手") {
		t.Fatalf("unattributed intent template must not be used, got %q", req.Messages[0].Content)
	}
}

// TestSingleKbIntentPromptTemplateMultipleEligibleFallsBack 验证多个意图都有归属时
// 不使用任何意图模板，走默认槽位。
func TestSingleKbIntentPromptTemplateMultipleEligibleFallsBack(t *testing.T) {
	builder := NewDefaultPromptBuilder()
	ctx := PromptContext{
		Question:  "问题",
		KbContext: "证据内容",
		KbIntents: []NodeScore{
			{Node: IntentNode{ID: "intent-1", Kind: IntentKindKB, PromptTemplate: "模板一"}, Score: 0.9},
			{Node: IntentNode{ID: "intent-2", Kind: IntentKindKB, PromptTemplate: "模板二"}, Score: 0.8},
		},
		EligibleIntentIds: map[string]struct{}{"intent-1": {}, "intent-2": {}},
	}

	req := builder.Build(ctx)
	if len(req.Messages) == 0 || req.Messages[0].Content == "模板一" || req.Messages[0].Content == "模板二" {
		t.Fatalf("multiple eligible intents should not use intent template, got %q", req.Messages[0].Content)
	}
}
