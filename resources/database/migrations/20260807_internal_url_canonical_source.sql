ALTER TABLE t_knowledge_document
    ADD COLUMN IF NOT EXISTS canonical_source_key VARCHAR(256),
    ADD COLUMN IF NOT EXISTS source_root_key VARCHAR(256),
    ADD COLUMN IF NOT EXISTS source_parent_key VARCHAR(256),
    ADD COLUMN IF NOT EXISTS source_content_hash VARCHAR(64);

COMMENT ON COLUMN t_knowledge_document.canonical_source_key IS '稳定来源唯一标识，用于同一知识库内远程文档去重';
COMMENT ON COLUMN t_knowledge_document.source_root_key IS '来源树根节点稳定标识，用于内部文档树归属';
COMMENT ON COLUMN t_knowledge_document.source_parent_key IS '来源直接父节点稳定标识，用于内部文档树层级定位';
COMMENT ON COLUMN t_knowledge_document.source_content_hash IS '原始文档内容哈希，用于判断内容是否变化';

WITH canonical_candidates AS (
    SELECT
        id,
        kb_id,
        source_type,
        create_time,
        'internal_url:geelib:' ||
        substring(file_url FROM 'spaceId=([^&]+)') ||
        ':' ||
        substring(file_url FROM 'docId=([^&]+)') AS canonical_key
    FROM t_knowledge_document
    WHERE deleted = 0
      AND source_type = 'internal_url'
      AND COALESCE(canonical_source_key, '') = ''
      AND file_url LIKE '%spaceId=%'
      AND file_url LIKE '%docId=%'
),
canonical_ranked AS (
    SELECT
        id,
        canonical_key,
        row_number() OVER (
            PARTITION BY kb_id, source_type, canonical_key
            ORDER BY create_time ASC, id ASC
        ) AS rn
    FROM canonical_candidates
    WHERE canonical_key IS NOT NULL
      AND canonical_key <> ''
)
UPDATE t_knowledge_document d
SET canonical_source_key = r.canonical_key,
    source_root_key = COALESCE(NULLIF(d.source_root_key, ''), r.canonical_key)
FROM canonical_ranked r
WHERE d.id = r.id
  AND r.rn = 1;

CREATE INDEX IF NOT EXISTS idx_knowledge_document_source_root
    ON t_knowledge_document (kb_id, source_type, source_root_key);

CREATE UNIQUE INDEX IF NOT EXISTS uk_knowledge_document_canonical_source
    ON t_knowledge_document (kb_id, source_type, canonical_source_key)
    WHERE deleted = 0
      AND canonical_source_key IS NOT NULL
      AND canonical_source_key <> '';
