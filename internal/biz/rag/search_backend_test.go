package rag

import (
	"errors"
	"strings"
	"testing"
)

func TestPGJiebaKeywordSearchQueryBindsArgumentsInSQLPlaceholderOrder(t *testing.T) {
	sql, args := pgJiebaKeywordSearchQuery("member", "扶摇 tag 去重", 7)
	if !strings.Contains(sql, "websearch_to_tsquery('jiebacfg', ?)") {
		t.Fatalf("expected pg_jieba websearch query, got %s", sql)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %v", args)
	}
	if args[0] != "扶摇 tag 去重" || args[1] != "member" || args[2] != 7 {
		t.Fatalf("unexpected args order: %v", args)
	}
}

func TestPGJiebaWebSearchTextUsesSignalTermsWithOr(t *testing.T) {
	got := pgJiebaWebSearchText("扶摇线上tag去重问题是什么导致的?")
	for _, want := range []string{"扶摇", "线上", "tag", "去重"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q to contain %q", got, want)
		}
	}
	if !strings.Contains(got, " OR ") {
		t.Fatalf("expected signal terms to be joined by OR, got %q", got)
	}
}

func TestMergeKeywordChunksKeepsStrongestDedupedAnchors(t *testing.T) {
	got := mergeKeywordChunks(2,
		[]RetrievedChunk{
			{ID: "b", Score: 3},
			{ID: "a", Score: 2},
		},
		[]RetrievedChunk{
			{ID: "a", Score: 8},
			{ID: "c", Score: 5},
		},
	)
	if len(got) != 2 {
		t.Fatalf("expected top 2 chunks, got %v", got)
	}
	if got[0].ID != "a" || got[0].Score != 8 {
		t.Fatalf("expected strongest duplicate first, got %+v", got[0])
	}
	if got[1].ID != "c" || got[1].Score != 5 {
		t.Fatalf("expected second strongest chunk, got %+v", got[1])
	}
}

func TestIsPGJiebaUnavailableError(t *testing.T) {
	cases := []error{
		errors.New(`ERROR: text search configuration "jiebacfg" does not exist (SQLSTATE 42704)`),
		errors.New(`ERROR: column v.search_vector does not exist (SQLSTATE 42703)`),
		errors.New(`ERROR: type "tsvector" does not exist (SQLSTATE 42704)`),
	}
	for _, err := range cases {
		if !isPGJiebaUnavailableError(err) {
			t.Fatalf("expected %v to be treated as pg_jieba unavailable", err)
		}
	}
	if isPGJiebaUnavailableError(errors.New("database timeout")) {
		t.Fatal("expected unrelated database error not to be treated as pg_jieba unavailable")
	}
}
