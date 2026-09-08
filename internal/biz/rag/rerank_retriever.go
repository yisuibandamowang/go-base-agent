package rag

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"

	"go-base-agent/internal/infra/rerank"
)

type RerankRetriever struct {
	base    Retriever
	rerank  rerank.Service
	enabled bool
	// minRerankScore 证据相关性闸门下限，<=0 关闭。对齐 Java EvidenceGatePostProcessor。
	minRerankScore float64
}

func NewRerankRetriever(base Retriever, rerankSvc rerank.Service, minRerankScore float64) *RerankRetriever {
	return &RerankRetriever{
		base:           base,
		rerank:         rerankSvc,
		enabled:        base != nil && rerankSvc != nil,
		minRerankScore: minRerankScore,
	}
}

func (r *RerankRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	chunks, err := r.base.Retrieve(ctx, question, topK)
	if err != nil || !r.enabled || len(chunks) == 0 {
		return chunks, err
	}

	byID := make(map[string]RetrievedChunk, len(chunks))
	candidates := make([]rerank.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		byID[chunk.ID] = chunk
		candidates = append(candidates, rerank.Chunk{
			ID:    chunk.ID,
			Text:  chunk.Text,
			Score: chunk.Score,
		})
	}

	reranked, err := r.rerank.Rerank(ctx, question, candidates, topK)
	if err != nil {
		return nil, err
	}
	result := make([]RetrievedChunk, 0, len(reranked))
	for _, item := range reranked {
		chunk, ok := byID[item.ID]
		if !ok {
			continue
		}
		chunk.Score = item.Score
		chunk.RerankScore = item.RerankScore
		result = append(result, chunk)
	}
	logRerankScoreSpread(result)
	logRerankAttribution(chunks, result)
	return r.applyEvidenceGate(restoreStrongKeywordAnchors(result, chunks, topK)), nil
}

// RetrieveWithContext runs retrieval with the richer search context when supported.
func (r *RerankRetriever) RetrieveWithContext(ctx context.Context, sc SearchContext) ([]RetrievedChunk, error) {
	result, err := r.RetrieveWithContextResult(ctx, sc)
	return result.Chunks, err
}

// RetrieveWithContextResult preserves retrieval scope metadata through reranking.
func (r *RerankRetriever) RetrieveWithContextResult(ctx context.Context, sc SearchContext) (RetrievalResult, error) {
	if r == nil || r.base == nil {
		return RetrievalResult{}, nil
	}
	var (
		result RetrievalResult
		err    error
	)
	if scoped, ok := r.base.(ScopedIntentAwareRetriever); ok {
		result, err = scoped.RetrieveWithContextResult(ctx, sc)
	} else if intentAware, ok := r.base.(IntentAwareRetriever); ok {
		result.Chunks, err = intentAware.RetrieveWithContext(ctx, sc)
	} else {
		question := firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)
		result.Chunks, err = r.base.Retrieve(ctx, question, sc.TopK)
	}
	if err != nil || !r.enabled || len(result.Chunks) == 0 {
		return result, err
	}

	byID := make(map[string]RetrievedChunk, len(result.Chunks))
	candidates := make([]rerank.Chunk, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		byID[chunk.ID] = chunk
		candidates = append(candidates, rerank.Chunk{
			ID:    chunk.ID,
			Text:  chunk.Text,
			Score: chunk.Score,
		})
	}

	question := firstSearchText(sc.RewrittenQuestion, sc.OriginalQuestion)
	reranked, err := r.rerank.Rerank(ctx, question, candidates, sc.TopK)
	if err != nil {
		return RetrievalResult{}, err
	}
	rerankedChunks := make([]RetrievedChunk, 0, len(reranked))
	for _, item := range reranked {
		chunk, ok := byID[item.ID]
		if !ok {
			continue
		}
		chunk.Score = item.Score
		chunk.RerankScore = item.RerankScore
		rerankedChunks = append(rerankedChunks, chunk)
	}
	logRerankScoreSpread(rerankedChunks)
	logRerankAttribution(result.Chunks, rerankedChunks)
	result.Chunks = r.applyEvidenceGate(restoreStrongKeywordAnchors(rerankedChunks, result.Chunks, sc.TopK))
	return result, nil
}

// applyEvidenceGate 证据相关性闸门：检索只保证返回最像的 N 条，库里没答案时照样满额返回，
// 下游又只看证据文本非空，噪声必然进提示词。闸门按整批最高精排分判定，不合格整批丢弃；
// 只管批级去留，过线后弱证据一并保留。对齐 Java EvidenceGatePostProcessor。
func (r *RerankRetriever) applyEvidenceGate(chunks []RetrievedChunk) []RetrievedChunk {
	if r == nil || r.minRerankScore <= 0 || len(chunks) == 0 {
		return chunks
	}
	topScore, ok := maxRerankScore(chunks)
	if !ok {
		// 无分可读一律放行：noop 降级只截断不打分，照拦等于在精排最不稳时关掉整条 KB 侧。
		// 走到这里说明闸门在空转，精排正常时不该出现，按 warn 打。
		slog.Warn("检索归因 - 证据闸门: 本批无精排分可读，闸门空转放行", "chunks", len(chunks))
		return chunks
	}
	if topScore >= r.minRerankScore {
		return chunks
	}
	slog.Info("检索归因 - 证据闸门: 最高精排分低于下限，丢弃全部证据",
		"top_score", topScore, "min_rerank_score", r.minRerankScore, "chunks", len(chunks))
	return nil
}

// maxRerankScore 全批缺分返回 false。按最高分而非逐条判：误丢比误放贵；
// 不取首条：rerank 客户端未承诺返回序，回填条目也没分。
func maxRerankScore(chunks []RetrievedChunk) (float64, bool) {
	var max float64
	found := false
	for _, chunk := range chunks {
		if chunk.RerankScore == nil || math.IsNaN(*chunk.RerankScore) || math.IsInf(*chunk.RerankScore, 0) {
			continue
		}
		if !found || *chunk.RerankScore > max {
			max = *chunk.RerankScore
			found = true
		}
	}
	return max, found
}

// logRerankScoreSpread 打本批精排分的高低两端，用于校准 rag.search.evidence.min-rerank-score。
func logRerankScoreSpread(chunks []RetrievedChunk) {
	var min, max float64
	count := 0
	for _, chunk := range chunks {
		if chunk.RerankScore == nil || math.IsNaN(*chunk.RerankScore) || math.IsInf(*chunk.RerankScore, 0) {
			continue
		}
		if count == 0 {
			min, max = *chunk.RerankScore, *chunk.RerankScore
		} else {
			min = math.Min(min, *chunk.RerankScore)
			max = math.Max(max, *chunk.RerankScore)
		}
		count++
	}
	if count == 0 {
		return
	}
	slog.Info("检索归因 - 精排分布", "scored", count, "max", max, "min", min)
}

// logRerankAttribution 归因日志：对比 Rerank 前后各通道的候选分布，重点是「图谱证据存活率」。
// 图谱大量进入 Rerank 却几乎不存活，说明其当前是纯成本（塞候选、占名额、被淘汰），
// 应下调图谱权重或先优化其长证据的可排性。对齐 Java RerankPostProcessor.logAttribution，
// 通道归属来自 fusion 阶段写入的 retrieval_channel metadata（多通道检索时才有标记）。
func logRerankAttribution(before, after []RetrievedChunk) {
	beforeCounts := countChannelsByAttribution(before)
	if len(beforeCounts) <= 1 {
		return
	}
	slog.Info("检索归因 - Rerank 输入按通道",
		"input", formatChannelCounts(beforeCounts),
		"output_top", len(after),
		"output", formatChannelCounts(countChannelsByAttribution(after)))
	if _, graphPresent := beforeCounts[string(ChannelGraph)]; graphPresent {
		// 按图谱通道在场判断而非 >0：0/N 恰是最需要看见的形态——图谱召回了却在精排全军覆没
		slog.Info("检索归因 - 图谱证据存活", "survived", countChannelChunks(after, ChannelGraph), "recalled", countChannelChunks(before, ChannelGraph))
	}
}

func countChannelChunks(chunks []RetrievedChunk, channel SearchChannelType) int {
	count := 0
	for _, chunk := range chunks {
		if chunk.Metadata["retrieval_channel"] == string(channel) {
			count++
		}
	}
	return count
}

func restoreStrongKeywordAnchors(reranked, candidates []RetrievedChunk, topK int) []RetrievedChunk {
	if len(reranked) == 0 || len(candidates) == 0 {
		return reranked
	}
	if topK <= 0 {
		topK = len(reranked)
	}
	present := make(map[string]bool, len(reranked))
	for _, chunk := range reranked {
		present[chunk.ID] = true
	}
	anchors := make([]RetrievedChunk, 0)
	for _, chunk := range candidates {
		if present[chunk.ID] || !isStrongKeywordAnchor(chunk) {
			continue
		}
		anchors = append(anchors, chunk)
	}
	if len(anchors) == 0 {
		return truncateRerankResult(reranked, topK)
	}
	sort.SliceStable(anchors, func(i, j int) bool {
		return keywordAnchorScore(anchors[i]) > keywordAnchorScore(anchors[j])
	})
	anchorLimit := topK / 5
	if anchorLimit < 1 {
		anchorLimit = 1
	}
	if anchorLimit > 3 {
		anchorLimit = 3
	}
	if len(anchors) > anchorLimit {
		anchors = anchors[:anchorLimit]
	}
	result := make([]RetrievedChunk, 0, topK)
	result = append(result, anchors...)
	used := make(map[string]bool, len(result))
	for _, chunk := range result {
		used[chunk.ID] = true
	}
	for _, chunk := range reranked {
		if used[chunk.ID] {
			continue
		}
		result = append(result, chunk)
		if len(result) >= topK {
			break
		}
	}
	return truncateRerankResult(result, topK)
}

func isStrongKeywordAnchor(chunk RetrievedChunk) bool {
	if len(chunk.Metadata) == 0 {
		return false
	}
	if strings.TrimSpace(chunk.Metadata["retrieval_channel"]) != "keyword" {
		return false
	}
	return keywordAnchorScore(chunk) >= 4
}

func keywordAnchorScore(chunk RetrievedChunk) float64 {
	if len(chunk.Metadata) == 0 {
		return 0
	}
	score, err := strconv.ParseFloat(strings.TrimSpace(chunk.Metadata["keyword_score"]), 64)
	if err != nil {
		return 0
	}
	return score
}

func truncateRerankResult(chunks []RetrievedChunk, topK int) []RetrievedChunk {
	if topK > 0 && len(chunks) > topK {
		return chunks[:topK]
	}
	return chunks
}
