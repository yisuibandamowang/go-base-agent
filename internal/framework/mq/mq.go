package mq

import "context"

type SendResult struct {
	MsgID  string `json:"msgId"`
	Status string `json:"status"`
}

type Message struct {
	Topic   string `json:"topic"`
	Keys    string `json:"keys"`
	BizDesc string `json:"bizDesc"`
	Body    []byte `json:"body"`
}

type TransactionExecutor func(ctx context.Context, msg Message) error

type TransactionChecker func(ctx context.Context, msg Message) (bool, error)

type Producer interface {
	Send(ctx context.Context, msg Message) (*SendResult, error)
	SendInTransaction(ctx context.Context, msg Message, executor TransactionExecutor) (*SendResult, error)
	RegisterTransactionChecker(topic string, checker TransactionChecker)
}

type MessageHandler func(ctx context.Context, msg Message) error

type Consumer interface {
	Subscribe(topic, group string, handler MessageHandler) error
	Start() error
	Shutdown() error
}
