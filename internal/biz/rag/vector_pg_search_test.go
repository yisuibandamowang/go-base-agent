package rag

import (
	"strings"
	"testing"
)

func TestRefreshSearchVectorSQLUsesDocNameAndContentWeights(t *testing.T) {
	sql, args := refreshSearchVectorSQL([]string{"chunk-a", "chunk-b"})
	if !strings.Contains(sql, "setweight(to_tsvector('jiebacfg', COALESCE(d.doc_name, '')), 'A')") {
		t.Fatalf("expected doc_name A weight in SQL, got %s", sql)
	}
	if !strings.Contains(sql, "setweight(to_tsvector('jiebacfg', COALESCE(v.content, '')), 'D')") {
		t.Fatalf("expected content D weight in SQL, got %s", sql)
	}
	if len(args) != 1 {
		t.Fatalf("expected ids arg, got %v", args)
	}
	ids, ok := args[0].([]string)
	if !ok || len(ids) != 2 || ids[0] != "chunk-a" || ids[1] != "chunk-b" {
		t.Fatalf("unexpected ids arg: %#v", args[0])
	}
}
