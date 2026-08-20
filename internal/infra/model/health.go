package model

import (
	"sync"
	"time"

	"go-base-agent/internal/framework/config"
)

type healthState int

const (
	stateClosed healthState = iota
	stateOpen
	stateHalfOpen
)

// CallPermit 一次模型调用的许可凭证。
// halfOpenToken 非 0 时表示本次调用持有半开探测名额，取消/中断后只允许释放该名额，
// 避免旧调用误标记新一轮探测的结果。
// 对齐 Java ModelHealthStore.CallPermit。
type CallPermit struct {
	ModelID       string
	halfOpenToken int64
}

// HasHalfOpenSlot 报告本次许可是否持有半开探测名额。
func (p CallPermit) HasHalfOpenSlot() bool {
	return p.halfOpenToken > 0
}

type health struct {
	consecutiveFailures int
	openUntil           time.Time
	halfOpenInFlight    bool
	halfOpenToken       int64
	state               healthState
}

// HealthStore tracks model health with a three-state circuit breaker.
// Aligns with Java ModelHealthStore.
type HealthStore struct {
	mu   sync.Mutex
	data map[string]*health
	cfg  config.AISelectionConfig

	probeTokenSeq int64
}

// NewHealthStore creates a new HealthStore.
func NewHealthStore(cfg config.AISelectionConfig) *HealthStore {
	return &HealthStore{
		data: make(map[string]*health),
		cfg:  cfg,
	}
}

// IsUnavailable reports whether a model is fully unavailable (OPEN or HALF_OPEN with inflight).
// Used by Selector to filter out candidates.
func (s *HealthStore) IsUnavailable(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.data[id]
	if !ok {
		return false
	}
	if h.state == stateOpen && time.Now().Before(h.openUntil) {
		return true
	}
	if h.state == stateHalfOpen && h.halfOpenInFlight {
		return true
	}
	return false
}

// AllowCall checks if a call is currently allowed for the model.
// Returns false if id is empty, the model is OPEN, or HALF_OPEN with an in-flight probe.
// On successful HALF_OPEN check, marks the probe as in-flight.
func (s *HealthStore) AllowCall(id string) bool {
	return s.AcquirePermit(id) != nil
}

// AcquirePermit 获取一次模型调用许可，返回 nil 表示拒绝调用。
// 持有半开探测名额时返回带 token 的凭证，调用方在取消/中断场景应调用 ReleaseHalfOpenPermit 而非 MarkFailure。
func (s *HealthStore) AcquirePermit(id string) *CallPermit {
	if id == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h := s.data[id]
	if h == nil {
		return &CallPermit{ModelID: id}
	}

	now := time.Now()

	switch h.state {
	case stateOpen:
		if h.openUntil.After(now) {
			return nil
		}
		h.state = stateHalfOpen
		h.halfOpenInFlight = true
		s.probeTokenSeq++
		h.halfOpenToken = s.probeTokenSeq
		return &CallPermit{ModelID: id, halfOpenToken: h.halfOpenToken}
	case stateHalfOpen:
		if h.halfOpenInFlight {
			return nil
		}
		h.halfOpenInFlight = true
		s.probeTokenSeq++
		h.halfOpenToken = s.probeTokenSeq
		return &CallPermit{ModelID: id, halfOpenToken: h.halfOpenToken}
	default:
		return &CallPermit{ModelID: id}
	}
}

// MarkSuccess marks a successful call, resetting the model to CLOSED state.
func (s *HealthStore) MarkSuccess(id string) {
	if id == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h := s.data[id]
	if h == nil {
		s.data[id] = &health{state: stateClosed}
		return
	}

	h.state = stateClosed
	h.consecutiveFailures = 0
	h.openUntil = time.Time{}
	h.halfOpenInFlight = false
}

// MarkFailure records a failed call. If consecutive failures reach the threshold,
// transitions the model to OPEN state. HALF_OPEN failures immediately go to OPEN.
func (s *HealthStore) MarkFailure(id string) {
	if id == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	h := s.data[id]
	if h == nil {
		h = &health{state: stateClosed}
		s.data[id] = h
	}

	if h.state == stateHalfOpen {
		h.state = stateOpen
		h.openUntil = now.Add(time.Duration(s.cfg.OpenDurationMs) * time.Millisecond)
		h.consecutiveFailures = 0
		h.halfOpenInFlight = false
		return
	}

	h.consecutiveFailures++
	if h.consecutiveFailures >= s.cfg.FailureThreshold {
		h.state = stateOpen
		h.openUntil = now.Add(time.Duration(s.cfg.OpenDurationMs) * time.Millisecond)
		h.consecutiveFailures = 0
	}
}

// ReleaseHalfOpenPermit 仅释放当前凭证持有的半开探测名额。
// 用于首包探测等待期间请求被取消/中断的场景：名额归还后允许下一次探测，
// 但不改变熔断状态（不标记失败也不标记成功）。
// 对齐 Java releaseHalfOpenPermit。
func (s *HealthStore) ReleaseHalfOpenPermit(permit *CallPermit) {
	if permit == nil || permit.halfOpenToken <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.data[permit.ModelID]
	if !ok {
		return
	}
	if h.state == stateHalfOpen && h.halfOpenInFlight && h.halfOpenToken == permit.halfOpenToken {
		h.halfOpenInFlight = false
	}
}
