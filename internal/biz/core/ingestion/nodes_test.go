package ingestion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/chat"
)

func TestFetcherNode_ReadsLocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("hello ingestion"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ctx := &rag.IngestionContext{Source: &rag.DocumentSource{Type: "local_file", Location: path}}
	result := NewFetcherNode(nil).Execute(context.Background(), ctx, rag.NodeConfig{})
	if !result.Success {
		t.Fatalf("unexpected fetch result: %+v", result)
	}
	if string(ctx.RawBytes) != "hello ingestion" {
		t.Fatalf("unexpected raw bytes: %q", string(ctx.RawBytes))
	}
	if !strings.HasPrefix(ctx.MimeType, "text/plain") {
		t.Fatalf("expected mime type, got %q", ctx.MimeType)
	}
	if ctx.Source.FileName != "doc.txt" {
		t.Fatalf("expected source file name to be filled, got %q", ctx.Source.FileName)
	}
}

func TestFetcherNode_ReadsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("url ingestion"))
	}))
	defer srv.Close()

	ctx := &rag.IngestionContext{Source: &rag.DocumentSource{Type: "url", Location: srv.URL}}
	result := NewFetcherNode(srv.Client()).Execute(context.Background(), ctx, rag.NodeConfig{})
	if !result.Success {
		t.Fatalf("unexpected fetch result: %+v", result)
	}
	if string(ctx.RawBytes) != "url ingestion" {
		t.Fatalf("unexpected raw bytes: %q", string(ctx.RawBytes))
	}
}

type testParserRegistry struct {
	doc     *rag.ParsedDocument
	options map[string]string
}

func (r *testParserRegistry) Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error) {
	r.options = options
	return r.doc, nil
}

func TestParserNode_ParsesAndCollectsAssets(t *testing.T) {
	registry := &testParserRegistry{doc: &rag.ParsedDocument{
		Blocks: []rag.Block{
			{Type: rag.BlockParagraph, Content: "正文"},
			{Type: rag.BlockImage, Caption: "图1", Description: "a chart", Asset: rag.AssetRef{PublicURL: "https://example.com/chart.png"}},
		},
		Metadata: map[string]string{"source": "parser"},
	}}
	ctx := &rag.IngestionContext{
		TaskID:   "task-1",
		Source:   &rag.DocumentSource{Type: "file", Location: "/tmp/doc.txt", FileName: "doc.txt"},
		RawBytes: []byte("hello"),
		MimeType: "text/plain",
	}
	result := NewParserNode(registry).Execute(context.Background(), ctx, rag.NodeConfig{})
	if !result.Success {
		t.Fatalf("unexpected parse result: %+v", result)
	}
	if !strings.Contains(ctx.RawText, "正文") {
		t.Fatalf("unexpected raw text: %q", ctx.RawText)
	}
	if ctx.Document == nil || len(ctx.Document.Blocks) != 2 {
		t.Fatalf("unexpected parsed document: %+v", ctx.Document)
	}
	if len(ctx.Assets) != 1 || ctx.Assets[0].PublicURL != "https://example.com/chart.png" {
		t.Fatalf("expected asset to be collected, got %+v", ctx.Assets)
	}
	if ctx.Metadata["source"] != "parser" {
		t.Fatalf("expected metadata merge, got %+v", ctx.Metadata)
	}
}

func TestParserNode_RejectsDisallowedRuleType(t *testing.T) {
	registry := &testParserRegistry{doc: &rag.ParsedDocument{
		Blocks: []rag.Block{{Type: rag.BlockParagraph, Content: "正文"}},
	}}
	ctx := &rag.IngestionContext{
		TaskID:   "task-1",
		Source:   &rag.DocumentSource{Type: "file", Location: "/tmp/doc.txt", FileName: "doc.txt"},
		RawBytes: []byte("hello"),
		MimeType: "text/plain",
	}
	result := NewParserNode(registry).Execute(context.Background(), ctx, rag.NodeConfig{
		Settings: map[string]any{
			"rules": []any{
				map[string]any{"mimeType": "PDF"},
			},
		},
	})
	if result.Success || !strings.Contains(result.ErrorMessage, "文件类型不符合要求") {
		t.Fatalf("expected disallowed type error, got %+v", result)
	}
}

func TestParserNode_PassesMatchedRuleOptions(t *testing.T) {
	registry := &testParserRegistry{doc: &rag.ParsedDocument{
		Blocks: []rag.Block{{Type: rag.BlockParagraph, Content: "正文"}},
	}}
	ctx := &rag.IngestionContext{
		TaskID:   "task-1",
		Source:   &rag.DocumentSource{Type: "file", Location: "/tmp/doc.md", FileName: "doc.md"},
		RawBytes: []byte("# hello"),
		MimeType: "text/markdown",
	}
	result := NewParserNode(registry).Execute(context.Background(), ctx, rag.NodeConfig{
		Settings: map[string]any{
			"rules": []any{
				map[string]any{
					"mimeType": "MARKDOWN",
					"options": map[string]any{
						"sourceFile": "from-rule.md",
						"custom":     "enabled",
						"maxDepth":   3,
					},
				},
			},
		},
	})
	if !result.Success {
		t.Fatalf("unexpected parse result: %+v", result)
	}
	if registry.options["sourceFile"] != "from-rule.md" {
		t.Fatalf("expected rule sourceFile not overwritten, got %+v", registry.options)
	}
	if registry.options["custom"] != "enabled" || registry.options["maxDepth"] != "3" || registry.options["documentId"] != "task-1" {
		t.Fatalf("expected rule options to be passed to parser, got %+v", registry.options)
	}
}

type testLLM struct {
	responses []string
	calls     int
}

func (l *testLLM) Chat(ctx context.Context, req chat.Request) (string, error) {
	return l.next(), nil
}

func (l *testLLM) ChatWithModel(ctx context.Context, req chat.Request, modelID string) (string, error) {
	return l.next(), nil
}

func (l *testLLM) StreamChat(ctx context.Context, req chat.Request, cb chat.StreamCallback) (chat.StreamHandle, error) {
	panic("unused")
}

func (l *testLLM) next() string {
	if l.calls >= len(l.responses) {
		return ""
	}
	resp := l.responses[l.calls]
	l.calls++
	return resp
}

func TestEnhancerNode_UpdatesContext(t *testing.T) {
	llm := &testLLM{responses: []string{
		"增强后的内容",
		`["关键词A","关键词B"]`,
		`["问题1","问题2"]`,
		`{"domain":"membership","priority":"high"}`,
	}}
	node := NewEnhancerNode(llm)
	ctx := &rag.IngestionContext{RawText: "原始内容"}
	settings := map[string]any{
		"tasks": []any{
			map[string]any{"type": "context_enhance"},
			map[string]any{"type": "keywords"},
			map[string]any{"type": "questions"},
			map[string]any{"type": "metadata"},
		},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: settings})
	if !result.Success {
		t.Fatalf("unexpected enhancer result: %+v", result)
	}
	if ctx.EnhancedText != "增强后的内容" {
		t.Fatalf("unexpected enhanced text: %q", ctx.EnhancedText)
	}
	if len(ctx.Keywords) != 2 || ctx.Keywords[0] != "关键词A" {
		t.Fatalf("unexpected keywords: %+v", ctx.Keywords)
	}
	if len(ctx.Questions) != 2 || ctx.Questions[0] != "问题1" {
		t.Fatalf("unexpected questions: %+v", ctx.Questions)
	}
	if ctx.Metadata["domain"] != "membership" || ctx.Metadata["priority"] != "high" {
		t.Fatalf("unexpected metadata: %+v", ctx.Metadata)
	}
}

func TestEnricherNode_UpdatesChunks(t *testing.T) {
	llm := &testLLM{responses: []string{
		`["关键字1","关键字2"]`,
		"分块摘要",
		`{"chunk_label":"important"}`,
	}}
	node := NewEnricherNode(llm)
	ctx := &rag.IngestionContext{
		Metadata: map[string]any{"doc_name": "doc.txt"},
		Chunks: []rag.VectorChunk{
			{Content: "分块内容", Index: 0},
		},
	}
	settings := map[string]any{
		"tasks": []any{
			map[string]any{"type": "keywords"},
			map[string]any{"type": "summary"},
			map[string]any{"type": "metadata"},
		},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: settings})
	if !result.Success {
		t.Fatalf("unexpected enricher result: %+v", result)
	}
	if got := ctx.Chunks[0].Metadata["keywords"]; got != "关键字1,关键字2" {
		t.Fatalf("unexpected chunk keywords: %q", got)
	}
	if got := ctx.Chunks[0].Metadata["summary"]; got != "分块摘要" {
		t.Fatalf("unexpected chunk summary: %q", got)
	}
	if got := ctx.Chunks[0].Metadata["chunk_label"]; got != "important" {
		t.Fatalf("unexpected chunk metadata: %+v", ctx.Chunks[0].Metadata)
	}
	if got := ctx.Chunks[0].Metadata["doc_name"]; got != "doc.txt" {
		t.Fatalf("expected document metadata to be attached, got %+v", ctx.Chunks[0].Metadata)
	}
}

func TestChunkerNode_ChunksStructuredBlocks(t *testing.T) {
	node := NewChunkerNode()
	ctx := &rag.IngestionContext{
		Document: &rag.ParsedDocument{Blocks: []rag.Block{
			{Type: rag.BlockHeading, Level: 1, Content: "会员 Agent"},
			{Type: rag.BlockParagraph, Content: "支持权益查询"},
			{Type: rag.BlockTable, Headers: []string{"能力", "说明"}, Rows: [][]string{{"积分", "支持"}}},
		}},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: map[string]any{"chunkSize": 128}})
	if !result.Success {
		t.Fatalf("unexpected chunker result: %+v", result)
	}
	if len(ctx.Chunks) == 0 {
		t.Fatal("expected chunks to be produced")
	}
	if ctx.Chunks[len(ctx.Chunks)-1].Metadata["block_type"] != string(rag.BlockTable) {
		t.Fatalf("expected table to stay atomic, got %+v", ctx.Chunks)
	}
}

func TestChunkerNode_SplitsTableByRowsPerChunk(t *testing.T) {
	node := NewChunkerNode()
	ctx := &rag.IngestionContext{
		Document: &rag.ParsedDocument{Blocks: []rag.Block{
			{Type: rag.BlockTable, Headers: []string{"能力", "说明"}, Rows: [][]string{
				{"权益查询", "支持"},
				{"积分查询", "支持"},
				{"发票查询", "支持"},
				{"订单查询", "支持"},
				{"退款查询", "支持"},
			}},
		}},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: map[string]any{"chunkSize": 1000, "rowsPerChunk": 2}})
	if !result.Success {
		t.Fatalf("unexpected chunker result: %+v", result)
	}
	if len(ctx.Chunks) != 3 {
		t.Fatalf("expected table to split into 3 chunks, got %+v", ctx.Chunks)
	}
	for _, chunk := range ctx.Chunks {
		if !strings.Contains(chunk.Content, "能力 | 说明") {
			t.Fatalf("expected each table chunk to repeat headers, got %q", chunk.Content)
		}
		if chunk.Metadata["block_type"] != string(rag.BlockTable) {
			t.Fatalf("expected table block metadata, got %+v", chunk.Metadata)
		}
	}
	if !strings.Contains(ctx.Chunks[0].Content, "权益查询 | 支持") || !strings.Contains(ctx.Chunks[1].Content, "发票查询 | 支持") || !strings.Contains(ctx.Chunks[2].Content, "退款查询 | 支持") {
		t.Fatalf("table rows not split in order: %+v", ctx.Chunks)
	}
	if !strings.Contains(ctx.Chunks[0].EmbeddingText, "能力: 权益查询; 说明: 支持") {
		t.Fatalf("expected table embedding text to be key-value rows, got %q", ctx.Chunks[0].EmbeddingText)
	}
}

func TestChunkerNode_PassesListChunkSettings(t *testing.T) {
	node := NewChunkerNode()
	ctx := &rag.IngestionContext{
		Document: &rag.ParsedDocument{Blocks: []rag.Block{
			{Type: rag.BlockList, Items: []string{"能力一", "能力二", "能力三", "能力四", "能力五"}},
		}},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: map[string]any{
		"chunkSize":         12,
		"maxListItems":      3,
		"listItemsPerChunk": 2,
	}})
	if !result.Success {
		t.Fatalf("unexpected chunker result: %+v", result)
	}
	if len(ctx.Chunks) != 3 {
		t.Fatalf("expected long list settings to split into 3 chunks, got %+v", ctx.Chunks)
	}
	if ctx.Chunks[1].Content != "- 能力三\n- 能力四" {
		t.Fatalf("unexpected second list chunk: %q", ctx.Chunks[1].Content)
	}
}

func TestChunkerNode_ChunksFallbackText(t *testing.T) {
	node := NewChunkerNode()
	ctx := &rag.IngestionContext{
		RawText: strings.Repeat("会员Agent支持查询。", 20),
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: map[string]any{"chunkSize": 40, "overlapSize": 0}})
	if !result.Success {
		t.Fatalf("unexpected chunker result: %+v", result)
	}
	if len(ctx.Chunks) < 2 {
		t.Fatalf("expected fallback text to be split, got %+v", ctx.Chunks)
	}
}

func TestChunkerNode_WholeDocumentSentinelKeepsFallbackTextTogether(t *testing.T) {
	node := NewChunkerNode()
	text := strings.Repeat("会员Agent支持查询。", 80)
	ctx := &rag.IngestionContext{RawText: text}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: map[string]any{"chunkSize": -1}})
	if !result.Success {
		t.Fatalf("unexpected chunker result: %+v", result)
	}
	if len(ctx.Chunks) != 1 {
		t.Fatalf("expected whole document chunk, got %+v", ctx.Chunks)
	}
	if ctx.Chunks[0].Content != text || ctx.Chunks[0].Metadata["block_type"] != "document" {
		t.Fatalf("unexpected whole document chunk: %+v", ctx.Chunks[0])
	}
}

func TestChunkerNode_WholeDocumentSentinelCarriesStructuredProvenance(t *testing.T) {
	node := NewChunkerNode()
	ctx := &rag.IngestionContext{
		Document: &rag.ParsedDocument{Blocks: []rag.Block{
			{
				Type:       rag.BlockTable,
				Provenance: rag.Provenance{SourceFile: "会员.xlsx", SheetName: "权益表"},
				Headers:    []string{"能力"},
				Rows:       [][]string{{"权益查询"}},
			},
		}},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: map[string]any{"chunkSize": -1}})
	if !result.Success {
		t.Fatalf("unexpected chunker result: %+v", result)
	}
	if len(ctx.Chunks) != 1 {
		t.Fatalf("expected whole document chunk, got %+v", ctx.Chunks)
	}
	chunk := ctx.Chunks[0]
	if chunk.Provenance.SourceFile != "会员.xlsx" || chunk.Provenance.SheetName != "权益表" {
		t.Fatalf("expected whole document provenance, got %+v", chunk.Provenance)
	}
	if chunk.Metadata["source_file"] != "会员.xlsx" || chunk.Metadata["sheet_name"] != "权益表" {
		t.Fatalf("expected whole document provenance metadata, got %+v", chunk.Metadata)
	}
}

type testVectorStore struct {
	collection string
	docID      string
	chunks     []rag.VectorChunk
}

func (s *testVectorStore) IndexDocumentChunks(ctx context.Context, collectionName, docID string, chunks []rag.VectorChunk) error {
	s.collection = collectionName
	s.docID = docID
	s.chunks = append([]rag.VectorChunk(nil), chunks...)
	return nil
}
func (s *testVectorStore) UpdateChunk(ctx context.Context, collectionName, docID string, chunk rag.VectorChunk) error {
	return nil
}
func (s *testVectorStore) DeleteDocumentVectors(ctx context.Context, collectionName, docID string) error {
	return nil
}
func (s *testVectorStore) DeleteChunkByID(ctx context.Context, collectionName, chunkID string) error {
	return nil
}
func (s *testVectorStore) DeleteChunksByIDs(ctx context.Context, collectionName string, chunkIDs []string) error {
	return nil
}

type testEmbedder struct{}

func (e *testEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return []float32{1, 2, 3}, nil
}
func (e *testEmbedder) EmbedWithModel(ctx context.Context, text, modelID string) ([]float32, error) {
	return []float32{1, 2, 3}, nil
}
func (e *testEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 2, 3}
	}
	return out, nil
}
func (e *testEmbedder) EmbedBatchWithModel(ctx context.Context, texts []string, modelID string) ([][]float32, error) {
	return e.EmbedBatch(ctx, texts)
}
func (e *testEmbedder) Dimension() int { return 3 }

type recordingEmbedder struct {
	texts   []string
	modelID string
}

func (e *recordingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.texts = append(e.texts, text)
	return []float32{1, 2, 3}, nil
}
func (e *recordingEmbedder) EmbedWithModel(ctx context.Context, text, modelID string) ([]float32, error) {
	return e.Embed(ctx, text)
}
func (e *recordingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	e.texts = append(e.texts, texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 2, 3}
	}
	return out, nil
}
func (e *recordingEmbedder) EmbedBatchWithModel(ctx context.Context, texts []string, modelID string) ([][]float32, error) {
	e.modelID = modelID
	return e.EmbedBatch(ctx, texts)
}
func (e *recordingEmbedder) Dimension() int { return 3 }

func TestIndexerNode_WritesVectors(t *testing.T) {
	store := &testVectorStore{}
	node := NewIndexerNode(&testEmbedder{}, store)
	ctx := &rag.IngestionContext{
		TaskID:        "task-9",
		VectorSpaceID: "kb-col",
		Metadata:      map[string]any{"doc_name": "doc.txt"},
		Chunks: []rag.VectorChunk{
			{Content: "第一段", Index: 0},
			{Content: "第二段", Index: 1},
		},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{Settings: map[string]any{"metadataFields": []any{"doc_name"}}})
	if !result.Success {
		t.Fatalf("unexpected indexer result: %+v", result)
	}
	if store.collection != "kb-col" || store.docID != "task-9" {
		t.Fatalf("unexpected vector store args: %+v", store)
	}
	if len(store.chunks) != 2 || len(store.chunks[0].Embedding) != 3 {
		t.Fatalf("unexpected indexed chunks: %+v", store.chunks)
	}
	if got := ctx.Chunks[0].Metadata["doc_name"]; got != "doc.txt" {
		t.Fatalf("expected metadata field copied, got %q", got)
	}
}

func TestIndexerNode_EmbedsEmbeddingTextWhenPresent(t *testing.T) {
	store := &testVectorStore{}
	embedder := &recordingEmbedder{}
	node := NewIndexerNode(embedder, store)
	ctx := &rag.IngestionContext{
		TaskID:        "task-9",
		VectorSpaceID: "kb-col",
		Chunks: []rag.VectorChunk{
			{
				Content:       "能力 | 说明\n权益查询 | 支持",
				EmbeddingText: "能力: 权益查询; 说明: 支持",
				Index:         0,
			},
		},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{})
	if !result.Success {
		t.Fatalf("unexpected indexer result: %+v", result)
	}
	if len(embedder.texts) != 1 || embedder.texts[0] != "能力: 权益查询; 说明: 支持" {
		t.Fatalf("expected embedding text to be used, got %+v", embedder.texts)
	}
}

func TestIndexerNode_UsesConfiguredEmbeddingModel(t *testing.T) {
	store := &testVectorStore{}
	embedder := &recordingEmbedder{}
	node := NewIndexerNode(embedder, store)
	ctx := &rag.IngestionContext{
		TaskID:        "task-9",
		VectorSpaceID: "kb-col",
		Chunks: []rag.VectorChunk{
			{Content: "第一段", Index: 0},
		},
	}
	result := node.Execute(context.Background(), ctx, rag.NodeConfig{
		Settings: map[string]any{"embeddingModel": "emb-special"},
	})
	if !result.Success {
		t.Fatalf("unexpected indexer result: %+v", result)
	}
	if embedder.modelID != "emb-special" {
		t.Fatalf("expected configured embedding model, got %q", embedder.modelID)
	}
}
