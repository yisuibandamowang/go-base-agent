package rag

import (
	"strings"
	"testing"
)

func TestDefaultPromptBuilder_WithCodeContext(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:    "这段代码为什么会空指针",
		CodeContext: "<code-documents>\nfunc Foo() {}\n</code-documents>",
	})

	content := req.Messages[1].Content
	if !strings.Contains(content, "<code-documents>") {
		t.Fatalf("expected code context in prompt, got %q", content)
	}
	if !strings.Contains(content, "代码仓库证据") {
		t.Fatalf("expected code-oriented instruction, got %q", content)
	}
}

func TestDefaultPromptBuilder_CodeAndKbContextTogether(t *testing.T) {
	b := NewDefaultPromptBuilder()
	req := b.Build(PromptContext{
		Question:    "这个字段在哪写入",
		KbContext:   "<documents>kb</documents>",
		CodeContext: "<code-documents>code</code-documents>",
		McpContext:  "<tool-data>mcp</tool-data>",
	})

	content := req.Messages[1].Content
	for _, want := range []string{"<documents>", "<code-documents>", "<tool-data>"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected prompt to contain %s, got %q", want, content)
		}
	}
	if !strings.Contains(content, "代码仓库证据") {
		t.Fatalf("expected combined instruction to mention code evidence, got %q", content)
	}
}
