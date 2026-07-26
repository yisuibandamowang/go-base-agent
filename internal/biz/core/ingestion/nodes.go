package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	corechunk "go-base-agent/internal/biz/core/chunk"
	"go-base-agent/internal/biz/rag"
	"go-base-agent/internal/infra/chat"
	"go-base-agent/internal/infra/embedding"
)

type parserRegistry interface {
	Parse(ctx context.Context, data []byte, mimeType string, options map[string]string) (*rag.ParsedDocument, error)
}

type parserSettings struct {
	Rules []parserRule `json:"rules"`
}

type parserRule struct {
	MimeType string         `json:"mimeType"`
	Options  map[string]any `json:"options"`
}

// FetcherNode loads document bytes from a local file or HTTP URL.
type FetcherNode struct {
	client *http.Client
}

// NewFetcherNode creates a new fetcher node.
func NewFetcherNode(client *http.Client) *FetcherNode {
	if client == nil {
		client = http.DefaultClient
	}
	return &FetcherNode{client: client}
}

// NodeType returns the node type.
func (n *FetcherNode) NodeType() rag.IngestionNodeType { return rag.NodeFetcher }

// Execute fetches document bytes into the ingestion context.
func (n *FetcherNode) Execute(ctx context.Context, nodeCtx *rag.IngestionContext, config rag.NodeConfig) rag.NodeResult {
	if nodeCtx == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "ingestion context is nil"}
	}
	if len(nodeCtx.RawBytes) > 0 {
		if strings.TrimSpace(nodeCtx.MimeType) == "" {
			nodeCtx.MimeType = detectMimeType(nodeCtx.Source, nodeCtx.RawBytes)
		}
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "已跳过获取器：原始字节已存在"}
	}
	source := nodeCtx.Source
	if source == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "文档来源不能为空"}
	}
	sourceType := normalizeSourceType(source.Type)
	switch sourceType {
	case "file":
		data, err := os.ReadFile(strings.TrimSpace(source.Location))
		if err != nil {
			return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("读取文件失败: %v", err)}
		}
		nodeCtx.RawBytes = data
		nodeCtx.MimeType = firstNonEmpty(nodeCtx.MimeType, detectMimeType(source, data))
		if strings.TrimSpace(source.FileName) == "" {
			source.FileName = filepath.Base(source.Location)
		}
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: fmt.Sprintf("已获取 %d 字节", len(data))}
	case "url":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(source.Location), nil)
		if err != nil {
			return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("创建请求失败: %v", err)}
		}
		if source.Credentials != nil {
			for k, v := range source.Credentials {
				if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
					req.Header.Set(k, v)
				}
			}
		}
		resp, err := n.client.Do(req)
		if err != nil {
			return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("获取 URL 失败: %v", err)}
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("获取 URL 失败: status=%d", resp.StatusCode)}
		}
		data, err := readAllLimited(resp.Body, 20<<20)
		if err != nil {
			return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("读取响应失败: %v", err)}
		}
		nodeCtx.RawBytes = data
		nodeCtx.MimeType = firstNonEmpty(nodeCtx.MimeType, normalizeContentType(resp.Header.Get("Content-Type")), detectMimeType(source, data))
		if strings.TrimSpace(source.FileName) == "" {
			if name := filenameFromContentDisposition(resp.Header.Get("Content-Disposition")); name != "" {
				source.FileName = name
			} else if u, err := url.Parse(source.Location); err == nil {
				source.FileName = filepath.Base(u.Path)
			}
		}
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: fmt.Sprintf("已获取 %d 字节", len(data))}
	default:
		return rag.NodeResult{Success: false, ErrorMessage: "不支持的来源类型: " + source.Type}
	}
}

// ParserNode parses raw bytes into a structured document.
type ParserNode struct {
	registry parserRegistry
}

// NewParserNode creates a parser node.
func NewParserNode(registry parserRegistry) *ParserNode {
	return &ParserNode{registry: registry}
}

// NodeType returns the node type.
func (n *ParserNode) NodeType() rag.IngestionNodeType { return rag.NodeParser }

// Execute parses the raw bytes and updates the ingestion context.
func (n *ParserNode) Execute(ctx context.Context, nodeCtx *rag.IngestionContext, config rag.NodeConfig) rag.NodeResult {
	if nodeCtx == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "ingestion context is nil"}
	}
	if len(nodeCtx.RawBytes) == 0 {
		return rag.NodeResult{Success: false, ErrorMessage: "解析器缺少原始字节"}
	}
	if strings.TrimSpace(nodeCtx.MimeType) == "" {
		nodeCtx.MimeType = detectMimeType(nodeCtx.Source, nodeCtx.RawBytes)
	}
	if n.registry == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "解析器注册表未配置"}
	}

	fileName := ""
	sourceURL := ""
	if nodeCtx.Source != nil {
		fileName = nodeCtx.Source.FileName
		sourceURL = nodeCtx.Source.Location
	}
	settings := parserSettingsFromConfig(config.Settings)
	if err := validateParserSettings(settings, nodeCtx.MimeType, fileName); err != nil {
		return rag.NodeResult{Success: false, ErrorMessage: err.Error()}
	}
	rule := matchParserRule(settings, nodeCtx.MimeType, fileName)
	options := map[string]string{}
	if rule != nil {
		for k, v := range rule.Options {
			if strings.TrimSpace(k) == "" {
				continue
			}
			options[k] = stringifyAny(v)
		}
	}
	if strings.TrimSpace(options["sourceFile"]) == "" && strings.TrimSpace(fileName) != "" {
		options["sourceFile"] = fileName
	}
	if strings.TrimSpace(options["documentId"]) == "" {
		options["documentId"] = firstNonEmpty(nodeCtx.TaskID, nodeCtx.PipelineID, fileName)
	}
	if strings.TrimSpace(options["sourceURL"]) == "" && strings.TrimSpace(sourceURL) != "" {
		options["sourceURL"] = sourceURL
	}
	if strings.TrimSpace(options["sourceType"]) == "" {
		options["sourceType"] = firstNonEmpty(sourceTypeOrDefault(nodeCtx.Source), "file")
	}
	parsed, err := n.registry.Parse(ctx, nodeCtx.RawBytes, nodeCtx.MimeType, options)
	if err != nil {
		return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("文件解析失败: %v", err)}
	}
	nodeCtx.Document = parsed
	nodeCtx.RawText = strings.TrimSpace(rag.RenderBlocks(parsed.Blocks))
	if nodeCtx.Metadata == nil {
		nodeCtx.Metadata = make(map[string]any)
	}
	for k, v := range parsed.Metadata {
		nodeCtx.Metadata[k] = v
	}
	nodeCtx.Assets = collectAssets(parsed.Blocks)
	if nodeCtx.RawText == "" {
		return rag.NodeResult{Success: false, ErrorMessage: "文件无有效文本内容"}
	}
	return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: fmt.Sprintf("解析完成, blocks=%d, 文本长度=%d", len(parsed.Blocks), len(nodeCtx.RawText))}
}

// EnhancerNode applies document-level LLM enhancements.
type EnhancerNode struct {
	llm chat.LLMService
}

// NewEnhancerNode creates an enhancer node.
func NewEnhancerNode(llm chat.LLMService) *EnhancerNode {
	return &EnhancerNode{llm: llm}
}

// NodeType returns the node type.
func (n *EnhancerNode) NodeType() rag.IngestionNodeType { return rag.NodeEnhancer }

// Execute enriches the raw document text with LLM tasks.
func (n *EnhancerNode) Execute(ctx context.Context, nodeCtx *rag.IngestionContext, config rag.NodeConfig) rag.NodeResult {
	if nodeCtx == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "ingestion context is nil"}
	}
	if n.llm == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "LLM 服务未配置"}
	}
	settings := enhancerSettingsFromConfig(config.Settings)
	if len(settings.Tasks) == 0 {
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "未配置增强任务"}
	}
	if nodeCtx.Metadata == nil {
		nodeCtx.Metadata = make(map[string]any)
	}
	for _, task := range settings.Tasks {
		if strings.TrimSpace(task.Type) == "" {
			continue
		}
		input := enhancerInputForType(nodeCtx, task.Type)
		if strings.TrimSpace(input) == "" {
			continue
		}
		systemPrompt := firstNonEmpty(task.SystemPrompt, defaultEnhancerSystemPrompt(task.Type))
		userPrompt := renderPromptTemplate(task.UserPromptTemplate, map[string]any{
			"text":       input,
			"content":    input,
			"mimeType":   nodeCtx.MimeType,
			"taskId":     nodeCtx.TaskID,
			"pipelineId": nodeCtx.PipelineID,
		})
		req := chat.Request{Messages: []chat.Message{chat.NewSystemMessage(systemPrompt), chat.NewUserMessage(userPrompt)}}
		resp, err := n.callChat(ctx, settings.ModelID, req)
		if err != nil {
			return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("增强失败: %v", err)}
		}
		applyEnhancerResult(nodeCtx, task.Type, resp)
	}
	return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "增强完成"}
}

// EnricherNode applies chunk-level LLM enrichment.
type EnricherNode struct {
	llm chat.LLMService
}

// NewEnricherNode creates an enricher node.
func NewEnricherNode(llm chat.LLMService) *EnricherNode {
	return &EnricherNode{llm: llm}
}

// NodeType returns the node type.
func (n *EnricherNode) NodeType() rag.IngestionNodeType { return rag.NodeEnricher }

// Execute enriches document chunks with LLM tasks.
func (n *EnricherNode) Execute(ctx context.Context, nodeCtx *rag.IngestionContext, config rag.NodeConfig) rag.NodeResult {
	if nodeCtx == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "ingestion context is nil"}
	}
	if n.llm == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "LLM 服务未配置"}
	}
	if len(nodeCtx.Chunks) == 0 {
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "No chunks to enrich"}
	}
	settings := enricherSettingsFromConfig(config.Settings)
	if len(settings.Tasks) == 0 {
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "No enricher tasks configured"}
	}
	attachMetadata := settings.AttachDocumentMetadata == nil || *settings.AttachDocumentMetadata
	for i := range nodeCtx.Chunks {
		chunk := &nodeCtx.Chunks[i]
		if strings.TrimSpace(chunk.Content) == "" {
			continue
		}
		if chunk.Metadata == nil {
			chunk.Metadata = make(map[string]string)
		}
		if attachMetadata {
			for k, v := range nodeCtx.Metadata {
				chunk.Metadata[k] = stringifyAny(v)
			}
		}
		for _, task := range settings.Tasks {
			if strings.TrimSpace(task.Type) == "" {
				continue
			}
			systemPrompt := firstNonEmpty(task.SystemPrompt, defaultEnricherSystemPrompt(task.Type))
			userPrompt := renderPromptTemplate(task.UserPromptTemplate, map[string]any{
				"text":       chunk.Content,
				"content":    chunk.Content,
				"chunkIndex": chunk.Index,
				"taskId":     nodeCtx.TaskID,
				"pipelineId": nodeCtx.PipelineID,
			})
			req := chat.Request{Messages: []chat.Message{chat.NewSystemMessage(systemPrompt), chat.NewUserMessage(userPrompt)}}
			resp, err := n.callChat(ctx, settings.ModelID, req)
			if err != nil {
				return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("分块增强失败: %v", err)}
			}
			applyEnricherResult(chunk, task.Type, resp)
		}
	}
	return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "Enricher completed"}
}

// ChunkerNode chunks parsed text or structured blocks.
type ChunkerNode struct{}

// NewChunkerNode creates a chunker node.
func NewChunkerNode() *ChunkerNode {
	return &ChunkerNode{}
}

// NodeType returns the node type.
func (n *ChunkerNode) NodeType() rag.IngestionNodeType { return rag.NodeChunker }

// Execute chunks the current document into vector-ready pieces.
func (n *ChunkerNode) Execute(ctx context.Context, nodeCtx *rag.IngestionContext, config rag.NodeConfig) rag.NodeResult {
	_ = ctx
	if nodeCtx == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "ingestion context is nil"}
	}
	settings := chunkerSettingsFromConfig(config.Settings)
	opts := rag.DefaultChunkingOptions()
	if settings.ChunkSize != nil {
		opts.ChunkSize = *settings.ChunkSize
	}
	if settings.OverlapSize != nil {
		opts.OverlapSize = *settings.OverlapSize
	}
	if settings.RowsPerChunk != nil && *settings.RowsPerChunk > 0 {
		opts.RowsPerChunk = *settings.RowsPerChunk
	}
	if settings.MaxListItems != nil && *settings.MaxListItems > 0 {
		opts.MaxListItems = *settings.MaxListItems
	}
	if settings.ListItemsPerChunk != nil && *settings.ListItemsPerChunk > 0 {
		opts.ListItemsPerChunk = *settings.ListItemsPerChunk
	}
	text := nodeCtx.EnhancedText
	if strings.TrimSpace(text) == "" {
		text = nodeCtx.RawText
	}
	if opts.ChunkSize == -1 {
		if nodeCtx.Document != nil && len(nodeCtx.Document.Blocks) > 0 {
			chunks := (&rag.StructureAwareChunker{}).ChunkBlocks(nodeCtx.Document.Blocks, opts)
			if len(chunks) == 0 {
				return rag.NodeResult{Success: false, ErrorMessage: "分块结果为空"}
			}
			nodeCtx.Chunks = chunks
			return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "已分块 1 段, path=whole-document"}
		}
		if strings.TrimSpace(text) == "" && nodeCtx.Document != nil {
			text = rag.RenderBlocks(nodeCtx.Document.Blocks)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return rag.NodeResult{Success: false, ErrorMessage: "可分块文本为空"}
		}
		nodeCtx.Chunks = []rag.VectorChunk{{
			ChunkID:       "chunk-0",
			Content:       text,
			EmbeddingText: text,
			Index:         0,
			Metadata:      map[string]string{"block_type": "document"},
		}}
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: "已分块 1 段, path=whole-document"}
	}
	if nodeCtx.Document != nil && len(nodeCtx.Document.Blocks) > 0 {
		chunks := (&rag.StructureAwareChunker{}).ChunkBlocks(nodeCtx.Document.Blocks, opts)
		if len(chunks) == 0 {
			return rag.NodeResult{Success: false, ErrorMessage: "分块结果为空"}
		}
		nodeCtx.Chunks = chunks
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: fmt.Sprintf("已分块 %d 段, path=block-aware", len(chunks))}
	}
	if strings.TrimSpace(text) == "" {
		return rag.NodeResult{Success: false, ErrorMessage: "可分块文本为空"}
	}
	chunks := fallbackTextChunker(settings.Strategy).Chunk(text, opts)
	if len(chunks) == 0 {
		return rag.NodeResult{Success: false, ErrorMessage: "分块结果为空"}
	}
	normalizeFallbackChunks(chunks)
	nodeCtx.Chunks = chunks
	return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: fmt.Sprintf("已分块 %d 段, path=legacy-text", len(chunks))}
}

// IndexerNode embeds chunks and writes them to a vector store.
type IndexerNode struct {
	embedder embedding.Service
	store    rag.VectorStoreService
}

// NewIndexerNode creates an indexer node.
func NewIndexerNode(embedder embedding.Service, store rag.VectorStoreService) *IndexerNode {
	return &IndexerNode{embedder: embedder, store: store}
}

// NodeType returns the node type.
func (n *IndexerNode) NodeType() rag.IngestionNodeType { return rag.NodeIndexer }

// Execute indexes the chunks into the configured vector store.
func (n *IndexerNode) Execute(ctx context.Context, nodeCtx *rag.IngestionContext, config rag.NodeConfig) rag.NodeResult {
	if nodeCtx == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "ingestion context is nil"}
	}
	if n.embedder == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "embedding 服务未配置"}
	}
	if n.store == nil {
		return rag.NodeResult{Success: false, ErrorMessage: "vector store 未配置"}
	}
	if len(nodeCtx.Chunks) == 0 {
		return rag.NodeResult{Success: false, ErrorMessage: "没有可索引的分块"}
	}
	settings := indexerSettingsFromConfig(config.Settings)
	collectionName := firstNonEmpty(settings.CollectionName, nodeCtx.VectorSpaceID)
	if strings.TrimSpace(collectionName) == "" {
		return rag.NodeResult{Success: false, ErrorMessage: "索引器需要指定集合名称"}
	}
	texts := make([]string, 0, len(nodeCtx.Chunks))
	for _, chunk := range nodeCtx.Chunks {
		text := embeddingTextOf(chunk)
		if strings.TrimSpace(text) == "" {
			continue
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return rag.NodeResult{Success: false, ErrorMessage: "所有分块内容均为空"}
	}
	embeddings, err := n.embedder.EmbedBatchWithModel(ctx, texts, settings.EmbeddingModel)
	if err != nil {
		return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("向量化失败: %v", err)}
	}
	if len(embeddings) == 0 {
		return rag.NodeResult{Success: false, ErrorMessage: "向量结果缺失"}
	}

	embeddedChunks := make([]rag.VectorChunk, 0, len(texts))
	textIdx := 0
	for i := range nodeCtx.Chunks {
		if strings.TrimSpace(nodeCtx.Chunks[i].Content) == "" {
			continue
		}
		chunk := nodeCtx.Chunks[i]
		if textIdx < len(embeddings) {
			chunk.Embedding = embeddings[textIdx]
		}
		if chunk.ChunkID == "" {
			chunk.ChunkID = fmt.Sprintf("%s-%d", firstNonEmpty(nodeCtx.TaskID, nodeCtx.PipelineID, "chunk"), i)
		}
		if chunk.Metadata == nil {
			chunk.Metadata = make(map[string]string)
		}
		if nodeCtx.Source != nil {
			chunk.Metadata["source_type"] = nodeCtx.Source.Type
			chunk.Metadata["source_location"] = nodeCtx.Source.Location
		}
		chunk.Metadata["task_id"] = nodeCtx.TaskID
		chunk.Metadata["pipeline_id"] = nodeCtx.PipelineID
		for _, field := range settings.MetadataFields {
			if strings.TrimSpace(field) == "" {
				continue
			}
			if value, ok := lookupAnyMetadata(nodeCtx, chunk, field); ok {
				chunk.Metadata[field] = stringifyAny(value)
			}
		}
		nodeCtx.Chunks[i] = chunk
		embeddedChunks = append(embeddedChunks, chunk)
		textIdx++
	}
	if len(embeddedChunks) == 0 {
		return rag.NodeResult{Success: false, ErrorMessage: "所有分块向量化失败"}
	}
	if nodeCtx.SkipIndexerWrite {
		return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: fmt.Sprintf("已准备 %d 个分块（向量写入由调用方统一完成）", len(embeddedChunks))}
	}
	if err := n.store.IndexDocumentChunks(ctx, collectionName, firstNonEmpty(nodeCtx.TaskID, nodeCtx.PipelineID), embeddedChunks); err != nil {
		return rag.NodeResult{Success: false, ErrorMessage: fmt.Sprintf("向量写入失败: %v", err)}
	}
	return rag.NodeResult{Success: true, ShouldContinue: true, ErrorMessage: fmt.Sprintf("已写入 %d 个分块到集合 %s", len(embeddedChunks), collectionName)}
}

type enhancerTaskSettings struct {
	Type               string `json:"type"`
	SystemPrompt       string `json:"systemPrompt"`
	UserPromptTemplate string `json:"userPromptTemplate"`
}

type enhancerSettings struct {
	ModelID string                 `json:"modelId"`
	Tasks   []enhancerTaskSettings `json:"tasks"`
}

type enricherTaskSettings struct {
	Type               string `json:"type"`
	SystemPrompt       string `json:"systemPrompt"`
	UserPromptTemplate string `json:"userPromptTemplate"`
}

type enricherSettings struct {
	ModelID                string                 `json:"modelId"`
	AttachDocumentMetadata *bool                  `json:"attachDocumentMetadata"`
	Tasks                  []enricherTaskSettings `json:"tasks"`
}

type chunkerSettings struct {
	Strategy          *string `json:"strategy"`
	ChunkSize         *int    `json:"chunkSize"`
	OverlapSize       *int    `json:"overlapSize"`
	RowsPerChunk      *int    `json:"rowsPerChunk"`
	MaxListItems      *int    `json:"maxListItems"`
	ListItemsPerChunk *int    `json:"listItemsPerChunk"`
}

type indexerSettings struct {
	EmbeddingModel string   `json:"embeddingModel"`
	CollectionName string   `json:"collectionName"`
	MetadataFields []string `json:"metadataFields"`
}

func enhancerSettingsFromConfig(settings map[string]any) enhancerSettings {
	var out enhancerSettings
	_ = decodeSettings(settings, &out)
	return out
}

func enricherSettingsFromConfig(settings map[string]any) enricherSettings {
	var out enricherSettings
	_ = decodeSettings(settings, &out)
	return out
}

func chunkerSettingsFromConfig(settings map[string]any) chunkerSettings {
	var out chunkerSettings
	_ = decodeSettings(settings, &out)
	return out
}

func indexerSettingsFromConfig(settings map[string]any) indexerSettings {
	var out indexerSettings
	_ = decodeSettings(settings, &out)
	return out
}

func fallbackTextChunker(strategy *string) rag.ChunkingStrategy {
	switch normalizeChunkingStrategy(strategy) {
	case "fixed_size":
		return &rag.FixedSizeChunker{}
	case "structure_aware":
		return &corechunk.SemanticChunker{}
	default:
		return &corechunk.SemanticChunker{}
	}
}

func normalizeChunkingStrategy(strategy *string) string {
	if strategy == nil {
		return ""
	}
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(*strategy)), "-", "_")
}

func normalizeFallbackChunks(chunks []rag.VectorChunk) {
	for i := range chunks {
		chunks[i].Index = i
		if strings.TrimSpace(chunks[i].ChunkID) == "" {
			chunks[i].ChunkID = fmt.Sprintf("chunk-%d", i)
		}
		if strings.TrimSpace(chunks[i].EmbeddingText) == "" {
			chunks[i].EmbeddingText = chunks[i].Content
		}
		if chunks[i].Metadata == nil {
			chunks[i].Metadata = make(map[string]string)
		}
	}
}

func decodeSettings(settings map[string]any, dst any) error {
	if len(settings) == 0 {
		return nil
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (n *EnhancerNode) callChat(ctx context.Context, modelID string, req chat.Request) (string, error) {
	if strings.TrimSpace(modelID) != "" {
		return n.llm.ChatWithModel(ctx, req, modelID)
	}
	return n.llm.Chat(ctx, req)
}

func (n *EnricherNode) callChat(ctx context.Context, modelID string, req chat.Request) (string, error) {
	if strings.TrimSpace(modelID) != "" {
		return n.llm.ChatWithModel(ctx, req, modelID)
	}
	return n.llm.Chat(ctx, req)
}

func enhancerInputForType(ctx *rag.IngestionContext, taskType string) string {
	switch normalizeTaskType(taskType) {
	case "context_enhance", "context":
		return ctx.RawText
	case "keywords":
		if strings.TrimSpace(ctx.EnhancedText) != "" {
			return ctx.EnhancedText
		}
		return ctx.RawText
	case "questions":
		if strings.TrimSpace(ctx.EnhancedText) != "" {
			return ctx.EnhancedText
		}
		return ctx.RawText
	case "metadata":
		if strings.TrimSpace(ctx.EnhancedText) != "" {
			return ctx.EnhancedText
		}
		return ctx.RawText
	default:
		return ctx.RawText
	}
}

func applyEnhancerResult(ctx *rag.IngestionContext, taskType, response string) {
	switch normalizeTaskType(taskType) {
	case "context_enhance", "context":
		ctx.EnhancedText = strings.TrimSpace(response)
	case "keywords":
		ctx.Keywords = parseStringListResponse(response)
	case "questions":
		ctx.Questions = parseStringListResponse(response)
	case "metadata":
		if ctx.Metadata == nil {
			ctx.Metadata = make(map[string]any)
		}
		for k, v := range parseObjectResponse(response) {
			ctx.Metadata[k] = v
		}
	}
}

func applyEnricherResult(chunk *rag.VectorChunk, taskType, response string) {
	if chunk.Metadata == nil {
		chunk.Metadata = make(map[string]string)
	}
	switch normalizeTaskType(taskType) {
	case "keywords":
		chunk.Metadata["keywords"] = strings.Join(parseStringListResponse(response), ",")
	case "summary":
		chunk.Metadata["summary"] = strings.TrimSpace(response)
	case "metadata":
		for k, v := range parseObjectResponse(response) {
			chunk.Metadata[k] = stringifyAny(v)
		}
	}
}

func normalizeTaskType(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"))
}

func defaultEnhancerSystemPrompt(taskType string) string {
	switch normalizeTaskType(taskType) {
	case "keywords":
		return "请从下面文本中提取关键词，严格返回 JSON 数组。"
	case "questions":
		return "请基于下面文本生成若干问题，严格返回 JSON 数组。"
	case "metadata":
		return "请从下面文本中提取结构化元数据，严格返回 JSON 对象。"
	default:
		return "请对下面文本进行增强，输出适合检索和回答的中文结果。"
	}
}

func defaultEnricherSystemPrompt(taskType string) string {
	switch normalizeTaskType(taskType) {
	case "keywords":
		return "请从下面分块中提取关键词，严格返回 JSON 数组。"
	case "summary":
		return "请为下面分块生成简洁摘要。"
	case "metadata":
		return "请从下面分块中提取结构化元数据，严格返回 JSON 对象。"
	default:
		return "请对下面分块进行增强。"
	}
}

func renderPromptTemplate(template string, vars map[string]any) string {
	if strings.TrimSpace(template) == "" {
		return fmt.Sprint(vars["content"])
	}
	replacer := strings.NewReplacer(
		"{{text}}", fmt.Sprint(vars["text"]),
		"{{content}}", fmt.Sprint(vars["content"]),
		"{{mimeType}}", fmt.Sprint(vars["mimeType"]),
		"{{taskId}}", fmt.Sprint(vars["taskId"]),
		"{{pipelineId}}", fmt.Sprint(vars["pipelineId"]),
		"{{chunkIndex}}", fmt.Sprint(vars["chunkIndex"]),
	)
	return replacer.Replace(template)
}

func parseStringListResponse(response string) []string {
	response = strings.TrimSpace(response)
	if response == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(response), &values); err == nil {
		filtered := make([]string, 0, len(values))
		for _, value := range values {
			if v := strings.TrimSpace(value); v != "" {
				filtered = append(filtered, v)
			}
		}
		return filtered
	}
	lines := strings.Split(response, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "•"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "、"))
		if line != "" {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func parseObjectResponse(response string) map[string]any {
	response = strings.TrimSpace(response)
	if response == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(response), &out); err == nil {
		return out
	}
	return map[string]any{}
}

func parserSettingsFromConfig(settings map[string]any) parserSettings {
	var out parserSettings
	_ = decodeSettings(settings, &out)
	return out
}

func validateParserSettings(settings parserSettings, mimeType, fileName string) error {
	if len(settings.Rules) == 0 {
		return nil
	}
	resolvedType := resolveParserType(mimeType, fileName)
	for _, rule := range settings.Rules {
		if configured := normalizeParserType(rule.MimeType); configured != "" && (configured == "ALL" || strings.EqualFold(configured, resolvedType)) {
			return nil
		}
	}
	allowed := make([]string, 0, len(settings.Rules))
	for _, rule := range settings.Rules {
		if configured := normalizeParserType(rule.MimeType); configured != "" {
			allowed = append(allowed, configured)
		}
	}
	return fmt.Errorf("文件类型不符合要求。当前文件类型: %s，允许的类型: %s", resolvedType, strings.Join(uniqueStrings(allowed), ", "))
}

func matchParserRule(settings parserSettings, mimeType, fileName string) *parserRule {
	if len(settings.Rules) == 0 {
		return nil
	}
	resolvedType := resolveParserType(mimeType, fileName)
	for i := range settings.Rules {
		configured := normalizeParserType(settings.Rules[i].MimeType)
		if configured != "" && (configured == "ALL" || strings.EqualFold(configured, resolvedType)) {
			return &settings.Rules[i]
		}
	}
	return nil
}

func resolveParserType(mimeType, fileName string) string {
	if resolved := resolveParserTypeByName(fileName); resolved != "" {
		return resolved
	}
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.Contains(lower, "pdf"):
		return "PDF"
	case strings.Contains(lower, "markdown"):
		return "MARKDOWN"
	case strings.Contains(lower, "word"), strings.Contains(lower, "msword"), strings.Contains(lower, "wordprocessingml"):
		return "WORD"
	case strings.Contains(lower, "excel"), strings.Contains(lower, "spreadsheetml"):
		return "EXCEL"
	case strings.Contains(lower, "powerpoint"), strings.Contains(lower, "presentation"):
		return "PPT"
	case strings.HasPrefix(lower, "image/"):
		return "IMAGE"
	case strings.HasPrefix(lower, "text/"):
		return "TEXT"
	default:
		return "UNKNOWN"
	}
}

func resolveParserTypeByName(fileName string) string {
	lower := strings.ToLower(strings.TrimSpace(fileName))
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "PDF"
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "MARKDOWN"
	case strings.HasSuffix(lower, ".doc"), strings.HasSuffix(lower, ".docx"):
		return "WORD"
	case strings.HasSuffix(lower, ".xls"), strings.HasSuffix(lower, ".xlsx"):
		return "EXCEL"
	case strings.HasSuffix(lower, ".ppt"), strings.HasSuffix(lower, ".pptx"):
		return "PPT"
	case strings.HasSuffix(lower, ".png"), strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"), strings.HasSuffix(lower, ".gif"), strings.HasSuffix(lower, ".bmp"), strings.HasSuffix(lower, ".webp"):
		return "IMAGE"
	case strings.HasSuffix(lower, ".txt"):
		return "TEXT"
	default:
		return ""
	}
}

func normalizeParserType(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	switch value := strings.ToUpper(strings.TrimSpace(raw)); value {
	case "*", "ALL", "DEFAULT":
		return "ALL"
	case "MD", "MARKDOWN":
		return "MARKDOWN"
	case "DOC", "DOCX", "WORD":
		return "WORD"
	case "XLS", "XLSX", "EXCEL":
		return "EXCEL"
	case "PPT", "PPTX", "POWERPOINT":
		return "PPT"
	case "TXT", "TEXT":
		return "TEXT"
	case "PNG", "JPG", "JPEG", "GIF", "BMP", "WEBP", "IMAGE", "IMG":
		return "IMAGE"
	case "PDF":
		return "PDF"
	default:
		return value
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func stringifyAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case []byte:
		return string(v)
	default:
		raw, err := json.Marshal(v)
		if err == nil {
			return string(raw)
		}
		return fmt.Sprint(v)
	}
}

func lookupAnyMetadata(ctx *rag.IngestionContext, chunk rag.VectorChunk, field string) (any, bool) {
	if ctx != nil && ctx.Metadata != nil {
		if value, ok := ctx.Metadata[field]; ok {
			return value, true
		}
	}
	if chunk.Metadata != nil {
		if value, ok := chunk.Metadata[field]; ok {
			return value, true
		}
	}
	return nil, false
}

func embeddingTextOf(chunk rag.VectorChunk) string {
	if strings.TrimSpace(chunk.EmbeddingText) != "" {
		return chunk.EmbeddingText
	}
	return chunk.Content
}

func collectAssets(blocks []rag.Block) []rag.AssetRef {
	assets := make([]rag.AssetRef, 0)
	for _, block := range blocks {
		if block.Type == rag.BlockImage && strings.TrimSpace(block.Asset.PublicURL) != "" {
			assets = append(assets, block.Asset)
		}
	}
	return assets
}

func detectMimeType(source *rag.DocumentSource, data []byte) string {
	if source != nil {
		if ext := filepath.Ext(source.FileName); ext != "" {
			if mimeType := mime.TypeByExtension(strings.ToLower(ext)); mimeType != "" {
				return mimeType
			}
		}
		if ext := filepath.Ext(source.Location); ext != "" {
			if mimeType := mime.TypeByExtension(strings.ToLower(ext)); mimeType != "" {
				return mimeType
			}
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return ""
}

func normalizeContentType(contentType string) string {
	if contentType == "" {
		return ""
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		return mediaType
	}
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		return strings.TrimSpace(contentType[:idx])
	}
	return strings.TrimSpace(contentType)
}

func filenameFromContentDisposition(value string) string {
	_, params, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return params["filename"]
}

func readAllLimited(body io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return io.ReadAll(body)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("body exceeds max size: %d > %d", len(data), maxBytes)
	}
	return data, nil
}

func normalizeSourceType(sourceType string) string {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(sourceType)), "-", "_")
	switch normalized {
	case "", "localfile", "local_file":
		return "file"
	default:
		return normalized
	}
}

func sourceTypeOrDefault(source *rag.DocumentSource) string {
	if source == nil {
		return ""
	}
	return source.Type
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
