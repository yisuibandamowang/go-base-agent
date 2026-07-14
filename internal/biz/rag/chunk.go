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

// StructureAwareChunker chunks parsed document blocks without splitting atomic blocks.
type StructureAwareChunker struct{}

func (s *StructureAwareChunker) Mode() ChunkingMode { return ChunkModeStructureAware }

func (s *StructureAwareChunker) Chunk(text string, opts ChunkingOptions) []VectorChunk {
	blocks := []Block{{Type: BlockParagraph, Content: text}}
	return s.ChunkBlocks(blocks, opts)
}

// ChunkBlocks chunks structured document blocks while preserving tables, code blocks and images.
func (s *StructureAwareChunker) ChunkBlocks(blocks []Block, opts ChunkingOptions) []VectorChunk {
	if len(blocks) == 0 {
		return nil
	}
	if opts.ChunkSize == -1 {
		content := strings.TrimSpace(RenderBlocks(blocks))
		if content == "" {
			return nil
		}
		return []VectorChunk{{
			ChunkID:       "chunk-0",
			Content:       content,
			EmbeddingText: content,
			Index:         0,
			Metadata:      map[string]string{"block_type": "document"},
		}}
	}
	if opts.ChunkSize <= 0 {
		opts = DefaultChunkingOptions()
	}

	var chunks []VectorChunk
	var current []string
	currentLen := 0

	flush := func() {
		content := strings.TrimSpace(strings.Join(current, "\n\n"))
		if content == "" {
			current = nil
			currentLen = 0
			return
		}
		chunks = append(chunks, VectorChunk{
			ChunkID:       fmt.Sprintf("chunk-%d", len(chunks)),
			Content:       content,
			EmbeddingText: content,
			Index:         len(chunks),
			Metadata:      map[string]string{"block_type": "mixed"},
		})
		current = nil
		currentLen = 0
	}

	for _, block := range blocks {
		content := strings.TrimSpace(RenderBlocks([]Block{block}))
		if content == "" {
			continue
		}
		if isAtomicBlock(block.Type) {
			flush()
			chunks = append(chunks, VectorChunk{
				ChunkID:       fmt.Sprintf("chunk-%d", len(chunks)),
				Content:       content,
				EmbeddingText: content,
				Index:         len(chunks),
				Metadata:      map[string]string{"block_type": string(block.Type)},
			})
			continue
		}
		contentLen := len([]rune(content))
		if currentLen > 0 && currentLen+contentLen > opts.ChunkSize {
			flush()
		}
		current = append(current, content)
		currentLen += contentLen
	}
	flush()
	return chunks
}

func isAtomicBlock(typ BlockType) bool {
	switch typ {
	case BlockTable, BlockCode, BlockImage:
		return true
	default:
		return false
	}
}
