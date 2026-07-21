package parser

import (
	"context"

	"go-base-agent/internal/biz/rag"
)

// PlainTextParser 解析纯文本文件。
type PlainTextParser struct{}

func (p *PlainTextParser) Type() rag.ParserType { return "TEXT" }

func (p *PlainTextParser) Supports(mimeType string) bool {
	switch normalizeMIMEType(mimeType) {
	case "text/plain", "text/csv", "application/json":
		return true
	}
	return false
}

func (p *PlainTextParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	return &rag.ParsedDocument{
		Blocks:   []rag.Block{{Type: rag.BlockParagraph, Content: string(data)}},
		Metadata: map[string]string{"mime": mimeType},
	}, nil
}
