package rag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	knowledgeModel "go-base-agent/internal/biz/knowledge/model"
	"go-base-agent/internal/biz/knowledge/repo"
	"go-base-agent/internal/infra/embedding"
)

// PgRetriever implements Retriever using pgvector similarity search against
// all enabled knowledge bases.
// Replaces NoopRetriever.
type PgRetriever struct {
	vectorSearch VectorSearchService
	emb          embedding.Service
	kbRepo       knowledgeBaseLister
	topK         int
}

type knowledgeBaseLister interface {
	List(ctx context.Context, page, size int, name string) ([]knowledgeModel.KnowledgeBase, int64, error)
}

// NewPgRetriever creates a PgRetriever.
func NewPgRetriever(
	vectorSearch VectorSearchService,
	emb embedding.Service,
	kbRepo *repo.KnowledgeBaseRepo,
	topK int,
) *PgRetriever {
	if topK <= 0 {
		topK = 10
	}
	return &PgRetriever{
		vectorSearch: vectorSearch,
		emb:          emb,
		kbRepo:       kbRepo,
		topK:         topK,
	}
}

// Retrieve performs pgvector similarity search across all knowledge bases.
func (r *PgRetriever) Retrieve(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	if topK <= 0 {
		topK = r.topK
	}
	if r.vectorSearch == nil {
		return nil, fmt.Errorf("vector search service not configured")
	}
	kbs, _, err := r.kbRepo.List(ctx, 1, 100, "")
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	if len(kbs) == 0 {
		return nil, nil
	}
	var allChunks []RetrievedChunk
	var embeddingErrors []error
	embeddingSucceeded := false

	for _, kb := range kbs {
		vec, err := r.emb.EmbedWithModel(ctx, question, kb.EmbeddingModel)
		if err != nil {
			slog.Warn("pg retriever: embed question failed", "kb", kb.Name, "model", kb.EmbeddingModel, "err", err)
			embeddingErrors = append(embeddingErrors, fmt.Errorf("%s(%s): %w", kb.Name, kb.EmbeddingModel, err))
			continue
		}
		embeddingSucceeded = true
		chunks, err := r.vectorSearch.Search(ctx, kb.CollectionName, vec, topK)
		if err != nil {
			slog.Warn("pg retriever: vector search failed", "kb", kb.Name, "err", err)
			continue
		}
		if len(chunks) == 0 {
			slog.Warn("pg retriever: no chunks found in knowledge base", "kb", kb.Name, "collection", kb.CollectionName)
			continue
		}
		for _, chunk := range chunks {
			allChunks = append(allChunks, RetrievedChunk{
				ID:       chunk.ChunkID,
				Text:     chunk.Content,
				Score:    chunk.Score,
				Metadata: metadataWithKnowledgeBase(chunk.Metadata, kb),
			})
		}
	}
	if !embeddingSucceeded && len(embeddingErrors) > 0 {
		return nil, fmt.Errorf("embed question failed for all knowledge bases: %w", errors.Join(embeddingErrors...))
	}
	return allChunks, nil
}

// SupportsGlobalRetrieval reports whether the vector backend can search multiple collections at once.
func (r *PgRetriever) SupportsGlobalRetrieval() bool {
	if r == nil || r.vectorSearch == nil {
		return false
	}
	_, ok := r.vectorSearch.(GlobalVectorSearchService)
	return ok
}

// RetrieveGlobal performs one total-budget retrieval across all enabled knowledge bases.
func (r *PgRetriever) RetrieveGlobal(ctx context.Context, question string, topK int) ([]RetrievedChunk, error) {
	if topK <= 0 {
		topK = r.topK
	}
	if r.vectorSearch == nil {
		return nil, fmt.Errorf("vector search service not configured")
	}
	globalSearch, ok := r.vectorSearch.(GlobalVectorSearchService)
	if !ok {
		return r.Retrieve(ctx, question, topK)
	}
	kbs, _, err := r.kbRepo.List(ctx, 1, 100, "")
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	if len(kbs) == 0 {
		return nil, nil
	}

	kbByCollection := make(map[string]knowledgeModel.KnowledgeBase, len(kbs))
	collectionsByModel := make(map[string][]string)
	for _, kb := range kbs {
		collectionName := strings.TrimSpace(kb.CollectionName)
		if collectionName == "" {
			continue
		}
		kbByCollection[collectionName] = kb
		collectionsByModel[kb.EmbeddingModel] = append(collectionsByModel[kb.EmbeddingModel], collectionName)
	}

	models := make([]string, 0, len(collectionsByModel))
	for modelID := range collectionsByModel {
		models = append(models, modelID)
	}
	sort.Strings(models)

	chunks := make([]RetrievedChunk, 0)
	var embeddingErrors []error
	embeddingSucceeded := false
	for _, modelID := range models {
		vec, err := r.emb.EmbedWithModel(ctx, question, modelID)
		if err != nil {
			embeddingErrors = append(embeddingErrors, fmt.Errorf("%s: %w", modelID, err))
			continue
		}
		embeddingSucceeded = true
		vectorChunks, err := globalSearch.SearchCollections(ctx, collectionsByModel[modelID], vec, topK)
		if err != nil {
			slog.Warn("pg retriever: global vector search failed", "model", modelID, "err", err)
			continue
		}
		for _, chunk := range vectorChunks {
			collectionName := strings.TrimSpace(chunk.Metadata["collection_name"])
			kb, ok := kbByCollection[collectionName]
			if !ok {
				kb = knowledgeModel.KnowledgeBase{CollectionName: collectionName}
			}
			chunks = append(chunks, RetrievedChunk{
				ID:       chunk.ChunkID,
				Text:     chunk.Content,
				Score:    chunk.Score,
				Metadata: metadataWithKnowledgeBase(chunk.Metadata, kb),
			})
		}
	}
	if !embeddingSucceeded && len(embeddingErrors) > 0 {
		return nil, fmt.Errorf("embed question failed for all knowledge bases: %w", errors.Join(embeddingErrors...))
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		return chunks[i].Score > chunks[j].Score
	})
	if topK > 0 && len(chunks) > topK {
		chunks = chunks[:topK]
	}
	return chunks, nil
}

func metadataWithKnowledgeBase(meta map[string]string, kb knowledgeModel.KnowledgeBase) map[string]string {
	return metadataWithSources(meta, kb, "", "", "")
}

func metadataWithSources(meta map[string]string, kb knowledgeModel.KnowledgeBase, docName, sourceLocation, fileURL string) map[string]string {
	if meta == nil {
		meta = make(map[string]string)
	}
	meta["kb_id"] = kb.ID
	meta["kb_name"] = kb.Name
	meta["collection_name"] = kb.CollectionName
	if docName != "" {
		meta["doc_name"] = docName
	}
	if meta["source_url"] == "" {
		if isHTTPURL(sourceLocation) {
			meta["source_url"] = sourceLocation
		} else if isHTTPURL(fileURL) {
			meta["source_url"] = fileURL
		}
	}
	return meta
}
