# go-base-agent — Go 复刻开发日志

> 从 Java 版 Ragent 用 Go 重写，目标功能 100% 对齐，前端零改动复用。

## 项目概览

| 维度 | 内容 |
|------|------|
| 仓库 | `github.com/nageoffer/go-base-agent` |
| Go 版本 | 1.25.8 |
| Web 框架 | Gin |
| ORM | GORM v2 |
| 数据库 | PostgreSQL 16 + pgvector |
| 缓存 | Redis 7 |
| 消息队列 | RocketMQ 5 |
| 对象存储 | S3 兼容 (RustFS) |
| 鉴权 | 自实现 JWT 中间件 (对齐 Sa-Token) |
| 迁移计划 | `../MIGRATION_PLAN.md`（位于 Java 仓库） |

## 2026-08-10 — pg_jieba 关键词召回增强

- 关键词召回按保守方案接入 `pg_jieba + ts_rank_cd`：`t_knowledge_vector.search_vector` 存储文档名 A 权重和 chunk 正文 D 权重，迁移脚本负责创建 `pg_jieba` 扩展、补字段、回填并创建 GIN 索引。
- `SearchKeywordChunks` 会把中英文信号词拼成 `OR` websearch 表达式，优先使用 `websearch_to_tsquery('jiebacfg', query)` 和 `ts_rank_cd` 做中文友好的全文检索排序；pg_jieba 正常时会合并 LIKE 强锚点候选并去重，当扩展、配置或字段不可用时自动回退原 LIKE 信号词匹配，避免迁移窗口影响主问答链路。
- `PgVectorStore` 在文档分块向量入库和单 chunk 更新后会尽力刷新对应 `search_vector`，刷新失败只记录 warning，不阻断 embedding/vector 写入主流程。
- RAG 主链路新增最终证据收口：进入 Prompt 和“依据”前按默认 topK 截断，稳定排序，并优先保留意图 collection 或元数据锚点命中的 collection；Prompt 明确要求只命中文档标题、目录或链接时不能推断正文细节。
- RAG 主链路新增 Redis 完整答案缓存：无历史、无 MCP 上下文的 standalone 问题会在检索、rerank 和最终证据收口后，按归一化原始问题、深度思考标记和最终证据指纹生成缓存 key；命中时只跳过回答 LLM，仍保存本轮会话消息并通过 SSE 返回缓存答案与来源依据。
- 答案缓存 key 已升级为 `v2` 证据指纹 schema，旧版仅按问题和 collection 命中的缓存会自然失效，避免低质量旧答案绕过当前更准确的检索结果。
- Assistant 消息持久化现在会把最终 `依据：` 块一并保存到 `t_message.content`，前端重新加载历史时可解析为独立的“来源依据”折叠面板；同一会话内默认展开最后一次回答的来源，历史回答来源默认折叠。

## 2026-08-04 — 后台用户接口契约对齐

- 用户管理创建/更新响应形状已继续对齐 Java `UserController`：`POST /users` 与 `/admin/users` 返回新用户 ID，`PUT /users/{id}` 与 `/admin/users/{id}` 返回空成功。
- API 文档补齐 Java 原始 `/users` 管理路径，并标明 `/admin/users` 兼容路径的返回契约。

## 2026-08-05 — 检索与限流默认值对齐

- `rag.search.default-top-k` 已补齐 Java 默认值 10，并把 `vector-global` / `intent-directed` / `keyword` / `web-search` / `fusion` 的默认参数收口到配置层，避免检索链路继续散落 10、60、50 之类的硬编码。
- `rag.rate-limit.global` 已补齐 Java 默认启用及默认值：`max-concurrent=50`、`max-wait-seconds=20`、`lease-seconds=600`、`poll-interval-ms=200`，显式关闭时会绕过聊天队列限流。

## 2026-08-06 — 内部 URL 上传入库

- 知识库文档上传新增 `internal_url` 来源类型，默认通过 `editor-cli` 拉取 geelib 内部文档。
- 内部 URL 上传链路已改为后台导入任务：`POST /knowledge-base/{kbId}/docs/upload` 在 `sourceType=internal_url` 时快速返回 `{taskId,status}`，前端通过 `GET /knowledge-base/{kbId}/docs/internal-url-import-tasks/{taskId}` 轮询最终导入汇总，避免父文档下子文档过多时被 60 秒请求超时截断；普通文件上传和 `sourceType=url` 主流程不变。
- 内部 URL 会先读取目录树，再递归读取全部节点内容；有正文的节点会逐条创建知识库文档，`editor-cli read` 返回 `该文档内容为空` 的目录占位节点不入库、不展示；创建后的文档默认停在 `pending`，多文档场景由前端提示是否一键分块，用户确认后才会批量触发分块。
- 内部 URL 后台导入任务新增 `phase/fetched/currentDocName/importedTotal` 进度与汇总字段；异步导入优先使用 `GeelibSource.WalkDocuments` 逐篇读取、判定、入库和写 S3，前端会展示“同步公司大文档”进度条，避免大目录场景长时间无反馈，也避免一次性持有全部子文档正文。
- 重复上传内部 URL 时，上传响应会按当前知识库内的 `canonical_source_key`、`source_content_hash` 和服务端规范化后的分块配置统计已存在且内容未变、已分块、已启用、新增、内容变化和分块策略变化文档；分块配置按 JSON 语义比较，避免字段顺序差异被误判为策略变化；已有文档内容未变时不会重复覆盖源文件，策略变化只让文档进入一键分块候选，上传阶段不改已有文档策略，真正重新分块仍由用户确认一键分块后触发。
- 内部 URL 勾选定时更新时会以父链接作为同步入口，后续定时扫描会感知有正文子文档的新增、内容变化和移除；新增/变化的子文档会保存源文件并重新分块，父目录下已移除的子文档会自动置为禁用。
- 父内部链接定时同步不会覆盖用户手动禁用的子文档；如果已有子文档源文件缺失，会按内容变化重新保存源文件并触发分块。
- `rag.knowledge.geelib` 配置已补齐，可通过 `command`、`timeout-seconds`、`max-bytes` 和 `domains` 控制抓取行为。
- 修复 `GEELIB_CLI_WORK_DIR` 为空默认值时的配置展开边界，`configs/config.yaml` / `config.example.yaml` 中的 `${GEELIB_CLI_WORK_DIR:}` 现在会正确展开为空字符串，不再把占位符原样传给 `editor-cli`。

## 2026-07-31 — 流式链路与辅助模型切换

- RAG 普通流式请求现在会显式传 `enable_thinking=false`，避免百炼兼容模型按默认思考模式卡在首包前。
- `ProbeBridge` 现在把 thinking chunk 也视为首包，避免深度思考流被误判为未产出。
- `LLMRewriter` 不再抢跑解析半截流，并且改写请求统一关闭 thinking，避免 rewrite/title/摘要这类辅助调用优先走本地模型。
- `buildPreferredLLMService` 已恢复本地 ollama 优先分支，rewrite / 摘要 / 标题 / MCP 选择抽参 / 普通非 RAG 回答会先走本地 `qwen3.6:latest`，失败再回落主路由服务。
- 前端流式回调已按当前 `assistantId` 做隔离，切换新对话后旧流不会再把“停止生成”状态写进当前会话。

## 2026-08-03 — 文档上传回读修复

- 上传文档时会把知识库 `collectionName` 写入 `FileURL` 的 `upload://collection/doc` 形式，保留原始来源信息的同时补上存储定位提示。
- 文档分块、预览和删除读取原始文件时会优先使用这个存储提示，再回退到知识库表查询，避免上传后立即分块时因 collection 解析失败读回到裸 `docID`。
- 业务变更审计表 `t_biz_change_log` 已补入 PostgreSQL schema，运行时也保留自动建表兜底，避免上传/删除类操作因审计表缺失反复报错。
- 审计 snapshot 现在会将空值写成合法 JSON，避免 `jsonb` 列因空字符串导致插入失败。
- `VectorGlobalSearch` 关闭全局向量召回时只统计带 `collectionName` 的 KB 意图；SYSTEM/MCP 意图或未绑定 collection 的 KB 意图不会误关全局兜底，避免知识库已有向量时返回“没有相关信息”。

## 阶段进度

| 阶段 | 状态 | 日期 | 产出 / 卡点 |
|------|------|------|------------|
| 0 — 工程骨架 | ✅ 完成 | 2026-07-07 | go mod init、目录骨架、Makefile、CI lint、docker-compose、config 配置加载、health 端点、README、配置安全处理 |
| 1 — framework 层 | ✅ 完成 | 2026-07-07 | 1.1 基础层 ✅ 见 docs/phase1-1-framework-foundation.md<br>1.2 队列限流 ✅ 见 docs/phase1-2-rate-limit.md<br>1.3 SSE 封装 ✅ 见 docs/phase1-3-sse-sender.md<br>1.4 trace 装饰器 ✅ 见 docs/phase1-4-trace-decorator.md<br>1.5 MQ 抽象 ✅ 见 docs/phase1-5-rocketmq.md |
| 2 — infra-ai 层 | ✅ 完成 | 2026-07-07 | 2A-1 配置可插拔化 ✅ 见 docs/phase2-1-config-pluggable.md<br>2A-2 模型目标与协议解析 ✅ 见 docs/phase2-2-model-core.md<br>2A-3 三态熔断 HealthStore ✅ 见 docs/phase2-3-health-store.md<br>2A-4 模型选择器 ✅ 见 docs/phase2-4-selector.md<br>2A-5 路由执行器 ✅ 见 docs/phase2-5-executor.md<br>2A-6 Chat 能力接口 ✅ 见 docs/phase2-6-chat-interface.md<br>2A-7 OpenAI 兼容 Chat Client + SSE 解析 ✅ 见 docs/phase2-7-openai-client.md<br>2A-8 Embedding 能力接口 ✅ 见 docs/phase2-8-embedding.md<br>2A-9 Rerank 能力接口 ✅ 见 docs/phase2-9-rerank.md<br>2A-10 集成组装 ✅ 见 docs/phase2-10-integration.md<br>设计文档：docs/superpowers/specs/2026-07-07-phase2-pluggable-ai-rag-design.md<br>测试：66 个全部通过，-race 无竞态 |
| 3 — 基础 RAG 主链路 (2B) | ✅ 完成 | 2026-07-08 | 2B-1~2B-8 全部完成<br>文档：phase2b-1~2b-8<br>测试：89 个全部通过
| 4 — 完整能力包 (2C) | ✅ 完成 | 2026-07-08 | 2C-1~2C-8 全部完成 ✅<br>文档：phase2c-1~2c-8<br>测试：117 个全部通过<br>**2026-07-08 补充**：检索闭环（替换 Noop → 真实实现）→ 见下方 4B 行
| 4B — 检索增强生成闭环 | ✅ 完成 | 2026-07-08 | 4.1 用户/鉴权 ✅<br>4.2 会话管理 ✅<br>4.3 检索/改写真实化 ✅<br>4.4 全链路串联 ✅<br>文档：phase4-retrieval-closed-loop.md<br>新增文件：15 个<br>新增 API 端点：10 个
| 5 — 知识库 + 文档入库 Pipeline | ✅ 完成 | 2026-07-08 | 3.0.1~3.0.3 前置准备 ✅<br>3.1.1~3.1.4 知识库 CRUD ✅<br>3.2.1~3.2.5 文档管理 CRUD ✅<br>3.3.1~3.3.6 文档解析器 ✅<br>3.4.1~3.4.3 分块策略 ✅<br>3.5 入库 Pipeline 引擎 ✅<br>3.6 PgVector Store / Milvus Store ✅<br>文档：phase3-0-1~phase3-final<br>新增文件：34 个<br>剩余任务：docs/phase3-plan.md
| 6 — MCP + Admin + 联调 | ✅ 完成 | 2026-07-08 | 5.1 MCP Server ✅<br>5.2 意图树 ✅<br>5.3 Admin ✅<br>5.4 联调 ✅<br>新增文件：16 个<br>新增 API 端点：41 个<br>文档：phase5-1-mcp-server.md、API_CONTRACT.md
| 7 — 收尾 | ✅ 完成 | 2026-07-08 | go mod tidy ✅<br>framework/cache Redis 缓存 ✅<br>framework/lock 分布式锁 ✅<br>framework/idempotent 幂等 ✅<br>README 更新 ✅<br>总 Go 文件：142 个 / 13,845 行 |

## 2026-07-30

- RAG 聊天队列的 permit 获取已改为同步触发 `OnAcquire`：`FairQueueLimiter` 不再把 `OnAcquire` 丢到后台 goroutine，避免 `/rag/v3/chat` 在流式回复完成前提前返回，导致前端必须切换会话才能刷新当前对话状态。

- 轻量 LLM 的本地优先降级语义已收口：`FallbackLLMService.ChatWithModel` 在本地模型失败后会回到云端默认 `Chat` 路由，而不是继续按本地模型 ID 复用 fallback，避免 `qwen3-local` 失败时把云端兜底也锁在本地模型上。
- 知识库名称唯一性语义已对齐 Java：创建/重命名时会忽略名称中的全部空白字符再做重名判断，避免 `知识 库A` 与 `知识库A` 这类仅空格不同的名称并存。
- 知识库更新语义已继续对齐 Java：`PUT /knowledge-base/{id}` 现在只允许改名称与嵌入模型，`collectionName` 虽然仍保留在 Go 请求体里做历史兼容，但更新时会被忽略，不再允许误改知识空间。
- 文档列表语义已继续对齐 Java：`GET /knowledge-base/{kb-id}/docs` 现在支持 `status` 和 `keyword` 过滤，按知识库、状态、关键字一起收敛分页结果。
- 文档更新返回形状已继续对齐 Java：`PUT /knowledge-base/docs/{docId}` 更新成功后返回空成功，不再把更新后的文档对象放入 `data`。
- 文档分块触发返回形状已继续对齐 Java：`POST /knowledge-base/docs/{docId}/chunk` 现在返回空成功，不再回显“文档分块任务已提交”的提示对象。
- Markdown 文档预览语义已对齐 Java：`GET /knowledge-base/docs/{docId}/preview` 现在只允许 `fileType=markdown`，并返回对象存储中的原始 Markdown 文本，不再拼接分块内容。
- RAG 评测接口补齐 MCP 结果回显：`/rag/eval` 现在会在可用时回收 MCP 上下文并返回 `hasMcp/mcpContext`，对齐 Java `EvalController` 的评测语义。
- 文档源文件下载语义已继续对齐 Java：`GET /knowledge-base/docs/{docId}/file` 现在只做对象存储源文件直出，源文件缺失时返回业务失败，不再回退到预览或分块文本。
- 关键词映射 Java 兼容路径已补齐：`POST /mappings` 现在返回新建 ID 字符串，`PUT /mappings/{id}` 返回空成功，保留 `/intent-tree/term-mappings` 作为 Go 扩展路径的对象响应。
- Chunk 列表筛选语义已继续对齐 Java：`GET /knowledge-base/docs/{doc-id}/chunks` 现在支持 `enabled` 查询参数，按启用状态过滤分页结果。
- Chunk 批量启停重复操作语义已继续对齐 Java：当所选 Chunk 已全部处于目标启用状态时，返回“所有 Chunk 已全部启用/禁用，无需重复操作”，并且只更新实际需要变化的分块。
- Ingestion Indexer 默认向量集合语义已继续对齐 Java：Pipeline 索引节点在未显式传 `settings.collectionName` 或 `VectorSpaceID` 时，会回退到 `rag.default.collection-name`，避免通用流水线缺少向量空间参数时直接失败。

## 2026-07-29

- 补齐文档调度 RUNNING 恢复超时配置：新增 `rag.knowledge.schedule.running-timeout-minutes`，默认按 Java `KnowledgeScheduleProperties` 使用 30 分钟；`RecoverStuckRunningDocuments` 现在按配置判断卡住文档，避免固定 10 分钟误将仍在处理的大文档置为 failed。
- 补齐体验环境只读拦截：`app.demo-mode=true` 时主服务会启用 demo-mode middleware，GET 查询放行，写操作返回 JSON 拒绝，`/rag/v3/chat` 以 SSE `reject/done` 事件返回拒绝信息，对齐 Java `DemoModeInterceptor`。
- 补齐 RAG Trace 查询 Java 语义：`/rag/traces/runs` 和 `/admin/traces` 支持 `current/page/size/traceId/conversationId/taskId/status`，列表与详情 VO 补齐 `entryMethod/username/question/ttftMs`，节点 VO 补齐 `className/methodName` 并按 `start_time ASC, id ASC` 排序。
- 补齐知识库文档列表 `chunksEdited` 回显：文档列表/详情会按 Java `findEditedDocIds` 语义判断是否存在 `update_time > create_time + 1s` 的未删除分块，前端可识别文档是否包含手工编辑过的 Chunk。
- 补齐会话列表默认全量语义：`GET /conversations` 默认按 Java `listByUserId` 返回当前用户全部会话数组，不再被默认 `size=10` 截断；仅显式 `paged=true` 时保留分页响应。

## 2026-07-28

- 补齐默认意图树启动初始化入口：新增 `app.intent-tree.init-from-factory` 开关，默认关闭；开启后主服务启动时调用 `IntentService.InitFromFactory(ctx)`，让 Java 默认意图树初始化能力可按需落地。
- 补齐意图树 Java 默认树初始化能力：`IntentService.InitFromFactory(ctx)` 现在会按 Java `IntentTreeFactory` 展平默认意图树并写入 18 个默认节点，已存在的 `intentCode` 会跳过，避免重复初始化。
- 补齐意图树树形查询排序语义：`GET /intent-tree/tree` / `/intent-tree/trees` 构建树前按 `sort_order ASC, id ASC` 查询未删除节点，对齐 Java `IntentTreeServiceImpl.getFullTree`，同级节点排序不再受创建时间影响。
- 补齐意图树更新不可变字段语义：`intentCode/kbId/mcpToolId` 在更新节点时保持不可变，和 Java `IntentNodeUpdateRequest` 的字段范围对齐，避免 Go 侧把路由键和知识库绑定在更新时改掉。
- 补齐知识库分块策略回显：`GET /knowledge-base/chunk-strategies` 现在只返回 Java `ChunkingMode` 对应的 `fixed_size` / `structure_aware` 两项，并对齐默认配置与展示文案，避免前端看到已经不再支持的 `paragraph` / `semantic` 选项。
- 补齐 ingestion 飞书来源执行能力：`FetcherNode` 现在支持 Java `SourceType.FEISHU`，可通过 `accessToken/tenantAccessToken` 或 `app_id/app_secret` 获取鉴权后拉取飞书 docx/docs `raw_content`，也支持普通飞书 URL 带鉴权 GET，避免任务创建允许 `feishu` 但执行节点报“不支持的来源类型”。

## 2026-07-26

- 补齐意图树批量操作层级保护：批量停用父节点前会拒绝遗漏未选中的启用后代；批量删除父节点前必须选择完整子树，遗漏任何后代都会拒绝，对齐 Java `batchDisableNodes` / `batchDeleteNodes`。
- 补齐意图树 Java 知识库绑定语义：创建 TOPIC 级 KB 节点必须指定 `kbId`；传入 `kbId` 时会查询知识库并用其 `collectionName` 回填意图节点，避免请求侧伪造或写错 collection。
- 补齐意图树 Java 创建/更新校验：创建节点会主动拒绝重复 `intentCode`，节点级 `topK` 未传时保留全局默认语义，显式配置时必须大于 0；`enabled` 未传时默认启用，显式传 `0` 仍会保留禁用，对齐 Java `IntentTreeServiceImpl.createNode` / `normalizeTopK`。
- 补齐意图节点 `examples` Java 请求形态：创建/更新节点时现在同时支持历史字符串和 Java `List<String>` 数组，数组会按 Gson 语义保存为 JSON 数组字符串，兼容前端直接传 `examples: ["示例"]`。
- 补齐意图树 Java 兼容路径返回形状：`POST /intent-tree` 现在返回新节点 ID 字符串，`PUT /intent-tree/{id}` 返回空成功，保留 `/intent-tree/nodes` 扩展接口返回完整节点对象，继续对齐 Java `IntentTreeController`。
- 补齐 RAG 会话记忆窗口 Java 语义：DBMemoryStore 内部加载上下文时改为按 `historyKeepTurns * 2` 取最新消息，再恢复升序传给模型；同时过滤空内容/非 user-assistant 消息并丢弃窗口开头的 assistant，避免长会话把最旧 100 条或不完整助手回复带入上下文；会话消息列表 API 仍保持默认全量返回。
- 补齐会话消息列表 Java 默认语义：`GET /conversations/{conversationId}/messages` 未传 `limit` 时不再默认截断 100 条，而是按 Java `ConversationMessageServiceImpl.listMessages(..., limit=null, ASC)` 返回该会话全量升序历史；显式 `limit` 仍可限制条数。
- 补齐轻量 LLM 本地优先鲁棒性：不走 RAG 强证据回答的普通回答链路、rewrite、会话摘要、标题生成、MCP 工具选择和参数抽取继续统一走 `preferredLLMService`；当配置存在 `ollama` provider 但未显式配置候选时，会自动补 `qwen3-local -> qwen3.6:latest` 作为本地优先模型，本地不可用时降级到原云端路由。
- 补齐关键词映射列表 Java 分页语义：`GET /intent-tree/term-mappings` / `/mappings` 现在支持 `current/page/size/domain/keyword`，关键词会模糊匹配 `sourceTerm` 或 `targetTerm`，排序改为 `priority ASC, update_time DESC`，对齐 Java `QueryTermMappingAdminServiceImpl.pageQuery`。
- 补齐 RAG 评测检索链路：`/api/ragent/rag/eval` 现在使用带意图上下文的完整 retriever，并将返回的文档 ID 优先归一化为 `doc_name` 去扩展名后的业务码，对齐 Java `EvalController` 的评测集字段语义。
- 补齐摄取分块策略生效：Go 侧 `ChunkerNode` 现在会在纯文本 fallback 链路读取 Java `ChunkerSettings.strategy`，`structure_aware` 按 Markdown 标题边界切分，`fixed_size` 保持固定大小切分。
- 补齐知识库 pipeline 任务 metadata 汇总：增强节点生成的 `keywords/questions` 会随 ingestion context metadata 一起返回给任务服务并写回任务详情，对齐 Java `buildTaskMetadata`。
- 补齐 ingestion 条件跳过日志语义：pipeline 节点 `condition` 运行时可解析任意 JSON 值；条件未满足时记录 `Skipped: 条件未满足` 的成功日志而不是错误字段，任务节点会展示为 `skipped`，对齐 Java `NodeResult.skip`。
- 补齐摄取任务执行 metadata 回写：`TaskExecutionResult` 新增运行结果 metadata，任务完成时会合并请求 metadata 与执行器 metadata 后写回 `metadata_json`；知识库 pipeline 执行器会把 ingestion context metadata 带回任务结果。
- 补齐 ingestion VO 回显归一化：流水线节点类型、任务来源类型、任务状态、任务节点类型和任务节点状态现在按 Java VO 转换语义归一化回显，兼容历史或外部写入的大写/横线值。
- 补齐摄取任务上传文件内容透传：`POST /ingestion/tasks/upload` 现在会读取 multipart 文件 bytes，识别 MIME，并通过 `CreateTaskReq` 的运行时字段传给执行器；知识库 pipeline 执行器也会优先使用请求中的 `RawBytes/MimeType`。
- 补齐摄取任务节点状态归一化：任务节点状态保存前会统一为小写下划线，非失败节点消息以 `Skipped:` 开头时保存为 `skipped`，对齐 Java `resolveNodeStatus` / `normalizeNodeStatus`。
- 补齐摄取任务节点顺序语义：任务节点 `node_order` 现在按流水线 `nextNodeId` 链路计算，未被引用的节点作为起点，剩余未访问节点按配置顺序补尾，对齐 Java `buildNodeOrderMap`。
- 补齐摄取任务节点输出截断保护：任务节点 `output_json` 写库前会按 Java `truncateOutputJson` 语义限制在 1MB 内，超出时追加 `输出过大，已截断` 提示。
- 补齐摄取任务创建参数校验：`POST /ingestion/tasks` 现在会在查流水线与落任务表前校验 `pipelineId`、`source`、`source.type`，空流水线返回 `必须传流水线ID`，空来源返回 `文档来源不能为空`，空来源类型返回 `文档来源类型不能为空`，未知来源类型返回 `未知文档来源类型: {type}`。
- 补齐摄取流水线节点替换语义：`POST/PUT /ingestion/pipelines` 替换节点时会物理删除旧节点后重新写入，对齐 Java `physicalDeleteByPipelineId`，避免 `(pipeline_id,node_id,deleted)` 唯一约束下重复更新同一节点失败。
- 补齐摄取流水线创建/更新事务一致性：`POST/PUT /ingestion/pipelines` 的主表写入与节点替换现在共享同一 GORM 事务，节点写入失败时会整体回滚，避免留下半成功流水线。
- 补齐摄取流水线节点类型校验：`POST/PUT /ingestion/pipelines` 现在只接受 Java 枚举中的 `fetcher/parser/enhancer/chunker/enricher/indexer`，支持大小写与横线归一化，未知类型返回 `未知节点类型: {nodeType}` 并拒绝写入。
- 补齐摄取流水线创建重名校验：`POST /ingestion/pipelines` 创建前会 trim 名称并检查未删除流水线重名，重复时返回 `流水线名称已存在`，和 Java `DuplicateKeyException` 转业务异常的提示保持一致。
- 补齐分块日志 Java VO 字段：`GET /knowledge-base/docs/{docId}/chunk-logs` 现在返回 `pipelineName`，pipeline 分支会把摄取流水线执行耗时记入 `chunkDuration`，并按 Java 公式计算 `otherDuration`；pipeline 模式按 `total - chunk - persist`，普通 chunk 模式按 `total - extract - chunk - embed - persist`，负值归零。
- 补齐 URL 文档定时启用参数校验：创建 URL 文档时 `scheduleEnabled=1` 必须提供 `scheduleCron`，更新 URL 文档启用定时后也必须保留有效 `sourceLocation/scheduleCron`，不再静默清空定时配置，继续对齐 Java `validateSourceAndSchedule` 与更新入口 final schedule 校验。
- 补齐文档上传解析器前置校验：创建文档前会使用与分块阶段一致的 parser registry 检查 MIME 支持，未知扩展不再伪装成纯文本；无解析器支持的文件类型会返回 `暂不支持的文件类型` 并拒绝落库，继续对齐 Java `parserSelector.selectByMimeType` 前置拦截。
- 补齐文档 Pipeline 存在性校验：`processMode=pipeline` 的上传/更新会通过现有摄取流水线服务查询 `pipelineId`，不存在时返回 `指定的Pipeline不存在: {pipelineId}`，避免无效流水线 ID 落库，继续对齐 Java `ingestionPipelineService.get`。
- 补齐文档处理配置校验：文档上传/更新现在会拒绝非法 `processMode`；`processMode=chunk` 或 Java 分块策略 `fixed_size/structure_aware` 会校验 `chunkConfig` 必须是合法 JSON，并包含 Java 默认配置里的必要字段；更新文档也已补齐 `processMode/pipelineId` 请求字段，继续对齐 Java `ProcessMode.normalize` 与 `validateAndNormalizeChunkConfig`。
- 补齐文档上传 Pipeline 模式校验：`POST /knowledge-base/{kbId}/docs/upload` 现在会识别 `processMode` 表单字段，`processMode=pipeline` 时必须提供 `pipelineId`，缺失时拒绝创建文档，避免 Go 侧静默降级为普通 chunk 模式，继续对齐 Java `resolveProcessModeConfig`。
- 补齐用户列表 Java 分页语义：`GET /users` / `/admin/users` 现在支持 `current/page/size/keyword`，关键词会匹配用户名或角色，并按 `update_time DESC` 返回，继续对齐 Java `UserServiceImpl.pageQuery`。
- 补齐用户管理默认管理员保护与参数校验：`/users` 创建会 trim 用户名并拒绝创建 `admin` 默认管理员用户名；更新/删除默认 `admin` 账号会被拒绝，更新用户名为 `admin` 也会被拒绝；创建/更新会主动校验用户名重名，角色仅允许 `admin/user`，同时补齐密码非空校验、角色归一化和 avatar trim，继续对齐 Java `UserServiceImpl`。
- 补齐 URL 文档上传 Java 对齐：`POST /knowledge-base/{kbId}/docs/upload` 在 `sourceType=url` 且未上传文件时，会立即通过 `sourceLocation` 抓取远端文件，识别文件名、类型与大小，创建文档记录后写入知识库 collection 下的文件存储，保证后续手动分块能读取到原文。
- 补齐用户账号接口 Java 对齐：`POST /auth/login` 响应补齐 `userId/role/avatar`，JWT 登录态携带 avatar，`GET /user/me` / `/auth/current-user` 返回 Java 风格 `userId/username/role/avatar` 并保留旧 `id` 字段；`PUT /user/password` 支持 Java 请求字段 `currentPassword/newPassword`，同时兼容旧 `oldPassword`。
- 补齐手工 Chunk CRUD Java 对齐：`POST /knowledge-base/docs/{docId}/chunks` 支持 `content/index/chunkId`，会在文档 `running` 或禁用时拒绝创建，并同步写入向量与递增 `chunk_count`；`PUT` / `DELETE` 会校验 Chunk 归属与文档运行态，更新内容时刷新 hash/字符数/token 数和向量，删除时软删 Chunk、删除向量并安全递减 `chunk_count`，HTTP 响应保持 Java 风格空成功。

## 2026-07-25

- 补齐知识库列表/重命名 Java 对齐：`GET /knowledge-base` 现在支持 `name` 模糊过滤、按 `update_time` 倒序并回显 `documentCount`；`PUT /knowledge-base/{id}` 支持只传 `name` 的重命名请求，保留原 embedding/collection，创建返回新 ID、更新返回空成功；已有分块文档时会拒绝修改 embedding 模型。
- 补齐示例问题 Java 对齐：`GET /sample-questions` 现在返回分页对象并支持 `current/page/size/keyword`，`GET /rag/sample-questions` 单独返回随机 3 条欢迎页问题；创建/更新会 trim 字段并拒绝空白 `question`，创建接口返回新 ID、更新接口返回空成功，对齐 Java `SampleQuestionController` / `SampleQuestionServiceImpl`。
- 补齐文档源文件下载路径：`GET /knowledge-base/docs/{docId}/file` 现在会先按文档所属知识库的 `collectionName` 读取对象存储中的原始文件，并设置 inline 文件名与更准确的 Content-Type；找不到原始文件时仍保留原有分块预览文本兜底，对齐 Java `KnowledgeDocumentController.file` 的源文件直出语义。
- 补齐文档源文件 MIME 映射：CSV / XLS / XLSX 下载会返回 Java 侧 `CONTENT_TYPE_MAP` 对应的 `text/csv`、`application/vnd.ms-excel`、`application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`，避免浏览器把表格文件按普通文本处理。

## 2026-07-24

- 补齐 Indexer 节点 embedding 模型配置：Go 侧 ingestion `IndexerNode` 现在会读取 `settings.embeddingModel` 并调用 `EmbedBatchWithModel`，支持按 pipeline 节点指定向量模型，对齐 Java `IndexerSettings.embeddingModel`。
- 补齐 Parser 节点规则配置：Go 侧 ingestion `ParserNode` 现在会读取 `settings.rules[].mimeType/options`，按 Java `ParserSettings` 语义校验文档类型，并把匹配规则的 options 透传给底层 parser；同时保留 `sourceFile/documentId/sourceURL/sourceType` 的默认注入逻辑。
- 补齐消息反馈异步 MQ：`POST /conversations/messages/{messageId}/feedback` 与 `DELETE /conversations/messages/{messageId}/feedback` 现在会在 RocketMQ 可用时优先发送 `message-feedback_topic`，消费者收到 `MessageFeedbackEvent` 后再异步落库到 `t_message_feedback`；取消反馈会写 tombstone，active/cancel 事件都按 `submitTime` 做最终写入保护，MQ 不可用时保留原来的同步写入 / 删除路径。
- 补齐知识库删除约束：`KnowledgeBaseService.Delete` 现在会先检查知识库下是否仍有未删除文档，存在文档时直接拒绝删除，避免留下孤儿文档与向量数据，对齐 Java `KnowledgeBaseServiceImpl.delete` 的保护语义。
- 补齐知识库分块 / 清理 MQ：文档分块与知识库删除现在会在 RocketMQ 可用时优先发送 `knowledge-document-chunk_topic` / `knowledge-base-cleanup_topic` 事件，本地不可用时自动回退为原来的同步 goroutine / 直接清理路径；对应消费者也已在 `cmd/ragent/main.go` 注册并启动。
- 补齐知识库分块 / 清理事务消息：`SendInTransaction` 已接入文档分块和知识库删除两条链路，本地事务回调会负责状态更新 / 软删提交，`cmd/ragent/main.go` 也已注册对应 topic 的回查逻辑，对齐 Java 的事务消息语义。
- 补齐知识库清理消费者：Go 侧已补 `KnowledgeDocumentChunkConsumer` / `KnowledgeBaseCleanupConsumer` 对应的事件处理函数，分块任务会恢复操作者上下文，知识库清理事件会执行向量空间删除。
- 补齐知识库创建时的向量空间预创建：`KnowledgeBaseService.Create` 现在会在落库后调用 `EnsureVectorSpace`，让知识库生命周期更接近 Java 侧的 `createKnowledgeSpace + ensureVectorSpace` 行为。
- 补齐知识库物理空间清理：文件存储已支持按知识库 collection 前缀存取与删除，`KnowledgeBaseCleanupConsumer` 会真正调用 `DeleteKnowledgeSpace` 清掉对应空间下的对象，知识库删除的物理收口更接近 Java `deleteKnowledgeSpace`。
- 补齐知识库删除 MQ 失败回滚：MQ 清理事件发送失败时会恢复知识库软删状态，避免出现“接口报错但知识库已删除、清理事件又没发出”的不一致状态，向 Java 事务消息语义继续靠近。
- 补齐知识库文档定时刷新后台执行：`cmd/ragent/main.go` 现在会随主进程启动 `DocumentScheduleService.Run`，让到期 schedule 真正被周期扫描执行；服务退出时通过应用级 context 停止后台循环。
- 补齐卡死分块恢复：定时刷新后台循环会每分钟扫描超出 `rag.knowledge.schedule.running-timeout-minutes` 的 `running` 文档并重置为 `failed`，默认 30 分钟，对齐 Java `KnowledgeDocumentScheduleJob.recoverStuckRunningDocuments` 的恢复语义。
- 补齐消息反馈对齐：`CreateFeedback` 现在会先按 `messageId + userId` 反查消息并校验必须是 `assistant` 消息，反馈记录的 `conversation_id` 也改为从消息表反查，不再信任请求体里的 `conversationId`；`FeedbackReq` 的 `conversationId` 绑定改为可选，和 Java `MessageFeedbackRequest` 的请求形状保持一致。

## 2026-07-21

- 补齐 Java 对齐缺口：新增 `ticket_query` MCP 工具，并将 `RenderBlocks` 对齐到 Java 的 `BlockTextRenderer` 语义，修正标题、列表、图片与块间空行输出。
- 补齐 ingestion 对齐能力：Go 侧 ingestion 通用引擎现在兼容 Java 风格节点类型归一化，并会把节点日志与执行状态回写到上下文中。
- 补齐摄取节点能力：Go 侧新增基础版 `Fetcher / Parser / Enhancer / Enricher / Indexer` 节点，实现了文件/URL 获取、结构化解析、LLM 增强、分块增强与向量索引写入的可执行链路。
- 补齐流水线执行能力：`DocumentService.ExecuteIngestionPipelineTask` 现已真正接入 ingestion engine，按 pipeline 节点顺序执行并回写真实节点输出，不再手工拼接伪节点结果。
- 补齐分块兼容：`ChunkerNode` 现支持 Java 的 `chunkSize=-1` 整篇保留语义，fallback 文本会直接生成单个 document chunk。
- 补齐表格分块：结构化分块现在支持 `rowsPerChunk`，表格 chunk 会重复表头并使用 key-value `EmbeddingText` 做向量输入，避免大表被整块吞入或用 markdown 表格直接嵌入。
- 补齐 block-aware 上下文：标题块现在只更新后续 chunk 的 `outline_path` 元数据，不再混入正文 chunk；相邻段落/列表/图片会按体量预算打包，减少图文和短文本碎片化。
- 补齐 VectorChunk 载荷：Go 侧 `VectorChunk` 现承载 `BlockType / OutlinePath / SourceBlockIDs / Assets / SectionContext`，并在 PgVector / Milvus metadata 中保留结构化检索字段。
- 补齐列表分块：结构化分块现在支持 `maxListItems / listItemsPerChunk`，长列表会按项分组并保留有序列表编号，`ChunkerNode` 可从 pipeline settings 透传这些参数。
- 补齐 Block 来源追踪：Go 侧 `Block` 现包含 `ID`，chunker 会把显式 block id 或 fallback id 写入 `VectorChunk.SourceBlockIDs` 与 metadata，补齐 Java `Block.id()` 到 `VectorChunk.sourceBlockIds` 的链路。
- 补齐 Provenance 来源追踪：Go 侧 `Block / VectorChunk` 现承载 `SourceFile / SheetName`，CSV 会写入 `sourceFile`，XLSX 会通过 `workbook.xml` + `workbook.xml.rels` 读取首个可见 sheet 的真实名称与 worksheet 路径，并在缺失时回落 `sheet1`；普通分块与 `chunkSize=-1` 整篇分块都会在 PgVector / Milvus metadata 中保留 `source_file / sheet_name`。
- 补齐 ingestion 条件与输出：Go 侧 `ConditionEvaluator` 现在兼容 Java 常见字符串条件表达式（字段 null 比较、`#ctx` 前缀、`contains` / `matches`、简单逻辑组合），Fetcher 节点日志 output 同步写入 `rawBytesBase64`，继续对齐 Java `ConditionEvaluator` / `NodeOutputExtractor`。
- 补齐 KB 上下文格式化：RAG Prompt 注入前会按 `doc_id` 聚合召回片段，文档间保留首次命中相关性顺序，文档内按 `chunk_index` 升序还原原文顺序，并用去扩展名后的 `doc_name` 生成 `<content source="...">` 锚点。
- 补齐 MCP 上下文格式化：工具返回 `text` 时按正文注入 `<data>`，返回 `isError=true` 或执行失败时集中写入 `<errors>` 的 `- 工具调用失败: ...` 列表，避免把远端 MCP 错误当普通事实喂给模型。
- 补齐 Prompt evidence/question 结构：默认 PromptBuilder 会把 MCP 证据包进 `<tool-data>`、KB 证据包进 `<documents>`，单问题使用 `<question>`，多子问题使用 `<questions>` 编号列表，主 Pipeline 会透传 query rewrite 产生的 `subQuestions`。
- 补齐 SYSTEM-only 意图分支：当意图解析结果全为 `IntentKindSystem` 时，Pipeline 会跳过 KB 检索，优先使用节点 `promptTemplate` 作为系统提示词直接流式回答；未配置模板时回落默认系统提示。
- 补齐 SSE 完成事件标题语义：新会话或无标题会话完成时，`finish` 会回传生成后的 `title`；已有标题的历史会话不重复下发 `title`，对齐 Java `CompletionPayload(messageId, title)` 行为。
- 补齐 SSE 完成/取消事件消息 ID：助手消息现在会先落库再返回 `finish` / `cancel` 的 `messageId`，并同步保存 thinking 内容与时长；流式 `message` 事件也按 `ai.stream.message-chunk-size` 进行 rune 级分块，继续对齐 Java `StreamChatEventHandler`。
- 补齐 RAG 聊天全局队列限流：主聊天入口已接入公平队列 limiter，超时会直接返回 `reject + finish + done` SSE，并在可落库时补齐消息与标题，对齐 Java `ChatQueueLimiter`。
- 补齐会话摘要加载边界：`LoadHistory` 在没有任何消息时会返回空历史，即使存在摘要记录也不单独注入摘要，对齐 Java `DefaultConversationMemoryService.attachSummary`。
- 补齐会话摘要压缩节流：摘要生成会按 `history-keep-turns` 半窗口裁剪待压缩消息，旧摘要仍覆盖最近窗口时不重复刷新；主服务会异步执行摘要压缩并通过 Redis 锁串行化同一会话任务，配置加载同步校验 Java 版 `history-keep-turns`、`summary-max-chars`、`title-max-length` 范围及 `summary-start-turns > history-keep-turns` 关系。
- 补齐会话重命名校验：标题更新会先 trim，再校验非空与 `rag.memory.title-max-length` 上限，错误消息和 Java 版“会话名称不能为空 / 不能超过 N 个字符”保持一致。

## 2026-07-22

- 补齐意图驱动检索透传：`Pipeline` 现在会把 `SubQuestionIntent` 通过 `SearchContext.Intents` 传给 intent-aware retriever，`MultiChannelRetriever` / `RerankRetriever` 会保留这份上下文；`IntentDirectedSearchChannel` 可直接按 KB 意图节点的 `collectionName/topK` 定向检索，对齐 Java `RetrievalEngine` / `IntentDirectedSearchChannel` 的核心路由语义。
- 补齐意图分类 LLM 主路径：`IntentResolver` 现在优先用本地优先的 `LLMService` 按 Java `intent-classifier.st` 风格给叶子节点打分，LLM 解析失败时自动回退到现有启发式评分；同时补齐多子问题总量裁剪，保持与 Java `IntentResolver.capTotalIntents` 接近的保底分配语义。
- 补齐意图树 Redis 缓存链路：新增缓存型 `IntentNodeLister`，意图解析与歧义澄清会优先读取 `ragent:intent:tree` 缓存，未命中回源数据库并回填 7 天缓存；意图节点创建、更新、启停、删除后会清理缓存，对齐 Java `IntentTreeCacheManager`。
- 补齐查询词归一化缓存链路：`DBQueryTermNormalizer` 现在会优先读 Redis 缓存的术语映射，缓存未命中再回源数据库并回填；`IntentService` 在关键词映射创建、更新、删除后会主动清缓存，对齐 Java `QueryTermMappingCacheManager` / `QueryTermMappingService`。
- 补齐意图歧义澄清收口：`IntentGuidanceService` 现在只对 KB 候选做澄清，按父链聚合系统节点后再判断是否需要提示；当问题已经包含领域名称时直接跳过澄清，边界区间会先交给 LLM 歧义复核器确认再决定是否追问。
- 补齐知识库文档定时刷新状态收口：远端内容未变化、文档正在分块时统一写入 `skipped` 调度状态与执行日志；调度成功/失败/跳过写回会校验当前 `lock_owner`，锁已转移时不覆盖调度主状态，并在执行日志中标注锁失效。
- 补齐知识库文档定时刷新同步分块：调度链路现在会优先调用同步分块入口等待文档分块完成后再写回成功态，异步分块仍保留给普通上传接口使用。
- 补齐文档启停 Java 对齐：文档处于 `running` 时禁止切换启用状态；启停文档会同步已有 schedule 的 `enabled/next_run_time`，禁用时保留调度记录并置为不可运行。
- 补齐 Chunk 启停 Java 对齐：文档处于 `running` 时禁止单条或批量修改 Chunk 启用状态；批量接口允许 Java 的空请求体进入业务校验，返回“请指定需要操作的 Chunk”而不是 JSON 解析错误。
- 补齐用户管理权限对齐：`/users` 创建、查询、更新、删除现在要求登录用户角色为 `admin`，普通用户会返回“无权限”，对齐 Java `StpUtil.checkRole("admin")`。
- 补齐登出会话失效：JWT 登录令牌现在带 `jti`，`/auth/logout` 会把当前 token 写入 Redis 黑名单直到自然过期，后续请求会被视为未登录，避免登出后旧 token 继续可用。
- 补齐关键词映射参数语义：创建/更新会 trim `sourceTerm/targetTerm/remark/domain`，空白原始词或目标词会返回业务错误；创建未传 `matchType/priority/enabled` 时分别按 Java 默认值 `1/0/1` 落库，同时保留显式禁用规则的能力。
- 补齐文档调度 cron 校验：创建/更新 URL 定时文档时会按 `rag.knowledge.schedule.min-interval-seconds` 拒绝过短周期，并按 cron 计算下一次执行时间。
- 补齐文档删除清理：删除知识库文档时会同时清理 schedule 与 schedule_exec 记录，对齐 Java `deleteByDocId` 的收口行为。
- 补齐文档删除全量清理：删除知识库文档时会继续软删关联 chunk、硬删 chunk_log，并按知识库集合名清理向量，进一步对齐 Java 删除入口的完整收口链路。
- 补齐文档删除对象文件清理：删除知识库文档时会继续尝试清理对象存储中的原始文件，失败仅记录 warn，不阻断主删除流程，对齐 Java `deleteStoredFileQuietly` 的语义。
- 补齐文档定时刷新锁心跳：长时间调度刷新会周期性续约 `lock_until`，避免长文档刷新期间锁过期后被其它实例抢占，对齐 Java 的 lease / heartbeat 稳态语义。
- 补齐多租户查询词隔离：查询词归一化现在会优先读取 request tenant 的 `domain`，仅加载对应领域的关键词映射，避免不同业务域的别名规则相互污染。
- 补齐文档更新 Java 对齐：`running` 文档禁止更新，文档名不能为空；URL 文档更新来源地址和 schedule 字段会真实持久化并同步调度表。
- 补齐文档删除运行态保护：文档处于 `running` 时禁止删除，对齐 Java 删除入口的状态保护。
- 补齐本地优先 LLM 策略：`ragent` 的普通回答链路（无 KB/MCP 证据时）、query rewrite、会话摘要、会话标题以及 MCP 工具选择/参数抽取现在优先走本地 Ollama `qwen3.6:latest`，带 KB / MCP 证据的 RAG 主回答继续保留现有阿里云路由，本地不可用时回退到云端。
- 补齐 MCP 并发执行：`DefaultMcpContextProvider` 现在会并发执行被选中的多个 MCP 工具，保持输出顺序稳定，并沿用 Java 版的工具错误聚合语义。
- 补齐 MCP 自定义抽参模板：Pipeline 会把已解析意图节点的 `paramPromptTemplate` 传给 MCP 上下文构建器，LLM 参数抽取器可按节点自定义 system prompt 抽取参数，对齐 Java `LLMMcpParameterExtractor.extractParameters(..., customPromptTemplate)`。
- 补齐 MCP 域授权过滤：MCP 工具与远端 server 注册现在会携带 `domains` 元数据，主服务按 request tenant 的 `domain` 过滤可见工具，独立 `mcp-server` 也会按 `X-Tenant-Domain` 只暴露允许的工具。

## 阶段 5 — 知识库 + 文档入库 Pipeline 交付物清单

### 新增文件（34 个）

#### 知识库 CRUD (`internal/biz/knowledge/`)
| 文件 | 包 | 说明 |
|------|-----|------|
| `model/knowledge_base.go` | model | KnowledgeBase GORM 模型 |
| `model/knowledge_document.go` | model | KnowledgeDocument + KnowledgeChunk GORM 模型 |
| `repo/knowledge_base.go` | repo | KnowledgeBaseRepo（7 方法） |
| `repo/knowledge_document.go` | repo | KnowledgeDocumentRepo（8 方法） |
| `repo/knowledge_chunk.go` | repo | KnowledgeChunkRepo（10 方法） |
| `service/knowledge_base.go` | service | KnowledgeBaseService（5 方法） |
| `service/knowledge_document.go` | service | DocumentService（11 方法） |
| `handler/knowledge_base.go` | handler | KnowledgeBaseHandler（6 端点） |
| `handler/knowledge_document.go` | handler | DocumentHandler（15 端点） |
| `dto/knowledge_base.go` | dto | 知识库 DTO |
| `dto/knowledge_document.go` | dto | 文档/分块 DTO |

#### 文档解析 (`internal/biz/core/parser/`)
| 文件 | 说明 |
|------|------|
| `registry.go` | Parser 注册表 + 自动 MIME 匹配 |
| `markdown.go` | Markdown 解析器 |
| `plaintext.go` | 纯文本解析器 |
| `office.go` | PDF + DOCX 解析器占位 |
| `tika.go` | Tika HTTP 解析器（兼容路径） |

#### 分块策略 (`internal/biz/core/chunk/`)
| 文件 | 说明 |
|------|------|
| `chunker.go` | ParagraphChunker + SemanticChunker + StrategyFactory |

#### 入库 Pipeline (`internal/biz/core/ingestion/`)
| 文件 | 说明 |
|------|------|
| `engine.go` | DefaultEngine — 解析→分块→向量化→写入 |

#### 向量库适配
| 文件 | 说明 |
|------|------|
| `internal/biz/rag/vector_pg.go` | PgVectorStore 实现 |
| `internal/biz/rag/vector_milvus.go` | MilvusVectorStore 实现 |
| `internal/biz/rag/vector_factory.go` | rag.vector.type 向量库切换工厂 |

#### 框架增强
| 文件 | 说明 |
|------|------|
| `internal/framework/db/db.go` | 数据库连接管理器 |
| `internal/framework/db/model.go` | BaseModel + 软删除 |
| `internal/framework/db/scope.go` | 分页 Scope |
| `internal/framework/convention/page.go` | PageResp 统一分页响应 |
| `internal/framework/middleware/db.go` | DB 中间件 |
| `resources/database/schema_pg.sql` | PostgreSQL schema（20 表） |
| `resources/database/init_data_pg.sql` | 初始数据 |

### 修改文件
| 文件 | 变更 |
|------|------|
| `go.mod` | 新增 gorm、pgx、pgvector 依赖 |
| `cmd/ragent/main.go` | DB 初始化 + 知识库/文档路由注册 |

- [x] `go mod init github.com/nageoffer/go-base-agent`
- [x] 目录骨架（cmd/internal/pkg/configs/deploy/scripts/docs/testdata）
- [x] `Makefile`（run/build/test/lint/migrate/fmt/vet/mod/clean）
- [x] `.golangci.yml` golangci-lint 配置
- [x] `deploy/docker-compose.yml`（PG16+pgvector、Redis7、RocketMQ5 namesrv+broker、RustFS）
- [x] `configs/config.yaml`（本地真实配置，gitignore）+ `configs/config.example.yaml`（提交版模板）
- [x] `.env`（本地环境变量，gitignore）+ `.env.example`（提交版模板）
- [x] `scripts/migrate.sh`（执行 schema_pg.sql + init_data_pg.sql）
- [x] `internal/framework/config/config.go` — Config 结构体 + Load 函数（godotenv → ExpandEnv → viper Unmarshal）
- [x] `cmd/ragent/main.go` — 从硬编码改为 config.Load 读取端口
- [x] `cmd/mcp-server/main.go` — 占位入口
- [x] `README.md` — 项目说明文档
- [x] `.gitignore`
- [x] 启动 Go 服务验证 health 端点 ✅

> 2026-07-14 补充：文档解析链路已从占位实现切换为真实解析，新增 PDF / DOCX / CSV / XLSX 解析器与 `StructureAwareChunker`，文档入库主链路已接入 parser registry。
> 2026-07-17 补充：新增图片文档图生文（VLM + 资产上传）与 MinerU 复杂版面解析，并把 `sourceFile` / `documentId` / `sourceURL` 等解析上下文传入入库主链路。

## 阶段 2 — infra-ai 层 交付物清单

### 新增包

| 包 | 文件数 | 说明 |
|----|--------|------|
| `internal/infra/model/` | 8 | Capability/Target/URL/HealthStore/Selector/Executor + 测试 |
| `internal/infra/chat/` | 10 | LLMService/ChatClient/StreamCallback/RoutingLLMService/SSEParser/StreamExecutor/OpenAIClient/Probe + 测试 |
| `internal/infra/embedding/` | 5 | Service/Client/RoutingService/OpenAIClient + 测试 |
| `internal/infra/rerank/` | 5 | Service/Client/RoutingService/NoopClient + 测试 |

### 修改文件

- [x] `internal/framework/config/config.go` — AIProvidersConfig → map，增加 Protocol/Enabled/URL/VLM 字段
- [x] `internal/framework/config/config_test.go` — 3 个配置解析测试
- [x] `configs/config.example.yaml` — 新增 openai/anthropic/noop provider，protocol 字段
- [x] `.env.example` — 新增 OPENAI_API_KEY、ANTHROPIC_API_KEY
- [x] `cmd/ragent/main.go` — AI 组件组装：HealthStore/Selector/Executor/Chat/Embedding/Rerank
- [x] `README.md` — 环境变量清单、架构图更新

### 测试覆盖

| 包 | 测试数 | 覆盖内容 |
|----|--------|----------|
| `config` | 3 | map 解析、IsEnabled、URL 覆盖 |
| `model` | 28 | URL 解析、三态熔断、选择器排序筛选、执行器 fallback |
| `chat` | 21 | LLMService 路由、SSE 解析、OpenAI 客户端同步/流式、首包探测、集成测试 |
| `embedding` | 6 | 单条/批量向量化、httptest 模拟 |
| `rerank` | 4 | Noop 截断、路由 fallback |
| **合计** | **62** | **-race 全绿** |

### 阶段文档

- [x] `docs/superpowers/specs/2026-07-07-phase2-pluggable-ai-rag-design.md` — 总体设计
- [x] `docs/phase2-1-config-pluggable.md` — 配置可插拔化
- [x] `docs/phase2-2-model-core.md` — 模型目标与协议解析
- [x] `docs/phase2-3-health-store.md` — 三态熔断
- [x] `docs/phase2-4-selector.md` — 模型选择器
- [x] `docs/phase2-5-executor.md` — 路由执行器
- [x] `docs/phase2-6-chat-interface.md` — Chat 能力接口
- [x] `docs/phase2-7-openai-client.md` — OpenAI 兼容客户端 + SSE
- [x] `docs/phase2-8-embedding.md` — Embedding 能力接口
- [x] `docs/phase2-9-rerank.md` — Rerank 能力接口
- [x] `docs/phase2-10-integration.md` — 集成组装

### 配置安全说明

- **提交到 git**：`configs/config.example.yaml`、`.env.example`
- **仅本地，不提交**：`configs/config.yaml`、`.env`、`.env.local`
- 生产环境计划使用配置中心（Apollo/Nacos/K8s ConfigMap），不依赖 `.env` 文件
- 开源前建议跑 `gitleaks detect` 检查无密钥泄漏

## 阶段 4B — 检索增强生成闭环 交付物清单

### 新增文件（15 个）

#### 用户/鉴权 (`internal/biz/user/`)
| 文件 | 包 | 说明 |
|------|-----|------|
| `model/user.go` | model | User GORM 模型 |
| `repo/user.go` | repo | UserRepo（FindByUsername/FindByID） |
| `service/auth.go` | service | AuthService（Login/ParseToken/TokenName） |
| `handler/auth.go` | handler | AuthHandler（Login/Logout/CurrentUser） |
| `dto/auth.go` | dto | LoginReq/LoginResp/UserInfoResp |

#### 会话管理 (`internal/biz/conversation/`)
| 文件 | 包 | 说明 |
|------|-----|------|
| `model/conversation.go` | model | Conversation/Message/Summary/Feedback 模型 |
| `repo/conversation.go` | repo | ConversationRepo + MessageRepo + FeedbackRepo |
| `service/conversation.go` | service | ConversationService + DBMemoryStore（MemoryStore 实现） |
| `handler/conversation.go` | handler | ConversationHandler（6 端点） |
| `dto/conversation.go` | dto | ConversationResp/MessageResp/UpdateTitleReq/FeedbackReq |

#### 检索/改写 (`internal/biz/rag/`)
| 文件 | 说明 |
|------|------|
| `pg_retriever.go` | PgRetriever（pgvector 余弦搜索，替换 NoopRetriever） |
| `llm_rewriter.go` | LLMRewriter（LLM 查询改写，替换 NoopRewriter） |

#### 中间件 (`internal/framework/middleware/`)
| 文件 | 说明 |
|------|------|
| `auth.go` | Auth 中间件 + RequireAuth + GetLoginUser |

### 修改文件
| 文件 | 变更 |
|------|------|
| `cmd/ragent/main.go` | 组装真实检索链路 + Auth/Conversation 路由 + middleware |
| `internal/framework/config/config.go` | AuthConfig 新增 JWTSecret 字段 |
| `configs/config.yaml` | 新增 `timeout-seconds`/`jwt-secret` |
| `internal/biz/knowledge/handler/knowledge_base.go` | userID() 改为从 LoginUser 获取 |
| `go.mod` | 新增 `github.com/golang-jwt/jwt/v5` |

### 新增 API 端点（10 个）
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/ragent/auth/login` | 登录（JWT 签发） |
| POST | `/api/ragent/auth/logout` | 登出 |
| GET | `/api/ragent/auth/current-user` | 当前登录用户 |
| GET | `/api/ragent/conversations` | 问答页会话列表，默认 `data` 为数组；`paged=true` 时返回分页对象 |
| GET | `/api/ragent/conversations/:conversationId` | 会话详情 |
| GET | `/api/ragent/conversations/:conversationId/messages` | 消息历史 |
| PUT | `/api/ragent/conversations/:conversationId/title` | 更新标题 |
| DELETE | `/api/ragent/conversations/:conversationId` | 删除会话 |
| POST | `/api/ragent/conversations/feedback` | 消息反馈 |

### RAG Pipeline 组件替换
```
NoopRewriter      ──→  LLMRewriter (LLM 查询改写)
NoopRetriever     ──→  PgRetriever (pgvector 余弦搜索)
NoopMemoryService ──→  DefaultMemoryService + DBMemoryStore (PostgreSQL)
```

## 阶段 5.1 — MCP Server 交付物清单

### 新增文件（3 个）

| 文件 | 包 | 说明 |
|------|-----|------|
| `internal/biz/mcp_tool/protocol.go` | mcp_tool | JSON-RPC 2.0 请求/响应/错误类型定义 |
| `internal/biz/mcp_tool/server.go` | mcp_tool | JSON-RPC 分发器 + HTTP Handler |
| `internal/biz/mcp_tool/tools.go` | mcp_tool | MCP 工具实现（4 个工具） |

### 修改文件
| 文件 | 变更 |
|------|------|
| `cmd/mcp-server/main.go` | 占位 → 完整实现（DI + HTTP Server + AI 组件） |

### MCP 协议方法
| 方法 | 说明 |
|------|------|
| `initialize` | 返回服务器能力声明（协议版本 2024-11-05） |
| `ping` | 心跳检测 |
| `tools/list` | 列出所有可用工具及 JSON Schema |
| `tools/call` | 调用工具执行 |

### MCP 工具（4 个）
| 工具名 | 参数 | 说明 |
|------|------|------|
| `search_knowledge_base` | question(必填), kb_name, top_k | 向量相似度跨知识库搜索 |
| `search_documents` | keyword(必填), kb_id | 关键词搜索文档 |
| `list_knowledge_bases` | — | 列出所有知识库 |
| `get_document_chunks` | doc_id(必填) | 获取文档分块内容 |

## 阶段 5.2 — 意图树 交付物清单

### 新增文件（5 个）
| 文件 | 包 | 说明 |
|------|-----|------|
| `internal/biz/intent_tree/model/intent.go` | model | IntentNode + QueryTermMapping GORM 模型 |
| `internal/biz/intent_tree/repo/intent.go` | repo | IntentRepo (11 方法) + TermMappingRepo (5 方法) |
| `internal/biz/intent_tree/service/intent.go` | service | IntentService (13 方法, 含树形构建) |
| `internal/biz/intent_tree/dto/intent.go` | dto | 请求/响应 DTO |
| `internal/biz/intent_tree/handler/intent.go` | handler | IntentHandler (11 端点) |

### 新增 API 端点（11 个）
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/ragent/intent-tree/tree` | 获取意图树（树形结构） |
| GET | `/api/ragent/intent-tree/nodes` | 意图节点列表（分页平铺） |
| POST | `/api/ragent/intent-tree/nodes` | 创建意图节点 |
| GET | `/api/ragent/intent-tree/nodes/:id` | 节点详情 |
| PUT | `/api/ragent/intent-tree/nodes/:id` | 更新节点 |
| DELETE | `/api/ragent/intent-tree/nodes/:id` | 删除节点 |
| PATCH | `/api/ragent/intent-tree/nodes/:id/enable` | 启用/禁用 |
| GET | `/api/ragent/intent-tree/term-mappings` | 关键词映射列表 |
| POST | `/api/ragent/intent-tree/term-mappings` | 创建映射 |
| PUT | `/api/ragent/intent-tree/term-mappings/:id` | 更新映射 |
| DELETE | `/api/ragent/intent-tree/term-mappings/:id` | 删除映射 |

## 阶段 5.3 — Admin 管理后台 交付物清单

### 新增文件（5 个）
| 文件 | 包 | 说明 |
|------|-----|------|
| `internal/biz/admin/model/sample_question.go` | model | SampleQuestion GORM 模型 |
| `internal/biz/admin/repo/admin.go` | repo | AdminRepo (Dashboard/Trace) + SampleQuestionRepo |
| `internal/biz/admin/service/admin.go` | service | AdminService (Dashboard/Trace/用户管理/示例问题) |
| `internal/biz/admin/dto/admin.go` | dto | DashboardResp / TraceResp / UserResp / SampleQuestionResp |
| `internal/biz/admin/handler/admin.go` | handler | AdminHandler (16 端点) |

### 新增 API 端点（16 个）
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/ragent/admin/dashboard` | 仪表盘统计 |
| GET | `/api/ragent/admin/traces` | 链路追踪列表 |
| GET | `/api/ragent/admin/traces/:traceId` | 链路详情（含节点） |
| GET | `/api/ragent/admin/sample-questions` | 示例问题列表 |
| POST | `/api/ragent/admin/sample-questions` | 创建示例问题 |
| PUT | `/api/ragent/admin/sample-questions/:id` | 更新示例问题 |
| DELETE | `/api/ragent/admin/sample-questions/:id` | 删除示例问题 |
| GET | `/api/ragent/admin/users` | 用户列表 |
| POST | `/api/ragent/admin/users` | 创建用户（bcrypt 密码） |
| PUT | `/api/ragent/admin/users/:id` | 更新用户 |
| DELETE | `/api/ragent/admin/users/:id` | 删除用户 |

## 阶段 5.4 — 前端联调

### 产出
| 项目 | 说明 |
|------|------|
| 路由审计 | 54 个端点全部验证，分页格式 100% 统一使用 `convention.PageResp` |
| API 契约 | `docs/API_CONTRACT.md` — 完整端点列表、请求/响应格式、鉴权流程 |
| JWT 流程 | 登录 → token 签发 → Authorization header → LoginUser 注入 → handlers 获取 |
| 会话端点 | 6 个 REST 端点 + DBMemoryStore 持久化（对齐 Java 版接口） |

### 端点统计
| 模块 | 端点数 | 鉴权 |
|------|:---:|:---:|
| 基础 | 1 | ❌ |
| 鉴权 | 3 | 混合 |
| 知识库 | 6 | ✅ |
| 文档管理 | 14 | ✅ |
| 会话 | 6 | ✅ |
| 意图树 | 11 | 混合 |
| Admin | 11 | 混合 |
| RAG Chat (SSE) | 2 | ❌ |
| MCP Server | 4（JSON-RPC） | ❌ |
| **合计** | **54** | **35 需鉴权** |

## 阶段 7 — 收尾 交付物清单

### 新增文件（3 个）
| 文件 | 包 | 说明 |
|------|-----|------|
| `internal/framework/cache/cache.go` | cache | RedisCache（Set/Get/JSON/TTL/Incr/Expire） |
| `internal/framework/lock/lock.go` | lock | RedisLock（SET NX 分布式锁 + RunWithLock） |
| `internal/framework/idempotent/idempotent.go` | idempotent | Guard（幂等检查/标记 + Key 生成） |

### 修改文件
| 文件 | 变更 |
|------|------|
| `README.md` | 阶段进度表更新为 0-7 全部完成 |

### 项目总览
| 指标 | 值 |
|------|-----|
| Go 文件总数 | 142 |
| 代码总行数 | 13,845 |
| biz 层文件 | 75 |
| framework 层文件 | 31 |
| infra 层文件 | 33 |
| API 端点 | 54 |
| 测试包通过 | 12 |
| 剩余空目录 | crawler/、ingestion/、provider/、token/（Agentic 阶段预留） |

## 2026-07-10 — RAG SSE 与向量维度修复

- 修复 Chat 首包探测桥接器重复写入探测 channel 导致多段 SSE content 阻塞的问题。
- 修复文档分块向量写入失败后仍返回成功的问题；向量写入失败会返回错误，由分块任务标记失败。
- 检索侧按知识库绑定的 `embedding_model` 调用 `EmbedWithModel`，禁止用默认 embedding 模型检索不同模型入库的向量。
- Ollama OpenAI 兼容 embedding 请求会携带配置的 `dimensions`，并校验返回向量长度与配置一致。
- 检索多个知识库时，单个知识库 query embedding 失败只跳过该知识库并记录告警，避免一个失效 token 中断其它可用知识库召回。
- RAG 回答末尾的依据补充命中的知识库名称，并将文档、页行号、URL 链接合并到同一条依据中，便于用户追溯来源。
- 检索返回依据时通过向量 metadata 中的 `doc_id` 关联文档表补齐原始文档名和 URL，禁止将内部文档 ID 当作文档名展示。
- 文档分块持久化写入向量前统一补齐 `doc_id`、`doc_name`、`source_type`、`source_url`、`chunk_index` metadata，确保新入库文档的引用来源直接包含原始文档名。
- 认证中间件支持从配置的 token-name 查询参数、`token`、`access_token`、`satoken` 读取 JWT，兼容 EventSource 无法设置自定义 header 的聊天场景，避免未登录上下文导致问答不写入会话历史。
- 问答页会话列表 `/api/ragent/conversations` 默认返回数组，避免前端 `data.map` 处理分页对象时报错；需要分页元数据时可显式传 `paged=true`。

## 2026-07-12 — Java Controller 接口补齐

- 按 Java `@RestController` 对外接口重新审查，Java Controller 对齐端点共 74 个。
- 补齐 ingestion 模块 10 个端点：pipeline CRUD、task 创建/上传/详情/节点/分页，不再返回 `not yet implemented` 占位。
- 补齐 `/api/ragent/rag/eval`，返回召回 chunk/doc/context、`hasKb`、`hasMcp` 等评测字段。
- `/api/ragent/rag/traces/runs/:id/nodes` 改为查询 `t_rag_trace_node` 真实节点记录。
- 补齐 `/api/ragent/mappings/:id` 和 `/api/ragent/sample-questions/:id` 详情接口。
- `/api/ragent/knowledge-base/docs/:docId/chunks` 从占位改为真实创建手工分块，并同步更新文档分块数。
- RAG 主链路新增本地流式任务管理，`/rag/v3/stop` 会按 `taskId` 取消正在进行的 LLM 流、发送 `cancel`/`done` SSE 事件并关闭连接，不再只是记录日志。
- RAG Prompt 支持可选 MCP 上下文注入，Pipeline 可通过 `SetMcpContextProvider` 把工具执行结果携带到模型请求中；工具选择与真实执行规划后续单独补齐。
- RAG 查询改写前先加载会话历史，并将历史传入 `QueryRewriter`，确保追问类问题能基于上下文改写后再检索。
- RAG 检索链路支持 `RewriteResult.SubQuestions`，会按改写问题和子问题顺序检索，并按 chunk ID 去重后合并到 Prompt。
- 新增默认 MCP 上下文提供器，可基于 `McpToolRegistry` 和 `McpParameterExtractor` 执行已注册工具，并将工具名、结果或错误格式化为可注入 Prompt 的上下文。
- MQ 基础设施新增 RocketMQ producer/consumer 适配器，服务启动时按 `rocketmq.name-server` 自动接入 RocketMQ，未配置或不可用时回退 noop。
- 文档 pipeline 分块模式接入 ingestion task service，不再直接 fallback 到普通 chunk 分块。
- Chat provider 支持 Anthropic Messages API，`protocol: anthropic` 会创建 Anthropic chat client，不再跳过。
- RAG 检索链路新增 rerank wrapper，PgVector 召回结果会经过 rerank service 后再进入 Prompt。
- Rerank provider 新增真实 HTTP 客户端，支持百炼/DashScope 风格 `input.documents` 请求与 `output.results` 响应解析；`cmd/ragent` 与 `cmd/mcp-server` 均会为 `openai-compatible` provider 注册 HTTP rerank client，并保留 noop 作为降级候选。
- Ingestion task service 支持注入实际执行器；主服务将 `DocumentService` 注册为执行器后，pipeline 模式会复用现有读取、分块、向量化、持久化链路并回写真实 chunk 数，不再只创建成功日志。
- MCP Server 的 Chat provider 组装补齐 `protocol: anthropic`，与主服务入口保持一致。
- PgVectorStore 补齐 pgvector 字面量解析，`Search` 返回的 `VectorChunk.Embedding` 不再为空；`UpdateChunk` 对齐 Java 版 upsert 行为，chunk 不存在时会插入，存在时更新内容、元数据和向量。
- crawler 目录补齐 `HTTPSource` 和 `FeishuSource` 基础实现，支持单个 HTTP/HTTPS 文档源的 HEAD 元信息探测、GET 拉取、token/header 注入和大小限制校验，为后续 Confluence/Notion/Git/S3 Source 扩展打底。

## 2026-07-14 — 多租户上下文打底

- 新增 `internal/framework/context/tenant.go`，补齐 `TenantContext` 及上下文读写/清理/判定函数。
- 新增 `middleware.Tenant()`，支持从 `X-Tenant-Id` / `X-Tenant-Domain` 以及 query 参数注入租户上下文。
- `cmd/ragent/main.go` 已接入 tenant middleware，RAG SSE 脱离请求上下文时会同时透传 tenant、user、trace_id。

## 2026-07-14 — 业务变更审计日志查询

- 对齐 Java `BizChangeLogController`，新增 `/api/ragent/biz-change-logs` 与 `/api/ragent/biz-change-logs/:id`。
- 新增 `t_biz_change_log` 的 Go 模型、仓储、服务和 handler，支持分页过滤、详情查询以及 `beginTime/endTime` 条件。
- 审计日志查询测试已覆盖 service 与 handler，`go test ./...` 通过。

## 2026-07-14 — 用户管理审计写入

- 新增 `BizChangeLogService.Record`，补齐业务变更日志写入能力，支持从请求上下文读取操作者信息并序列化快照。
- `AdminService` 的用户创建、更新、删除已接入审计写入，创建的用户变更会落到 `t_biz_change_log`。
- 该步先覆盖用户管理这一条 Java 已有审计路径，后续再继续把知识库、文档、意图树、摄取等写操作逐步接上。

## 2026-07-14 — 示例问题审计写入

- 对齐 Java `SampleQuestionServiceImpl` 的 `@LogRecord` 行为，示例问题创建、更新、删除已写入 `t_biz_change_log`。
- 新增 service 测试覆盖示例问题 CREATE / UPDATE / DELETE 三类审计记录及快照内容。

## 2026-07-16 — 知识库审计写入

- 对齐 Java `KnowledgeBaseServiceImpl` 的变更审计行为，知识库创建、更新、删除已接入 `t_biz_change_log` 写入。
- `KnowledgeBaseService` 新增可选审计记录器，`cmd/ragent/main.go` 复用同一个审计服务实例同时服务管理后台与知识库变更记录。
- 新增知识库 service 测试覆盖 CREATE / UPDATE / DELETE 三类审计日志及快照内容。

## 2026-07-16 — 知识库文档审计写入

- 文档创建、更新、删除已接入 `t_biz_change_log` 写入，审计对象类型为 `KNOWLEDGE_DOCUMENT`。
- `DocumentService` 新增可选审计记录器，主服务启动时复用统一审计服务实例。
- 新增文档 service 测试覆盖 CREATE / UPDATE / DELETE 三类审计日志及快照内容。

## 2026-07-16 — 知识库分块审计写入

- 手工分块创建、更新、删除已接入 `t_biz_change_log` 写入，审计对象类型为 `KNOWLEDGE_CHUNK`。
- `CreateChunk`、`UpdateChunk`、`DeleteChunk` 已补齐变更前后快照记录，审计失败只记录告警不阻断主流程。
- 新增分块 service 测试覆盖 CREATE / UPDATE / DELETE 三类审计日志及快照内容。

## 2026-07-16 — 文档与分块启停审计写入

- 文档启用/禁用已接入 `t_biz_change_log` 写入，审计动作分别为 `ENABLE` / `DISABLE`。
- 分块单条启用/禁用与批量启用/禁用已接入 `t_biz_change_log` 写入，支持记录启停前后快照。
- `ToggleDoc` 已修正为调用专用启停接口，避免复用更新接口误改文档名称。

## 2026-07-16 — 摄取任务执行审计写入

- 摄取任务执行成功/失败已接入 `t_biz_change_log` 写入，审计对象类型为 `INGESTION_TASK`，动作类型为 `RUN`。
- `TaskService` 新增可选审计记录器，主服务启动时复用统一审计服务实例。
- 新增摄取任务 service 测试覆盖执行成功与执行失败两类审计日志，失败日志会记录错误信息。

## 2026-07-16 — 摄取流水线审计写入

- 摄取流水线创建、更新、删除已接入 `t_biz_change_log` 写入，审计对象类型为 `INGESTION_PIPELINE`。
- `PipelineService` 新增可选审计记录器，主服务启动时复用统一审计服务实例。
- 新增摄取流水线 service 测试覆盖 CREATE / UPDATE / DELETE 三类审计日志及节点快照内容。

## 2026-07-16 — 意图树节点审计写入

- 意图树节点创建、更新、删除、启用/禁用已接入 `t_biz_change_log` 写入，审计对象类型为 `INTENT_TREE`。
- `IntentService` 新增可选审计记录器，主服务启动时复用统一审计服务实例。
- 新增意图树 service 测试覆盖 CREATE / UPDATE / DELETE / ENABLE / DISABLE 五类审计日志及快照内容。

## 2026-07-16 — 意图树批量操作审计写入

- 意图树批量启用、批量禁用、批量删除已改为调用 service 批量接口，不再在 handler 中循环吞错。
- 批量操作会逐条复用单节点审计写入，`INTENT_TREE` 的审计记录与单节点操作保持一致。
- 新增批量操作测试覆盖批量启用、批量删除以及错误节点 ID 的失败场景。

## 2026-07-16 — 查询词映射审计写入

- 查询词映射创建、更新、删除已接入 `t_biz_change_log` 写入，审计对象类型为 `QUERY_TERM_MAPPING`。
- `IntentService` 继续复用统一审计服务实例，关键词映射的变更也会记录前后快照。
- 新增关键词映射 service 测试覆盖 CREATE / UPDATE / DELETE 三类审计日志及快照内容。

## 2026-07-16 — 会话摘要与反馈链路补齐

- 会话摘要表已接入读写链路，`DBMemoryStore` 在达到摘要阈值后会生成并保存摘要，`LoadHistory` 会把最新摘要作为系统消息注入上下文。
- 会话删除时会同时清理摘要记录；消息列表响应新增 `vote` 回显，前端可以直接看到点赞/点踩状态。
- 新增消息反馈取消接口，支持按消息 ID 删除当前用户的反馈记录；相关 service/handler 测试已补齐并通过 `go test ./...` 验证。

## 2026-07-16 — 文档上传并发限流接入

- 知识库文档上传接口接入 `rag.semaphore.document-upload` 配置，服务启动时会创建独立的 Redis 公平队列限流器。
- `DocumentHandler.Upload` 会先获取上传 permit，再解析 multipart 表单，避免限流时仍消耗上传解析资源。
- 新增 handler 测试覆盖限流先于 multipart 解析的场景。

## 2026-07-16 — 文档定时同步接入

- 新增 `t_knowledge_document_schedule` / `t_knowledge_document_schedule_exec` 的 Go 模型与仓储，补齐定时刷新任务表的读写链路。
- `DocumentService` 已可在文档创建/更新/删除时同步维护 schedule 记录，`scheduleEnabled/scheduleCron` 会落到文档表和定时任务表。
- `URL` 与 `internal_url` 来源文档会保留定时刷新，文件上传文档会自动清理 schedule，避免本地文件进入远程回源链路。
- 新增 `DocumentScheduleService`：支持按 cron 扫描到期任务、拉取远程 HTTP 文档、写回文件存储并触发重新分块。
- `cmd/ragent/main.go` 已接入后台扫描协程，并注册 HTTP 与 Feishu 文档源。

## 2026-07-16 — 飞书 Wiki 文档拉取支持

- `FeishuSource` 支持 `https://*.feishu.cn/wiki/<node_token>` 链接，会先调用 wiki `get_node` 解析 `obj_token/obj_type`，再拉取 docx raw content。
- 文档调度 source 选择会优先识别飞书 wiki/docx/docs URL，避免把飞书链接当普通 HTTP 页面抓取。
- 新增 `rag.knowledge.feishu` 配置与 `.env.example` 变量模板，用于注入飞书 App 凭证或访问 token。

## 2026-07-16 — RAG 设置、幂等与测试路由补齐

- `/rag/settings` 已补齐 `maxRequestSize`、`rag.default`、`rag.memory`、`rag.rateLimit.global`、脱敏后的 provider 信息以及 chat/embedding/rerank/vlm 候选项。
- `/rag/v3/chat` 和 `/rag/v3/stop` 已接入 Redis 幂等保护，重复提交会直接返回提示，不再重复进入主流程。
- 新增 `/test/langchain4j/*` 烟囱路由，提供 hello、简单流式 chat、图片生成和图片分析的后端测试入口。

## 2026-07-16 — Java 高优先级能力补齐

- 知识库文件存储新增 RustFS/S3 兼容后端，`rustfs.url/access-key-id/secret-access-key/kb-bucket` 配置完整时自动使用对象存储；配置不完整时回退内存存储，方便本地测试。
- `/rag/v3/chat` 主链路接入 `t_rag_trace_run` / `t_rag_trace_node` 持久化，覆盖 history、rewrite、retrieve、llm-stream 与 first-packet 节点，支持成功、失败、取消收尾。
- 文档 pipeline 上传会正确写入 `process_mode=pipeline` 与 `pipeline_id`；摄取任务支持 pipeline-aware executor 返回节点级执行结果，真实节点状态会写入 `t_ingestion_task_node`。
- RAG 检索主链路由单一 pgvector 切换为 multi-channel：意图定向、本地关键词、全局向量、可选 You.com web search 召回后统一去重，再进入 rerank。
- 新增相关单元测试并通过 `go test ./... -count=1` 全量验证。

> 2026-07-17 补充：向量存储已从固定 pgvector 接口切换为 `rag.vector.type` 可配置工厂，Milvus 读写与检索已接入，默认仍回落到 pgvector。
> 2026-07-17 补充：`/api/ragent/admin/dashboard/trends` 已按 Java 对齐为参数化接口，支持 `metric/window/granularity`，并修正小时粒度趋势桶归档。
> 2026-07-17 补充：Admin overview/performance 已补齐 `window` 参数和 Java 风格 KPI/性能指标结构；知识库文档/分块启停接口已兼容 `?value=`，批量分块支持 Java 风格 `chunkIds`。
> 2026-07-17 补充：知识库文档搜索已对齐 Java，`/knowledge-base/docs/search` 返回数组并补齐 `kbName`，空关键词返回空数组。

## 2026-07-20 — Java 高优先级 RAG 能力补齐

- RAG 查询改写链新增术语归一化层：从 `t_query_term_mapping` 加载启用且 `match_type=1` 的规则，按优先级和源词长度排序，并复刻 Java 的安全替换逻辑，避免把已经是目标词的片段重复替换。
- 主服务启动时会把术语归一化包装到 `LLMRewriter` 前，用户问题会先归一化再进入上下文追问改写与检索。
- 主服务新增远程 MCP 客户端接入：读取 `rag.mcp.servers`，自动补齐 `/mcp` 端点，依次执行 `initialize`、`tools/list`，并把远端工具注册到 `McpToolRegistry`。
- MCP 上下文链路新增 LLM 参数提取器，按工具参数定义从用户问题中提取 JSON 参数，调用远端 `tools/call` 后把工具文本结果注入 Prompt。
- MCP 上下文提供器新增工具选择阶段，主服务默认使用 LLM 从已注册工具中选择相关工具，避免每轮问答无差别调用所有 MCP 工具。
- MCP 参数提取器对齐 Java 默认值行为，会从工具 schema 透传 `default/enum`，LLM 未返回可选参数时自动回填默认值。
- 查询改写链对齐 Java 的多问句改写能力：LLM 输出支持 `rewrite/sub_questions` JSON，关闭 `rag.query-rewrite.enabled` 时退回规则拆分。
- `search_documents` 已补齐 `kb_id` 过滤，不再忽略 MCP 工具入参中的知识库范围。
- 低优先级联调能力补齐：`/test/langchain4j/image-analysis` 在 VLM 已配置时会读取 data URL 或 http/https 图片并调用真实 VLM；未配置时保留 demo 降级响应。
- 新增 Go 版 `ConversationGroupService`，对齐 Java 内部会话窗口查询能力，支持最新用户消息、ID 区间消息、时间点前最大消息 ID、用户消息计数、最新摘要与会话查询。
- 会话记忆加载在历史过长时会保留首条系统摘要，再截取最近消息，避免摘要被窗口裁掉；最新摘要查询也已改为按 ID 倒序，和 Java 的最新摘要语义对齐。

## 2026-07-20 — 摄取引擎与会话标题补齐

- `internal/biz/rag/ingestion.go` 新增起始节点自动探测、条件规则跳过、缺失 nextNode 报错与环检测，和 Java `IngestionEngine` 的核心链式执行语义对齐。
- `internal/biz/conversation/service/title_generator.go` 新增 LLM 会话标题生成器，首条消息创建会优先走 prompt + LLM 生成，失败时回退到本地标题截断。
- `cmd/ragent/main.go` 已把会话标题生成器接入 DBMemoryStore，并使用 `rag.memory.title-max-length` 作为标题长度上限。

## 2026-07-20 — 需求 3/4/5/6 继续补齐

- 会话删除已补齐消息与反馈级联软删，和会话、摘要一起收口。
- `/rag/eval` 改为 `app.eval.enabled` 按配置挂载，并补齐 `latencyMs`、`mcpContext`、`subIntents`、`intentLeafIds` 等返回字段。
- 图片解析链路新增 `rag.image-parse.max-output-tokens`，并将其透传到 VLM 请求体的 `max_tokens`。
- `rag.parser.tika-url` 配置启用后，Tika 解析器会进入默认注册链，兼容 `application/rtf` 等兜底格式。

## 2026-07-21 — 意图与解析高优先级补齐

- RAG 多通道检索改为并发执行，缩短多源召回等待时间。
- 新增意图树启发式解析与歧义澄清服务，`rag.guidance.enabled=true` 时会在检索前先提示用户收敛到更明确的方向。
- `/api/ragent/rag/eval` 现在会按子问题返回 `intentLeafIds`，无 resolver 或未命中时保留 `null` 空位，和 Java 评测输出语义对齐。
- 图片解析链路对 `image/svg+xml` 先栅格化为 PNG，再交给 VLM 描述和对象存储上传。
- XLSX 解析补齐公式缓存值与超链接 markdown 内联输出，避免丢失 Excel 里的可点击语义。
- 新增 HTML / XML / PPTX 原生解析器，默认注册链优先使用 Go 侧实现，Tika 仅作为低优先级兜底。
- 解析器 `Supports` 现在会先归一化 MIME 参数，`text/csv; charset=gbk`、`text/html; charset=utf-8` 这类真实 Content-Type 也能正确命中。
- 文档来源类型补齐 `localfile` / `local_file` 别名兼容，创建文档与摄取任务时都会归一化为 `file`。

## 2026-08-04 — 文档更新契约对齐

- 文档更新接口 `PUT /api/ragent/knowledge-base/docs/:docId` 现在允许省略 `docName`，未传时保留原文档名，显式传空字符串仍会拒绝，继续对齐 Java 局部更新语义。
- 文档更新持久化白名单补齐 `process_mode/pipeline_id`，`processMode=pipeline` 的修改现在会真正落库，避免只改内存对象不改数据库。
- 文档上传创建现在支持可选 `enabled` 表单字段，显式传 `0/false` 时初始创建为禁用文档；未传时仍沿用默认启用，补齐 Java 创建请求中的初始启停能力。
- RAG 停止接口 `POST /rag/v3/stop` 成功响应改为统一 `Result<Void>` 空成功，不再手写 `message: "success"`，继续对齐 Java `RAGChatController.stop`。
- 文档源文件下载未知扩展默认 MIME 改为 `application/octet-stream`，对齐 Java `KnowledgeDocumentController.file` 的兜底 `Content-Type`，避免未知或二进制文件被浏览器当文本处理。
- 单个 Chunk 启停现在会校验路径 `docId` 与 `chunkId` 归属关系，避免通过 `/knowledge-base/docs/{docId}/chunks/{chunkId}/enable` 误操作其他文档的分块，对齐 Java `KnowledgeChunkServiceImpl.enableChunk`。
- 手工创建/更新 Chunk 现在只用 `TrimSpace` 做空白校验，落库内容、哈希、字符数和向量同步保留用户原始 `content`，并统一空内容提示为 Java 的 `Chunk 内容不能为空`。
- MCP 内置工具 `tools/list` schema 补齐 `enum/default` 输出，覆盖 weather/sales/ticket/youcom 的查询类型、时间段、条数和 freshness 等参数，便于工具选择与参数抽取复用 Java MCP 工具契约。
- 关键词映射创建/更新的 `enabled` 请求字段现在兼容 Java Boolean 形态，同时保留 Go 侧历史 `0/1` 数值形态，详情/列表响应也改为 Java 的 Boolean 形态，数据库仍落原有 `smallint`。
- 摄取任务上传接口 `/ingestion/tasks/upload` 现在会优先读取 multipart 表单里的 `pipelineId`，再回退 query 参数，和 Java `@RequestParam` 的提交习惯对齐。
- 知识库文档详情/列表/上传响应中的 `enabled` 已改为 Java `KnowledgeDocumentVO` 的 Boolean 形态，底层表字段和启停操作仍保持原有 `smallint`。
- 知识库文档启停审计快照里的 `enabled` 也同步改为 Boolean 形态，分块 `enabled` 仍保持数值语义，和 Java 文档/分块 VO 的字段类型分别对齐。
- 示例问题分页列表未显式传 `size` 时默认返回 10 条，和 Java `Page` 默认分页大小保持一致。
- RAG 末端元数据富化新增 `rag.context.enrich.enabled` 配置开关，默认开启；关闭时会跳过 `MetadataEnrichingRetriever` 包装，和 Java 的上下文富化功能开关契约对齐。
- 多通道去重处理器改为按通道优先级合并重复 chunk，并在重复命中时保留更高分那一份，避免低分重复结果覆盖高分结果。
- RAG 精排新增 `rag.rerank.enabled` 配置开关，默认开启；显式关闭时跳过 `RerankRetriever` 包装，和 Java `RerankPostProcessor.isEnabled` 契约对齐。
- `rag.query-rewrite.enabled` 与 `rag.rate-limit.global.enabled` 现在也按 Java 默认值语义处理：未配置时默认开启，显式 `false` 才关闭，避免 Go 侧因 YAML 缺省把主链路能力关掉。
- `rag.search.channels.keyword` 现在作为主问答召回安全网默认开启，显式配置 `enabled: false` 时才关闭，避免向量召回走偏时漏掉具有明确词面命中的知识库文档。
- 关键词召回修复参数绑定顺序，按 `SELECT` 里的 score pattern、`WHERE collection_name`、where pattern、limit 依次传参，避免 collectionName 被错绑成 `%关键词%` 导致 keyword 恒为空。
- `KeywordSearch mode=both` 现在会先查意图 collection，再补查其余知识库，并按跨知识库关键词分稳定排序；Rerank 后会保留少量高分 keyword anchor，避免强词面命中的文档被精排完全剔除。
- `rag.guidance.enabled` 与 `rag.trace.enabled` 也按 Java 默认值语义处理：未配置时默认开启，显式 `false` 才关闭；歧义澄清默认展示选项数同步为 Java 的 6。
- `rag.default.sse-timeout-ms` 已接入 RAG 聊天超时控制，未配置时默认 5 分钟，和 Java `SseEmitter` 的默认超时一致。
- AI 配置默认值继续对齐 Java：`ai.selection.failure-threshold=2`、`open-duration-ms=30000`、`ai.stream.message-chunk-size=5`，模型候选 `priority` 未配置时默认 100；Go 扩展的首包探测超时未配置时默认 60 秒。

## 2026-07-23 — 意图定向向量召回补齐

- `IntentDirectedSearch` 已从按 collection 最近更新时间取 chunk，补齐为优先按 KB 意图节点的 `collectionName/topK` 做向量召回。
- 意图定向召回会按 collection 匹配知识库，使用该知识库的 `embeddingModel` 向量化查询，再调用配置的向量检索服务；依赖缺失、未匹配到知识库或向量检索无结果时返回空候选，不再用最新分块作为相关性兜底，避免无关证据进入主链路。
- `rag.search.channels.intent-directed.min-intent-score` 与 `top-k-multiplier` 已接入通道执行：低于阈值的 KB 意图不再触发意图定向召回，命中节点的 TopK 会按配置倍率放大。
- 主服务启动时已向意图定向通道注入 `vecStore`、`embService`、`kbRepo`，对齐 Java `IntentParallelRetriever` 的核心检索语义。
- `VectorGlobalSearch` 已接入 `confidence-threshold`、`single-intent-supplement-threshold` 与 `top-k-multiplier`：意图定向启用且已有高置信意图时不再额外跑全局向量兜底，无意图、低置信意图或单一中等置信意图时继续启用，并按倍率扩大召回候选。
- `KeywordSearch` 已接入 `mode` 与 `top-k-multiplier`：`both` 模式有 KB 意图时优先查意图 collection，无意图时回退全库；`global` 强制全库，`intent` 仅查意图域，SQL LIMIT 按倍率扩大候选。
- 多通道 RRF 融合已接入 `rag.search.fusion.strategy/rrf-k/rerank-candidate-limit`，融合排序后会先截断候选池再进入 Rerank；单通道时保留原始召回分数与顺序、仅做候选截断，`strategy=off` 时仅保留去重处理器，继续对齐 Java `FusionPostProcessor` 的成本控制语义。
- `VectorGlobalSearch` 已补 `candidate-budget` 单次全局检索预算：pgvector 后端会在多个 collection 范围内执行一次总预算 TopN 召回，并按 `collection_name` 补回知识库 metadata；不支持全局检索的后端继续保持逐库 fan-out。
- PgVector 检索前会在同一连接事务中尽力设置 `hnsw.ef_search=200` 和 `hnsw.iterative_scan=relaxed_order`，提升 collection 过滤后的召回稳定性，对齐 Java `PgRetrieverService` 的查询调优语义。
- Rerank 后新增最终候选 metadata 富化 wrapper，会按 chunk ID 批量回查 `t_knowledge_chunk` / `t_knowledge_document`，补齐 `doc_id/chunk_index/doc_name` 且不改变相关性顺序，对齐 Java `MetadataEnrichmentPostProcessor`。
