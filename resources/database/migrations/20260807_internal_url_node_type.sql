ALTER TABLE t_knowledge_document
    ADD COLUMN IF NOT EXISTS source_node_type VARCHAR(16);

COMMENT ON COLUMN t_knowledge_document.source_node_type IS '来源节点类型：document/folder';

UPDATE t_knowledge_document
SET source_node_type = 'document'
WHERE deleted = 0
  AND source_type = 'internal_url'
  AND COALESCE(source_node_type, '') = '';
