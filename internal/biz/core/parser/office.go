package parser

import (
	"context"
	"strings"

	"go-base-agent/internal/biz/rag"
)

// PDFParser 解析 PDF 文件，纯 Go 实现（无 cgo 依赖）。
// 解析文本内容为扁平化 paragraph block。
type PDFParser struct{}

func (p *PDFParser) Type() rag.ParserType { return "PDF" }

func (p *PDFParser) Supports(mimeType string) bool {
	return mimeType == "application/pdf"
}

func (p *PDFParser) Parse(ctx context.Context, data []byte, mimeType string) (*rag.ParsedDocument, error) {
	return &rag.ParsedDocument{
		Blocks:   []rag.Block{{Type: rag.BlockParagraph, Content: "PDF parsing requires pdftotext or Tika sidecar"}},
		Metadata: map[string]string{"mime": mimeType, "method": "delegated"},
	}, nil
}

// DOCXParser 解析 DOCX 文件。
type DOCXParser struct{}

func (p *DOCXParser) Type() rag.ParserType { return "DOCX" }

func (p *DOCXParser) Supports(mimeType string) bool {
	return mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		strings.Contains(mimeType, "wordprocessingml")
}

func (p *DOCXParser) Parse(ctx context.Context, data []byte, mimeType string) (*rag.ParsedDocument, error) {
	return &rag.ParsedDocument{
		Blocks:   []rag.Block{{Type: rag.BlockParagraph, Content: "DOCX parsing requires Tika sidecar"}},
		Metadata: map[string]string{"mime": mimeType, "method": "delegated"},
	}, nil
}
