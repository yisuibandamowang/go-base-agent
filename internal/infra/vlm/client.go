package vlm

import (
	"context"

	"go-base-agent/internal/infra/model"
)

// Client 负责单个 VLM 候选的图像描述调用。
type Client interface {
	Provider() string
	DescribeImage(ctx context.Context, image []byte, mimeType, prompt string, target model.Target, maxOutputTokens ...int) (string, error)
}
