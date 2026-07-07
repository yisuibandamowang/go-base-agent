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

type health struct {
	consecutiveFailures int
	openUntil           time.Time
	halfOpenInFlight    bool
	state               healthState
}

// HealthStore tracks model health with a three-state circuit breaker.
// Aligns with Java ModelHealthStore.
type HealthStore struct {
	mu   sync.Mutex
	data map[string]*health
	cfg  config.AISelectionConfig
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
	if id == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	h := s.data[id]
	if h == nil {
		return true
	}

	now := time.Now()

	switch h.state {
	case stateOpen:
		if h.openUntil.After(now) {
			return false
		}
		h.state = stateHalfOpen
		h.halfOpenInFlight = true
		return true
	case stateHalfOpen:
		if h.halfOpenInFlight {
			return false
		}
		h.halfOpenInFlight = true
		return true
	default:
		return true
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
