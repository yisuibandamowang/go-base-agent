package parser

import (
	"context"
	"testing"

	"go-base-agent/internal/biz/rag"
)

type profileTestParser struct {
	typ rag.ParserType
}

func (p profileTestParser) Type() rag.ParserType { return p.typ }
func (p profileTestParser) Supports(string) bool { return true }
func (p profileTestParser) Parse(context.Context, []byte, string, map[string]string) (*rag.ParsedDocument, error) {
	return &rag.ParsedDocument{Metadata: map[string]string{"parser": string(p.typ)}}, nil
}

func TestRegistryFastProfileDoesNotFallForwardToFidelityParser(t *testing.T) {
	registry := NewRegistry(nil)
	registry.Register(profileTestParser{typ: rag.ParserMinerU})

	if _, err := registry.Parse(context.Background(), []byte("doc"), "application/pdf", map[string]string{"parseProfile": "fast"}); err == nil {
		t.Fatal("expected fast profile to reject when only fidelity parser is available")
	}
}

func TestRegistryFidelityProfileFallsBackToFastParser(t *testing.T) {
	registry := NewRegistry(nil)
	registry.Register(profileTestParser{typ: rag.ParserMarkdown})

	doc, err := registry.Parse(context.Background(), []byte("doc"), "text/markdown", map[string]string{"parseProfile": "fidelity"})
	if err != nil {
		t.Fatalf("parse with fidelity fallback: %v", err)
	}
	if doc.Metadata["parser"] != string(rag.ParserMarkdown) {
		t.Fatalf("unexpected parser: %+v", doc.Metadata)
	}
}
