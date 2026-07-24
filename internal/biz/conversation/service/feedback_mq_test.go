package service

import (
	"context"
	"encoding/json"
	"testing"

	conversationModel "go-base-agent/internal/biz/conversation/model"
	"go-base-agent/internal/biz/conversation/repo"
	"go-base-agent/internal/framework/db"
	"go-base-agent/internal/framework/mq"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type capturingConversationMQProducer struct {
	messages []mq.Message
}

func (p *capturingConversationMQProducer) Send(_ context.Context, msg mq.Message) (*mq.SendResult, error) {
	p.messages = append(p.messages, msg)
	return &mq.SendResult{MsgID: "msg-1", Status: "SEND_OK"}, nil
}

func (p *capturingConversationMQProducer) SendInTransaction(_ context.Context, msg mq.Message, executor mq.TransactionExecutor) (*mq.SendResult, error) {
	p.messages = append(p.messages, msg)
	return &mq.SendResult{MsgID: "msg-1", Status: "SEND_OK"}, nil
}

func (p *capturingConversationMQProducer) RegisterTransactionChecker(string, mq.TransactionChecker) {}

func TestConversationService_CreateFeedbackPublishesMQEventWhenEnabled(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	producer := &capturingConversationMQProducer{}
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	svc.SetFeedbackMQProducer(producer, true)

	if err := svc.CreateFeedback(context.Background(), struct {
		MessageID      string
		ConversationID string
		UserID         string
		Vote           int16
		Reason         string
		Comment        string
	}{
		MessageID: "msg-assistant",
		UserID:    "user-1",
		Vote:      1,
		Reason:    "good",
		Comment:   "useful",
	}); err != nil {
		t.Fatalf("create feedback: %v", err)
	}

	if len(producer.messages) != 1 {
		t.Fatalf("expected one feedback event, got %+v", producer.messages)
	}
	msg := producer.messages[0]
	if msg.Topic != MessageFeedbackTopic || msg.Keys != "user-1:msg-assistant" || msg.BizDesc != "消息反馈" {
		t.Fatalf("unexpected feedback mq message: %+v", msg)
	}
	var event MessageFeedbackEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		t.Fatalf("decode feedback event: %v", err)
	}
	if event.MessageID != "msg-assistant" || event.UserID != "user-1" || event.Vote != 1 || event.Cancelled {
		t.Fatalf("unexpected feedback event: %+v", event)
	}

	var count int64
	if err := gdb.Model(&conversationModel.MessageFeedback{}).Where("message_id = ?", "msg-assistant").Count(&count).Error; err != nil {
		t.Fatalf("count feedback: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected mq path not to persist inline, got %d rows", count)
	}
}

func TestRegisterMessageFeedbackConsumerPersistsEvent(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	consumer := mq.NewNoopConsumer()
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	if err := RegisterMessageFeedbackConsumer(consumer, svc); err != nil {
		t.Fatalf("register feedback consumer: %v", err)
	}
	body, err := json.Marshal(MessageFeedbackEvent{
		MessageID:  "msg-assistant",
		UserID:     "user-1",
		Vote:       -1,
		Reason:     "bad",
		Comment:    "too short",
		SubmitTime: 1795406400000,
	})
	if err != nil {
		t.Fatalf("marshal feedback event: %v", err)
	}
	if err := consumer.DispatchForTest(context.Background(), MessageFeedbackTopic, mq.Message{Body: body}); err != nil {
		t.Fatalf("dispatch feedback event: %v", err)
	}

	var feedback conversationModel.MessageFeedback
	if err := gdb.Where("message_id = ? AND user_id = ?", "msg-assistant", "user-1").First(&feedback).Error; err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	if feedback.ConversationID != "conv-1" || feedback.Vote != -1 || feedback.Reason != "bad" || feedback.Comment != "too short" {
		t.Fatalf("unexpected feedback row: %+v", feedback)
	}
}

func TestConversationService_DeleteFeedbackPublishesCancelEventWhenEnabled(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := gdb.Create(&conversationModel.MessageFeedback{
		MessageID:      "msg-assistant",
		ConversationID: "conv-1",
		UserID:         "user-1",
		Vote:           1,
	}).Error; err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	producer := &capturingConversationMQProducer{}
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	svc.SetFeedbackMQProducer(producer, true)

	if err := svc.DeleteFeedback(context.Background(), "msg-assistant", "user-1"); err != nil {
		t.Fatalf("delete feedback: %v", err)
	}

	if len(producer.messages) != 1 {
		t.Fatalf("expected one cancel event, got %+v", producer.messages)
	}
	msg := producer.messages[0]
	if msg.Topic != MessageFeedbackTopic || msg.Keys != "user-1:msg-assistant" || msg.BizDesc != "取消消息反馈" {
		t.Fatalf("unexpected cancel mq message: %+v", msg)
	}
	var event MessageFeedbackEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		t.Fatalf("decode cancel event: %v", err)
	}
	if event.MessageID != "msg-assistant" || event.UserID != "user-1" || !event.Cancelled {
		t.Fatalf("unexpected cancel event: %+v", event)
	}

	var count int64
	if err := gdb.Model(&conversationModel.MessageFeedback{}).Where("message_id = ?", "msg-assistant").Count(&count).Error; err != nil {
		t.Fatalf("count feedback: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected mq path not to delete inline, got %d rows", count)
	}
}

func TestRegisterMessageFeedbackConsumerCancelsEvent(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := gdb.Create(&conversationModel.MessageFeedback{
		MessageID:      "msg-assistant",
		ConversationID: "conv-1",
		UserID:         "user-1",
		Vote:           1,
	}).Error; err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	consumer := mq.NewNoopConsumer()
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	if err := RegisterMessageFeedbackConsumer(consumer, svc); err != nil {
		t.Fatalf("register feedback consumer: %v", err)
	}
	body, err := json.Marshal(MessageFeedbackEvent{
		MessageID:  "msg-assistant",
		UserID:     "user-1",
		Cancelled:  true,
		SubmitTime: 1795406400000,
	})
	if err != nil {
		t.Fatalf("marshal cancel event: %v", err)
	}
	if err := consumer.DispatchForTest(context.Background(), MessageFeedbackTopic, mq.Message{Body: body}); err != nil {
		t.Fatalf("dispatch cancel event: %v", err)
	}

	var count int64
	if err := gdb.Scopes(db.NotDeletedScope()).Model(&conversationModel.MessageFeedback{}).Where("message_id = ?", "msg-assistant").Count(&count).Error; err != nil {
		t.Fatalf("count active feedback: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected cancel event to soft delete feedback, got %d rows", count)
	}
}

func TestRegisterMessageFeedbackConsumerCreatesTombstoneForCancelWithoutActiveFeedback(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	consumer := mq.NewNoopConsumer()
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	if err := RegisterMessageFeedbackConsumer(consumer, svc); err != nil {
		t.Fatalf("register feedback consumer: %v", err)
	}
	body, err := json.Marshal(MessageFeedbackEvent{
		MessageID:  "msg-assistant",
		UserID:     "user-1",
		Cancelled:  true,
		SubmitTime: 1795406400000,
	})
	if err != nil {
		t.Fatalf("marshal cancel event: %v", err)
	}
	if err := consumer.DispatchForTest(context.Background(), MessageFeedbackTopic, mq.Message{Body: body}); err != nil {
		t.Fatalf("dispatch cancel event: %v", err)
	}

	var feedback conversationModel.MessageFeedback
	if err := gdb.Where("message_id = ? AND user_id = ?", "msg-assistant", "user-1").First(&feedback).Error; err != nil {
		t.Fatalf("load feedback tombstone: %v", err)
	}
	if feedback.ConversationID != "conv-1" || feedback.Vote != 0 || feedback.Reason != "" || feedback.Comment != "" || feedback.Deleted != 1 {
		t.Fatalf("unexpected tombstone row: %+v", feedback)
	}
}

func TestRegisterMessageFeedbackConsumerIgnoresStaleActiveEventAfterNewerCancel(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	consumer := mq.NewNoopConsumer()
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	if err := RegisterMessageFeedbackConsumer(consumer, svc); err != nil {
		t.Fatalf("register feedback consumer: %v", err)
	}
	cancelBody, err := json.Marshal(MessageFeedbackEvent{
		MessageID:  "msg-assistant",
		UserID:     "user-1",
		Cancelled:  true,
		SubmitTime: 2000,
	})
	if err != nil {
		t.Fatalf("marshal cancel event: %v", err)
	}
	if err := consumer.DispatchForTest(context.Background(), MessageFeedbackTopic, mq.Message{Body: cancelBody}); err != nil {
		t.Fatalf("dispatch cancel event: %v", err)
	}
	activeBody, err := json.Marshal(MessageFeedbackEvent{
		MessageID:  "msg-assistant",
		UserID:     "user-1",
		Vote:       1,
		Reason:     "late",
		Comment:    "too late",
		SubmitTime: 1000,
	})
	if err != nil {
		t.Fatalf("marshal active event: %v", err)
	}
	if err := consumer.DispatchForTest(context.Background(), MessageFeedbackTopic, mq.Message{Body: activeBody}); err != nil {
		t.Fatalf("dispatch active event: %v", err)
	}

	var feedback conversationModel.MessageFeedback
	if err := gdb.Where("message_id = ? AND user_id = ?", "msg-assistant", "user-1").First(&feedback).Error; err != nil {
		t.Fatalf("load feedback tombstone: %v", err)
	}
	if feedback.Deleted != 1 || feedback.Vote != 0 || feedback.Reason != "" || feedback.Comment != "" {
		t.Fatalf("expected newer cancel to win, got %+v", feedback)
	}
}

func TestRegisterMessageFeedbackConsumerAppliesNewerActiveEventAfterCancel(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&conversationModel.Message{}, &conversationModel.MessageFeedback{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := gdb.Create(&conversationModel.Message{
		BaseModel:      db.BaseModel{ID: "msg-assistant"},
		ConversationID: "conv-1",
		UserID:         "user-1",
		Role:           "assistant",
		Content:        "回答",
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	consumer := mq.NewNoopConsumer()
	svc := NewConversationService(repo.NewConversationRepo(gdb), repo.NewMessageRepo(gdb), repo.NewFeedbackRepo(gdb), repo.NewConversationSummaryRepo(gdb))
	if err := RegisterMessageFeedbackConsumer(consumer, svc); err != nil {
		t.Fatalf("register feedback consumer: %v", err)
	}
	cancelBody, err := json.Marshal(MessageFeedbackEvent{
		MessageID:  "msg-assistant",
		UserID:     "user-1",
		Cancelled:  true,
		SubmitTime: 1000,
	})
	if err != nil {
		t.Fatalf("marshal cancel event: %v", err)
	}
	if err := consumer.DispatchForTest(context.Background(), MessageFeedbackTopic, mq.Message{Body: cancelBody}); err != nil {
		t.Fatalf("dispatch cancel event: %v", err)
	}
	activeBody, err := json.Marshal(MessageFeedbackEvent{
		MessageID:  "msg-assistant",
		UserID:     "user-1",
		Vote:       1,
		Reason:     "great",
		Comment:    "nice",
		SubmitTime: 2000,
	})
	if err != nil {
		t.Fatalf("marshal active event: %v", err)
	}
	if err := consumer.DispatchForTest(context.Background(), MessageFeedbackTopic, mq.Message{Body: activeBody}); err != nil {
		t.Fatalf("dispatch active event: %v", err)
	}

	var feedback conversationModel.MessageFeedback
	if err := gdb.Where("message_id = ? AND user_id = ?", "msg-assistant", "user-1").First(&feedback).Error; err != nil {
		t.Fatalf("load feedback row: %v", err)
	}
	if feedback.Deleted != 0 || feedback.Vote != 1 || feedback.Reason != "great" || feedback.Comment != "nice" {
		t.Fatalf("expected active feedback to win, got %+v", feedback)
	}
}
