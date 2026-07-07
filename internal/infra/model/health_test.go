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
