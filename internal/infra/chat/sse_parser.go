package chat

import (
	"encoding/json"
	"strings"
)

const (
	sseDataPrefix = "data:"
	sseDoneMarker = "[DONE]"
)

// SSEParsedEvent represents a parsed SSE event line.
type SSEParsedEvent struct {
	content   string
	reasoning string
	completed bool
}

// HasContent reports whether content was extracted.
func (e SSEParsedEvent) HasContent() bool { return e.content != "" }

// HasReasoning reports whether reasoning content was extracted.
func (e SSEParsedEvent) HasReasoning() bool { return e.reasoning != "" }

// Completed reports whether this event signals stream completion.
func (e SSEParsedEvent) Completed() bool { return e.completed }

// Content returns the extracted content.
func (e SSEParsedEvent) Content() string { return e.content }

// Reasoning returns the extracted reasoning content.
func (e SSEParsedEvent) Reasoning() string { return e.reasoning }

// ParseSSELine parses an SSE event line.
// Aligns with Java OpenAIStyleSseParser.
func ParseSSELine(line string, reasoningEnabled bool) SSEParsedEvent {
	line = strings.TrimSpace(line)
	if line == "" {
		return SSEParsedEvent{}
	}

	payload := line
	if strings.HasPrefix(payload, sseDataPrefix) {
		payload = strings.TrimSpace(strings.TrimPrefix(payload, sseDataPrefix))
	}
	if strings.EqualFold(payload, sseDoneMarker) {
		return SSEParsedEvent{completed: true}
	}

	var obj ssePayload
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return SSEParsedEvent{}
	}

	if len(obj.Choices) == 0 {
		return SSEParsedEvent{}
	}

	choice := obj.Choices[0]
	content := extractDeltaField(choice, "content")
	reasoning := ""
	if reasoningEnabled {
		reasoning = extractDeltaField(choice, "reasoning_content")
	}
	completed := choice.FinishReason != nil && *choice.FinishReason != ""

	return SSEParsedEvent{
		content:   content,
		reasoning: reasoning,
		completed: completed,
	}
}

// extractDeltaField extracts a field from delta or message in a choice.
func extractDeltaField(choice sseChoice, field string) string {
	if choice.Delta != nil {
		if v, ok := choice.Delta[field]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	if choice.Message != nil {
		if v, ok := choice.Message[field]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

type ssePayload struct {
	Choices []sseChoice `json:"choices"`
}

type sseChoice struct {
	Delta        map[string]interface{} `json:"delta"`
	Message      map[string]interface{} `json:"message"`
	FinishReason *string                `json:"finish_reason"`
}
