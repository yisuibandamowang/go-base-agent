package mq

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"github.com/google/uuid"
)

type rocketProducerClient interface {
	Start() error
	Shutdown() error
	SendSync(ctx context.Context, msgs ...*primitive.Message) (*primitive.SendResult, error)
}

type rocketTransactionProducerClient interface {
	Start() error
	Shutdown() error
	SendMessageInTransaction(ctx context.Context, msg *primitive.Message) (*primitive.TransactionSendResult, error)
}

type RocketProducerConfig struct {
	NameServers        []string
	Group              string
	SendMessageTimeout int
}

type RocketProducer struct {
	sendClient rocketProducerClient
	txClient   rocketTransactionProducerClient

	mu      sync.Mutex
	started bool

	txMu        sync.RWMutex
	executions  map[string]transactionExecution
	checkers    map[string]TransactionChecker
	transaction *rocketTransactionListener
}

type transactionExecution struct {
	ctx      context.Context
	executor TransactionExecutor
}

type rocketTransactionListener struct {
	producer *RocketProducer
}

const transactionExecutionIDKey = "GO_TXN_ID"

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

	p := &RocketProducer{
		executions: make(map[string]transactionExecution),
		checkers:   make(map[string]TransactionChecker),
	}
	sendClient, err := rocketmq.NewProducer(opts...)
	if err != nil {
		return nil, fmt.Errorf("create rocketmq producer: %w", err)
	}
	txClient, err := rocketmq.NewTransactionProducer(&rocketTransactionListener{producer: p}, opts...)
	if err != nil {
		_ = sendClient.Shutdown()
		return nil, fmt.Errorf("create rocketmq transaction producer: %w", err)
	}
	p.sendClient = sendClient
	p.txClient = txClient
	p.transaction = &rocketTransactionListener{producer: p}
	return p, nil
}

func NewRocketProducerWithClient(client rocketProducerClient) *RocketProducer {
	return newRocketProducer(client, nil)
}

func NewRocketProducerWithClients(sendClient rocketProducerClient, txClient rocketTransactionProducerClient) *RocketProducer {
	return newRocketProducer(sendClient, txClient)
}

func newRocketProducer(sendClient rocketProducerClient, txClient rocketTransactionProducerClient) *RocketProducer {
	p := &RocketProducer{
		sendClient: sendClient,
		txClient:   txClient,
		executions: make(map[string]transactionExecution),
		checkers:   make(map[string]TransactionChecker),
	}
	p.transaction = &rocketTransactionListener{producer: p}
	return p
}

func (p *RocketProducer) Send(ctx context.Context, msg Message) (*SendResult, error) {
	if err := p.start(); err != nil {
		return nil, err
	}
	if p.sendClient == nil {
		return nil, fmt.Errorf("rocketmq producer client not configured")
	}
	rocketMsg := buildRocketMessage(msg)
	result, err := p.sendClient.SendSync(ctx, rocketMsg)
	if err != nil {
		return nil, fmt.Errorf("rocketmq send sync: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("rocketmq send sync: empty result")
	}
	return &SendResult{MsgID: result.MsgID, Status: rocketSendStatusText(result.Status)}, nil
}

func (p *RocketProducer) SendInTransaction(ctx context.Context, msg Message, executor TransactionExecutor) (*SendResult, error) {
	if err := p.start(); err != nil {
		return nil, err
	}
	if p.txClient == nil {
		return nil, fmt.Errorf("rocketmq transaction producer not configured")
	}
	if executor == nil {
		return nil, fmt.Errorf("rocketmq transaction executor required")
	}
	rocketMsg := buildRocketMessage(msg)
	executionID := uuid.NewString()
	rocketMsg.WithProperty(transactionExecutionIDKey, executionID)
	p.registerTransactionExecution(executionID, transactionExecution{ctx: ctx, executor: executor})
	defer p.removeTransactionExecution(executionID)

	result, err := p.txClient.SendMessageInTransaction(ctx, rocketMsg)
	if err != nil {
		return nil, fmt.Errorf("rocketmq send transaction: %w", err)
	}
	if result == nil || result.SendResult == nil {
		return nil, fmt.Errorf("rocketmq send transaction: empty result")
	}
	if result.State != primitive.CommitMessageState {
		return nil, fmt.Errorf("rocketmq transaction state: %v", result.State)
	}
	return &SendResult{MsgID: result.MsgID, Status: rocketSendStatusText(result.Status)}, nil
}

func (p *RocketProducer) RegisterTransactionChecker(topic string, checker TransactionChecker) {
	topic = strings.TrimSpace(topic)
	if topic == "" || checker == nil {
		return
	}
	p.txMu.Lock()
	defer p.txMu.Unlock()
	if p.checkers == nil {
		p.checkers = make(map[string]TransactionChecker)
	}
	p.checkers[topic] = checker
}

func (p *RocketProducer) Shutdown() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil
	}
	var firstErr error
	if p.txClient != nil {
		if err := p.txClient.Shutdown(); err != nil {
			firstErr = fmt.Errorf("shutdown rocketmq transaction producer: %w", err)
		}
	}
	if p.sendClient != nil {
		if err := p.sendClient.Shutdown(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("shutdown rocketmq producer: %w", err)
		}
	}
	p.started = false
	return firstErr
}

func (p *RocketProducer) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return nil
	}
	if p.sendClient != nil {
		if err := p.sendClient.Start(); err != nil {
			return fmt.Errorf("start rocketmq producer: %w", err)
		}
	}
	if p.txClient != nil {
		if err := p.txClient.Start(); err != nil {
			return fmt.Errorf("start rocketmq transaction producer: %w", err)
		}
	}
	p.started = true
	return nil
}

func (p *RocketProducer) registerTransactionExecution(id string, execution transactionExecution) {
	p.txMu.Lock()
	defer p.txMu.Unlock()
	if p.executions == nil {
		p.executions = make(map[string]transactionExecution)
	}
	p.executions[id] = execution
}

func (p *RocketProducer) removeTransactionExecution(id string) {
	p.txMu.Lock()
	defer p.txMu.Unlock()
	delete(p.executions, id)
}

func (p *RocketProducer) takeTransactionExecution(id string) (transactionExecution, bool) {
	p.txMu.Lock()
	defer p.txMu.Unlock()
	execution, ok := p.executions[id]
	if ok {
		delete(p.executions, id)
	}
	return execution, ok
}

func (p *RocketProducer) lookupTransactionChecker(topic string) (TransactionChecker, bool) {
	p.txMu.RLock()
	defer p.txMu.RUnlock()
	checker, ok := p.checkers[topic]
	return checker, ok
}

func buildRocketMessage(msg Message) *primitive.Message {
	rocketMsg := primitive.NewMessage(msg.Topic, msg.Body)
	if msg.Keys != "" {
		rocketMsg.WithKeys([]string{msg.Keys})
	}
	if msg.BizDesc != "" {
		rocketMsg.WithProperty("BIZ_DESC", msg.BizDesc)
	}
	return rocketMsg
}

func (l *rocketTransactionListener) ExecuteLocalTransaction(msg *primitive.Message) primitive.LocalTransactionState {
	if l == nil || l.producer == nil {
		return primitive.RollbackMessageState
	}
	executionID := msg.GetProperty(transactionExecutionIDKey)
	if strings.TrimSpace(executionID) == "" {
		return primitive.RollbackMessageState
	}
	execution, ok := l.producer.takeTransactionExecution(executionID)
	if !ok || execution.executor == nil {
		return primitive.RollbackMessageState
	}
	if err := execution.executor(execution.ctx, primitiveMessageToMessage(msg)); err != nil {
		return primitive.RollbackMessageState
	}
	return primitive.CommitMessageState
}

func (l *rocketTransactionListener) CheckLocalTransaction(msg *primitive.MessageExt) primitive.LocalTransactionState {
	if l == nil || l.producer == nil {
		return primitive.UnknowState
	}
	checker, ok := l.producer.lookupTransactionChecker(msg.Topic)
	if !ok || checker == nil {
		return primitive.UnknowState
	}
	committed, err := checker(context.Background(), primitiveMessageExtToMessage(msg))
	if err != nil {
		return primitive.UnknowState
	}
	if committed {
		return primitive.CommitMessageState
	}
	return primitive.RollbackMessageState
}

func primitiveMessageToMessage(msg *primitive.Message) Message {
	if msg == nil {
		return Message{}
	}
	return Message{
		Topic: msg.Topic,
		Keys:  firstNonEmpty(msg.GetKeys(), msg.TransactionId),
		Body:  msg.Body,
	}
}

func primitiveMessageExtToMessage(msg *primitive.MessageExt) Message {
	if msg == nil {
		return Message{}
	}
	return Message{
		Topic: msg.Topic,
		Keys:  firstNonEmpty(msg.GetKeys(), msg.MsgId),
		Body:  msg.Body,
	}
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

type RocketConsumerConfig struct {
	NameServers []string
	Group       string
}

type RocketConsumer struct {
	client rocketPushConsumerClient
}

type rocketPushConsumerClient interface {
	Start() error
	Shutdown() error
	Subscribe(topic string, selector consumer.MessageSelector, callback func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error)) error
	Unsubscribe(topic string) error
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
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
