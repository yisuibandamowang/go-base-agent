-- 推荐追问结果缓存：null 表示未生成，[] 表示已生成但无合适结果。
ALTER TABLE t_message ADD COLUMN IF NOT EXISTS recommended_questions JSONB;
COMMENT ON COLUMN t_message.recommended_questions IS '推荐追问问题';
