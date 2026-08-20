package rag

import (
	"strings"
	"testing"
)

// TestAssembleSources 验证文档级来源装配：按 docId 去重保留最高分、
// 按分数降序赋号、摘录截断、url 来源带链接。
// 对齐 Java SourcesAssembler。
func TestAssembleSources(t *testing.T) {
	chunks := []RetrievedChunk{
		{ID: "c1", Text: strings.Repeat("长文本", 60), Score: 0.9, Metadata: map[string]string{
			"doc_id": "doc-a", "doc_name": "文档A", "source_type": "file", "file_type": "pdf",
		}},
		{ID: "c2", Text: "文档B片段", Score: 0.7, Metadata: map[string]string{
			"doc_id": "doc-b", "doc_name": "文档B", "source_type": "url", "source_url": "https://example.com/b",
		}},
		// 同文档低分片段：归并时不覆盖高分摘录
		{ID: "c3", Text: "文档A低分片段", Score: 0.3, Metadata: map[string]string{
			"doc_id": "doc-a", "doc_name": "文档A", "source_type": "file",
		}},
		// 无 doc_id 的片段不产生来源
		{ID: "c4", Text: "无来源片段", Score: 0.95, Metadata: map[string]string{}},
	}

	sources := AssembleSources(chunks)
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d: %+v", len(sources), sources)
	}
	// 最高分文档排前
	if sources[0].Index != 1 || sources[0].DocID != "doc-a" || sources[0].DocName != "文档A" {
		t.Fatalf("unexpected first source: %+v", sources[0])
	}
	if sources[0].FileType != "pdf" || sources[0].URL != "" {
		t.Fatalf("file source should have fileType without url: %+v", sources[0])
	}
	// 摘录截断到 100 字符加省略号
	if len([]rune(sources[0].Excerpt)) != 103 || !strings.HasSuffix(sources[0].Excerpt, "...") {
		t.Fatalf("excerpt should be truncated with ellipsis, got %d runes", len([]rune(sources[0].Excerpt)))
	}
	// url 来源带外链
	if sources[1].Index != 2 || sources[1].DocID != "doc-b" || sources[1].URL != "https://example.com/b" {
		t.Fatalf("unexpected second source: %+v", sources[1])
	}
}

// TestAssembleSourcesEmpty 验证空输入返回 nil。
func TestAssembleSourcesEmpty(t *testing.T) {
	if sources := AssembleSources(nil); sources != nil {
		t.Fatalf("expected nil, got %+v", sources)
	}
	if sources := AssembleSources([]RetrievedChunk{{ID: "c", Metadata: map[string]string{}}}); sources != nil {
		t.Fatalf("expected nil for chunks without docId, got %+v", sources)
	}
}

// TestMarshalUnmarshalSourcesRoundTrip 验证来源列表序列化往返。
func TestMarshalUnmarshalSourcesRoundTrip(t *testing.T) {
	if marshalSources(nil) != "" {
		t.Fatal("empty sources should marshal to empty string")
	}
	if unmarshalSources("") != nil {
		t.Fatal("empty raw should unmarshal to nil")
	}

	sources := []SourceRef{{Index: 1, DocID: "d1", DocName: "文档"}}
	raw := marshalSources(sources)
	if raw == "" {
		t.Fatal("expected non-empty json")
	}
	restored := unmarshalSources(raw)
	if len(restored) != 1 || restored[0].DocID != "d1" || restored[0].Index != 1 {
		t.Fatalf("round trip mismatch: %+v", restored)
	}

	if unmarshalSources("not json") != nil {
		t.Fatal("malformed json should return nil")
	}
}
