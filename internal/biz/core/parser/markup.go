package parser

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"go-base-agent/internal/biz/rag"

	"golang.org/x/net/html"
)

// HTMLParser extracts visible text from HTML documents.
type HTMLParser struct{}

func (p *HTMLParser) Type() rag.ParserType { return "HTML" }

func (p *HTMLParser) Supports(mimeType string) bool {
	lower := normalizeMIMEType(mimeType)
	return lower == "text/html" || lower == "application/xhtml+xml"
}

func (p *HTMLParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	blocks := make([]rag.Block, 0)
	walkHTML(doc, &blocks)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("parse html: empty content")
	}
	return &rag.ParsedDocument{
		Blocks:   blocks,
		Metadata: map[string]string{"mime": mimeType, "method": "html"},
	}, nil
}

func walkHTML(n *html.Node, blocks *[]rag.Block) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		name := strings.ToLower(n.Data)
		if name == "script" || name == "style" || name == "noscript" {
			return
		}
		if level := htmlHeadingLevel(name); level > 0 {
			if text := htmlNodeText(n); text != "" {
				*blocks = append(*blocks, rag.Block{Type: rag.BlockHeading, Content: text, Level: level})
			}
			return
		}
		switch name {
		case "title", "p", "li", "blockquote":
			if text := htmlNodeText(n); text != "" {
				*blocks = append(*blocks, rag.Block{Type: rag.BlockParagraph, Content: text})
			}
			return
		case "pre":
			if text := htmlNodeText(n); text != "" {
				*blocks = append(*blocks, rag.Block{Type: rag.BlockCode, Content: text})
			}
			return
		case "tr":
			if row := htmlTableRowText(n); row != "" {
				*blocks = append(*blocks, rag.Block{Type: rag.BlockParagraph, Content: row})
			}
			return
		}
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, blocks)
	}
}

func htmlHeadingLevel(name string) int {
	if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
		return int(name[1] - '0')
	}
	return 0
}

func htmlTableRowText(n *html.Node) string {
	cells := make([]string, 0)
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		name := strings.ToLower(child.Data)
		if name != "td" && name != "th" {
			continue
		}
		if text := htmlNodeText(child); text != "" {
			cells = append(cells, text)
		}
	}
	return strings.Join(cells, " | ")
}

func htmlNodeText(n *html.Node) string {
	var b strings.Builder
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.TextNode {
			if text := strings.TrimSpace(node.Data); text != "" {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString(text)
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// XMLParser extracts text from XML and JSON-like text payloads.
type XMLParser struct{}

func (p *XMLParser) Type() rag.ParserType { return "XML" }

func (p *XMLParser) Supports(mimeType string) bool {
	lower := normalizeMIMEType(mimeType)
	return lower == "text/xml" || lower == "application/xml"
}

func (p *XMLParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	blocks, err := xmlTextBlocks(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parse xml: %w", err)
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("parse xml: empty content")
	}
	return &rag.ParsedDocument{
		Blocks:   blocks,
		Metadata: map[string]string{"mime": mimeType, "method": "xml"},
	}, nil
}

func xmlTextBlocks(r io.Reader) ([]rag.Block, error) {
	decoder := xml.NewDecoder(r)
	var paragraph strings.Builder
	blocks := make([]rag.Block, 0)
	flush := func() {
		text := strings.Join(strings.Fields(paragraph.String()), " ")
		if text != "" {
			blocks = append(blocks, rag.Block{Type: rag.BlockParagraph, Content: text})
		}
		paragraph.Reset()
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" {
				if paragraph.Len() > 0 {
					paragraph.WriteString(" ")
				}
				paragraph.WriteString(text)
			}
		case xml.EndElement:
			if isXMLBlockBoundary(t.Name.Local) {
				flush()
			}
		}
	}
	flush()
	return blocks, nil
}

func isXMLBlockBoundary(name string) bool {
	switch strings.ToLower(name) {
	case "p", "paragraph", "para", "section", "item", "li", "title", "row", "record", "entry", "line":
		return true
	default:
		return false
	}
}
