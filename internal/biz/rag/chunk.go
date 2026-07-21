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
	ChunkSize         int // target chunk size in characters
	OverlapSize       int // overlap between chunks in characters
	MinChunkSize      int // minimum chunk size (shorter chunks merged)
	RowsPerChunk      int // maximum table rows per chunk
	MaxListItems      int // short-list atomic threshold
	ListItemsPerChunk int // maximum list items per chunk for long lists
}

// DefaultChunkingOptions returns sensible defaults for fixed-size chunking.
func DefaultChunkingOptions() ChunkingOptions {
	return ChunkingOptions{
		ChunkSize:         512,
		OverlapSize:       128,
		MinChunkSize:      100,
		RowsPerChunk:      50,
		MaxListItems:      15,
		ListItemsPerChunk: 10,
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
		provenance := blocksProvenance(blocks)
		return []VectorChunk{{
			ChunkID:       "chunk-0",
			Content:       content,
			EmbeddingText: content,
			Index:         0,
			BlockType:     "document",
			Provenance:    provenance,
			Metadata:      chunkMetadataWithSourceIDs("document", nil, "", nil, provenance),
		}}
	}
	if opts.ChunkSize <= 0 {
		opts = DefaultChunkingOptions()
	}

	var chunks []VectorChunk
	var current []string
	var currentSourceIDs []string
	var currentProvenance Provenance
	currentLen := 0
	outlinePath := make([]string, 0)

	flush := func() {
		content := strings.TrimSpace(strings.Join(current, "\n\n"))
		if content == "" {
			current = nil
			currentLen = 0
			return
		}
		chunks = append(chunks, VectorChunk{
			ChunkID:        fmt.Sprintf("chunk-%d", len(chunks)),
			Content:        content,
			EmbeddingText:  content,
			Index:          len(chunks),
			BlockType:      "mixed",
			OutlinePath:    copyStrings(outlinePath),
			SourceBlockIDs: appendUniqueStrings(nil, currentSourceIDs...),
			Provenance:     currentProvenance,
			Metadata:       chunkMetadataWithSourceIDs("mixed", outlinePath, "", currentSourceIDs, currentProvenance),
		})
		current = nil
		currentSourceIDs = nil
		currentProvenance = Provenance{}
		currentLen = 0
	}

	for blockIndex, block := range blocks {
		if block.Type == BlockHeading {
			flush()
			outlinePath = updateOutlinePath(outlinePath, block)
			continue
		}
		content := strings.TrimSpace(RenderBlocks([]Block{block}))
		if content == "" {
			continue
		}
		if block.Type == BlockTable {
			flush()
			chunks = append(chunks, chunkTableBlock(block, opts, len(chunks), outlinePath, blockSourceID(block, blockIndex))...)
			continue
		}
		if block.Type == BlockList {
			flush()
			chunks = append(chunks, chunkListBlock(block, opts, len(chunks), outlinePath, blockSourceID(block, blockIndex))...)
			continue
		}
		if isAtomicBlock(block.Type) {
			flush()
			embeddingText := content
			if block.Type == BlockImage && strings.TrimSpace(block.Description) != "" {
				embeddingText = strings.TrimSpace(block.Description)
			}
			chunks = append(chunks, VectorChunk{
				ChunkID:        fmt.Sprintf("chunk-%d", len(chunks)),
				Content:        content,
				EmbeddingText:  embeddingText,
				Index:          len(chunks),
				BlockType:      string(block.Type),
				OutlinePath:    copyStrings(outlinePath),
				SourceBlockIDs: blockSourceBlockIDs(block, blockIndex),
				Assets:         blockAssets(block),
				Provenance:     block.Provenance,
				Metadata:       chunkMetadataWithSourceIDs(string(block.Type), outlinePath, blockSectionContext(block), blockSourceBlockIDs(block, blockIndex), block.Provenance),
			})
			continue
		}
		contentLen := len([]rune(content))
		if currentLen > 0 && currentLen+contentLen > opts.ChunkSize {
			flush()
		}
		current = append(current, content)
		currentSourceIDs = appendUniqueStrings(currentSourceIDs, blockSourceID(block, blockIndex))
		currentProvenance = firstProvenance(currentProvenance, block.Provenance)
		currentLen += contentLen
	}
	flush()
	return packMergeableChunks(chunks, opts.ChunkSize, opts.OverlapSize)
}

func chunkTableBlock(block Block, opts ChunkingOptions, startIndex int, outlinePath []string, sourceBlockID string) []VectorChunk {
	headers := block.Headers
	rows := block.Rows
	if len(headers) == 0 && len(rows) == 0 {
		return nil
	}
	budget := opts.ChunkSize
	if budget <= 0 {
		budget = DefaultChunkingOptions().ChunkSize
	}
	maxRows := opts.RowsPerChunk
	if maxRows <= 0 {
		maxRows = DefaultChunkingOptions().RowsPerChunk
	}
	sectionContext := tableSectionContext(block)
	build := func(group [][]string, index int) VectorChunk {
		content := renderMarkdownTable(headers, group)
		embeddingText := tableEmbeddingText(headers, group, sectionContext)
		if strings.TrimSpace(embeddingText) == "" {
			embeddingText = content
		}
		return VectorChunk{
			ChunkID:        fmt.Sprintf("chunk-%d", index),
			Content:        content,
			EmbeddingText:  embeddingText,
			Index:          index,
			BlockType:      string(BlockTable),
			OutlinePath:    copyStrings(outlinePath),
			SourceBlockIDs: []string{sourceBlockID},
			SectionContext: sectionContext,
			Provenance:     block.Provenance,
			Metadata:       chunkMetadataWithSourceIDs(string(BlockTable), outlinePath, sectionContext, []string{sourceBlockID}, block.Provenance),
		}
	}
	if len(rows) == 0 {
		return []VectorChunk{build(nil, startIndex)}
	}

	result := make([]VectorChunk, 0, (len(rows)+maxRows-1)/maxRows)
	group := make([][]string, 0, maxRows)
	groupCost := 0
	flushGroup := func() {
		if len(group) == 0 {
			return
		}
		result = append(result, build(group, startIndex+len(result)))
		group = make([][]string, 0, maxRows)
		groupCost = 0
	}
	for _, row := range rows {
		rowCost := len([]rune(renderKeyValueTableRow(headers, row)))
		overCap := len(group) >= maxRows
		overBudget := len(group) > 0 && groupCost+rowCost > budget
		if overCap || overBudget {
			flushGroup()
		}
		group = append(group, row)
		groupCost += rowCost
	}
	flushGroup()
	return result
}

func chunkListBlock(block Block, opts ChunkingOptions, startIndex int, outlinePath []string, sourceBlockID string) []VectorChunk {
	if len(block.Items) == 0 {
		return nil
	}
	maxItems := opts.MaxListItems
	if maxItems <= 0 {
		maxItems = DefaultChunkingOptions().MaxListItems
	}
	itemsPerChunk := opts.ListItemsPerChunk
	if itemsPerChunk <= 0 {
		itemsPerChunk = DefaultChunkingOptions().ListItemsPerChunk
	}
	if len(block.Items) <= maxItems {
		return []VectorChunk{buildListChunk(block, block.Items, 1, startIndex, outlinePath, sourceBlockID)}
	}
	chunks := make([]VectorChunk, 0, (len(block.Items)+itemsPerChunk-1)/itemsPerChunk)
	for i := 0; i < len(block.Items); i += itemsPerChunk {
		end := i + itemsPerChunk
		if end > len(block.Items) {
			end = len(block.Items)
		}
		chunks = append(chunks, buildListChunk(block, block.Items[i:end], i+1, startIndex+len(chunks), outlinePath, sourceBlockID))
	}
	return chunks
}

func buildListChunk(block Block, items []string, startNumber, index int, outlinePath []string, sourceBlockID string) VectorChunk {
	var sb strings.Builder
	for i, item := range items {
		if block.Ordered {
			sb.WriteString(fmt.Sprintf("%d. ", startNumber+i))
		} else {
			sb.WriteString("- ")
		}
		sb.WriteString(item)
		if i < len(items)-1 {
			sb.WriteByte('\n')
		}
	}
	content := sb.String()
	return VectorChunk{
		ChunkID:        fmt.Sprintf("chunk-%d", index),
		Content:        content,
		EmbeddingText:  content,
		Index:          index,
		BlockType:      string(BlockList),
		OutlinePath:    copyStrings(outlinePath),
		SourceBlockIDs: []string{sourceBlockID},
		SectionContext: blockSectionContext(block),
		Provenance:     block.Provenance,
		Metadata:       chunkMetadataWithSourceIDs(string(BlockList), outlinePath, blockSectionContext(block), []string{sourceBlockID}, block.Provenance),
	}
}

func updateOutlinePath(current []string, heading Block) []string {
	level := heading.Level
	if level < 1 {
		level = 1
	}
	keep := level - 1
	if keep > len(current) {
		keep = len(current)
	}
	next := make([]string, 0, keep+1)
	next = append(next, current[:keep]...)
	next = append(next, strings.TrimSpace(heading.Content))
	return next
}

func blockAssets(block Block) []AssetRef {
	if block.Type != BlockImage || strings.TrimSpace(block.Asset.PublicURL) == "" {
		return nil
	}
	return []AssetRef{block.Asset}
}

func blockSourceBlockIDs(block Block, blockIndex int) []string {
	if block.Type == BlockImage && strings.TrimSpace(block.Asset.SourceBlockID) != "" {
		return []string{strings.TrimSpace(block.Asset.SourceBlockID)}
	}
	return []string{blockSourceID(block, blockIndex)}
}

func blockSourceID(block Block, blockIndex int) string {
	if strings.TrimSpace(block.ID) != "" {
		return strings.TrimSpace(block.ID)
	}
	if block.Type == BlockImage && strings.TrimSpace(block.Asset.SourceBlockID) != "" {
		return strings.TrimSpace(block.Asset.SourceBlockID)
	}
	blockType := strings.TrimSpace(string(block.Type))
	if blockType == "" {
		blockType = "block"
	}
	return fmt.Sprintf("%s-%d", blockType, blockIndex)
}

func firstProvenance(current, next Provenance) Provenance {
	if strings.TrimSpace(current.SourceFile) != "" || strings.TrimSpace(current.SheetName) != "" {
		return current
	}
	return next
}

func blocksProvenance(blocks []Block) Provenance {
	var provenance Provenance
	for _, block := range blocks {
		provenance = firstProvenance(provenance, block.Provenance)
	}
	return provenance
}

func blockSectionContext(block Block) string {
	if strings.TrimSpace(block.Provenance.SheetName) == "" {
		return ""
	}
	return "sheet=" + strings.TrimSpace(block.Provenance.SheetName)
}

func chunkMetadata(blockType string, outlinePath []string, sectionContext string) map[string]string {
	return chunkMetadataWithSourceIDs(blockType, outlinePath, sectionContext, nil, Provenance{})
}

func chunkMetadataWithSourceIDs(blockType string, outlinePath []string, sectionContext string, sourceBlockIDs []string, provenance Provenance) map[string]string {
	metadata := map[string]string{"block_type": blockType}
	if len(outlinePath) > 0 {
		metadata["outline_path"] = outlinePathString(outlinePath)
	}
	if strings.TrimSpace(sectionContext) != "" {
		metadata["section_context"] = sectionContext
	}
	sourceBlockIDs = appendUniqueStrings(nil, sourceBlockIDs...)
	if len(sourceBlockIDs) > 0 {
		metadata["source_block_ids"] = strings.Join(sourceBlockIDs, ",")
	}
	if strings.TrimSpace(provenance.SourceFile) != "" {
		metadata["source_file"] = strings.TrimSpace(provenance.SourceFile)
	}
	if strings.TrimSpace(provenance.SheetName) != "" {
		metadata["sheet_name"] = strings.TrimSpace(provenance.SheetName)
	}
	return metadata
}

func outlinePathString(path []string) string {
	parts := make([]string, 0, len(path))
	for _, item := range path {
		if v := strings.TrimSpace(item); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " > ")
}

func splitOutlinePath(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	raw := strings.Split(value, " > ")
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if v := strings.TrimSpace(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func packMergeableChunks(chunks []VectorChunk, maxChars, overlapChars int) []VectorChunk {
	if len(chunks) <= 1 {
		return chunks
	}
	if maxChars <= 0 {
		maxChars = DefaultChunkingOptions().ChunkSize
	}
	result := make([]VectorChunk, 0, len(chunks))
	buffer := make([]VectorChunk, 0)
	bufferLen := 0

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		if len(buffer) == 1 {
			result = append(result, buffer[0])
		} else {
			result = append(result, mergeChunks(buffer))
		}
		buffer = nil
		bufferLen = 0
	}

	for _, chunk := range chunks {
		if !isMergeableChunk(chunk, maxChars) {
			flush()
			result = append(result, chunk)
			continue
		}
		addLen := len([]rune(chunk.Content))
		sepLen := 0
		if len(buffer) > 0 {
			sepLen = 2
		}
		if len(buffer) > 0 && bufferLen+sepLen+addLen > maxChars {
			flush()
			if overlapChars > 0 && addLen+2 < maxChars {
				buffer = overlapTail(chunksForOverlap(result), minInt(overlapChars, maxChars-addLen-2))
				bufferLen = chunksContentLength(buffer)
			}
		}
		if len(buffer) > 0 {
			bufferLen += 2
		}
		buffer = append(buffer, chunk)
		bufferLen += addLen
	}
	flush()
	for i := range result {
		result[i].Index = i
		if result[i].ChunkID == "" || strings.HasPrefix(result[i].ChunkID, "chunk-") {
			result[i].ChunkID = fmt.Sprintf("chunk-%d", i)
		}
	}
	return result
}

func isMergeableChunk(chunk VectorChunk, maxChars int) bool {
	blockType := chunk.BlockType
	if chunk.Metadata != nil {
		blockType = firstNonBlank(blockType, chunk.Metadata["block_type"])
	}
	switch blockType {
	case "mixed", string(BlockParagraph), string(BlockList), string(BlockImage):
		return len([]rune(chunk.Content)) < maxChars
	default:
		return false
	}
}

func mergeChunks(chunks []VectorChunk) VectorChunk {
	contents := make([]string, 0, len(chunks))
	embeddingTexts := make([]string, 0, len(chunks))
	hasExplicitEmbedding := false
	blockType := ""
	outlinePath := []string(nil)
	sectionContext := ""
	provenance := Provenance{}
	sourceBlockIDs := make([]string, 0)
	assets := make([]AssetRef, 0)
	homogeneous := true
	for i, chunk := range chunks {
		if strings.TrimSpace(chunk.Content) != "" {
			contents = append(contents, chunk.Content)
		}
		effectiveEmbedding := chunk.Content
		if strings.TrimSpace(chunk.EmbeddingText) != "" {
			effectiveEmbedding = chunk.EmbeddingText
			hasExplicitEmbedding = true
		}
		if strings.TrimSpace(effectiveEmbedding) != "" {
			embeddingTexts = append(embeddingTexts, effectiveEmbedding)
		}
		currentType := chunk.BlockType
		if currentType == "" && chunk.Metadata != nil {
			currentType = chunk.Metadata["block_type"]
		}
		chunkOutlinePath := chunk.OutlinePath
		if len(chunkOutlinePath) == 0 && chunk.Metadata != nil {
			chunkOutlinePath = splitOutlinePath(chunk.Metadata["outline_path"])
		}
		if i == 0 {
			outlinePath = copyStrings(chunkOutlinePath)
		} else {
			outlinePath = commonStringPrefix(outlinePath, chunkOutlinePath)
		}
		sectionContext = firstNonBlank(sectionContext, chunk.SectionContext)
		if chunk.Metadata != nil {
			sectionContext = firstNonBlank(sectionContext, chunk.Metadata["section_context"])
		}
		provenance = firstProvenance(provenance, chunk.Provenance)
		if chunk.Metadata != nil {
			provenance = firstProvenance(provenance, Provenance{
				SourceFile: chunk.Metadata["source_file"],
				SheetName:  chunk.Metadata["sheet_name"],
			})
		}
		sourceBlockIDs = appendUniqueStrings(sourceBlockIDs, chunk.SourceBlockIDs...)
		assets = append(assets, chunk.Assets...)
		if chunk.Metadata != nil {
			sourceBlockIDs = appendUniqueStrings(sourceBlockIDs, splitCSVMetadata(chunk.Metadata["source_block_ids"])...)
		}
		if i == 0 {
			blockType = currentType
		} else if currentType != blockType {
			homogeneous = false
		}
	}
	if !homogeneous || blockType == "" {
		blockType = "mixed"
	}
	metadata := map[string]string{"block_type": blockType}
	if len(outlinePath) > 0 {
		metadata["outline_path"] = outlinePathString(outlinePath)
	}
	if sectionContext != "" {
		metadata["section_context"] = sectionContext
	}
	if len(sourceBlockIDs) > 0 {
		metadata["source_block_ids"] = strings.Join(sourceBlockIDs, ",")
	}
	if strings.TrimSpace(provenance.SourceFile) != "" {
		metadata["source_file"] = strings.TrimSpace(provenance.SourceFile)
	}
	if strings.TrimSpace(provenance.SheetName) != "" {
		metadata["sheet_name"] = strings.TrimSpace(provenance.SheetName)
	}
	merged := VectorChunk{
		Content:        strings.Join(contents, "\n\n"),
		Index:          chunks[0].Index,
		BlockType:      blockType,
		OutlinePath:    outlinePath,
		SourceBlockIDs: sourceBlockIDs,
		Assets:         assets,
		SectionContext: sectionContext,
		Provenance:     provenance,
		Metadata:       metadata,
	}
	if hasExplicitEmbedding {
		merged.EmbeddingText = strings.Join(embeddingTexts, "\n\n")
	} else {
		merged.EmbeddingText = merged.Content
	}
	return merged
}

func chunksForOverlap(result []VectorChunk) []VectorChunk {
	if len(result) == 0 {
		return nil
	}
	last := result[len(result)-1]
	if !isMergeableChunk(last, 1<<30) {
		return nil
	}
	return []VectorChunk{last}
}

func overlapTail(buffer []VectorChunk, budget int) []VectorChunk {
	if budget <= 0 || len(buffer) == 0 {
		return nil
	}
	carry := make([]VectorChunk, 0)
	total := 0
	for i := len(buffer) - 1; i >= 0; i-- {
		add := len([]rune(buffer[i].Content))
		if len(carry) > 0 {
			add += 2
		}
		if total+add > budget {
			break
		}
		carry = append([]VectorChunk{buffer[i]}, carry...)
		total += add
	}
	return carry
}

func chunksContentLength(chunks []VectorChunk) int {
	total := 0
	for i, chunk := range chunks {
		if i > 0 {
			total += 2
		}
		total += len([]rune(chunk.Content))
	}
	return total
}

func commonOutlinePath(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	left := strings.Split(a, " > ")
	right := strings.Split(b, " > ")
	keep := 0
	for keep < len(left) && keep < len(right) && left[keep] == right[keep] {
		keep++
	}
	return strings.Join(left[:keep], " > ")
}

func commonStringPrefix(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	keep := 0
	for keep < len(a) && keep < len(b) && a[keep] == b[keep] {
		keep++
	}
	return copyStrings(a[:keep])
}

func appendUniqueStrings(values []string, more ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(more))
	out := make([]string, 0, len(values)+len(more))
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, value := range more {
		v := strings.TrimSpace(value)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func splitCSVMetadata(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	raw := strings.Split(value, ",")
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if v := strings.TrimSpace(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func renderMarkdownTable(headers []string, rows [][]string) string {
	var sb strings.Builder
	if len(headers) > 0 {
		appendMarkdownTableRow(&sb, headers)
		sb.WriteByte('|')
		for range headers {
			sb.WriteString(" --- |")
		}
		sb.WriteByte('\n')
	}
	for _, row := range rows {
		appendMarkdownTableRow(&sb, row)
	}
	return strings.TrimSpace(sb.String())
}

func appendMarkdownTableRow(sb *strings.Builder, cells []string) {
	sb.WriteByte('|')
	for _, cell := range cells {
		sb.WriteByte(' ')
		sb.WriteString(sanitizeMarkdownTableCell(cell))
		sb.WriteString(" |")
	}
	sb.WriteByte('\n')
}

func sanitizeMarkdownTableCell(cell string) string {
	cell = strings.ReplaceAll(cell, "|", `\|`)
	cell = strings.NewReplacer("\r\n", "<br>", "\r", "<br>", "\n", "<br>").Replace(cell)
	return cell
}

func tableEmbeddingText(headers []string, rows [][]string, sectionContext string) string {
	lines := make([]string, 0, len(rows)+1)
	if strings.TrimSpace(sectionContext) != "" {
		lines = append(lines, sectionContext)
	}
	for _, row := range rows {
		if line := renderKeyValueTableRow(headers, row); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func renderKeyValueTableRow(headers []string, row []string) string {
	parts := make([]string, 0, len(row))
	for i, cell := range row {
		value := strings.TrimSpace(oneLine(cell))
		if value == "" {
			continue
		}
		key := ""
		if i < len(headers) {
			key = strings.TrimSpace(oneLine(headers[i]))
		}
		if key != "" {
			parts = append(parts, key+": "+value)
		} else {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "; ")
}

func tableSectionContext(block Block) string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(block.Provenance.SheetName) != "" {
		parts = append(parts, "sheet="+strings.TrimSpace(oneLine(block.Provenance.SheetName)))
	}
	if strings.TrimSpace(block.Caption) != "" {
		parts = append(parts, "caption="+strings.TrimSpace(oneLine(block.Caption)))
	}
	if len(block.Headers) > 0 {
		headers := make([]string, 0, len(block.Headers))
		for _, header := range block.Headers {
			if v := strings.TrimSpace(oneLine(header)); v != "" {
				headers = append(headers, v)
			}
		}
		if len(headers) > 0 {
			parts = append(parts, "headers="+strings.Join(headers, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func isAtomicBlock(typ BlockType) bool {
	switch typ {
	case BlockTable, BlockCode, BlockImage:
		return true
	default:
		return false
	}
}
