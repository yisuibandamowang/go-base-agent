package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed queue_claim_atomic.lua
var claimLua string

var acquireLua = redis.NewScript(`
	local current = tonumber(redis.call('GET', KEYS[1]) or '0')
	local max = tonumber(redis.call('GET', KEYS[2]) or '0')
	if current < max then
		local id = redis.call('INCR', KEYS[1])
		redis.call('PEXPIRE', KEYS[1], ARGV[1])
		return id
	end
	return nil
`)

type LimiterConfig struct {
	MaxConcurrent  int
	MaxWaitSeconds int
	LeaseSeconds   int
	PollIntervalMs int
}

type AcquireRequest struct {
	MaxWait   time.Duration
	OnAcquire func()
	OnTimeout func()
}

type ticket struct {
	requestID string
	deadline  time.Time
	req       AcquireRequest
	state     atomic.Int32
	pending   chan struct{}
	done      chan struct{}
}

const (
	statePending   = 0
	stateGranted   = 1
	stateTimeout   = 2
	stateCancelled = 3
)

type FairQueueLimiter struct {
	name          string
	rdb           *redis.Client
	cfg           LimiterConfig
	semaphoreKey  string
	queueKey      string
	queueSeqKey   string
	notifyTopic   string
	entryPrefix   string
	maxCounterKey string
	enabled       bool

	mu       sync.Mutex
	pollers  map[string]chan struct{}
	cancelFn context.CancelFunc
	ctx      context.Context
}

func NewFairQueueLimiter(name string, rdb *redis.Client, cfg LimiterConfig) *FairQueueLimiter {
	ctx, cancel := context.WithCancel(context.Background())
	l := &FairQueueLimiter{
		name:          name,
		rdb:           rdb,
		cfg:           cfg,
		semaphoreKey:  name + ":semaphore",
		queueKey:      name + ":queue",
		queueSeqKey:   name + ":queue:seq",
		notifyTopic:   name + ":queue:notify",
		entryPrefix:   name + ":entry:",
		maxCounterKey: name + ":max",
		enabled:       cfg.MaxConcurrent > 0,
		pollers:       make(map[string]chan struct{}),
		cancelFn:      cancel,
		ctx:           ctx,
	}
	if l.enabled {
		l.rdb.Set(ctx, l.maxCounterKey, cfg.MaxConcurrent, 0)
		go l.listenNotify()
	}
	return l
}

func (l *FairQueueLimiter) Shutdown() {
	l.cancelFn()
}

func (l *FairQueueLimiter) Acquire(ctx context.Context, req AcquireRequest) error {
	if !l.enabled {
		req.OnAcquire()
		return nil
	}

	t := &ticket{
		requestID: fmt.Sprintf("%d", time.Now().UnixNano()),
		deadline:  time.Now().Add(req.MaxWait),
		req:       req,
		pending:   make(chan struct{}, 1),
		done:      make(chan struct{}, 1),
	}
	t.state.Store(statePending)

	go l.cancelOnDisconnect(ctx, t)

	ttl := max(5000, req.MaxWait.Milliseconds()+5000)
	_ = l.rdb.Set(ctx, l.entryPrefix+t.requestID, "1", time.Duration(ttl)*time.Millisecond)

	score := l.nextSeq(ctx)
	_ = l.rdb.ZAdd(ctx, l.queueKey, redis.Z{Score: float64(score), Member: t.requestID})

	if l.tryClaim(ctx, t) {
		return nil
	}

	l.mu.Lock()
	l.pollers[t.requestID] = t.pending
	l.mu.Unlock()

	pollInterval := time.Duration(max(50, l.cfg.PollIntervalMs)) * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			if t.state.CompareAndSwap(statePending, stateCancelled) {
				l.cleanup(t)
				return ctx.Err()
			}
			return nil
		case <-t.pending:
			if l.tryClaim(ctx, t) {
				return nil
			}
		case <-ticker.C:
			if time.Now().After(t.deadline) {
				if t.state.CompareAndSwap(statePending, stateTimeout) {
					l.cleanup(t)
					go t.req.OnTimeout()
					return fmt.Errorf("queue timeout after %v", req.MaxWait)
				}
				return nil
			}
			if l.tryClaim(ctx, t) {
				return nil
			}
		}
	}
}

func (l *FairQueueLimiter) tryClaim(ctx context.Context, t *ticket) bool {
	avail, err := l.availablePermits(ctx)
	if err != nil || avail <= 0 {
		return false
	}

	result, err := l.rdb.Eval(ctx, claimLua,
		[]string{l.queueKey},
		t.requestID, fmt.Sprintf("%d", avail), l.entryPrefix,
	).Result()
	if err != nil {
		return false
	}

	arr, ok := result.([]any)
	if !ok || len(arr) == 0 {
		return false
	}
	claimed, _ := arr[0].(int64)
	if claimed != 1 {
		return false
	}

	acquired, err := acquireLua.Run(ctx, l.rdb,
		[]string{l.semaphoreKey, l.maxCounterKey},
		fmt.Sprintf("%d", l.cfg.LeaseSeconds*1000),
	).Int64()
	if err != nil || acquired == 0 {
		reScore := float64(l.nextSeq(ctx))
		_ = l.rdb.Set(ctx, l.entryPrefix+t.requestID, "1",
			time.Duration(max(1000, time.Until(t.deadline).Milliseconds()+5000))*time.Millisecond)
		_ = l.rdb.ZAdd(ctx, l.queueKey, redis.Z{Score: reScore, Member: t.requestID})
		l.notify(ctx)
		if t.state.Load() != statePending {
			_ = l.rdb.ZRem(ctx, l.queueKey, t.requestID)
			_ = l.rdb.Del(ctx, l.entryPrefix+t.requestID)
		}
		return false
	}

	if !t.state.CompareAndSwap(statePending, stateGranted) {
		l.releasePermit(ctx)
		l.cleanup(t)
		return false
	}

	l.mu.Lock()
	delete(l.pollers, t.requestID)
	l.mu.Unlock()
	close(t.done)

	defer l.releasePermit(context.Background())
	t.req.OnAcquire()

	return true
}

func (l *FairQueueLimiter) availablePermits(ctx context.Context) (int, error) {
	current, err := l.rdb.Get(ctx, l.semaphoreKey).Int()
	if err != nil && err != redis.Nil {
		return 0, err
	}
	maxVal, err := l.rdb.Get(ctx, l.maxCounterKey).Int()
	if err != nil {
		return 0, err
	}
	return maxVal - current, nil
}

func (l *FairQueueLimiter) releasePermit(ctx context.Context) {
	if err := l.rdb.Decr(ctx, l.semaphoreKey).Err(); err != nil {
		slog.Warn("release permit failed", "key", l.semaphoreKey, "err", err)
	}
	l.notify(ctx)
}

func (l *FairQueueLimiter) nextSeq(ctx context.Context) int64 {
	return l.rdb.Incr(ctx, l.queueSeqKey).Val()
}

func (l *FairQueueLimiter) notify(ctx context.Context) {
	_ = l.rdb.Publish(ctx, l.notifyTopic, "permit_changed")
}

func (l *FairQueueLimiter) cleanup(t *ticket) {
	_ = l.rdb.ZRem(context.Background(), l.queueKey, t.requestID)
	_ = l.rdb.Del(context.Background(), l.entryPrefix+t.requestID)
	l.mu.Lock()
	delete(l.pollers, t.requestID)
	l.mu.Unlock()
	select {
	case t.done <- struct{}{}:
	default:
	}
}

func (l *FairQueueLimiter) cancelOnDisconnect(ctx context.Context, t *ticket) {
	<-ctx.Done()
	if t.state.CompareAndSwap(statePending, stateCancelled) {
		l.cleanup(t)
	}
}

func (l *FairQueueLimiter) listenNotify() {
	pubsub := l.rdb.Subscribe(l.ctx, l.notifyTopic)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ch:
			l.mu.Lock()
			for _, p := range l.pollers {
				select {
				case p <- struct{}{}:
				default:
				}
			}
			l.mu.Unlock()
		}
	}
}
