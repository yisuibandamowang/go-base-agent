package rerank

// Chunk represents a retrieved document chunk for reranking.
type Chunk struct {
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
	// RerankScore 真实精排客户端写入的精排分，与被各通道轮番覆写的 Score 分离：
	// nil 表示这条候选没经过精排（API 未返回或精排关闭），证据闸门据此识别。
	RerankScore *float64 `json:"rerankScore,omitempty"`
}

// HasRerankScore reports whether this chunk carries a reranker-assigned score.
func (c Chunk) HasRerankScore() bool {
	return c.RerankScore != nil
}
