package rag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-base-agent/internal/infra/chat"
)

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
