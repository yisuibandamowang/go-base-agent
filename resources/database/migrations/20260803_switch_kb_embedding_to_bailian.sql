-- Switch existing knowledge bases from local embedding models to Bailian.
UPDATE t_knowledge_base
SET embedding_model = 'text-embedding-v2',
    update_time = NOW()
WHERE deleted = 0
  AND embedding_model IN ('qwen3-embedding:8b-fp16', 'qwen-emb-8b');
