package ingestion

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go-base-agent/internal/biz/core/parser"
	"go-base-agent/internal/biz/rag"
)

// DefaultEngine 默认文档入库 Pipeline 引擎。
type DefaultEngine struct {
	registry     *parser.Registry
	vectorStore  rag.VectorStoreService
	embedder     EmbedderFunc
	chunkOptions rag.ChunkingOptions
}

// EmbedderFunc 向量化函数签名。
type EmbedderFunc func(ctx context.Context, texts []string) ([][]float32, error)

// NewDefaultEngine 创建默认入库引擎。
func NewDefaultEngine(reg *parser.Registry, vs rag.VectorStoreService, embed EmbedderFunc) *DefaultEngine {
	return &DefaultEngine{
		registry:     reg,
		vectorStore:  vs,
		embedder:     embed,
		chunkOptions: rag.DefaultChunkingOptions(),
	}
}

// Run 执行完整入库流程：解析 → 分块 → 向量化 → 写入向量库。
func (e *DefaultEngine) Run(ctx context.Context, taskID, collectionName, docID string, data []byte, mimeType string) ([]rag.VectorChunk, error) {
	start := time.Now()
	slog.Info("ingestion pipeline started", "taskID", taskID, "docID", docID, "mime", mimeType, "size", len(data))

	parsed, err := e.registry.Parse(ctx, data, mimeType, map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("parse failed: %w", err)
	}
	chunker := &rag.StructureAwareChunker{}
	chunks := chunker.ChunkBlocks(parsed.Blocks, e.chunkOptions)
	if len(chunks) == 0 {
		chunks = chunker.Chunk(rag.RenderBlocks(parsed.Blocks), e.chunkOptions)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no chunks produced")
	}

	for i := range chunks {
		chunks[i].DocID = docID
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = embeddingTextOf(c)
	}
	embeddings, err := e.embedder(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed failed: %w", err)
	}
	for i := range chunks {
		if i < len(embeddings) {
			chunks[i].Embedding = embeddings[i]
		}
	}

	if err := e.vectorStore.IndexDocumentChunks(ctx, collectionName, docID, chunks); err != nil {
		return nil, fmt.Errorf("vector write failed: %w", err)
	}

	slog.Info("ingestion pipeline completed", "taskID", taskID, "chunks", len(chunks), "duration", time.Since(start))
	return chunks, nil
}
