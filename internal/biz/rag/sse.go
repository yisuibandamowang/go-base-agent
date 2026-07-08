package rag

import (
	"encoding/json"

	"go-base-agent/internal/framework/sse"
)

// SSE event names, byte-level aligned with Java SSEEventType.
const (
	EventMeta    = "meta"
	EventMessage = "message"
	EventFinish  = "finish"
	EventDone    = "done"
	EventCancel  = "cancel"
	EventReject  = "reject"
)

// MetaPayload is sent on stream start with session identifiers.
// Aligns with Java MetaPayload(conversationId, taskId).
type MetaPayload struct {
	ConversationID string `json:"conversationId"`
	TaskID         string `json:"taskId"`
}

// MessageDelta carries an incremental content chunk.
// Aligns with Java MessageDelta(type, delta).
type MessageDelta struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

// MessageType constants for MessageDelta.Type.
const (
	MsgTypeThink    = "think"
	MsgTypeResponse = "response"
)

// CompletionPayload is sent when the model finishes generating.
// Aligns with Java CompletionPayload(messageId, title) — title is omitempty.
type CompletionPayload struct {
	MessageID string `json:"messageId,omitempty"`
	Title     string `json:"title,omitempty"`
}

// DonePayload is the final "[DONE]" marker.
const DonePayload = "[DONE]"

// SSESender wraps framework/sse.Sender with RAG event helpers.
type SSESender struct {
	inner *sse.Sender
}

// NewSSESender creates a new RAG SSE sender.
func NewSSESender(s *sse.Sender) *SSESender {
	return &SSESender{inner: s}
}

func (s *SSESender) sendEvent(event string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.inner.Send(event, string(data))
}

// SendMeta sends the meta event with conversation and task IDs.
func (s *SSESender) SendMeta(conversationID, taskID string) error {
	return s.sendEvent(EventMeta, MetaPayload{
		ConversationID: conversationID,
		TaskID:         taskID,
	})
}

// SendMessage sends an incremental message delta.
func (s *SSESender) SendMessage(msgType, delta string) error {
	return s.sendEvent(EventMessage, MessageDelta{
		Type:  msgType,
		Delta: delta,
	})
}

// SendFinish sends the completion event.
func (s *SSESender) SendFinish(messageID, title string) error {
	return s.sendEvent(EventFinish, CompletionPayload{
		MessageID: messageID,
		Title:     title,
	})
}

// SendDone sends the final done marker.
func (s *SSESender) SendDone() error {
	return s.inner.Send(EventDone, DonePayload)
}

// SendReject sends a rate-limit reject message.
func (s *SSESender) SendReject() error {
	return s.sendEvent(EventReject, MessageDelta{
		Type:  MsgTypeResponse,
		Delta: "系统繁忙，请稍后再试",
	})
}

// SendCancel sends a cancellation event.
func (s *SSESender) SendCancel(messageID, title string) error {
	return s.sendEvent(EventCancel, CompletionPayload{
		MessageID: messageID,
		Title:     title,
	})
}

// Close closes the underlying SSE connection.
func (s *SSESender) Close() {
	s.inner.Close()
}
