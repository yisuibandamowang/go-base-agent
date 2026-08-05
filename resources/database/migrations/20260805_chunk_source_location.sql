ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS source_version VARCHAR(64);
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS source_hash VARCHAR(64);
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS chunk_config_hash VARCHAR(64);
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS block_index INTEGER;
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS block_type VARCHAR(64);
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS source_start_offset INTEGER;
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS source_end_offset INTEGER;
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS core_start_offset INTEGER;
ALTER TABLE t_knowledge_chunk ADD COLUMN IF NOT EXISTS core_end_offset INTEGER;

COMMENT ON COLUMN t_knowledge_chunk.source_version IS '原文版本哈希';
COMMENT ON COLUMN t_knowledge_chunk.source_hash IS '原文片段哈希';
COMMENT ON COLUMN t_knowledge_chunk.chunk_config_hash IS '分块配置哈希';
COMMENT ON COLUMN t_knowledge_chunk.block_index IS '来源块序号';
COMMENT ON COLUMN t_knowledge_chunk.block_type IS '来源块类型';
COMMENT ON COLUMN t_knowledge_chunk.source_start_offset IS '原文起始偏移';
COMMENT ON COLUMN t_knowledge_chunk.source_end_offset IS '原文结束偏移';
COMMENT ON COLUMN t_knowledge_chunk.core_start_offset IS '去重核心起始偏移';
COMMENT ON COLUMN t_knowledge_chunk.core_end_offset IS '去重核心结束偏移';

CREATE INDEX IF NOT EXISTS idx_chunk_doc_source_range
    ON t_knowledge_chunk (doc_id, source_start_offset, source_end_offset)
    WHERE deleted = 0;
COMMENT ON INDEX idx_chunk_doc_source_range IS '知识库分块原文范围索引';
