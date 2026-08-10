CREATE EXTENSION IF NOT EXISTS pg_jieba;

ALTER TABLE t_knowledge_vector
    ADD COLUMN IF NOT EXISTS search_vector TSVECTOR;

COMMENT ON COLUMN t_knowledge_vector.search_vector IS 'pg_jieba 全文检索向量，包含文档名和分块文本';

UPDATE t_knowledge_vector AS v
SET search_vector =
    setweight(to_tsvector('jiebacfg', COALESCE(d.doc_name, '')), 'A') ||
    setweight(to_tsvector('jiebacfg', COALESCE(v.content, '')), 'D')
FROM t_knowledge_document AS d
WHERE d.id = v.metadata::jsonb->>'doc_id'
  AND v.deleted = 0
  AND d.deleted = 0;

CREATE INDEX IF NOT EXISTS idx_kv_search_vector
    ON t_knowledge_vector
    USING gin(search_vector);

COMMENT ON INDEX idx_kv_search_vector IS '知识库分块 pg_jieba 全文检索 GIN 索引';
