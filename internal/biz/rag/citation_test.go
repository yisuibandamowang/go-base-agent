package rag

import (
	"strings"
	"testing"
)

func TestEnrichCitationContextUsesSourceIndexesAndHidesDocumentIDs(t *testing.T) {
	context := `<documents>
<content data-ragent-doc-id="doc-b">
内容 B
</content>
<content data-ragent-doc-id="doc-a">
内容 A
</content>
</documents>`
	sources := []SourceRef{
		{Index: 1, DocID: "doc-a"},
		{Index: 2, DocID: "doc-b"},
	}

	got := EnrichCitationContext(context, sources, true)
	if !strings.Contains(got, `<content ref="2">`) || !strings.Contains(got, `<content ref="1">`) {
		t.Fatalf("expected source refs in context, got %q", got)
	}
	if strings.Contains(got, "data-ragent-doc-id") || strings.Contains(got, "doc-a") || strings.Contains(got, "doc-b") {
		t.Fatalf("expected internal document IDs to be hidden, got %q", got)
	}
}

func TestEnrichCitationContextRemovesInternalMarkerWhenDisabled(t *testing.T) {
	got := EnrichCitationContext(`<content data-ragent-doc-id="doc-a">内容</content>`, nil, false)
	if got != "<content>内容</content>" {
		t.Fatalf("expected internal marker removal, got %q", got)
	}
}

func TestStripInlineCitations(t *testing.T) {
	got := StripInlineCitations("答案。[1](#cite-1)补充。[2](#cite-9)")
	if got != "答案。补充。" {
		t.Fatalf("expected inline citations stripped, got %q", got)
	}
}
