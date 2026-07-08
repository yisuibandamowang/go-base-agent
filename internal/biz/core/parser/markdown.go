package parser

import (
	"context"
	"strings"

	"go-base-agent/internal/biz/rag"
)

// MarkdownParser 解析 Markdown 文件。
type MarkdownParser struct{}

func (p *MarkdownParser) Type() rag.ParserType { return rag.ParserMarkdown }

func (p *MarkdownParser) Supports(mimeType string) bool {
	return mimeType == "text/markdown" || mimeType == "text/x-markdown" ||
		strings.HasSuffix(mimeType, "/markdown")
}

func (p *MarkdownParser) Parse(ctx context.Context, data []byte, mimeType string) (*rag.ParsedDocument, error) {
	blocks := parseMarkdownBlocks(string(data))
	return &rag.ParsedDocument{Blocks: blocks, Metadata: map[string]string{"mime": mimeType}}, nil
}

func parseMarkdownBlocks(content string) []rag.Block {
	lines := strings.Split(content, "\n")
	blocks := make([]rag.Block, 0)
	inCode := false
	codeBuf := strings.Builder{}
	codeLang := ""
	paraBuf := strings.Builder{}

	flushPara := func() {
		if paraBuf.Len() > 0 {
			blocks = append(blocks, rag.Block{
				Type:    rag.BlockParagraph,
				Content: strings.TrimSpace(paraBuf.String()),
			})
			paraBuf.Reset()
		}
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inCode {
				blocks = append(blocks, rag.Block{
					Type:     rag.BlockCode,
					Content:  strings.TrimSuffix(codeBuf.String(), "\n"),
					Language: codeLang,
				})
				codeBuf.Reset()
				inCode = false
			} else {
				flushPara()
				inCode = true
				codeLang = strings.TrimPrefix(line, "```")
			}
			continue
		}
		if inCode {
			codeBuf.WriteString(line + "\n")
			continue
		}

		if strings.HasPrefix(line, "#") {
			flushPara()
			level := 0
			for i, c := range line {
				if c == '#' {
					level = i + 1
				} else {
					break
				}
			}
			blocks = append(blocks, rag.Block{
				Type:    rag.BlockHeading,
				Content: strings.TrimSpace(line[level:]),
				Level:   level,
			})
			continue
		}

		if line == "" {
			flushPara()
			continue
		}

		if paraBuf.Len() > 0 {
			paraBuf.WriteString(" ")
		}
		paraBuf.WriteString(line)
	}
	flushPara()
	return blocks
}
