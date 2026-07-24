package service

import (
	"context"
	"encoding/json"
	"fmt"

	"go-base-agent/internal/framework/mq"
)

const (
	// MessageFeedbackTopic 是消息反馈事件主题。
	MessageFeedbackTopic = "message-feedback_topic"
	// MessageFeedbackConsumerGroup 是消息反馈事件消费组。
	MessageFeedbackConsumerGroup = "message-feedback_cg"
)

// MessageFeedbackEvent 对齐 Java MessageFeedbackEvent。
type MessageFeedbackEvent struct {
	MessageID  string `json:"messageId"`
	UserID     string `json:"userId"`
	Vote       int16  `json:"vote,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Comment    string `json:"comment,omitempty"`
	Cancelled  bool   `json:"cancelled"`
	SubmitTime int64  `json:"submitTime"`
}

// RegisterMessageFeedbackConsumer 注册消息反馈消费者。
func RegisterMessageFeedbackConsumer(consumer mq.Consumer, svc *ConversationService) error {
	if consumer == nil {
		return fmt.Errorf("mq consumer is nil")
	}
	if svc == nil {
		return fmt.Errorf("conversation service is nil")
	}
	return consumer.Subscribe(MessageFeedbackTopic, MessageFeedbackConsumerGroup, func(ctx context.Context, msg mq.Message) error {
		var event MessageFeedbackEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			return fmt.Errorf("decode message feedback event: %w", err)
		}
		return svc.SubmitFeedbackByEvent(ctx, event)
	})
}
