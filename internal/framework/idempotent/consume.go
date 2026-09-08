package idempotent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 消费状态三态（对齐 Java IdempotentConsumeStatusEnum）：
// CONSUMING=0 表示正在消费，CONSUMED=1 表示消费完成。
const (
	StatusConsuming = "0"
	StatusConsumed  = "1"
)

var (
	// ErrConsuming 消息正在被其他消费者处理，应延迟重试而不是立即重投。
	ErrConsuming = errors.New("消息正在被其他消费者处理，等待延迟重试")
	// ErrConsumed 消息已完成消费，可直接跳过。
	ErrConsumed = errors.New("消息已完成消费")
)

// setNXGet 原子地写入 CONSUMING 并返回旧值（对齐 Java IdempotentConsumeAspect 的
// SET key value NX GET PX Lua 脚本：只有拿到旧值才能区分「消费中」与「已完成」）。
var setNXGet = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2])
return old
`)

// ConsumeGuard MQ 消费幂等守卫：三态语义（消费中 / 已完成 / 无记录），
// 防止 RocketMQ at-least-once 投递下的重复消费。
type ConsumeGuard struct {
	client *redis.Client
	prefix string
}

// NewConsumeGuard 创建消费幂等守卫。
func NewConsumeGuard(client *redis.Client) *ConsumeGuard {
	return &ConsumeGuard{client: client, prefix: "idempotent:consume:"}
}

// TryBegin 尝试把消息标记为消费中。
// 返回 nil 表示获得执行权；ErrConsuming 表示别的消费者正在处理（应让消费失败触发延迟重试）；
// ErrConsumed 表示已完成（应直接跳过）。
func (g *ConsumeGuard) TryBegin(ctx context.Context, key string, ttl time.Duration) error {
	fullKey := g.prefix + key
	old, err := setNXGet.Run(ctx, g.client, []string{fullKey}, StatusConsuming, ttl.Milliseconds()).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("idempotent consume begin: %w", err)
	}
	switch old {
	case nil:
		return nil
	case StatusConsuming:
		return ErrConsuming
	case StatusConsumed:
		return ErrConsumed
	default:
		// 未知旧值按无记录处理，覆盖写入消费中状态
		return nil
	}
}

// Complete 标记消费完成，保留 ttl 作为幂等窗口。
func (g *ConsumeGuard) Complete(ctx context.Context, key string, ttl time.Duration) error {
	return g.client.Set(ctx, g.prefix+key, StatusConsumed, ttl).Err()
}

// Fail 删除消费标记，让消息重投后可以重试。
func (g *ConsumeGuard) Fail(ctx context.Context, key string) error {
	return g.client.Del(ctx, g.prefix+key).Err()
}
