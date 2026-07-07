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

type Producer interface {
	Send(ctx context.Context, msg Message) (*SendResult, error)
}

type MessageHandler func(ctx context.Context, msg Message) error

type Consumer interface {
	Subscribe(topic, group string, handler MessageHandler) error
	Start() error
	Shutdown() error
}
