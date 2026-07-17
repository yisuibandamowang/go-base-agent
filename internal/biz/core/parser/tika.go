package parser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go-base-agent/internal/biz/rag"
)

// TikaParser 通过 HTTP 调用 Apache Tika Server 解析文档。
// 作为 Go 原生解析器的兼容路径，支持 PDF/DOCX/PPTX/XLSX 等格式。
type TikaParser struct {
	url    string
	client *http.Client
}

// NewTikaParser 创建 TikaParser。
func NewTikaParser(url string) *TikaParser {
	return &TikaParser{
		url: url,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (p *TikaParser) Type() rag.ParserType { return rag.ParserTika }

func (p *TikaParser) Supports(mimeType string) bool {
	return true
}

func (p *TikaParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.url+"/tika", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create tika request: %w", err)
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Accept", "text/plain")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tika request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read tika response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tika returned status %d: %s", resp.StatusCode, string(body))
	}

	blocks := splitTextToBlocks(string(body))
	return &rag.ParsedDocument{
		Blocks:   blocks,
		Metadata: map[string]string{"mime": mimeType, "parser": "tika"},
	}, nil
}

func splitTextToBlocks(text string) []rag.Block {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	blocks := make([]rag.Block, 0)
	buf := strings.Builder{}
	for _, line := range lines {
		if line == "" {
			if buf.Len() > 0 {
				blocks = append(blocks, rag.Block{
					Type:    rag.BlockParagraph,
					Content: strings.TrimSpace(buf.String()),
				})
				buf.Reset()
			}
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(line)
	}
	if buf.Len() > 0 {
		blocks = append(blocks, rag.Block{
			Type:    rag.BlockParagraph,
			Content: strings.TrimSpace(buf.String()),
		})
	}
	return blocks
}
