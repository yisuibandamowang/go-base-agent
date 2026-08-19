package model

import (
	"sync"
	"testing"
	"time"

	"go-base-agent/internal/framework/config"
)

func newTestHealthStore() *HealthStore {
	return NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 2,
		OpenDurationMs:   100,
	})
}

func TestHealthStore_InitialState(t *testing.T) {
	s := newTestHealthStore()

	if s.IsUnavailable("m1") {
		t.Fatal("new model should not be unavailable")
	}
	if !s.AllowCall("m1") {
		t.Fatal("new model should allow call")
	}
}

func TestHealthStore_EmptyID(t *testing.T) {
	s := newTestHealthStore()

	if s.AllowCall("") {
		t.Fatal("empty id should not allow call")
	}
	s.MarkSuccess("")
	s.MarkFailure("")
	// should not panic
}

func TestHealthStore_OpenAfterThreshold(t *testing.T) {
	s := newTestHealthStore()

	s.MarkFailure("m1")
	if !s.AllowCall("m1") {
		t.Fatal("one failure should still allow")
	}

	s.MarkFailure("m1")
	if s.AllowCall("m1") {
		t.Fatal("two failures should open circuit")
	}
	if !s.IsUnavailable("m1") {
		t.Fatal("should be unavailable after open")
	}
}

func TestHealthStore_HalfOpenSuccess(t *testing.T) {
	s := NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 2,
		OpenDurationMs:   1, // immediate expire
	})

	s.MarkFailure("m1")
	s.MarkFailure("m1")

	time.Sleep(2 * time.Millisecond)

	if !s.AllowCall("m1") {
		t.Fatal("half-open should allow one call")
	}
	s.MarkSuccess("m1")

	if !s.AllowCall("m1") {
		t.Fatal("after success should allow calls")
	}
	if s.IsUnavailable("m1") {
		t.Fatal("should not be unavailable after success")
	}
}

func TestHealthStore_HalfOpenFailure(t *testing.T) {
	s := NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 2,
		OpenDurationMs:   1,
	})

	s.MarkFailure("m1")
	s.MarkFailure("m1")

	time.Sleep(2 * time.Millisecond)

	if !s.AllowCall("m1") {
		t.Fatal("half-open should allow one call")
	}
	s.MarkFailure("m1")

	if s.AllowCall("m1") {
		t.Fatal("half-open failure should re-open circuit")
	}
}

func TestHealthStore_HalfOpenOnlyOne(t *testing.T) {
	s := NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 1,
		OpenDurationMs:   1,
	})

	s.MarkFailure("m1")

	time.Sleep(2 * time.Millisecond)

	if !s.AllowCall("m1") {
		t.Fatal("first half-open should allow")
	}
	if s.AllowCall("m1") {
		t.Fatal("second half-open should deny")
	}
	// IsUnavailable should still be true while half-open inflight
	if !s.IsUnavailable("m1") {
		t.Fatal("should be unavailable while half-open inflight")
	}
}

func TestHealthStore_Concurrent(t *testing.T) {
	s := newTestHealthStore()
	var wg sync.WaitGroup

	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				s.AllowCall("m1")
				s.MarkSuccess("m1")
				s.MarkFailure("m1")
				s.IsUnavailable("m1")
			}
		}()
	}
	wg.Wait()
}

func TestHealthStore_MarkSuccessUnknownID(t *testing.T) {
	s := newTestHealthStore()
	s.MarkSuccess("unknown")
	// should not panic, just register as closed
	if !s.AllowCall("unknown") {
		t.Fatal("should allow after MarkSuccess on unknown")
	}
}

func TestHealthStore_ReleaseHalfOpenPermit(t *testing.T) {
	s := NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 1,
		OpenDurationMs:   1,
	})

	s.MarkFailure("m1")
	time.Sleep(2 * time.Millisecond)

	permit := s.AcquirePermit("m1")
	if permit == nil {
		t.Fatal("half-open should grant permit")
	}
	if !permit.HasHalfOpenSlot() {
		t.Fatal("half-open permit should carry slot token")
	}

	// 名额被占用时第二次探测应被拒绝
	if s.AcquirePermit("m1") != nil {
		t.Fatal("second half-open call should be denied while inflight")
	}

	// 持有者释放名额后，状态仍是半开但允许下一次探测
	s.ReleaseHalfOpenPermit(permit)
	if s.AcquirePermit("m1") == nil {
		t.Fatal("permit should be reusable after release")
	}
}

func TestHealthStore_ReleaseHalfOpenPermitStaleToken(t *testing.T) {
	s := NewHealthStore(config.AISelectionConfig{
		FailureThreshold: 1,
		OpenDurationMs:   1,
	})

	s.MarkFailure("m1")
	time.Sleep(2 * time.Millisecond)

	first := s.AcquirePermit("m1")
	if first == nil {
		t.Fatal("half-open should grant permit")
	}
	// 模拟旧调用先失败转 OPEN 再进入新一轮探测
	s.MarkFailure("m1")
	time.Sleep(2 * time.Millisecond)
	second := s.AcquirePermit("m1")
	if second == nil {
		t.Fatal("new probe round should grant permit")
	}

	// 旧凭证释放不得影响新一轮探测的占用状态
	s.ReleaseHalfOpenPermit(first)
	if s.AcquirePermit("m1") != nil {
		t.Fatal("stale permit release must not free the new probe slot")
	}
}

func TestHealthStore_ReleaseHalfOpenPermitNoop(t *testing.T) {
	s := newTestHealthStore()

	// 普通许可（非半开）释放应是 no-op
	permit := s.AcquirePermit("m1")
	if permit == nil {
		t.Fatal("closed state should grant permit")
	}
	if permit.HasHalfOpenSlot() {
		t.Fatal("closed state permit should not carry slot")
	}
	s.ReleaseHalfOpenPermit(permit)
	s.ReleaseHalfOpenPermit(nil)
}
