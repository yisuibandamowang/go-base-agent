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
	deepThinking := req.Thinking != nil && *req.Thinking
	targets := s.selector.SelectChatCandidates(deepThinking)

	return model.ExecuteWithFallback(
		s.executor,
		model.CapabilityChat,
		targets,
		func(t model.Target) (ChatClient, bool) {
			c, ok := s.clients[t.Candidate.Provider]
			return c, ok
		},
		func(client ChatClient, t model.Target) (string, error) {
			return client.Chat(ctx, req, t)
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

func (s *RoutingLLMService) StreamChat(ctx context.Context, req Request, cb StreamCallback) (StreamHandle, error) {
	deepThinking := req.Thinking != nil && *req.Thinking
	targets := s.selector.SelectChatCandidates(deepThinking)

	if len(targets) == 0 {
		return nil, errors.New("no available chat model")
	}

	var last error
	for _, target := range targets {
		client, ok := s.clients[target.Candidate.Provider]
		if !ok {
			continue
		}
		if !s.health.AllowCall(target.ID) {
			continue
		}

		bridge := NewProbeBridge(cb)
		handle, err := client.StreamChat(ctx, req, bridge, target)
		if err != nil {
			s.health.MarkFailure(target.ID)
			last = err
			continue
		}
		if handle == nil {
			s.health.MarkFailure(target.ID)
			continue
		}

		result, err := s.firstPacketProbe.AwaitFirstPacket(bridge, s.firstPacketTimeout)
		if err != nil {
			handle.Cancel()
			s.health.MarkFailure(target.ID)
			last = err
			continue
		}

		if result.Success {
			s.health.MarkSuccess(target.ID)
			return handle, nil
		}

		handle.Cancel()
		s.health.MarkFailure(target.ID)
		last = result.Error
	}

	if last != nil {
		return nil, last
	}
	return nil, errors.New("all chat models failed")
}
