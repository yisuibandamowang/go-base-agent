package chat

// Role represents the role of a chat message.
// Aligns with Java ChatMessage.Role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// MessageStatus describes how an assistant message ended.
type MessageStatus string

const (
	MessageStatusNormal      MessageStatus = "NORMAL"
	MessageStatusInterrupted MessageStatus = "INTERRUPTED"
	MessageStatusRejected    MessageStatus = "REJECTED"
)

// Message represents a single chat message.
// Aligns with Java ChatMessage.
type Message struct {
	Role             Role   `json:"role"`
	Content          string `json:"content"`
	ThinkingContent  string `json:"thinkingContent,omitempty"`
	ThinkingDuration int    `json:"thinkingDuration,omitempty"`
	// Sources 回答来源（JSON 序列化的文档级来源列表），仅随助手消息落库与回显，不参与模型请求。
	Sources string `json:"sources,omitempty"`
	// RetrievedChunks 推荐追问使用的 grounding 片段 JSON，仅随助手消息落库。
	RetrievedChunks string `json:"retrievedChunks,omitempty"`
	// RecommendedQuestions 推荐追问结果 JSON，仅随助手消息落库。
	RecommendedQuestions string `json:"recommendedQuestions,omitempty"`
	// ReplyToMessageID 是 assistant 消息对应的用户消息 ID。
	ReplyToMessageID string `json:"replyToMessageId,omitempty"`
	// MessageStatus 是消息结束状态，仅落库消息使用。
	MessageStatus MessageStatus `json:"messageStatus,omitempty"`
}

// GroundingChunk 是推荐追问使用的文档片段证据。
type GroundingChunk struct {
	DocName string `json:"docName"`
	Text    string `json:"text"`
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
