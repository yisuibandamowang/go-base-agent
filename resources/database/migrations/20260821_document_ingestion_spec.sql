ALTER TABLE t_knowledge_document
    ADD COLUMN IF NOT EXISTS ingestion_spec JSONB;

COMMENT ON COLUMN t_knowledge_document.ingestion_spec IS '文档级摄取配置：解析档位与分块预算';
