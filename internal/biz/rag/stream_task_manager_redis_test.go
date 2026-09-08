package rag

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestStreamTaskManagerPropagatesCancellationAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: server.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: server.Addr()})

	managerA := newStreamTaskManager()
	managerB := newStreamTaskManager()
	managerA.setRedisClient(clientA)
	managerB.setRedisClient(clientB)
	t.Cleanup(func() {
		managerA.close()
		managerB.close()
		_ = clientA.Close()
		_ = clientB.Close()
	})

	sender, _ := newTestSSESender(t)
	managerB.register("task-cross-node", "user-b", sender, func() {})
	// 系统侧回收无条件放行（对齐 Java SYSTEM_REQUESTER）
	managerA.cancel("task-cross-node")

	deadline := time.Now().Add(time.Second)
	for !managerB.isCancelled("task-cross-node") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !managerB.isCancelled("task-cross-node") {
		t.Fatal("expected cancellation to propagate to the other instance")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := clientB.Get(ctx, streamCancelKey("task-cross-node")).Result()
	if err != nil {
		t.Fatalf("read cancellation marker: %v", err)
	}
	if value != systemRequester {
		t.Fatalf("expected cancellation marker %q, got %q", systemRequester, value)
	}
}

func TestStreamTaskManagerHonorsCancellationMarkerOnRegistration(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	manager := newStreamTaskManager()
	manager.setRedisClient(client)
	t.Cleanup(func() {
		manager.close()
		_ = client.Close()
	})

	if err := client.Set(context.Background(), streamCancelKey("task-already-cancelled"), systemRequester, streamCancelTTL).Err(); err != nil {
		t.Fatalf("set cancellation marker: %v", err)
	}

	sender, _ := newTestSSESender(t)
	task := manager.register("task-already-cancelled", "user-1", sender, func() {})
	if !task.isCancelled() {
		t.Fatal("expected registration to honor the existing cancellation marker")
	}
}

func TestStreamTaskManagerRejectsCrossUserStop(t *testing.T) {
	// taskId 是时间有序可预测的雪花 ID，不是访问凭证：属主不匹配的停止请求必须被拒绝
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	manager := newStreamTaskManager()
	manager.setRedisClient(client)
	t.Cleanup(func() {
		manager.close()
		_ = client.Close()
	})

	sender, _ := newTestSSESender(t)
	task := manager.register("task-owner-check", "user-owner", sender, func() {})
	if err := manager.cancelByUser("task-owner-check", "user-attacker"); err != ErrTaskNotCancellable {
		t.Fatalf("expected cross-user stop to be rejected, got %v", err)
	}
	if task.isCancelled() {
		t.Fatal("rejected stop must not cancel the task")
	}
	if err := manager.cancelByUser("task-owner-check", "user-owner"); err != nil {
		t.Fatalf("owner stop should pass: %v", err)
	}
	// 取消经 Redis 广播异步到达本节点 listener，轮询等待
	deadline := time.Now().Add(time.Second)
	for !task.isCancelled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !task.isCancelled() {
		t.Fatal("owner stop should cancel the task")
	}
}

func TestStreamTaskManagerIgnoresForeignCancellationMarker(t *testing.T) {
	// 非属主埋下的取消标记（属主落地前的抢跑）要在 register 那一刻复核拦截
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	manager := newStreamTaskManager()
	manager.setRedisClient(client)
	t.Cleanup(func() {
		manager.close()
		_ = client.Close()
	})

	ctx := context.Background()
	if err := client.Set(ctx, streamCancelKey("task-foreign-marker"), "user-attacker", streamCancelTTL).Err(); err != nil {
		t.Fatalf("set foreign cancellation marker: %v", err)
	}

	sender, _ := newTestSSESender(t)
	task := manager.register("task-foreign-marker", "user-owner", sender, func() {})
	if task.isCancelled() {
		t.Fatal("foreign cancellation marker must be ignored at registration")
	}
}

func TestSplitCancelPayload(t *testing.T) {
	taskID, requester := splitCancelPayload("task-1|user-9")
	if taskID != "task-1" || requester != "user-9" {
		t.Fatalf("unexpected split: %q %q", taskID, requester)
	}
	taskID, requester = splitCancelPayload("task-1")
	if taskID != "task-1" || requester != "" {
		t.Fatalf("unexpected bare payload split: %q %q", taskID, requester)
	}
	if strings.Contains(taskID, streamPayloadSeparator) {
		t.Fatal("task id must not contain separator")
	}
}
