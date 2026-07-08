package rerank

// Chunk represents a retrieved document chunk for reranking.
type Chunk struct {
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}
