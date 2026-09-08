package parser

import (
	"context"
	"regexp"
	"strings"

	"go-base-agent/internal/biz/rag"
)

// markdownImagePattern markdown 图片语法；独立成段的图片提升为 ImageBlock 而不是压成文字段落。
var markdownImagePattern = regexp.MustCompile(`^!\[([^\]]*)\]\(([^)]+)\)$`)

// guessImageMimeFromURL 按地址后缀猜图片 MIME：图片地址不经过字节探测，只能按扩展名给一个合理值。
func guessImageMimeFromURL(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".svg"):
		return "image/svg+xml"
	default:
		return "image/png"
	}
}

// MarkdownParser 解析 Markdown 文件。
type MarkdownParser struct{}

func (p *MarkdownParser) Type() rag.ParserType { return rag.ParserMarkdown }

func (p *MarkdownParser) Supports(mimeType string) bool {
	mimeType = normalizeMIMEType(mimeType)
	return mimeType == "text/markdown" || mimeType == "text/x-markdown" ||
		mimeType == "text/plain" || strings.HasSuffix(mimeType, "/markdown")
}

func (p *MarkdownParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
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
			content := strings.TrimSpace(paraBuf.String())
			// 独占一行的图片按图片块产出，而不是压成一段只剩 alt 文本的文字
			//（对齐 Java MarkdownDocumentParser.asStandaloneImage）
			if block, ok := standaloneImageBlock(content); ok {
				blocks = append(blocks, block)
				paraBuf.Reset()
				return
			}
			blocks = append(blocks, rag.Block{
				Type:    rag.BlockParagraph,
				Content: content,
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

		// MinerU 的表格以原始 HTML 嵌在 markdown 里：单拎出来按 HTML 表格块产出，
		// 落成段落会被按字符硬切、断面停在标签中间（对齐 Java UnpackVisitor.visit(HtmlBlock)）
		if strings.HasPrefix(strings.ToLower(trimmed), "<table") {
			flushPara()
			flushList()
			var htmlLines []string
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				htmlLines = append(htmlLines, lines[i])
				i++
			}
			blocks = append(blocks, rag.Block{
				Type:    rag.BlockHtmlTable,
				Content: strings.Join(htmlLines, "\n"),
			})
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

// standaloneImageBlock 判断段落内容是否只是一张 markdown 图片（允许图片带 title），
// 是则产出 ImageBlock（地址是作者写的原样地址，不经过资产上传，向量文本由分块阶段回落到链接本身）。
func standaloneImageBlock(content string) (rag.Block, bool) {
	if !strings.HasPrefix(content, "![") {
		return rag.Block{}, false
	}
	match := markdownImagePattern.FindStringSubmatch(content)
	if match == nil {
		return rag.Block{}, false
	}
	altText := strings.TrimSpace(match[1])
	dest := strings.TrimSpace(match[2])
	// 带标题语法 ![alt](url "title") 的 dest 含空白，拆出纯地址部分
	if idx := strings.IndexAny(dest, " \t"); idx >= 0 {
		dest = strings.TrimSpace(dest[:idx])
	}
	if dest == "" {
		return rag.Block{}, false
	}
	return rag.Block{
		Type:    rag.BlockImage,
		Caption: altText,
		AltText: altText,
		Asset:   rag.AssetRef{PublicURL: dest, Mime: guessImageMimeFromURL(dest)},
	}, true
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
