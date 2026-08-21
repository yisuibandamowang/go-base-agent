package rag

import (
	"encoding/json"
	"sort"
	"strings"

	"go-base-agent/internal/infra/chat"
)

// GroundingChunk 是推荐追问使用的文档片段证据，不参与主回答模型上下文。
type GroundingChunk = chat.GroundingChunk

const groundingMaxChunks = 8

// AssembleGroundingChunks 按文档去重并保留每个文档得分最高的片段。
func AssembleGroundingChunks(chunks []RetrievedChunk) []GroundingChunk {
	bestByDoc := make(map[string]RetrievedChunk)
	for _, chunk := range chunks {
		docID := strings.TrimSpace(chunk.Metadata["doc_id"])
		if docID == "" || strings.TrimSpace(chunk.Text) == "" {
			continue
		}
		if current, ok := bestByDoc[docID]; !ok || chunk.Score > current.Score {
			bestByDoc[docID] = chunk
		}
	}
	ordered := make([]RetrievedChunk, 0, len(bestByDoc))
	for _, chunk := range bestByDoc {
		ordered = append(ordered, chunk)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Score > ordered[j].Score })
	if len(ordered) > groundingMaxChunks {
		ordered = ordered[:groundingMaxChunks]
	}
	result := make([]GroundingChunk, 0, len(ordered))
	for _, chunk := range ordered {
		docName := strings.TrimSpace(chunk.Metadata["doc_name"])
		if docName == "" {
			docName = strings.TrimSpace(chunk.Metadata["doc_id"])
		}
		result = append(result, GroundingChunk{DocName: docName, Text: strings.TrimSpace(chunk.Text)})
	}
	return result
}

// MarshalGroundingChunks 序列化 grounding 片段，空列表返回空串。
func MarshalGroundingChunks(chunks []GroundingChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	data, err := json.Marshal(chunks)
	if err != nil {
		return ""
	}
	return string(data)
}

// ParseGroundingChunks 解析消息中保存的 grounding 片段。
func ParseGroundingChunks(raw string) []GroundingChunk {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var chunks []GroundingChunk
	if err := json.Unmarshal([]byte(raw), &chunks); err != nil {
		return nil
	}
	return chunks
}
