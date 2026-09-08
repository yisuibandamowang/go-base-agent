package service

import (
	"context"
	"strings"
	"testing"

	"go-base-agent/internal/infra/chat"
)

type recordingSummaryLLM struct {
	request chat.Request
	output  string
	calls   int
}

func (r *recordingSummaryLLM) Chat(ctx context.Context, req chat.Request) (string, error) {
	r.calls++
	r.request = req
	return r.output, nil
}

func (r *recordingSummaryLLM) ChatWithModel(ctx context.Context, req chat.Request, modelID string) (string, error) {
	return r.Chat(ctx, req)
}

func (r *recordingSummaryLLM) StreamChat(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
	return nil, nil
}

// TestLLMSummaryGenerator_ArrangesMultiTurnMessagesLikeJava 校验摘要 LLM 的多段消息编排与采样参数
//（对齐 Java summarizeMessages：system 提示词 → assistant 历史摘要 → 待压缩对话 → user 合并指令；
//temperature 0.3 / topP 0.9 / 关闭思考）。
func TestLLMSummaryGenerator_ArrangesMultiTurnMessagesLikeJava(t *testing.T) {
	llm := &recordingSummaryLLM{output: "用户咨询了年假计算规则（已解答）。关键词：人事政策"}
	gen := NewLLMSummaryGenerator(llm, "")

	history := []chat.Message{
		chat.NewUserMessage("请问年假怎么算？"),
		{Role: chat.RoleAssistant, Content: "根据公司规定，入职满1年可享受5天年假。"},
	}
	summary, err := gen.Generate(context.Background(), history, "用户咨询过报销流程。", 200)
	if err != nil {
		t.Fatalf("generate summary: %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("expected one llm call, got %d", llm.calls)
	}

	msgs := llm.request.Messages
	if len(msgs) != 5 {
		t.Fatalf("expected 5 arranged messages (system+assistant summary+2 history+user merge), got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != chat.RoleSystem || !strings.Contains(msgs[0].Content, "话题索引") {
		t.Fatalf("expected system summary prompt first, got %+v", msgs[0])
	}
	if msgs[1].Role != chat.RoleAssistant || !strings.Contains(msgs[1].Content, "历史摘要") || !strings.Contains(msgs[1].Content, "报销流程") {
		t.Fatalf("expected assistant-carried previous summary, got %+v", msgs[1])
	}
	if msgs[2].Content != "请问年假怎么算？" || msgs[3].Content != "根据公司规定，入职满1年可享受5天年假。" {
		t.Fatalf("expected history messages in order, got %+v", msgs[2:4])
	}
	if msgs[4].Role != chat.RoleUser || !strings.Contains(msgs[4].Content, "合并以上对话与历史摘要") || !strings.Contains(msgs[4].Content, "200") {
		t.Fatalf("expected user merge instruction with max chars, got %+v", msgs[4])
	}

	if llm.request.Temperature == nil || *llm.request.Temperature != 0.3 {
		t.Fatalf("expected temperature 0.3, got %+v", llm.request.Temperature)
	}
	if llm.request.TopP == nil || *llm.request.TopP != 0.9 {
		t.Fatalf("expected topP 0.9, got %+v", llm.request.TopP)
	}
	if llm.request.Thinking == nil || *llm.request.Thinking {
		t.Fatalf("expected thinking disabled, got %+v", llm.request.Thinking)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

// TestLLMSummaryGenerator_OmitsSummarySlotWhenEmpty 无历史摘要时不注入 assistant 段。
func TestLLMSummaryGenerator_OmitsSummarySlotWhenEmpty(t *testing.T) {
	llm := &recordingSummaryLLM{output: "用户咨询了年假计算规则（已解答）。"}
	gen := NewLLMSummaryGenerator(llm, "")

	if _, err := gen.Generate(context.Background(), []chat.Message{chat.NewUserMessage("年假怎么算？")}, "", 200); err != nil {
		t.Fatalf("generate summary: %v", err)
	}
	msgs := llm.request.Messages
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages without previous summary slot, got %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != chat.RoleUser {
		t.Fatalf("expected history right after system prompt, got %+v", msgs[1])
	}
}
