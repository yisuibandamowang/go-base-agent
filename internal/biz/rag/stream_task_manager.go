package rag

import (
	"context"
	"errors"
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
	streamOwnerKeyPrefix  = "ragent:stream:owner:"
	streamCancelTTL       = 30 * time.Minute
	// systemRequester 系统侧回收的发起方占位，与任何用户 ID 都不会撞（用户 ID 是雪花数字串）。
	systemRequester = "__system__"
	// streamPayloadSeparator 广播载荷 taskId|requester 的分隔符：taskId 与用户 ID 都不含竖线。
	streamPayloadSeparator = "|"
)

// ErrTaskNotCancellable 不区分「任务不存在」与「非属主」，免得停止接口变成他人任务的探测器。
var ErrTaskNotCancellable = errors.New("任务不存在或已结束")

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
	ownerUserID     string
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
			taskID, requester := splitCancelPayload(message.Payload)
			if taskID != "" {
				m.cancelLocal(taskID, requester)
			}
		}
	}
}

// register 注册流式任务并落属主。属主必须先于收尾回调落地：反过来的话，
// 两条赋值之间到达的广播能拿本地回调杀掉一条无主的流。对齐 Java StreamTaskManager.register。
func (m *streamTaskManager) register(taskID, ownerUserID string, sender *SSESender, cancel context.CancelFunc) *streamTask {
	task := &streamTask{
		ownerUserID: ownerUserID,
		cancel:      cancel,
		sender:      sender,
	}
	m.mu.Lock()
	m.tasks[taskID] = task
	client := m.redis
	m.mu.Unlock()
	// 属主进 Redis 而非只留本地：停止请求可能落在没跑这条流的节点上
	if client != nil && strings.TrimSpace(ownerUserID) != "" {
		ctx, cancelOwner := context.WithTimeout(context.Background(), 2*time.Second)
		if err := client.Set(ctx, streamOwnerKey(taskID), ownerUserID, streamCancelTTL).Err(); err != nil {
			slog.Warn("rag stream task: persist owner failed", "taskId", taskID, "err", err)
		}
		cancelOwner()
	}
	if m.isCancellationMarked(taskID, task) {
		task.cancelTask()
	}
	return task
}

// cancelByUser 用户主动停止：taskId 是雪花 ID，时间有序可预测，不是访问凭证，必须比对属主。
// 属主查不到多半是任务已结束，也可能是注册还没落地，故标记带上发起方交给注册那一刻复核。
func (m *streamTaskManager) cancelByUser(taskID, requester string) error {
	m.mu.Lock()
	client := m.redis
	m.mu.Unlock()
	if client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ownerUserID, err := client.Get(ctx, streamOwnerKey(taskID)).Result()
		if err == nil && ownerUserID != "" && ownerUserID != requester {
			slog.Warn("拒绝越权停止流式任务", "taskId", taskID, "owner", ownerUserID, "requester", requester)
			return ErrTaskNotCancellable
		}
		if err != nil && err != redis.Nil {
			slog.Warn("rag stream task: read owner failed", "taskId", taskID, "err", err)
		}
	}
	m.publishCancel(taskID, requester)
	return nil
}

// cancel 系统侧回收，无条件放行。
func (m *streamTaskManager) cancel(taskID string) {
	m.publishCancel(taskID, systemRequester)
}

func (m *streamTaskManager) publishCancel(taskID, requester string) {
	m.mu.Lock()
	client := m.redis
	m.mu.Unlock()
	if client == nil || strings.TrimSpace(taskID) == "" {
		m.cancelLocal(taskID, requester)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// 先设置 Redis 标记（值=发起方，执行端要靠它复核），再发布消息通知所有节点
	if err := client.Set(ctx, streamCancelKey(taskID), requester, streamCancelTTL).Err(); err != nil {
		slog.Warn("rag stream task: mark cancellation failed", "taskId", taskID, "err", err)
	}
	payload := taskID
	if requester != "" {
		payload = taskID + streamPayloadSeparator + requester
	}
	if err := client.Publish(ctx, streamCancelTopic, payload).Err(); err != nil {
		slog.Warn("rag stream task: publish cancellation failed", "taskId", taskID, "err", err)
	}
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
	if err := client.Del(ctx, streamCancelKey(taskID), streamOwnerKey(taskID)).Err(); err != nil {
		slog.Warn("rag stream task: delete cancellation marker failed", "taskId", taskID, "err", err)
	}
}

func (m *streamTaskManager) isCancelled(taskID string) bool {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	return task != nil && task.isCancelled()
}

// isCancellationMarked 检查任务是否在 Redis 中被标记为已取消。
// 标记可能先于注册到达，那一刻还没有属主可比对，只能推到 register 那一刻复核：
// 只有发布端校验挡不住属主落地前的抢跑。
func (m *streamTaskManager) isCancellationMarked(taskID string, task *streamTask) bool {
	if task != nil && task.isCancelled() {
		return true
	}
	m.mu.Lock()
	client := m.redis
	m.mu.Unlock()
	if client == nil || strings.TrimSpace(taskID) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	requester, err := client.Get(ctx, streamCancelKey(taskID)).Result()
	if err == redis.Nil {
		return false
	}
	if err != nil {
		slog.Warn("rag stream task: read cancellation marker failed", "taskId", taskID, "err", err)
		return false
	}
	if !isRequesterAllowed(task, requester) {
		slog.Warn("忽略非属主埋下的取消标记", "taskId", taskID, "owner", taskOwner(task), "requester", requester)
		return false
	}
	return true
}

// isRequesterAllowed 系统侧回收无条件放行；用户侧只认精确属主，
// 属主还没落地（注册未发生）时一律不认，这正是预埋标记要在 register 那一刻复核的窗口。
func isRequesterAllowed(task *streamTask, requester string) bool {
	if requester == systemRequester {
		return true
	}
	owner := taskOwner(task)
	return owner != "" && owner == requester
}

func taskOwner(task *streamTask) string {
	if task == nil {
		return ""
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.ownerUserID
}

// cancelLocal 按发起方复核后取消本节点任务：不匹配时连 cancelled 都不置——
// 置了会让 register 的复核短路，等于把标记复核那道门绕开。
func (m *streamTaskManager) cancelLocal(taskID, requester string) {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil {
		return
	}
	if !isRequesterAllowed(task, requester) {
		slog.Warn("拒绝越权取消流式任务", "taskId", taskID, "owner", taskOwner(task), "requester", requester)
		return
	}
	task.cancelTask()
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

func streamOwnerKey(taskID string) string {
	return streamOwnerKeyPrefix + taskID
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

// splitCancelPayload 解析 taskId|requester 载荷；无分隔符时 requester 为空（视为系统侧放行）。
func splitCancelPayload(payload string) (string, string) {
	payload = strings.TrimSpace(payload)
	if idx := strings.Index(payload, streamPayloadSeparator); idx >= 0 {
		return payload[:idx], payload[idx+1:]
	}
	return payload, ""
}
