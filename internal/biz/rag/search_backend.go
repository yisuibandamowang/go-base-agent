package rag

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"

	"gorm.io/gorm"
)

// KnowledgeSearchBackend provides lexical and fallback retrieval capabilities.
type KnowledgeSearchBackend interface {
	ListKnowledgeBases(ctx context.Context) ([]knowledgeModel.KnowledgeBase, error)
	SearchKeywordChunks(ctx context.Context, kb knowledgeModel.KnowledgeBase, query string, topK int) ([]RetrievedChunk, error)
	SearchRecentChunks(ctx context.Context, collectionName string, topK int) ([]RetrievedChunk, error)
	MatchIntentCollections(ctx context.Context, query string, limit int) ([]string, error)
}

type PgKnowledgeSearchBackend struct {
	vectorDB *gorm.DB
	kbRepo   knowledgeBaseLister
}

var _ KnowledgeSearchBackend = (*PgKnowledgeSearchBackend)(nil)

// NewPgKnowledgeSearchBackend creates a backend that keeps PG-specific query logic out of the business layer.
func NewPgKnowledgeSearchBackend(vectorDB *gorm.DB, kbRepo knowledgeBaseLister) *PgKnowledgeSearchBackend {
	return &PgKnowledgeSearchBackend{vectorDB: vectorDB, kbRepo: kbRepo}
}

// NewKnowledgeSearchBackend creates the business-neutral search backend alias.
func NewKnowledgeSearchBackend(vectorDB *gorm.DB, kbRepo knowledgeBaseLister) *PgKnowledgeSearchBackend {
	return NewPgKnowledgeSearchBackend(vectorDB, kbRepo)
}

// ListKnowledgeBases returns all known knowledge bases.
func (b *PgKnowledgeSearchBackend) ListKnowledgeBases(ctx context.Context) ([]knowledgeModel.KnowledgeBase, error) {
	if b == nil || b.kbRepo == nil {
		return nil, nil
	}
	kbs, _, err := b.kbRepo.List(ctx, 1, 100, "")
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	return kbs, nil
}

// SearchKeywordChunks performs simple PostgreSQL content keyword recall.
func (b *PgKnowledgeSearchBackend) SearchKeywordChunks(ctx context.Context, kb knowledgeModel.KnowledgeBase, query string, topK int) ([]RetrievedChunk, error) {
	if b == nil || b.vectorDB == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	type row struct {
		ID             string  `gorm:"column:id"`
		Content        string  `gorm:"column:content"`
		Metadata       string  `gorm:"column:metadata"`
		DocName        string  `gorm:"column:doc_name"`
		SourceLocation string  `gorm:"column:source_location"`
		FileURL        string  `gorm:"column:file_url"`
		Score          float64 `gorm:"column:score"`
	}
	terms := keywordSearchTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	scoreParts := make([]string, 0, len(terms)*2)
	whereParts := make([]string, 0, len(terms))
	scoreArgs := make([]any, 0, len(terms)*2)
	whereArgs := make([]any, 0, len(terms)*2)
	for _, term := range terms {
		pattern := "%" + term + "%"
		scoreParts = append(scoreParts,
			"CASE WHEN LOWER(COALESCE(d.doc_name, '')) LIKE LOWER(?) THEN 4 ELSE 0 END",
			"CASE WHEN LOWER(v.content) LIKE LOWER(?) THEN 1 ELSE 0 END",
		)
		scoreArgs = append(scoreArgs, pattern, pattern)
		whereParts = append(whereParts, "(LOWER(COALESCE(d.doc_name, '')) LIKE LOWER(?) OR LOWER(v.content) LIKE LOWER(?))")
		whereArgs = append(whereArgs, pattern, pattern)
	}
	args := make([]any, 0, len(scoreArgs)+1+len(whereArgs)+1)
	args = append(args, scoreArgs...)
	args = append(args, kb.CollectionName)
	args = append(args, whereArgs...)
	args = append(args, topK)
	rows := make([]row, 0)
	err := b.vectorDB.WithContext(ctx).Raw(
		`SELECT v.id, v.content, v.metadata, d.doc_name, d.source_location, d.file_url, (`+strings.Join(scoreParts, " + ")+`)::float AS score
		 FROM t_knowledge_vector v
		 LEFT JOIN t_knowledge_document d
		   ON d.id = v.metadata::jsonb->>'doc_id'
		  AND d.deleted = 0
		 WHERE v.collection_name = ?
		   AND (`+strings.Join(whereParts, " OR ")+`)
		 ORDER BY score DESC, v.create_time DESC
		 LIMIT ?`,
		args...,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search keyword chunks: %w", err)
	}
	chunks := make([]RetrievedChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, RetrievedChunk{
			ID:       row.ID,
			Text:     row.Content,
			Score:    row.Score,
			Metadata: metadataWithSources(parseVectorMetadata(row.Metadata), knowledgeModel.KnowledgeBase(kb), row.DocName, row.SourceLocation, row.FileURL),
		})
	}
	return chunks, nil
}

func keywordSearchTerms(query string) []string {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return nil
	}
	seen := make(map[string]bool)
	terms := make([]string, 0)
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] || isKeywordStopTerm(term) {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}

	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() > 0 {
			add(latin.String())
			latin.Reset()
		}
	}
	flushCJK := func() {
		if len(cjk) >= 2 {
			for i := 0; i+1 < len(cjk); i++ {
				add(string(cjk[i : i+2]))
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range query {
		switch {
		case isCJKRune(r):
			flushLatin()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			latin.WriteRune(unicode.ToLower(r))
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	if len(terms) > 16 {
		return terms[:16]
	}
	return terms
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

func isKeywordStopTerm(term string) bool {
	switch term {
	case "什么", "是什", "怎么", "如何", "为什", "为何", "原因", "导致", "的是", "什么原因":
		return true
	default:
		return false
	}
}

// SearchRecentChunks returns the newest chunks in a collection as a fallback path.
func (b *PgKnowledgeSearchBackend) SearchRecentChunks(ctx context.Context, collectionName string, topK int) ([]RetrievedChunk, error) {
	if b == nil || b.vectorDB == nil {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	type row struct {
		ID             string  `gorm:"column:id"`
		Content        string  `gorm:"column:content"`
		Metadata       string  `gorm:"column:metadata"`
		DocName        string  `gorm:"column:doc_name"`
		SourceLocation string  `gorm:"column:source_location"`
		FileURL        string  `gorm:"column:file_url"`
		Score          float64 `gorm:"column:score"`
	}
	rows := make([]row, 0)
	err := b.vectorDB.WithContext(ctx).Raw(
		`SELECT v.id, v.content, v.metadata, d.doc_name, d.source_location, d.file_url, 0.7 AS score
		 FROM t_knowledge_vector v
		 LEFT JOIN t_knowledge_document d
		   ON d.id = v.metadata::jsonb->>'doc_id'
		  AND d.deleted = 0
		 WHERE v.collection_name = ?
		 ORDER BY v.create_time DESC
		 LIMIT ?`,
		collectionName, topK,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search recent chunks: %w", err)
	}
	chunks := make([]RetrievedChunk, 0, len(rows))
	for _, row := range rows {
		chunks = append(chunks, RetrievedChunk{
			ID:       row.ID,
			Text:     row.Content,
			Score:    row.Score,
			Metadata: metadataWithSources(parseVectorMetadata(row.Metadata), knowledgeModel.KnowledgeBase{CollectionName: collectionName}, row.DocName, row.SourceLocation, row.FileURL),
		})
	}
	return chunks, nil
}

// MatchIntentCollections returns collections whose intent node content matches the query.
func (b *PgKnowledgeSearchBackend) MatchIntentCollections(ctx context.Context, query string, limit int) ([]string, error) {
	if b == nil || b.vectorDB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	like := "%" + strings.TrimSpace(query) + "%"
	type row struct {
		CollectionName string `gorm:"column:collection_name"`
	}
	rows := make([]row, 0)
	err := b.vectorDB.WithContext(ctx).Raw(
		`SELECT DISTINCT collection_name
		 FROM t_intent_node
		 WHERE deleted = 0
		   AND enabled = 1
		   AND collection_name <> ''
		   AND (
		     LOWER(name) LIKE LOWER(?)
		     OR LOWER(description) LIKE LOWER(?)
		     OR LOWER(examples) LIKE LOWER(?)
		   )
		 ORDER BY collection_name ASC
		 LIMIT ?`,
		like, like, like, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("match intent collections: %w", err)
	}
	collections := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.CollectionName) != "" {
			collections = append(collections, row.CollectionName)
		}
	}
	return collections, nil
}
