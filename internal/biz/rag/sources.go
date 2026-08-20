package rag

import (
	"encoding/json"
	"strings"
)

// SourceRef 回答来源引用（文档级）。
// 由检索片段按文档去重、赋号后得到，同时用于 SSE 下发、消息落库、前端来源面板与预览。
// 对齐 Java SourceRef。
type SourceRef struct {
	// Index 来源序号，从 1 开始。
	Index int `json:"index"`
	// DocID 文档 ID，用于预览取原文。
	DocID string `json:"docId,omitempty"`
	// DocName 文档名称，面板标题。
	DocName string `json:"docName,omitempty"`
	// SourceType 来源类型 file/url/feishu。
	SourceType string `json:"sourceType,omitempty"`
	// FileType 文件类型，前端据此为本地文件选类型图标，网页来源可为空。
	FileType string `json:"fileType,omitempty"`
	// URL 外部原始链接，url/feishu 有值，file 走 docId 预览提取正文。
	URL string `json:"url,omitempty"`
	// Excerpt 摘录，取该文档最相关片段的截断文本。
	Excerpt string `json:"excerpt,omitempty"`
}

const (
	sourceExcerptMaxLength = 100
	sourceMaxCount         = 20
)

// AssembleSources 由检索片段装配文档级来源列表。
// 按 docId 归并保留最高分片段（作为摘录与排序依据），按最高分降序赋号，取上限 20 条。
// 对齐 Java SourcesAssembler.assemble。
func AssembleSources(chunks []RetrievedChunk) []SourceRef {
	if len(chunks) == 0 {
		return nil
	}

	// 按 docId 归并，保留最高分片段
	bestByDoc := make(map[string]RetrievedChunk)
	order := make([]string, 0)
	for _, chunk := range chunks {
		docID := strings.TrimSpace(chunk.Metadata["doc_id"])
		if docID == "" {
			continue
		}
		if existing, ok := bestByDoc[docID]; !ok {
			bestByDoc[docID] = chunk
			order = append(order, docID)
		} else if chunk.Score > existing.Score {
			bestByDoc[docID] = chunk
		}
	}
	if len(bestByDoc) == 0 {
		return nil
	}

	// 按最高分降序
	ordered := make([]RetrievedChunk, 0, len(bestByDoc))
	for _, docID := range order {
		ordered = append(ordered, bestByDoc[docID])
	}
	sortSourcesByScoreDesc(ordered)
	if len(ordered) > sourceMaxCount {
		ordered = ordered[:sourceMaxCount]
	}

	sources := make([]SourceRef, 0, len(ordered))
	for i, chunk := range ordered {
		sources = append(sources, SourceRef{
			Index:      i + 1,
			DocID:      strings.TrimSpace(chunk.Metadata["doc_id"]),
			DocName:    strings.TrimSpace(chunk.Metadata["doc_name"]),
			SourceType: strings.TrimSpace(chunk.Metadata["source_type"]),
			FileType:   strings.TrimSpace(chunk.Metadata["file_type"]),
			URL:        resolveSourceURL(chunk.Metadata),
			Excerpt:    truncateSourceExcerpt(chunk.Text),
		})
	}
	return sources
}

func sortSourcesByScoreDesc(chunks []RetrievedChunk) {
	for i := 1; i < len(chunks); i++ {
		for j := i; j > 0 && chunks[j].Score > chunks[j-1].Score; j-- {
			chunks[j], chunks[j-1] = chunks[j-1], chunks[j]
		}
	}
}

// resolveSourceURL 仅 url/feishu 来源返回外部链接，file 来源走 docId 预览。
func resolveSourceURL(meta map[string]string) string {
	switch strings.TrimSpace(meta["source_type"]) {
	case "url", "feishu":
		url := strings.TrimSpace(meta["source_url"])
		if isHTTPURL(url) {
			return url
		}
	}
	return ""
}

// truncateSourceExcerpt 截断摘录，超出以省略号结尾。
func truncateSourceExcerpt(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= sourceExcerptMaxLength {
		return text
	}
	return string(runes[:sourceExcerptMaxLength]) + "..."
}

// marshalSources 序列化来源列表为 JSON，空列表返回空串。
func marshalSources(sources []SourceRef) string {
	if len(sources) == 0 {
		return ""
	}
	data, err := json.Marshal(sources)
	if err != nil {
		return ""
	}
	return string(data)
}

// unmarshalSources 反序列化来源列表，空串或解析失败返回 nil。
func unmarshalSources(raw string) []SourceRef {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var sources []SourceRef
	if err := json.Unmarshal([]byte(raw), &sources); err != nil {
		return nil
	}
	return sources
}
