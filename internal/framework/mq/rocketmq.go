package mq

import (
	"context"
	"fmt"
	"strings"
	"sync"

	rocketmq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
)

type rocketProducerClient interface {
	Start() error
	Shutdown() error
	SendSync(ctx context.Context, msgs ...*primitive.Message) (*primitive.SendResult, error)
}

type rocketPushConsumerClient interface {
	Start() error
	Shutdown() error
	Subscribe(topic string, selector consumer.MessageSelector, callback func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error)) error
	Unsubscribe(topic string) error
}

type RocketProducerConfig struct {
	NameServers        []string
	Group              string
	SendMessageTimeout int
}

type RocketProducer struct {
	client  rocketProducerClient
	mu      sync.Mutex
	started bool
}

func NewRocketProducer(cfg RocketProducerConfig) (*RocketProducer, error) {
	if len(cfg.NameServers) == 0 {
		return nil, fmt.Errorf("rocketmq name servers required")
	}
	opts := []producer.Option{
		producer.WithNsResolver(primitive.NewPassthroughResolver(cfg.NameServers)),
		producer.WithRetry(2),
	}
	if cfg.Group != "" {
		opts = append(opts, producer.WithGroupName(cfg.Group))
	}
	client, err := rocketmq.NewProducer(opts...)
	if err != nil {
		return nil, fmt.Errorf("create rocketmq producer: %w", err)
	}
	return NewRocketProducerWithClient(client), nil
}

func NewRocketProducerWithClient(client rocketProducerClient) *RocketProducer {
	return &RocketProducer{client: client}
}

func (p *RocketProducer) Send(ctx context.Context, msg Message) (*SendResult, error) {
	if err := p.start(); err != nil {
		return nil, err
	}
	rocketMsg := primitive.NewMessage(msg.Topic, msg.Body)
	if msg.Keys != "" {
		rocketMsg.WithKeys([]string{msg.Keys})
	}
	if msg.BizDesc != "" {
		rocketMsg.WithProperty("BIZ_DESC", msg.BizDesc)
	}

	result, err := p.client.SendSync(ctx, rocketMsg)
	if err != nil {
		return nil, fmt.Errorf("rocketmq send sync: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("rocketmq send sync: empty result")
	}
	return &SendResult{MsgID: result.MsgID, Status: rocketSendStatusText(result.Status)}, nil
}

func (p *RocketProducer) Shutdown() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	if err := p.client.Shutdown(); err != nil {
		return fmt.Errorf("shutdown rocketmq producer: %w", err)
	}
	p.started = false
	return nil
}

func (p *RocketProducer) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}
	if err := p.client.Start(); err != nil {
		return fmt.Errorf("start rocketmq producer: %w", err)
	}
	p.started = true
	return nil
}

type RocketConsumerConfig struct {
	NameServers []string
	Group       string
}

type RocketConsumer struct {
	client rocketPushConsumerClient
}

func NewRocketConsumer(cfg RocketConsumerConfig) (*RocketConsumer, error) {
	if len(cfg.NameServers) == 0 {
		return nil, fmt.Errorf("rocketmq name servers required")
	}
	group := strings.TrimSpace(cfg.Group)
	if group == "" {
		return nil, fmt.Errorf("rocketmq consumer group required")
	}
	client, err := rocketmq.NewPushConsumer(
		consumer.WithGroupName(group),
		consumer.WithNsResolver(primitive.NewPassthroughResolver(cfg.NameServers)),
		consumer.WithConsumerModel(consumer.Clustering),
	)
	if err != nil {
		return nil, fmt.Errorf("create rocketmq consumer: %w", err)
	}
	return NewRocketConsumerWithClient(client), nil
}

func NewRocketConsumerWithClient(client rocketPushConsumerClient) *RocketConsumer {
	return &RocketConsumer{client: client}
}

func (c *RocketConsumer) Subscribe(topic, group string, handler MessageHandler) error {
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("rocketmq subscribe topic required")
	}
	if handler == nil {
		return fmt.Errorf("rocketmq subscribe handler required")
	}
	err := c.client.Subscribe(topic, consumer.MessageSelector{}, func(ctx context.Context, messages ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		for _, rocketMsg := range messages {
			msg := Message{
				Topic: rocketMsg.Topic,
				Keys:  firstNonEmpty(rocketMsg.GetKeys(), rocketMsg.MsgId),
				Body:  rocketMsg.Body,
			}
			if err := handler(ctx, msg); err != nil {
				return consumer.ConsumeRetryLater, err
			}
		}
		return consumer.ConsumeSuccess, nil
	})
	if err != nil {
		return fmt.Errorf("rocketmq subscribe %s: %w", topic, err)
	}
	return nil
}

func (c *RocketConsumer) Start() error {
	if err := c.client.Start(); err != nil {
		return fmt.Errorf("start rocketmq consumer: %w", err)
	}
	return nil
}

func (c *RocketConsumer) Shutdown() error {
	if err := c.client.Shutdown(); err != nil {
		return fmt.Errorf("shutdown rocketmq consumer: %w", err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func rocketSendStatusText(status primitive.SendStatus) string {
	switch status {
	case primitive.SendOK:
		return "SEND_OK"
	case primitive.SendFlushDiskTimeout:
		return "FLUSH_DISK_TIMEOUT"
	case primitive.SendFlushSlaveTimeout:
		return "FLUSH_SLAVE_TIMEOUT"
	case primitive.SendSlaveNotAvailable:
		return "SLAVE_NOT_AVAILABLE"
	default:
		return "UNKNOWN"
	}
}
