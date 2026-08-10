ALTER TABLE t_knowledge_internal_url_import_task
    ADD COLUMN IF NOT EXISTS phase VARCHAR(32);

ALTER TABLE t_knowledge_internal_url_import_task
    ADD COLUMN IF NOT EXISTS fetched INTEGER DEFAULT 0;

ALTER TABLE t_knowledge_internal_url_import_task
    ADD COLUMN IF NOT EXISTS current_doc_name VARCHAR(512);

COMMENT ON COLUMN t_knowledge_internal_url_import_task.phase IS '任务阶段：queued/fetching/success/failed';
COMMENT ON COLUMN t_knowledge_internal_url_import_task.fetched IS '已遍历拉取的内部文档数';
COMMENT ON COLUMN t_knowledge_internal_url_import_task.current_doc_name IS '当前正在拉取的内部文档名称';
