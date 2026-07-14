package mq

import (
	"context"
	"errors"
	"testing"

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
)

type fakeRocketProducerClient struct {
	started  bool
	shutdown bool
	sent     []*primitive.Message
	result   *primitive.SendResult
	err      error
}

func (c *fakeRocketProducerClient) Start() error {
	c.started = true
	return nil
}

func (c *fakeRocketProducerClient) Shutdown() error {
	c.shutdown = true
	return nil
}

func (c *fakeRocketProducerClient) SendSync(ctx context.Context, msgs ...*primitive.Message) (*primitive.SendResult, error) {
	c.sent = append(c.sent, msgs...)
	return c.result, c.err
}

type fakeRocketPushConsumerClient struct {
	started     bool
	shutdown    bool
	topic       string
	callback    func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error)
	unsubscribe string
}

func (c *fakeRocketPushConsumerClient) Start() error {
	c.started = true
	return nil
}

func (c *fakeRocketPushConsumerClient) Shutdown() error {
	c.shutdown = true
	return nil
}

func (c *fakeRocketPushConsumerClient) Subscribe(topic string, selector consumer.MessageSelector, callback func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error)) error {
	c.topic = topic
	c.callback = callback
	return nil
}

func (c *fakeRocketPushConsumerClient) Unsubscribe(topic string) error {
	c.unsubscribe = topic
	return nil
}

func TestRocketProducer_SendStartsAndMapsMessage(t *testing.T) {
	client := &fakeRocketProducerClient{result: &primitive.SendResult{MsgID: "msg-1", Status: primitive.SendOK}}
	producer := NewRocketProducerWithClient(client)

	result, err := producer.Send(context.Background(), Message{
		Topic:   "topic-a",
		Keys:    "key-a",
		BizDesc: "业务描述",
		Body:    []byte(`{"id":1}`),
	})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	if !client.started {
		t.Fatal("expected producer to start lazily before first send")
	}
	if result.MsgID != "msg-1" || result.Status != "SEND_OK" {
		t.Fatalf("unexpected send result: %+v", result)
	}
	if len(client.sent) != 1 {
		t.Fatalf("expected one rocket message, got %d", len(client.sent))
	}
	msg := client.sent[0]
	if msg.Topic != "topic-a" || string(msg.Body) != `{"id":1}` || msg.GetKeys() != "key-a" {
		t.Fatalf("unexpected rocket message: topic=%s keys=%s body=%s", msg.Topic, msg.GetKeys(), string(msg.Body))
	}
}

func TestRocketProducer_SendReturnsClientError(t *testing.T) {
	client := &fakeRocketProducerClient{err: errors.New("broker unavailable")}
	producer := NewRocketProducerWithClient(client)

	_, err := producer.Send(context.Background(), Message{Topic: "topic-a"})
	if err == nil || err.Error() != "rocketmq send sync: broker unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRocketConsumer_SubscribeMapsMessagesAndRetriesOnHandlerError(t *testing.T) {
	client := &fakeRocketPushConsumerClient{}
	rocketConsumer := NewRocketConsumerWithClient(client)

	var received Message
	err := rocketConsumer.Subscribe("topic-a", "group-a", func(ctx context.Context, msg Message) error {
		received = msg
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}
	if client.topic != "topic-a" || client.callback == nil {
		t.Fatal("expected rocket subscribe to register callback")
	}

	status, err := client.callback(context.Background(), &primitive.MessageExt{
		Message: primitive.Message{Topic: "topic-a", Body: []byte("hello")},
		MsgId:   "msg-1",
	})
	if err != nil || status != consumer.ConsumeSuccess {
		t.Fatalf("expected consume success, status=%v err=%v", status, err)
	}
	if received.Topic != "topic-a" || string(received.Body) != "hello" || received.Keys != "msg-1" {
		t.Fatalf("unexpected received message: %+v", received)
	}

	_ = rocketConsumer.Subscribe("topic-b", "group-a", func(ctx context.Context, msg Message) error {
		return errors.New("handler failed")
	})
	status, err = client.callback(context.Background(), &primitive.MessageExt{
		Message: primitive.Message{Topic: "topic-b"},
		MsgId:   "msg-2",
	})
	if err == nil || status != consumer.ConsumeRetryLater {
		t.Fatalf("expected retry later on handler error, status=%v err=%v", status, err)
	}
}
