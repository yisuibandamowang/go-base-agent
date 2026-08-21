-- 推荐追问 grounding：对齐 Java t_message.retrieved_chunks。
ALTER TABLE t_message ADD COLUMN IF NOT EXISTS retrieved_chunks JSONB;
COMMENT ON COLUMN t_message.retrieved_chunks IS '推荐问题 grounding 片段';
