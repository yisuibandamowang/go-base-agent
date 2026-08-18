-- v1.1.0 20260818 智能体人设配置
-- 新增智能体管理表与默认内置智能体数据，供后台管理页使用

CREATE TABLE IF NOT EXISTS t_agent_profile (
    id          VARCHAR(20)  NOT NULL PRIMARY KEY,
    name        VARCHAR(64)  NOT NULL,
    description VARCHAR(512),
    avatar      VARCHAR(32),
    builtin     SMALLINT     NOT NULL DEFAULT 0,
    active      SMALLINT     NOT NULL DEFAULT 0,
    create_by   VARCHAR(20),
    update_by   VARCHAR(20),
    create_time TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    update_time TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted     SMALLINT     NOT NULL DEFAULT 0,
    CONSTRAINT uk_agent_name UNIQUE (name)
);
CREATE INDEX IF NOT EXISTS idx_agent_active ON t_agent_profile (active);
COMMENT ON TABLE t_agent_profile IS '智能体人设配置表';

CREATE TABLE IF NOT EXISTS t_agent_prompt (
    id          VARCHAR(20)  NOT NULL PRIMARY KEY,
    agent_id    VARCHAR(20)  NOT NULL,
    slot_key    VARCHAR(64)  NOT NULL,
    content     TEXT,
    create_by   VARCHAR(20),
    update_by   VARCHAR(20),
    create_time TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    update_time TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted     SMALLINT     NOT NULL DEFAULT 0,
    CONSTRAINT uk_agent_slot UNIQUE (agent_id, slot_key)
);
CREATE INDEX IF NOT EXISTS idx_agent_prompt_agent ON t_agent_prompt (agent_id);
COMMENT ON TABLE t_agent_prompt IS '智能体提示词槽位表';

INSERT INTO t_agent_profile (id, name, description, avatar, builtin, active, create_time, update_time, deleted)
VALUES ('2001523723396309001', '默认助手', '系统默认人设，其他智能体未覆盖的提示词都会回落到这里', 'orbit-indigo', 1, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0)
ON CONFLICT DO NOTHING;

INSERT INTO t_agent_prompt (id, agent_id, slot_key, content, create_time, update_time, deleted)
VALUES
    ('2001523723396309011', '2001523723396309001', 'SYSTEM_CHAT', '你是企业内部知识助手，优先基于知识库内容回答，未知时明确说明并给出下一步建议。', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0),
    ('2001523723396309012', '2001523723396309001', 'MCP_ANSWER', '你只能基于当前工具返回的数据回答，不要补充未提供的信息。', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0),
    ('2001523723396309013', '2001523723396309001', 'MIXED_ANSWER', '请综合多个工具结果，给出自然、清晰、准确的业务结论。', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0),
    ('2001523723396309014', '2001523723396309001', 'AGENT_MAIN', '你是企业内部智能助手，负责在 Agent 模式下协调工具并输出最终答案。', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0),
    ('2001523723396309015', '2001523723396309001', 'KB_ANSWER', '请基于知识库检索结果作答，信息不足时明确说明。', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0),
    ('2001523723396309016', '2001523723396309001', 'CONVERSATION_SUMMARY', '请将对话压缩为简洁摘要，保留关键事实，摘要长度不超过 {summary_max_chars} 字。', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0),
    ('2001523723396309017', '2001523723396309001', 'RECOMMENDED_QUESTIONS', '根据上下文生成 {count} 个推荐问题，围绕 {question} 与 {answer} 以及 {chunks} 展开。', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0)
ON CONFLICT DO NOTHING;
