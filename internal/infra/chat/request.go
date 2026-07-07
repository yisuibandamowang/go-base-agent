package chat

// Role represents the role of a chat message.
// Aligns with Java ChatMessage.Role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message represents a single chat message.
// Aligns with Java ChatMessage.
type Message struct {
	Role             Role   `json:"role"`
	Content          string `json:"content"`
	ThinkingContent  string `json:"thinkingContent,omitempty"`
	ThinkingDuration int    `json:"thinkingDuration,omitempty"`
}

// NewSystemMessage creates a system message.
func NewSystemMessage(content string) Message {
	return Message{Role: RoleSystem, Content: content}
}

// NewUserMessage creates a user message.
func NewUserMessage(content string) Message {
	return Message{Role: RoleUser, Content: content}
}

// NewAssistantMessage creates an assistant message.
func NewAssistantMessage(content string) Message {
	return Message{Role: RoleAssistant, Content: content}
}

// Request represents a complete chat request.
// Aligns with Java ChatRequest.
type Request struct {
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"topP,omitempty"`
	TopK        *int      `json:"topK,omitempty"`
	MaxTokens   *int      `json:"maxTokens,omitempty"`
	Thinking    *bool     `json:"thinking,omitempty"`
}

// SimpleRequest creates a Request with a single user message.
func SimpleRequest(prompt string) Request {
	return Request{
		Messages: []Message{NewUserMessage(prompt)},
	}
}
