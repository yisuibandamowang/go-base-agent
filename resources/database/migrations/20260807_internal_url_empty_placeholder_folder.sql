BEGIN;

CREATE TEMP TABLE tmp_internal_url_empty_placeholder_folder_docs ON COMMIT DROP AS
SELECT id
FROM t_knowledge_document
WHERE deleted = 0
  AND source_type = 'internal_url'
  AND source_content_hash = '7f99cacad10ed887caf8d9ec0bf4da29e50a6005b611a5c1b68dd9ad1261f80d';

UPDATE t_knowledge_document
SET source_node_type = 'folder',
    status = 'success',
    chunk_count = 0,
    update_time = CURRENT_TIMESTAMP
WHERE id IN (SELECT id FROM tmp_internal_url_empty_placeholder_folder_docs);

UPDATE t_knowledge_chunk
SET deleted = 1,
    update_time = CURRENT_TIMESTAMP
WHERE deleted = 0
  AND doc_id IN (SELECT id FROM tmp_internal_url_empty_placeholder_folder_docs);

DELETE FROM t_knowledge_vector
WHERE metadata::jsonb ->> 'doc_id' IN (SELECT id FROM tmp_internal_url_empty_placeholder_folder_docs);

COMMIT;
