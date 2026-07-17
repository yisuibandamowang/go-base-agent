package parser

import (
	"context"
	"fmt"
	"sync"

	"go-base-agent/internal/biz/rag"
)

// Registry 文档解析器注册表，按 MIME 类型匹配合适的解析器。
type Registry struct {
	mu       sync.RWMutex
	parsers  []rag.DocumentParser
	fallback rag.DocumentParser
}

// NewRegistry 创建解析器注册表。
func NewRegistry(fallback rag.DocumentParser) *Registry {
	return &Registry{fallback: fallback}
}

// Register 注册一个解析器。
func (r *Registry) Register(p rag.DocumentParser) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parsers = append(r.parsers, p)
}

// Parse 根据 MIME 类型自动选择解析器解析文档。
// 如果找不到匹配的解析器，使用 fallback；未配置 fallback 时返回不支持格式错误。
func (r *Registry) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, p := range r.parsers {
		if p.Supports(mimeType) {
			return p.Parse(ctx, data, mimeType, options)
		}
	}
	if r.fallback == nil {
		return nil, ErrUnsupportedFormat
	}
	return r.fallback.Parse(ctx, data, mimeType, options)
}

// Supports 检查是否有解析器支持给定 MIME 类型。
func (r *Registry) Supports(mimeType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.parsers {
		if p.Supports(mimeType) {
			return true
		}
	}
	if r.fallback == nil {
		return false
	}
	return r.fallback.Supports(mimeType)
}

// List 返回所有已注册解析器的类型列表。
func (r *Registry) List() []rag.ParserType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]rag.ParserType, 0, len(r.parsers))
	for _, p := range r.parsers {
		types = append(types, p.Type())
	}
	return types
}

// DefaultRegistry 创建包含所有默认解析器的注册表。
func DefaultRegistry() *Registry {
	reg := NewRegistry(nil)
	reg.Register(&MarkdownParser{})
	reg.Register(&CSVParser{})
	reg.Register(&XLSXParser{})
	reg.Register(&PDFParser{})
	reg.Register(&DOCXParser{})
	reg.Register(&PlainTextParser{})
	return reg
}

// ErrUnsupportedFormat 不支持的文档格式错误。
var ErrUnsupportedFormat = fmt.Errorf("unsupported document format")
