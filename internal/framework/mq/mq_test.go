package mq_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nageoffer/ragent-go/internal/framework/mq"
)

func TestNoopProducer_Send(t *testing.T) {
	p := mq.NewNoopProducer()
	result, err := p.Send(context.Background(), mq.Message{
		Topic:   "test-topic",
		Keys:    "key-1",
		BizDesc: "测试消息",
		Body:    []byte(`{"id":1}`),
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if result.MsgID == "" {
		t.Fatal("expected non-empty msgId")
	}
	if result.Status != "NOOP_OK" {
		t.Fatalf("expected NOOP_OK, got %s", result.Status)
	}
}

func TestNoopConsumer_SubscribeAndDispatch(t *testing.T) {
	raw := mq.NewNoopConsumer()
	c := raw // *NoopConsumer implements Consumer

	received := make(chan mq.Message, 1)
	err := c.Subscribe("test-topic", "test-group", func(ctx context.Context, msg mq.Message) error {
		received <- msg
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}

	testMsg := mq.Message{
		Topic:   "test-topic",
		Keys:    "key-2",
		BizDesc: "测试消费",
		Body:    []byte(`{"action":"test"}`),
	}
	if err := c.DispatchForTest(context.Background(), "test-topic", testMsg); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case msg := <-received:
		if msg.Keys != "key-2" {
			t.Fatalf("expected key-2, got %s", msg.Keys)
		}
	default:
		t.Fatal("expected message to be received")
	}
}

func TestNoopConsumer_HandlerError(t *testing.T) {
	c := mq.NewNoopConsumer()

	err := c.Subscribe("err-topic", "g", func(ctx context.Context, msg mq.Message) error {
		return errors.New("handler failed")
	})
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}

	err = c.DispatchForTest(context.Background(), "err-topic", mq.Message{
		Topic: "err-topic",
	})
	if err == nil {
		t.Fatal("expected handler error")
	}
}

func TestNoopProducer_Concurrent(t *testing.T) {
	p := mq.NewNoopProducer()

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Send(context.Background(), mq.Message{
				Topic: "concurrent",
				Body:  []byte("msg"),
			})
			if err != nil {
				t.Errorf("concurrent send failed: %v", err)
			}
		}()
	}
	wg.Wait()
}
