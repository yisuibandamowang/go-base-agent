package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/chat"
)

const recommendedQuestionPromptFile = "conversation_recommended_questions.txt"

// RecommendedQuestionsStatus describes the generation result state.
type RecommendedQuestionsStatus string

const (
	RecommendedQuestionsStatusSuccess RecommendedQuestionsStatus = "SUCCESS"
	RecommendedQuestionsStatusEmpty   RecommendedQuestionsStatus = "EMPTY"
	RecommendedQuestionsStatusFailed  RecommendedQuestionsStatus = "FAILED"
)

// RecommendedQuestionsPayload is the response returned by the recommended question endpoint.
type RecommendedQuestionsPayload struct {
	Status    RecommendedQuestionsStatus `json:"status"`
	Questions []string                   `json:"questions"`
}

// RecommendedQuestionsSuccess creates a successful payload.
func RecommendedQuestionsSuccess(questions []string) RecommendedQuestionsPayload {
	if len(questions) == 0 {
		return RecommendedQuestionsEmpty()
	}
	return RecommendedQuestionsPayload{Status: RecommendedQuestionsStatusSuccess, Questions: append([]string(nil), questions...)}
}

// RecommendedQuestionsEmpty creates an empty payload.
func RecommendedQuestionsEmpty() RecommendedQuestionsPayload {
	return RecommendedQuestionsPayload{Status: RecommendedQuestionsStatusEmpty, Questions: []string{}}
}

// RecommendedQuestionsFailed creates a failed payload.
func RecommendedQuestionsFailed() RecommendedQuestionsPayload {
	return RecommendedQuestionsPayload{Status: RecommendedQuestionsStatusFailed, Questions: []string{}}
}

type recommendedQuestionGenerator interface {
	Generate(ctx context.Context, question, answer string) RecommendedQuestionsPayload
}

// LLMRecommendedQuestionGenerator generates follow-up questions with an LLM.
type LLMRecommendedQuestionGenerator struct {
	llm      chat.LLMService
	loader   *rag.PromptLoader
	prompt   rag.RuntimePromptResolver
	maxCount int
}

// NewLLMRecommendedQuestionGenerator creates a recommended question generator.
func NewLLMRecommendedQuestionGenerator(llm chat.LLMService, externalPromptDir string, promptResolver ...rag.RuntimePromptResolver) *LLMRecommendedQuestionGenerator {
	var resolver rag.RuntimePromptResolver
	if len(promptResolver) > 0 {
		resolver = promptResolver[0]
	}
	return &LLMRecommendedQuestionGenerator{
		llm:      llm,
		loader:   rag.NewPromptLoader(externalPromptDir),
		prompt:   resolver,
		maxCount: 3,
	}
}

// Generate returns up to three recommended questions for the given answer.
func (g *LLMRecommendedQuestionGenerator) Generate(ctx context.Context, question, answer string) RecommendedQuestionsPayload {
	question = strings.TrimSpace(question)
	answer = strings.TrimSpace(stripRecommendationCitations(answer))
	if question == "" || answer == "" {
		return RecommendedQuestionsEmpty()
	}
	if g == nil || g.llm == nil || g.loader == nil {
		return RecommendedQuestionsFailed()
	}

	prompt, err := g.renderPrompt(question, answer)
	if err != nil {
		slog.Warn("render recommended questions prompt failed", "err", err)
		return RecommendedQuestionsFailed()
	}

	temperature := 0.7
	topP := 0.8
	thinking := false
	raw, err := g.llm.Chat(ctx, chat.Request{
		Messages:    []chat.Message{chat.NewUserMessage(prompt)},
		Temperature: &temperature,
		TopP:        &topP,
		Thinking:    &thinking,
		MaxTokens:   intPtr(256),
	})
	if err != nil {
		slog.Warn("recommended questions llm failed", "err", err)
		return RecommendedQuestionsFailed()
	}
	return parseRecommendedQuestions(raw, g.maxCount)
}

func (g *LLMRecommendedQuestionGenerator) renderPrompt(question, answer string) (string, error) {
	data := map[string]any{
		"Question": question,
		"Answer":   answer,
		"Count":    g.maxCount,
		"Chunks":   "（无检索片段，仅依据问答生成）",
	}
	if g.prompt != nil {
		if rendered, err := g.prompt.Render("RECOMMENDED_QUESTIONS", data); err == nil && strings.TrimSpace(rendered) != "" {
			return normalizeRecommendedQuestionPrompt(rendered, question, answer, g.maxCount), nil
		}
	}
	rendered, err := g.loader.Render(recommendedQuestionPromptFile, data)
	if err != nil {
		return "", fmt.Errorf("render recommended questions prompt: %w", err)
	}
	return normalizeRecommendedQuestionPrompt(rendered, question, answer, g.maxCount), nil
}

func normalizeRecommendedQuestionPrompt(prompt, question, answer string, count int) string {
	replacer := strings.NewReplacer(
		"{question}", question,
		"{answer}", answer,
		"{count}", fmt.Sprintf("%d", count),
		"{chunks}", "（无检索片段，仅依据问答生成）",
	)
	return replacer.Replace(prompt)
}

func parseRecommendedQuestions(raw string, maxCount int) RecommendedQuestionsPayload {
	raw = strings.TrimSpace(stripCodeFence(raw))
	if raw == "" {
		return RecommendedQuestionsFailed()
	}

	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return RecommendedQuestionsFailed()
	}

	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		runes := []rune(item)
		if len(runes) > 200 {
			item = string(runes[:200])
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
		if maxCount > 0 && len(out) >= maxCount {
			break
		}
	}
	if len(out) == 0 {
		return RecommendedQuestionsEmpty()
	}
	return RecommendedQuestionsSuccess(out)
}

func stripCodeFence(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return ""
	}
	lines = lines[1:]
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func stripRecommendationCitations(answer string) string {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return ""
	}
	if idx := strings.Index(answer, "\n\n依据："); idx >= 0 {
		return strings.TrimSpace(answer[:idx])
	}
	if idx := strings.Index(answer, "\n依据："); idx >= 0 {
		return strings.TrimSpace(answer[:idx])
	}
	return answer
}

func intPtr(v int) *int {
	return &v
}

// GenerateRecommendedQuestions finds the source Q&A pair and returns follow-up questions.
func (s *ConversationService) GenerateRecommendedQuestions(ctx context.Context, messageID, userID string) (RecommendedQuestionsPayload, error) {
	if s == nil || s.msgRepo == nil {
		return RecommendedQuestionsFailed(), fmt.Errorf("message repo is nil")
	}
	msg, err := s.msgRepo.FindByIDAndUserID(ctx, messageID, userID)
	if err != nil {
		return RecommendedQuestionsFailed(), fmt.Errorf("消息不存在: %w", err)
	}
	if !strings.EqualFold(msg.Role, string(chat.RoleAssistant)) {
		return RecommendedQuestionsFailed(), fmt.Errorf("仅支持对助手消息生成推荐追问")
	}
	if s.recommendedQuestions == nil {
		return RecommendedQuestionsEmpty(), nil
	}
	question, err := s.loadQuestionBeforeAssistantMessage(ctx, msg.ConversationID, userID, msg.ID)
	if err != nil {
		return RecommendedQuestionsFailed(), err
	}
	if strings.TrimSpace(question) == "" {
		return RecommendedQuestionsEmpty(), nil
	}
	return s.recommendedQuestions.Generate(ctx, question, msg.Content), nil
}

func (s *ConversationService) loadQuestionBeforeAssistantMessage(ctx context.Context, conversationID, userID, assistantMessageID string) (string, error) {
	if s.msgRepo == nil {
		return "", fmt.Errorf("message repo is nil")
	}
	msgs, err := s.msgRepo.LoadHistory(ctx, conversationID, userID, 0)
	if err != nil {
		return "", err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].ID != assistantMessageID {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if strings.EqualFold(msgs[j].Role, string(chat.RoleUser)) {
				return msgs[j].Content, nil
			}
		}
		break
	}
	return "", nil
}
