CREATE TABLE IF NOT EXISTS t_knowledge_internal_url_import_task (
    id VARCHAR(20) PRIMARY KEY,
    kb_id VARCHAR(20) NOT NULL,
    source_location VARCHAR(1024) NOT NULL,
    status VARCHAR(16) NOT NULL,
    total INTEGER DEFAULT 0,
    success INTEGER DEFAULT 0,
    failed INTEGER DEFAULT 0,
    existing_unchanged INTEGER DEFAULT 0,
    existing_chunked INTEGER DEFAULT 0,
    existing_enabled INTEGER DEFAULT 0,
    new_documents INTEGER DEFAULT 0,
    changed_documents INTEGER DEFAULT 0,
    strategy_changed_documents INTEGER DEFAULT 0,
    chunkable INTEGER DEFAULT 0,
    skipped_chunked INTEGER DEFAULT 0,
    result_json JSONB,
    error_message VARCHAR(1024),
    created_by VARCHAR(20) NOT NULL,
    updated_by VARCHAR(20),
    create_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    update_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    deleted SMALLINT DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_internal_url_import_kb ON t_knowledge_internal_url_import_task (kb_id);
CREATE INDEX IF NOT EXISTS idx_internal_url_import_status ON t_knowledge_internal_url_import_task (status);

COMMENT ON TABLE t_knowledge_internal_url_import_task IS '内部URL文档导入任务表';
COMMENT ON COLUMN t_knowledge_internal_url_import_task.kb_id IS '知识库ID';
COMMENT ON COLUMN t_knowledge_internal_url_import_task.source_location IS '内部文档来源地址';
COMMENT ON COLUMN t_knowledge_internal_url_import_task.status IS '任务状态：running/success/failed';
COMMENT ON COLUMN t_knowledge_internal_url_import_task.result_json IS '导入完成后的结果摘要JSON';
COMMENT ON COLUMN t_knowledge_internal_url_import_task.error_message IS '失败原因';
