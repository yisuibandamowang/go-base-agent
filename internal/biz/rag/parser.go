package rag

import "context"

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

// Block represents a parsed document block element.
// Aligns with Java Block (sealed interface) + subtypes.
type Block struct {
	Type    BlockType
	Content string
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
	Parse(ctx context.Context, data []byte, mimeType string) (*ParsedDocument, error)
}

// NoopParser is a fallback parser that returns raw text as a single paragraph.
type NoopParser struct{}

func (n *NoopParser) Type() ParserType              { return ParserTika }
func (n *NoopParser) Supports(mimeType string) bool { return true }
func (n *NoopParser) Parse(ctx context.Context, data []byte, mimeType string) (*ParsedDocument, error) {
	return &ParsedDocument{
		Blocks: []Block{{Type: BlockParagraph, Content: string(data)}},
	}, nil
}

// RenderBlocks converts a list of blocks to plain text.
// Aligns with Java BlockTextRenderer.
func RenderBlocks(blocks []Block) string {
	var result string
	for _, b := range blocks {
		if result != "" {
			result += "\n"
		}
		switch b.Type {
		case BlockHeading:
			for i := 0; i < b.Level; i++ {
				result += "#"
			}
			result += " " + b.Content
		case BlockParagraph:
			result += b.Content
		case BlockCode:
			result += "```" + b.Language + "\n" + b.Content + "\n```"
		case BlockList:
			for i, item := range b.Items {
				if i > 0 {
					result += "\n"
				}
				if b.Ordered {
					result += "- " + item
				} else {
					result += "- " + item
				}
			}
		case BlockTable:
			for _, row := range b.Rows {
				for j, cell := range row {
					if j > 0 {
						result += " | "
					}
					result += cell
				}
				result += "\n"
			}
		case BlockImage:
			if b.Description != "" {
				result += "[Image: " + b.Description + "]"
			} else if b.AltText != "" {
				result += "[Image: " + b.AltText + "]"
			}
		}
	}
	return result
}
