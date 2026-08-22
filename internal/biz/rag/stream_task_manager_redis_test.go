package rag

import (
	"context"
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
	managerB.register("task-cross-node", sender, func() {})
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
	if value != "1" {
		t.Fatalf("expected cancellation marker 1, got %q", value)
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

	if err := client.Set(context.Background(), streamCancelKey("task-already-cancelled"), "1", streamCancelTTL).Err(); err != nil {
		t.Fatalf("set cancellation marker: %v", err)
	}

	sender, _ := newTestSSESender(t)
	task := manager.register("task-already-cancelled", sender, func() {})
	if !task.isCancelled() {
		t.Fatal("expected registration to honor the existing cancellation marker")
	}
}
