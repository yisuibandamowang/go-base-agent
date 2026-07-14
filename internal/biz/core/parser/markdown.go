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
	listItems := make([]string, 0)
	listOrdered := false

	flushPara := func() {
		if paraBuf.Len() > 0 {
			blocks = append(blocks, rag.Block{
				Type:    rag.BlockParagraph,
				Content: strings.TrimSpace(paraBuf.String()),
			})
			paraBuf.Reset()
		}
	}
	flushList := func() {
		if len(listItems) == 0 {
			return
		}
		items := append([]string(nil), listItems...)
		blocks = append(blocks, rag.Block{
			Type:    rag.BlockList,
			Ordered: listOrdered,
			Items:   items,
		})
		listItems = listItems[:0]
		listOrdered = false
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
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
				flushList()
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
			flushList()
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

		if isMarkdownTableStart(lines, i) {
			flushPara()
			flushList()
			block, next := parseMarkdownTable(lines, i)
			blocks = append(blocks, block)
			i = next - 1
			continue
		}

		if item, ordered, ok := parseMarkdownListItem(trimmed); ok {
			flushPara()
			if len(listItems) > 0 && listOrdered != ordered {
				flushList()
			}
			listOrdered = ordered
			listItems = append(listItems, item)
			continue
		}

		if line == "" {
			flushPara()
			flushList()
			continue
		}

		flushList()
		if paraBuf.Len() > 0 {
			paraBuf.WriteString(" ")
		}
		paraBuf.WriteString(line)
	}
	flushPara()
	flushList()
	return blocks
}

func parseMarkdownListItem(line string) (string, bool, bool) {
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return strings.TrimSpace(line[2:]), false, true
	}
	for i, r := range line {
		if r < '0' || r > '9' {
			if r == '.' && i > 0 && len(line) > i+1 && line[i+1] == ' ' {
				return strings.TrimSpace(line[i+2:]), true, true
			}
			break
		}
	}
	return "", false, false
}

func isMarkdownTableStart(lines []string, idx int) bool {
	if idx+1 >= len(lines) {
		return false
	}
	header := strings.TrimSpace(lines[idx])
	separator := strings.TrimSpace(lines[idx+1])
	return strings.Contains(header, "|") && isMarkdownTableSeparator(separator)
}

func isMarkdownTableSeparator(line string) bool {
	cells := splitMarkdownTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.Trim(cell, " :-")
		if cell != "" {
			return false
		}
	}
	return true
}

func parseMarkdownTable(lines []string, idx int) (rag.Block, int) {
	headers := splitMarkdownTableRow(lines[idx])
	rows := make([][]string, 0)
	next := idx + 2
	for next < len(lines) {
		line := strings.TrimSpace(lines[next])
		if line == "" || !strings.Contains(line, "|") {
			break
		}
		rows = append(rows, splitMarkdownTableRow(line))
		next++
	}
	return rag.Block{Type: rag.BlockTable, Headers: headers, Rows: rows}, next
}

func splitMarkdownTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}
