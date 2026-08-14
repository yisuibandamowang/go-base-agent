package codeqna

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchFindsCodeEvidenceFromQuestion(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "service.go")
	if err := os.WriteFile(file, []byte(`package demo

func HandleConversionEventQbusMessage() {}

type Report struct {}
`), 0o644); err != nil {
		t.Fatalf("write temp code file: %v", err)
	}

	items, err := Search(context.Background(), SearchRequest{
		RepoPath: dir,
		Question: "HandleConversionEventQbusMessage 在哪里",
		MaxLines: 10,
	})
	if err != nil {
		t.Fatalf("search code evidence: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected code evidence, got none")
	}
	if !strings.Contains(items[0].Content, "HandleConversionEventQbusMessage") {
		t.Fatalf("expected function name in first evidence, got %+v", items[0])
	}
}
