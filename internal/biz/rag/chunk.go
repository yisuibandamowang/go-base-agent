package rag

import (
	"fmt"
	"strings"
)

// ChunkingMode enumerates supported chunking strategies.
// Aligns with Java ChunkingMode.
type ChunkingMode string

const (
	ChunkModeFixedSize      ChunkingMode = "FIXED_SIZE"
	ChunkModeStructureAware ChunkingMode = "STRUCTURE_AWARE"
)

// ChunkingOptions holds common chunking parameters.
type ChunkingOptions struct {
	ChunkSize    int // target chunk size in characters
	OverlapSize  int // overlap between chunks in characters
	MinChunkSize int // minimum chunk size (shorter chunks merged)
}

// DefaultChunkingOptions returns sensible defaults for fixed-size chunking.
func DefaultChunkingOptions() ChunkingOptions {
	return ChunkingOptions{
		ChunkSize:    512,
		OverlapSize:  128,
		MinChunkSize: 100,
	}
}

// ChunkingStrategy splits text into VectorChunks.
// Aligns with Java ChunkingStrategy.
type ChunkingStrategy interface {
	Mode() ChunkingMode
	Chunk(text string, opts ChunkingOptions) []VectorChunk
}

// FixedSizeChunker splits text by character count with overlap.
type FixedSizeChunker struct{}

func (f *FixedSizeChunker) Mode() ChunkingMode { return ChunkModeFixedSize }

func (f *FixedSizeChunker) Chunk(text string, opts ChunkingOptions) []VectorChunk {
	if opts.ChunkSize <= 0 {
		opts = DefaultChunkingOptions()
	}
	runes := []rune(text)
	total := len(runes)
	if total == 0 {
		return nil
	}

	var chunks []VectorChunk
	step := opts.ChunkSize - opts.OverlapSize
	if step <= 0 {
		step = opts.ChunkSize
	}

	for start := 0; start < total; start += step {
		end := start + opts.ChunkSize
		if end > total {
			end = total
		}
		content := string(runes[start:end])
		chunks = append(chunks, VectorChunk{
			ChunkID:       fmt.Sprintf("chunk-%d", len(chunks)),
			Content:       content,
			EmbeddingText: content,
			Index:         len(chunks),
		})

		if end >= total {
			break
		}
	}

	if len(chunks) > 1 && opts.MinChunkSize > 0 {
		chunks = mergeSmallChunks(chunks, opts.MinChunkSize)
	}

	return chunks
}

// mergeSmallChunks merges trailing chunks smaller than minSize into the previous one.
func mergeSmallChunks(chunks []VectorChunk, minSize int) []VectorChunk {
	if len(chunks) <= 1 {
		return chunks
	}
	result := make([]VectorChunk, 0, len(chunks))
	for i := range chunks {
		if i == len(chunks)-1 && len([]rune(chunks[i].Content)) < minSize {
			prev := &result[len(result)-1]
			prev.Content += "\n" + chunks[i].Content
			prev.EmbeddingText = prev.Content
		} else {
			result = append(result, chunks[i])
		}
	}
	return result
}

// NoopChunker returns the entire text as a single chunk.
type NoopChunker struct{}

func (n *NoopChunker) Mode() ChunkingMode { return ChunkModeFixedSize }
func (n *NoopChunker) Chunk(text string, opts ChunkingOptions) []VectorChunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []VectorChunk{{
		ChunkID:       "chunk-0",
		Content:       text,
		EmbeddingText: text,
		Index:         0,
	}}
}
