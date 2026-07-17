package rag

import (
	"context"
	"strings"
	"testing"
)

func TestNoopParser(t *testing.T) {
	p := &NoopParser{}
	if p.Type() != ParserTika {
		t.Fatalf("unexpected type: %s", p.Type())
	}
	if !p.Supports("application/pdf") {
		t.Fatal("noop should support everything")
	}

	doc, err := p.Parse(context.Background(), []byte("hello world"), "text/plain", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(doc.Blocks))
	}
	if doc.Blocks[0].Type != BlockParagraph {
		t.Fatalf("unexpected block type: %s", doc.Blocks[0].Type)
	}
	if doc.Blocks[0].Content != "hello world" {
		t.Fatalf("unexpected content: %s", doc.Blocks[0].Content)
	}
}

func TestRenderBlocks(t *testing.T) {
	blocks := []Block{
		{Type: BlockHeading, Level: 1, Content: "Title"},
		{Type: BlockParagraph, Content: "Some text"},
		{Type: BlockCode, Language: "go", Content: "fmt.Println()"},
		{Type: BlockImage, Description: "a chart"},
	}
	result := RenderBlocks(blocks)

	if !strings.Contains(result, "# Title") {
		t.Fatal("missing heading")
	}
	if !strings.Contains(result, "Some text") {
		t.Fatal("missing paragraph")
	}
	if !strings.Contains(result, "```go") {
		t.Fatal("missing code block")
	}
	if !strings.Contains(result, "a chart") {
		t.Fatal("missing image description")
	}
}

func TestRenderBlocks_Table(t *testing.T) {
	blocks := []Block{
		{Type: BlockTable, Headers: []string{"A", "B"}, Rows: [][]string{{"1", "2"}, {"3", "4"}}},
	}
	result := RenderBlocks(blocks)
	if !strings.Contains(result, "1 | 2") {
		t.Fatalf("unexpected table output: %s", result)
	}
}
