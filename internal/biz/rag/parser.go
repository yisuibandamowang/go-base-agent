package rag

import (
	"context"
	"fmt"
	"strings"
)

// ParserType enumerates supported document parser types.
// Aligns with Java ParserType.
type ParserType string

const (
	ParserTika     ParserType = "TIKA"
	ParserMarkdown ParserType = "MARKDOWN"
	ParserExcelPOI ParserType = "EXCEL_POI"
	ParserCSV      ParserType = "CSV"
	ParserMinerU   ParserType = "MINERU"
	ParserImage    ParserType = "IMAGE"
)

// BlockType classifies a document block element.
type BlockType string

const (
	BlockHeading   BlockType = "heading"
	BlockParagraph BlockType = "paragraph"
	BlockTable     BlockType = "table"
	BlockImage     BlockType = "image"
	BlockCode      BlockType = "code"
	BlockList      BlockType = "list"
)

// AssetRef is a reference to an external asset (e.g. extracted image).
type AssetRef struct {
	PublicURL     string
	Mime          string
	SourceBlockID string
}

// Provenance describes where a parsed block came from.
// Aligns with Java Provenance.
type Provenance struct {
	SourceFile string
	SheetName  string
}

// Block represents a parsed document block element.
// Aligns with Java Block (sealed interface) + subtypes.
type Block struct {
	ID         string
	Type       BlockType
	Content    string
	Provenance Provenance
	// for HeadingBlock
	Level int
	// for TableBlock
	Headers []string
	Rows    [][]string
	Caption string
	// for ListBlock
	Ordered bool
	Items   []string
	// for ImageBlock
	Asset       AssetRef
	AltText     string
	Description string
	// for CodeBlock
	Language string
}

// ParsedDocument is the output of a document parser.
type ParsedDocument struct {
	Blocks   []Block
	Metadata map[string]string
}

// DocumentParser parses document bytes into structured blocks.
// Aligns with Java DocumentParser.
type DocumentParser interface {
	Type() ParserType
	Supports(mimeType string) bool
	Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*ParsedDocument, error)
}

// NoopParser is a fallback parser that returns raw text as a single paragraph.
type NoopParser struct{}

func (n *NoopParser) Type() ParserType              { return ParserTika }
func (n *NoopParser) Supports(mimeType string) bool { return true }
func (n *NoopParser) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*ParsedDocument, error) {
	return &ParsedDocument{
		Blocks: []Block{{Type: BlockParagraph, Content: string(data)}},
	}, nil
}

// RenderBlocks converts a list of blocks to plain text.
// Aligns with Java BlockTextRenderer.
func RenderBlocks(blocks []Block) string {
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case BlockHeading:
			level := b.Level
			if level < 1 {
				level = 1
			}
			sb.WriteString(strings.Repeat("#", level))
			sb.WriteByte(' ')
			sb.WriteString(b.Content)
			sb.WriteString("\n\n")
		case BlockParagraph:
			sb.WriteString(b.Content)
			sb.WriteString("\n\n")
		case BlockCode:
			sb.WriteString("```")
			sb.WriteString(b.Language)
			sb.WriteByte('\n')
			sb.WriteString(b.Content)
			sb.WriteString("\n```\n\n")
		case BlockList:
			for i, item := range b.Items {
				if b.Ordered {
					sb.WriteString(fmt.Sprintf("%d. ", i+1))
				} else {
					sb.WriteString("- ")
				}
				sb.WriteString(item)
				sb.WriteByte('\n')
			}
			if len(b.Items) > 0 {
				sb.WriteByte('\n')
			}
		case BlockTable:
			if len(b.Headers) > 0 {
				sb.WriteString(strings.Join(b.Headers, " | "))
				sb.WriteByte('\n')
			}
			for _, row := range b.Rows {
				sb.WriteString(strings.Join(row, " | "))
				sb.WriteByte('\n')
			}
			sb.WriteByte('\n')
		case BlockImage:
			if desc := strings.TrimSpace(b.Description); desc != "" {
				sb.WriteString(desc)
				sb.WriteString("\n\n")
			}
			sb.WriteString("![")
			sb.WriteString(b.Caption)
			sb.WriteString("](")
			sb.WriteString(b.Asset.PublicURL)
			sb.WriteString(")\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}
