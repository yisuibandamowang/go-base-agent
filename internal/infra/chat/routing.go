package chat

import (
	"context"
	"errors"
	"sync"
	"time"

	"go-base-agent/internal/infra/model"
)

// FirstPacketProbe waits for the first packet from a streaming response.
// The implementation is provided in the chat package (2A-7).
// This interface is defined here to avoid circular dependencies.
type FirstPacketProbe interface {
	AwaitFirstPacket(bridge *ProbeBridge, timeout time.Duration) (ProbeResult, error)
}

// ProbeResult contains the result of a first-packet probe.
type ProbeResult struct {
	Success bool
	Error   error
}

// ProbeBridge bridges a StreamCallback to capture the first packet result.
// Aligns with Java ProbeStreamBridge.
type ProbeBridge struct {
	inner StreamCallback
	once  sync.Once
	ctx   context.Context

	received bool
	ch       chan ProbeResult
}

// NewProbeBridge creates a ProbeBridge wrapping an inner callback.
func NewProbeBridge(inner StreamCallback) *ProbeBridge {
	return &ProbeBridge{
		inner: inner,
		ch:    make(chan ProbeResult, 1),
	}
}

// NewProbeBridgeWithContext creates a ProbeBridge bound to a request context,
// so first-packet probes can observe cancellation.
func NewProbeBridgeWithContext(ctx context.Context, inner StreamCallback) *ProbeBridge {
	bridge := NewProbeBridge(inner)
	bridge.ctx = ctx
	return bridge
}

// Context returns the bound request context, or nil when unbound.
func (b *ProbeBridge) Context() context.Context {
	if b == nil {
		return nil
	}
	return b.ctx
}

func (b *ProbeBridge) OnContent(content string) {
	b.received = true
	b.notify(ProbeResult{Success: true})
	b.inner.OnContent(content)
}

func (b *ProbeBridge) OnThinking(content string) {
	b.received = true
	b.notify(ProbeResult{Success: true})
	b.inner.OnThinking(content)
}

func (b *ProbeBridge) OnComplete() {
	if !b.received {
		b.notify(ProbeResult{Success: false})
	}
	b.inner.OnComplete()
}

func (b *ProbeBridge) OnError(err error) {
	if !b.received {
		b.notify(ProbeResult{Success: false, Error: err})
	}
	b.inner.OnError(err)
}

func (b *ProbeBridge) notify(result ProbeResult) {
	b.once.Do(func() {
		b.ch <- result
	})
}

// AwaitResult returns the channel to wait on for the first packet result.
func (b *ProbeBridge) AwaitResult() <-chan ProbeResult {
	return b.ch
}

// RoutingLLMService implements LLMService with model routing, health checks, and fallback.
// Aligns with Java RoutingLLMService.
type RoutingLLMService struct {
	selector           *model.Selector
	health             *model.HealthStore
	executor           *model.RoutingExecutor
	clients            map[string]ChatClient
	firstPacketProbe   FirstPacketProbe
	firstPacketTimeout time.Duration
}

// NewRoutingLLMService creates a new RoutingLLMService.
func NewRoutingLLMService(
	selector *model.Selector,
	health *model.HealthStore,
	executor *model.RoutingExecutor,
	clients []ChatClient,
	probe FirstPacketProbe,
	firstPacketTimeout time.Duration,
) *RoutingLLMService {
	byProvider := make(map[string]ChatClient, len(clients))
	for _, c := range clients {
		byProvider[c.Provider()] = c
	}
	return &RoutingLLMService{
		selector:           selector,
		health:             health,
		executor:           executor,
		clients:            byProvider,
		firstPacketProbe:   probe,
		firstPacketTimeout: firstPacketTimeout,
	}
}

func (s *RoutingLLMService) Chat(ctx context.Context, req Request) (string, error) {
	return s.ChatWithTier(ctx, req, "")
}

// ChatWithTier performs synchronous chat using an explicit configured tier.
func (s *RoutingLLMService) ChatWithTier(ctx context.Context, req Request, tier string) (string, error) {
	deepThinking := req.Thinking != nil && *req.Thinking
	targets := s.selector.SelectChatCandidatesForTier(deepThinking, tier, "")

	return model.ExecuteWithFallback(
		s.executor,
		model.CapabilityChat,
		targets,
		func(t model.Target) (ChatClient, bool) {
			c, ok := s.clients[t.Candidate.Provider]
			return c, ok
		},
		func(client ChatClient, t model.Target) (string, error) {
			callCtx, cancel := modelCallContext(ctx, t)
			defer cancel()
			return client.Chat(callCtx, req, t)
		},
	)
}

// ChatWithTierAndModel routes a preferred model first, then the tier fallback.
func (s *RoutingLLMService) ChatWithTierAndModel(ctx context.Context, req Request, tier, modelID string) (string, error) {
	deepThinking := req.Thinking != nil && *req.Thinking
	targets := s.selector.SelectChatCandidatesForTier(deepThinking, tier, modelID)
	return model.ExecuteWithFallback(
		s.executor,
		model.CapabilityChat,
		targets,
		func(t model.Target) (ChatClient, bool) {
			client, ok := s.clients[t.Candidate.Provider]
			return client, ok
		},
		func(client ChatClient, t model.Target) (string, error) {
			callCtx, cancel := modelCallContext(ctx, t)
			defer cancel()
			return client.Chat(callCtx, req, t)
		},
	)
}

func (s *RoutingLLMService) ChatWithModel(ctx context.Context, req Request, modelID string) (string, error) {
	if modelID == "" {
		return s.Chat(ctx, req)
	}

	deepThinking := req.Thinking != nil && *req.Thinking
	targets := s.selector.SelectChatCandidates(deepThinking)

	for _, t := range targets {
		if t.ID == modelID {
			return model.ExecuteWithFallback(
				s.executor,
				model.CapabilityChat,
				[]model.Target{t},
				func(t model.Target) (ChatClient, bool) {
					c, ok := s.clients[t.Candidate.Provider]
					return c, ok
				},
				func(client ChatClient, t model.Target) (string, error) {
					return client.Chat(ctx, req, t)
				},
			)
		}
	}

	return "", errors.New("specified model not found: " + modelID)
}

func modelCallContext(ctx context.Context, target model.Target) (context.Context, context.CancelFunc) {
	if target.TimeoutMs <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(target.TimeoutMs)*time.Millisecond)
}

func (s *RoutingLLMService) StreamChat(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error) {
	return s.streamChatWithTier(ctx, req, cb, "")
}

func (s *RoutingLLMService) streamChatWithTier(ctx context.Context, req Request, cb StreamCallback, tier string) (StreamHandle, error) {
	deepThinking := req.Thinking != nil && *req.Thinking
	targets := s.selector.SelectChatCandidatesForTier(deepThinking, tier, "")

	if len(targets) == 0 {
		return nil, errors.New("no available chat model")
	}

	var last error
	for _, target := range targets {
		client, ok := s.clients[target.Candidate.Provider]
		if !ok {
			continue
		}
		permit := s.health.AcquirePermit(target.ID)
		if permit == nil {
			continue
		}

		bridge := NewProbeBridgeWithContext(ctx, cb)
		callCtx, cancel := modelCallContext(ctx, target)
		handle, err := client.StreamChat(callCtx, req, bridge, target)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				// 请求取消属于客户端行为，不算模型失败：持有者只释放探测名额
				s.health.ReleaseHalfOpenPermit(permit)
				return nil, ctx.Err()
			}
			s.health.MarkFailure(target.ID)
			last = err
			continue
		}
		if handle == nil {
			cancel()
			s.health.MarkFailure(target.ID)
			continue
		}

		probeTimeout := s.firstPacketTimeout
		if target.TimeoutMs > 0 && (probeTimeout <= 0 || time.Duration(target.TimeoutMs)*time.Millisecond < probeTimeout) {
			probeTimeout = time.Duration(target.TimeoutMs) * time.Millisecond
		}
		result, err := s.firstPacketProbe.AwaitFirstPacket(bridge, probeTimeout)
		if err != nil {
			cancel()
			handle.Cancel()
			if ctx.Err() != nil {
				// 等待首包时请求被取消：探测名额由持有者释放，避免半开名额被占用
				s.health.ReleaseHalfOpenPermit(permit)
				return nil, ctx.Err()
			}
			s.health.MarkFailure(target.ID)
			last = err
			continue
		}

		if result.Success {
			s.health.MarkSuccess(target.ID)
			return &timedStreamHandle{inner: handle, cancel: cancel}, nil
		}

		handle.Cancel()
		cancel()
		s.health.MarkFailure(target.ID)
		last = result.Error
	}

	if last != nil {
		return nil, last
	}
	return nil, errors.New("all chat models failed")
}

type timedStreamHandle struct {
	inner  StreamHandle
	cancel context.CancelFunc
	once   sync.Once
}

func (h *timedStreamHandle) Cancel() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
		if h.inner != nil {
			h.inner.Cancel()
		}
	})
}

func (h *timedStreamHandle) Wait() {
	if h == nil || h.inner == nil {
		return
	}
	h.inner.Wait()
	h.Cancel()
}

// StreamChatWithTier starts a streaming chat using an explicit configured tier.
func (s *RoutingLLMService) StreamChatWithTier(ctx context.Context, req Request, cb StreamCallback, tier string) (StreamHandle, error) {
	return s.streamChatWithTier(ctx, req, cb, tier)
}
