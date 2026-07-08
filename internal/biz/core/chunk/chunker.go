package chunk

import (
	"strings"

	"go-base-agent/internal/biz/rag"
)

// ParagraphChunker 按双换行符分隔段落进行分块。
type ParagraphChunker struct{}

func (p *ParagraphChunker) Mode() rag.ChunkingMode { return "PARAGRAPH" }

func (p *ParagraphChunker) Chunk(text string, opts rag.ChunkingOptions) []rag.VectorChunk {
	paragraphs := strings.Split(text, "\n\n")
	return chunkWithOverlap(paragraphs, opts)
}

// SemanticChunker 按 Markdown 标题层级进行语义分块。
type SemanticChunker struct{}

func (s *SemanticChunker) Mode() rag.ChunkingMode { return "SEMANTIC" }

func (s *SemanticChunker) Chunk(text string, opts rag.ChunkingOptions) []rag.VectorChunk {
	sections := splitByHeadings(text)
	return chunkWithOverlap(sections, opts)
}

func splitByHeadings(text string) []string {
	lines := strings.Split(text, "\n")
	sections := make([]string, 0)
	current := strings.Builder{}
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			if current.Len() > 0 {
				sections = append(sections, strings.TrimSpace(current.String()))
				current.Reset()
			}
			current.WriteString(line + "\n")
			continue
		}
		current.WriteString(line + "\n")
	}
	if current.Len() > 0 {
		sections = append(sections, strings.TrimSpace(current.String()))
	}
	return sections
}

func chunkWithOverlap(segments []string, opts rag.ChunkingOptions) []rag.VectorChunk {
	if opts.ChunkSize <= 0 {
		opts = rag.DefaultChunkingOptions()
	}
	chunks := make([]rag.VectorChunk, 0)
	index := 0
	for _, seg := range segments {
		for _, sub := range splitToSize(seg, opts.ChunkSize, opts.OverlapSize) {
			chunks = append(chunks, rag.VectorChunk{
				Index:    index,
				Content:  sub,
				Metadata: map[string]string{},
			})
			index++
		}
	}
	return chunks
}

func splitToSize(text string, chunkSize, overlap int) []string {
	runes := []rune(text)
	if len(runes) <= chunkSize {
		if len(runes) >= overlap/2 {
			return []string{text}
		}
		return nil
	}
	step := chunkSize - overlap
	if step <= 0 {
		step = chunkSize
	}
	result := make([]string, 0)
	for i := 0; i < len(runes); i += step {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		result = append(result, string(runes[i:end]))
	}
	return result
}

// StrategyFactory 根据策略名称创建分块器。
func StrategyFactory(mode rag.ChunkingMode) rag.ChunkingStrategy {
	switch mode {
	case "PARAGRAPH":
		return &ParagraphChunker{}
	case "SEMANTIC":
		return &SemanticChunker{}
	default:
		return &rag.FixedSizeChunker{}
	}
}

// ListStrategies 返回所有可用分块策略。
func ListStrategies() []map[string]string {
	return []map[string]string{
		{"code": "FIXED_SIZE", "name": "固定大小分块"},
		{"code": "PARAGRAPH", "name": "段落分块"},
		{"code": "SEMANTIC", "name": "语义分块（Markdown标题）"},
	}
}
