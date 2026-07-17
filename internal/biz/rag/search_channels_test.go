package rag

import (
	"strings"
	"testing"
)

func TestParseWebSearchChunksKeepsSourceURL(t *testing.T) {
	body := []byte(`{"results":{"web":[{"url":"https://example.com/a","title":"会员Agent","description":"能力说明","snippets":["支持错误排查"]}]}}`)

	chunks := parseWebSearchChunks(body, 5)
	if len(chunks) != 1 {
		t.Fatalf("expected one chunk, got %+v", chunks)
	}
	if chunks[0].Metadata["source_url"] != "https://example.com/a" {
		t.Fatalf("expected source url metadata, got %+v", chunks[0].Metadata)
	}
	if !strings.Contains(chunks[0].Text, "支持错误排查") {
		t.Fatalf("expected snippet in text, got %q", chunks[0].Text)
	}
}
