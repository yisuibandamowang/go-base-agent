-- 回答来源：t_message 增加 sources 列（JSONB），对齐 Java v1.1.0 schema
ALTER TABLE t_message ADD COLUMN IF NOT EXISTS sources JSONB;
COMMENT ON COLUMN t_message.sources IS '回答来源';
