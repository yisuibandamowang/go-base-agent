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

func TestRegistryLayoutMIMERoutesToMinerUOnEveryProfile(t *testing.T) {
	// 版面类文档（PDF/Word/PPT）无论 FAST/FIDELITY 档都由 MinerU 认领（对齐 Java LAYOUT_MIME_TYPES）
	for _, profile := range []string{"", "fast", "fidelity"} {
		registry := NewRegistry(nil)
		registry.Register(profileTestParser{typ: rag.ParserMinerU})

		doc, err := registry.Parse(context.Background(), []byte("doc"), "application/pdf", map[string]string{"parseProfile": profile})
		if err != nil {
			t.Fatalf("parse pdf with profile %q: %v", profile, err)
		}
		if doc.Metadata["parser"] != string(rag.ParserMinerU) {
			t.Fatalf("profile %q: unexpected parser: %+v", profile, doc.Metadata)
		}
	}
}

func TestRegistryFastProfileDoesNotFallForwardToFidelityParser(t *testing.T) {
	// 表格类 MIME 在快速档不归 MinerU：仅有 MinerU 时应拒绝，而不是前向落到高成本解析
	registry := NewRegistry(nil)
	registry.Register(profileTestParser{typ: rag.ParserMinerU})

	if _, err := registry.Parse(context.Background(), []byte("doc"), "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", map[string]string{"parseProfile": "fast"}); err == nil {
		t.Fatal("expected fast profile to reject spreadsheet when only fidelity parser is available")
	}
}

func TestRegistryFidelitySpreadsheetRoutesToMinerU(t *testing.T) {
	registry := NewRegistry(nil)
	registry.Register(profileTestParser{typ: rag.ParserMinerU})

	doc, err := registry.Parse(context.Background(), []byte("doc"), "application/vnd.ms-excel", map[string]string{"parseProfile": "fidelity"})
	if err != nil {
		t.Fatalf("parse xls with fidelity profile: %v", err)
	}
	if doc.Metadata["parser"] != string(rag.ParserMinerU) {
		t.Fatalf("unexpected parser: %+v", doc.Metadata)
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

func TestClaimsMinerUMatrix(t *testing.T) {
	cases := []struct {
		mime    string
		profile string
		want    bool
	}{
		{"application/pdf", "", true},
		{"application/x-pdf; charset=binary", "fast", true},
		{"application/msword", "fidelity", true},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "fidelity", true},
		{"application/vnd.ms-excel", "fidelity", true},
		{"application/vnd.ms-excel", "", false},
		{"application/vnd.ms-excel", "fast", false},
		{"text/markdown", "fidelity", false},
	}
	for _, c := range cases {
		if got := claimsMinerU(c.mime, c.profile); got != c.want {
			t.Fatalf("claimsMinerU(%q, %q) = %v, want %v", c.mime, c.profile, got, c.want)
		}
	}
}
