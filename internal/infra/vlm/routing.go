package vlm

import (
	"context"
	"errors"

	"go-base-agent/internal/infra/model"
)

// Service 提供 VLM 图像描述能力。
type Service interface {
	DescribeImage(ctx context.Context, image []byte, mimeType, prompt string) (string, error)
}

// RoutingService 负责按候选模型顺序执行图像描述并回退。
type RoutingService struct {
	selector *model.Selector
	executor *model.RoutingExecutor
	clients  map[string]Client
}

// NewRoutingService 创建路由式 VLM 服务。
func NewRoutingService(selector *model.Selector, executor *model.RoutingExecutor, clients []Client) *RoutingService {
	byProvider := make(map[string]Client, len(clients))
	for _, c := range clients {
		byProvider[c.Provider()] = c
	}
	return &RoutingService{
		selector: selector,
		executor: executor,
		clients:  byProvider,
	}
}

// DescribeImage 按候选顺序调用可用 VLM 模型。
func (s *RoutingService) DescribeImage(ctx context.Context, image []byte, mimeType, prompt string) (string, error) {
	targets := s.selector.SelectVlmCandidates()
	return model.ExecuteWithFallback(
		s.executor,
		model.CapabilityVLM,
		targets,
		func(t model.Target) (Client, bool) {
			c, ok := s.clients[t.Candidate.Provider]
			return c, ok
		},
		func(client Client, t model.Target) (string, error) {
			return client.DescribeImage(ctx, image, mimeType, prompt, t)
		},
	)
}

var _ Service = (*RoutingService)(nil)

// ErrNoVLMClient 表示没有可用 VLM 客户端。
var ErrNoVLMClient = errors.New("no available vlm client")
