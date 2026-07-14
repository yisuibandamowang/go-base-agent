package parser

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"go-base-agent/internal/biz/rag"

	pdf "github.com/ledongthuc/pdf"
)

// PDFParser 解析 PDF 文件，纯 Go 实现（无 cgo 依赖）。
// 解析文本内容为扁平化 paragraph block。
type PDFParser struct{}

func (p *PDFParser) Type() rag.ParserType { return "PDF" }

func (p *PDFParser) Supports(mimeType string) bool {
	return mimeType == "application/pdf"
}

func (p *PDFParser) Parse(ctx context.Context, data []byte, mimeType string) (*rag.ParsedDocument, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("parse pdf reader: %w", err)
	}
	plain, err := reader.GetPlainText()
	if err != nil {
		return nil, fmt.Errorf("extract pdf text: %w", err)
	}
	textBytes, err := io.ReadAll(plain)
	if err != nil {
		return nil, fmt.Errorf("read pdf text: %w", err)
	}
	blocks := textToParagraphBlocks(string(textBytes))
	if len(blocks) == 0 {
		return nil, fmt.Errorf("extract pdf text: empty content")
	}
	return &rag.ParsedDocument{
		Blocks:   blocks,
		Metadata: map[string]string{"mime": mimeType, "method": "pdf"},
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
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open docx zip: %w", err)
	}
	var document *zip.File
	for _, f := range reader.File {
		if f.Name == "word/document.xml" {
			document = f
			break
		}
	}
	if document == nil {
		return nil, fmt.Errorf("docx document.xml not found")
	}
	rc, err := document.Open()
	if err != nil {
		return nil, fmt.Errorf("open docx document.xml: %w", err)
	}
	defer rc.Close()

	paragraphs, err := docxParagraphs(rc)
	if err != nil {
		return nil, fmt.Errorf("parse docx document.xml: %w", err)
	}
	blocks := make([]rag.Block, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			blocks = append(blocks, rag.Block{Type: rag.BlockParagraph, Content: paragraph})
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("extract docx text: empty content")
	}
	return &rag.ParsedDocument{
		Blocks:   blocks,
		Metadata: map[string]string{"mime": mimeType, "method": "docx"},
	}, nil
}

func docxParagraphs(r io.Reader) ([]string, error) {
	decoder := xml.NewDecoder(r)
	var paragraphs []string
	var current strings.Builder
	inParagraph := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "p" {
				inParagraph = true
				current.Reset()
			}
		case xml.CharData:
			if inParagraph {
				current.Write([]byte(t))
			}
		case xml.EndElement:
			if t.Name.Local == "p" && inParagraph {
				paragraphs = append(paragraphs, current.String())
				inParagraph = false
			}
		}
	}
	return paragraphs, nil
}

func textToParagraphBlocks(text string) []rag.Block {
	parts := strings.Split(text, "\n")
	blocks := make([]rag.Block, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			blocks = append(blocks, rag.Block{Type: rag.BlockParagraph, Content: part})
		}
	}
	return blocks
}
