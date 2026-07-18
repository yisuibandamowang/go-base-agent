package mcp_tool

import (
	"context"
	"fmt"
	"strings"

	"go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/infra/embedding"

	"gorm.io/gorm"
)

// Tool represents a registered MCP tool.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]propDesc
	Required    []string
	Execute     func(ctx context.Context, args map[string]interface{}) ([]toolContent, error)
}

func (t *Tool) toDesc() toolDesc {
	return toolDesc{
		Name:        t.Name,
		Description: t.Description,
		InputSchema: inputSchema{
			Type:       "object",
			Properties: t.Properties,
			Required:   t.Required,
		},
	}
}

// RegisterTools creates the default set of MCP tools.
func RegisterTools(
	vectorDB *gorm.DB,
	emb embedding.Service,
	kbRepo *repo.KnowledgeBaseRepo,
	docRepo *repo.KnowledgeDocumentRepo,
	chunkRepo *repo.KnowledgeChunkRepo,
) []*Tool {
	tools := []*Tool{
		searchKBTool(vectorDB, emb, kbRepo),
		searchDocsTool(docRepo),
		listKBsTool(kbRepo),
		listChunksTool(chunkRepo),
	}
	return append(tools, builtinTools()...)
}

// search_knowledge_base: vector search across knowledge bases.
func searchKBTool(vectorDB *gorm.DB, emb embedding.Service, kbRepo *repo.KnowledgeBaseRepo) *Tool {
	return &Tool{
		Name:        "search_knowledge_base",
		Description: "使用向量相似度搜索知识库。输入自然语言问题，返回最相关的知识片段。",
		Properties: map[string]propDesc{
			"question": {Type: "string", Description: "要搜索的问题"},
			"kb_name":  {Type: "string", Description: "可选：指定知识库名称，不指定则搜索所有知识库"},
			"top_k":    {Type: "integer", Description: "返回结果数量，默认5"},
		},
		Required: []string{"question"},
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			question, _ := args["question"].(string)
			if question == "" {
				return errorContent("缺少参数: question"), nil
			}
			topK := 5
			if v, ok := args["top_k"].(float64); ok {
				topK = int(v)
			}

			vec, err := emb.Embed(ctx, question)
			if err != nil {
				return errorContent(fmt.Sprintf("向量化失败: %v", err)), nil
			}

			kbs, _, err := kbRepo.List(ctx, 1, 100)
			if err != nil {
				return errorContent(fmt.Sprintf("查询知识库失败: %v", err)), nil
			}

			kbName, _ := args["kb_name"].(string)
			if kbName != "" {
				filtered := kbs[:0]
				for _, kb := range kbs {
					if strings.Contains(strings.ToLower(kb.Name), strings.ToLower(kbName)) {
						filtered = append(filtered, kb)
					}
				}
				kbs = filtered
			}

			var sb strings.Builder
			for _, kb := range kbs {
				_ = searchVector(ctx, vectorDB, kb.CollectionName, vec, topK, &sb)
			}
			if sb.Len() == 0 {
				return []toolContent{{Type: "text", Text: "未找到相关知识。"}}, nil
			}
			return []toolContent{{Type: "text", Text: sb.String()}}, nil
		},
	}
}

// search_documents: keyword search for documents.
func searchDocsTool(docRepo *repo.KnowledgeDocumentRepo) *Tool {
	return &Tool{
		Name:        "search_documents",
		Description: "通过关键词搜索知识库中的文档名称。",
		Properties: map[string]propDesc{
			"keyword": {Type: "string", Description: "搜索关键词"},
			"kb_id":   {Type: "string", Description: "可选：知识库ID"},
		},
		Required: []string{"keyword"},
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			keyword, _ := args["keyword"].(string)
			if keyword == "" {
				return errorContent("缺少参数: keyword"), nil
			}
			kbID, _ := args["kb_id"].(string)
			docs, _, err := docRepo.SearchDocs(ctx, keyword, 1, 20)
			if err != nil {
				return errorContent(fmt.Sprintf("搜索文档失败: %v", err)), nil
			}
			_ = kbID // not yet used in search filter
			if len(docs) == 0 {
				return []toolContent{{Type: "text", Text: "未找到匹配的文档。"}}, nil
			}
			var sb strings.Builder
			for i, d := range docs {
				sb.WriteString(fmt.Sprintf("%d. %s (状态:%s, 分块数:%d)\n", i+1, d.DocName, d.Status, d.ChunkCount))
			}
			return []toolContent{{Type: "text", Text: sb.String()}}, nil
		},
	}
}

// list_knowledge_bases: list all knowledge bases.
func listKBsTool(kbRepo *repo.KnowledgeBaseRepo) *Tool {
	return &Tool{
		Name:        "list_knowledge_bases",
		Description: "列出所有可用的知识库。",
		Properties:  map[string]propDesc{},
		Required:    nil,
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			kbs, _, err := kbRepo.List(ctx, 1, 100)
			if err != nil {
				return errorContent(fmt.Sprintf("查询知识库失败: %v", err)), nil
			}
			if len(kbs) == 0 {
				return []toolContent{{Type: "text", Text: "暂无知识库。"}}, nil
			}
			var sb strings.Builder
			for i, kb := range kbs {
				sb.WriteString(fmt.Sprintf("%d. %s (embedding:%s, collection:%s)\n",
					i+1, kb.Name, kb.EmbeddingModel, kb.CollectionName))
			}
			return []toolContent{{Type: "text", Text: sb.String()}}, nil
		},
	}
}

// list_chunks: list chunks for a document.
func listChunksTool(chunkRepo *repo.KnowledgeChunkRepo) *Tool {
	return &Tool{
		Name:        "get_document_chunks",
		Description: "获取指定文档的所有分块内容。",
		Properties: map[string]propDesc{
			"doc_id": {Type: "string", Description: "文档ID"},
		},
		Required: []string{"doc_id"},
		Execute: func(ctx context.Context, args map[string]interface{}) ([]toolContent, error) {
			docID, _ := args["doc_id"].(string)
			if docID == "" {
				return errorContent("缺少参数: doc_id"), nil
			}
			chunks, _, err := chunkRepo.ListByDoc(ctx, docID, 1, 100)
			if err != nil {
				return errorContent(fmt.Sprintf("查询分块失败: %v", err)), nil
			}
			if len(chunks) == 0 {
				return []toolContent{{Type: "text", Text: "该文档暂无分块。"}}, nil
			}
			var sb strings.Builder
			for i, c := range chunks {
				preview := c.Content
				if len([]rune(preview)) > 200 {
					preview = string([]rune(preview)[:200]) + "..."
				}
				sb.WriteString(fmt.Sprintf("--- 第%d块 ---\n%s\n\n", i+1, preview))
			}
			return []toolContent{{Type: "text", Text: sb.String()}}, nil
		},
	}
}

func searchVector(ctx context.Context, vectorDB *gorm.DB, collectionName string, vec []float32, topK int, sb *strings.Builder) error {
	type row struct {
		Content string  `gorm:"column:content"`
		Score   float64 `gorm:"column:score"`
	}
	vecStr := vecToString(vec)
	var rows []row
	err := vectorDB.WithContext(ctx).Raw(
		`SELECT content, 1 - (embedding <=> ?) AS score
		 FROM t_knowledge_vector
		 WHERE collection_name = ?
		 ORDER BY embedding <=> ?
		 LIMIT ?`,
		vecStr, collectionName, vecStr, topK,
	).Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, r := range rows {
		preview := r.Content
		if len([]rune(preview)) > 300 {
			preview = string([]rune(preview)[:300]) + "..."
		}
		sb.WriteString(fmt.Sprintf("[%s] 相似度:%.2f | %s\n\n", collectionName, r.Score, preview))
	}
	return nil
}

func errorContent(msg string) []toolContent {
	return []toolContent{{Type: "text", Text: msg}}
}

func vecToString(vec []float32) string {
	if len(vec) == 0 {
		return "[]"
	}
	s := "["
	for i, v := range vec {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%f", v)
	}
	s += "]"
	return s
}

// Ensure model types are resolved.
var _ = model.KnowledgeBase{}
