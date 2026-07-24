package mq

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

type noopProducer struct{}

func NewNoopProducer() Producer { return &noopProducer{} }

func (p *noopProducer) Send(ctx context.Context, msg Message) (*SendResult, error) {
	slog.Info("mq: noop send", "topic", msg.Topic, "keys", msg.Keys, "desc", msg.BizDesc)
	return &SendResult{
		MsgID:  uuid.New().String(),
		Status: "NOOP_OK",
	}, nil
}

func (p *noopProducer) SendInTransaction(ctx context.Context, msg Message, executor TransactionExecutor) (*SendResult, error) {
	slog.Info("mq: noop transaction send", "topic", msg.Topic, "keys", msg.Keys, "desc", msg.BizDesc)
	if executor != nil {
		if err := executor(ctx, msg); err != nil {
			return nil, err
		}
	}
	return &SendResult{
		MsgID:  uuid.New().String(),
		Status: "NOOP_OK",
	}, nil
}

func (p *noopProducer) RegisterTransactionChecker(topic string, checker TransactionChecker) {}

type NoopConsumer struct {
	handlers map[string]MessageHandler
}

func NewNoopConsumer() *NoopConsumer {
	return &NoopConsumer{
		handlers: make(map[string]MessageHandler),
	}
}

func (c *NoopConsumer) Subscribe(topic, group string, handler MessageHandler) error {
	c.handlers[topic] = handler
	slog.Info("mq: noop subscribe", "topic", topic, "group", group)
	return nil
}

func (c *NoopConsumer) Start() error {
	slog.Info("mq: noop consumer started")
	return nil
}

func (c *NoopConsumer) Shutdown() error {
	slog.Info("mq: noop consumer shutdown")
	return nil
}

func (c *NoopConsumer) DispatchForTest(ctx context.Context, topic string, msg Message) error {
	h, ok := c.handlers[topic]
	if !ok {
		return nil
	}
	return h(ctx, msg)
}
