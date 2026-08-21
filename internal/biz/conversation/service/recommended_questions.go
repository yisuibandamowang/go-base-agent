package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	conversationModel "go-base-agent/internal/biz/conversation/model"
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
	Generate(ctx context.Context, question, answer string, chunks []rag.GroundingChunk) RecommendedQuestionsPayload
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
func (g *LLMRecommendedQuestionGenerator) Generate(ctx context.Context, question, answer string, chunks []rag.GroundingChunk) RecommendedQuestionsPayload {
	question = strings.TrimSpace(question)
	answer = strings.TrimSpace(stripRecommendationCitations(answer))
	answer = strings.TrimSpace(rag.StripInlineCitations(answer))
	if question == "" || answer == "" {
		return RecommendedQuestionsEmpty()
	}
	if g == nil || g.llm == nil || g.loader == nil {
		return RecommendedQuestionsFailed()
	}

	prompt, err := g.renderPrompt(question, answer, chunks)
	if err != nil {
		slog.Warn("render recommended questions prompt failed", "err", err)
		return RecommendedQuestionsFailed()
	}

	temperature := 0.7
	topP := 0.8
	thinking := false
	raw, err := chat.ChatWithTier(ctx, g.llm, chat.Request{
		Messages:    []chat.Message{chat.NewUserMessage(prompt)},
		Temperature: &temperature,
		TopP:        &topP,
		Thinking:    &thinking,
		MaxTokens:   intPtr(256),
	}, "fast")
	if err != nil {
		slog.Warn("recommended questions llm failed", "err", err)
		return RecommendedQuestionsFailed()
	}
	return parseRecommendedQuestions(raw, g.maxCount)
}

func (g *LLMRecommendedQuestionGenerator) renderPrompt(question, answer string, chunks []rag.GroundingChunk) (string, error) {
	data := map[string]any{
		"Question": truncateRecommendedInput(question, 1000),
		"Answer":   truncateRecommendedInput(answer, 6000),
		"Count":    g.maxCount,
		"Chunks":   buildRecommendedGroundingText(chunks),
	}
	if g.prompt != nil {
		if rendered, err := g.prompt.Render("RECOMMENDED_QUESTIONS", data); err == nil && strings.TrimSpace(rendered) != "" {
			return normalizeRecommendedQuestionPrompt(rendered, question, answer, g.maxCount, chunks), nil
		}
	}
	rendered, err := g.loader.Render(recommendedQuestionPromptFile, data)
	if err != nil {
		return "", fmt.Errorf("render recommended questions prompt: %w", err)
	}
	return normalizeRecommendedQuestionPrompt(rendered, question, answer, g.maxCount, chunks), nil
}

func normalizeRecommendedQuestionPrompt(prompt, question, answer string, count int, chunks []rag.GroundingChunk) string {
	replacer := strings.NewReplacer(
		"{question}", question,
		"{answer}", answer,
		"{count}", fmt.Sprintf("%d", count),
		"{chunks}", buildRecommendedGroundingText(chunks),
	)
	return replacer.Replace(prompt)
}

func truncateRecommendedInput(value string, max int) string {
	runes := []rune(value)
	if len(runes) > max {
		return string(runes[:max])
	}
	return value
}

func buildRecommendedGroundingText(chunks []rag.GroundingChunk) string {
	if len(chunks) == 0 {
		return "（无检索片段，仅依据问答生成）"
	}
	const maxChars = 6000
	var builder strings.Builder
	for index, chunk := range chunks {
		text := strings.TrimSpace(chunk.Text)
		if text == "" || builder.Len() >= maxChars {
			continue
		}
		prefix := fmt.Sprintf("%d. 【%s】", index+1, strings.TrimSpace(chunk.DocName))
		remaining := maxChars - builder.Len() - len([]rune(prefix)) - 1
		if remaining <= 0 {
			break
		}
		text = truncateRecommendedInput(text, remaining)
		builder.WriteString(prefix)
		builder.WriteString(text)
		builder.WriteByte('\n')
	}
	if builder.Len() == 0 {
		return "（无检索片段，仅依据问答生成）"
	}
	return strings.TrimSpace(builder.String())
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
	if cached, ok := parseCachedRecommendedQuestions(msg.RecommendedQuestions); ok {
		return RecommendedQuestionsSuccess(cached), nil
	}
	if status := strings.TrimSpace(msg.MessageStatus); status != "" && !strings.EqualFold(status, string(chat.MessageStatusNormal)) {
		return RecommendedQuestionsEmpty(), nil
	}
	if s.recommendedQuestions == nil {
		return RecommendedQuestionsEmpty(), nil
	}
	question, err := s.loadQuestionForAssistant(ctx, msg, userID)
	if err != nil {
		return RecommendedQuestionsFailed(), err
	}
	if strings.TrimSpace(question) == "" {
		return RecommendedQuestionsEmpty(), nil
	}
	generated := s.recommendedQuestions.Generate(ctx, question, msg.Content, rag.ParseGroundingChunks(msg.RetrievedChunks))
	if generated.Status == RecommendedQuestionsStatusFailed {
		return generated, nil
	}
	if err := s.msgRepo.UpdateRecommendedQuestions(ctx, msg.ID, generated.Questions); err != nil {
		return RecommendedQuestionsFailed(), err
	}
	return generated, nil
}

func (s *ConversationService) loadQuestionForAssistant(ctx context.Context, assistant *conversationModel.Message, userID string) (string, error) {
	if strings.TrimSpace(assistant.ReplyToMessageID) != "" {
		question, err := s.msgRepo.FindByIDAndUserID(ctx, assistant.ReplyToMessageID, userID)
		if err != nil || question == nil || !strings.EqualFold(question.Role, string(chat.RoleUser)) || question.ConversationID != assistant.ConversationID {
			return "", nil
		}
		return question.Content, nil
	}
	return s.loadQuestionBeforeAssistantMessage(ctx, assistant.ConversationID, userID, assistant.ID)
}

func parseCachedRecommendedQuestions(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var questions []string
	if err := json.Unmarshal([]byte(raw), &questions); err != nil || questions == nil {
		return nil, false
	}
	return questions, true
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
