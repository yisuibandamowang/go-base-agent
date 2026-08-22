package rag

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go-base-agent/internal/infra/chat"

	"github.com/redis/go-redis/v9"
)

const (
	streamCancelTopic     = "ragent:stream:cancel"
	streamCancelKeyPrefix = "ragent:stream:cancel:"
	streamCancelTTL       = 30 * time.Minute
)

type streamTaskManager struct {
	mu           sync.Mutex
	tasks        map[string]*streamTask
	redis        *redis.Client
	pubsub       *redis.PubSub
	redisCancel  context.CancelFunc
	listenerWait sync.WaitGroup
}

type streamTask struct {
	mu              sync.Mutex
	once            sync.Once
	cancelled       bool
	cancel          context.CancelFunc
	handle          chat.StreamHandle
	sender          *SSESender
	cancelPayloadFn func() CompletionPayload
}

func newStreamTaskManager() *streamTaskManager {
	return &streamTaskManager{tasks: make(map[string]*streamTask)}
}

func (m *streamTaskManager) setRedisClient(client *redis.Client) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	pubsub := client.Subscribe(ctx, streamCancelTopic)
	receiveCtx, receiveCancel := context.WithTimeout(ctx, 2*time.Second)
	_, err := pubsub.Receive(receiveCtx)
	receiveCancel()
	if err != nil {
		cancel()
		_ = pubsub.Close()
		slog.Warn("rag stream task: subscribe cancellation topic failed", "err", err)
		return
	}

	m.mu.Lock()
	if m.pubsub != nil {
		m.mu.Unlock()
		cancel()
		_ = pubsub.Close()
		return
	}
	m.redis = client
	m.pubsub = pubsub
	m.redisCancel = cancel
	m.listenerWait.Add(1)
	m.mu.Unlock()

	go m.listenCancellationTopic(ctx, pubsub)
}

func (m *streamTaskManager) listenCancellationTopic(ctx context.Context, pubsub *redis.PubSub) {
	defer m.listenerWait.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-pubsub.Channel():
			if !ok {
				return
			}
			taskID := strings.TrimSpace(message.Payload)
			if taskID != "" {
				m.cancelLocal(taskID)
			}
		}
	}
}

func (m *streamTaskManager) register(taskID string, sender *SSESender, cancel context.CancelFunc) *streamTask {
	task := &streamTask{
		cancel: cancel,
		sender: sender,
	}
	m.mu.Lock()
	m.tasks[taskID] = task
	m.mu.Unlock()
	if m.isCancellationMarked(taskID) {
		task.cancelTask()
	}
	return task
}

func (m *streamTaskManager) cancel(taskID string) {
	m.markCancellation(taskID)
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil {
		return
	}
	task.cancelTask()
}

func (m *streamTaskManager) unregister(taskID string) {
	m.mu.Lock()
	delete(m.tasks, taskID)
	client := m.redis
	m.mu.Unlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Del(ctx, streamCancelKey(taskID)).Err(); err != nil {
		slog.Warn("rag stream task: delete cancellation marker failed", "taskId", taskID, "err", err)
	}
}

func (m *streamTaskManager) isCancelled(taskID string) bool {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	return task != nil && task.isCancelled()
}

func (m *streamTaskManager) markCancellation(taskID string) {
	m.mu.Lock()
	client := m.redis
	m.mu.Unlock()
	if client == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Set(ctx, streamCancelKey(taskID), "1", streamCancelTTL).Err(); err != nil {
		slog.Warn("rag stream task: mark cancellation failed", "taskId", taskID, "err", err)
	}
	if err := client.Publish(ctx, streamCancelTopic, taskID).Err(); err != nil {
		slog.Warn("rag stream task: publish cancellation failed", "taskId", taskID, "err", err)
	}
}

func (m *streamTaskManager) isCancellationMarked(taskID string) bool {
	m.mu.Lock()
	client := m.redis
	m.mu.Unlock()
	if client == nil || strings.TrimSpace(taskID) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := client.Get(ctx, streamCancelKey(taskID)).Result()
	if err == redis.Nil {
		return false
	}
	if err != nil {
		slog.Warn("rag stream task: read cancellation marker failed", "taskId", taskID, "err", err)
		return false
	}
	return value == "1" || strings.EqualFold(value, "true")
}

func (m *streamTaskManager) cancelLocal(taskID string) {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task != nil {
		task.cancelTask()
	}
}

func (m *streamTaskManager) close() {
	m.mu.Lock()
	cancel := m.redisCancel
	pubsub := m.pubsub
	m.redisCancel = nil
	m.pubsub = nil
	m.redis = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if pubsub != nil {
		_ = pubsub.Close()
	}
	m.listenerWait.Wait()
}

func streamCancelKey(taskID string) string {
	return streamCancelKeyPrefix + taskID
}

func (t *streamTask) bindHandle(handle chat.StreamHandle) {
	t.mu.Lock()
	t.handle = handle
	cancelled := t.cancelled
	t.mu.Unlock()
	if cancelled && handle != nil {
		handle.Cancel()
	}
}

func (t *streamTask) setCancelPayloadFn(fn func() CompletionPayload) {
	t.mu.Lock()
	t.cancelPayloadFn = fn
	t.mu.Unlock()
}

func (t *streamTask) cancelTask() {
	t.once.Do(func() {
		t.mu.Lock()
		t.cancelled = true
		cancel := t.cancel
		handle := t.handle
		sender := t.sender
		payloadFn := t.cancelPayloadFn
		t.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		if handle != nil {
			handle.Cancel()
		}
		if sender != nil && !sender.IsClosed() {
			payload := CompletionPayload{}
			if payloadFn != nil {
				payload = payloadFn()
			}
			_ = sender.SendCancel(payload.MessageID, payload.Title)
			_ = sender.SendDone()
			sender.Close()
		}
	})
}

func (t *streamTask) isCancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelled
}
