package rag

import (
	"context"
	"sync"

	"go-base-agent/internal/infra/chat"
)

type streamTaskManager struct {
	mu    sync.Mutex
	tasks map[string]*streamTask
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

func (m *streamTaskManager) register(taskID string, sender *SSESender, cancel context.CancelFunc) *streamTask {
	task := &streamTask{
		cancel: cancel,
		sender: sender,
	}
	m.mu.Lock()
	m.tasks[taskID] = task
	m.mu.Unlock()
	return task
}

func (m *streamTaskManager) cancel(taskID string) {
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
	m.mu.Unlock()
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
