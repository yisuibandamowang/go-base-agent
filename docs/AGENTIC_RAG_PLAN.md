# Agentic RAG 升级规划 — 会员-支付中台

> 本文档描述 go-base-agent 从基础 RAG 升级为 Agentic RAG 的路径，目标应用于公司会员-支付中台。

## 背景

当前 Ragent 是"用户提问 → 检索 → 生成回答"的被动式 RAG。公司会员-支付中台场景需要更主动的能力：

- 定时抓取公司内部文档（Confluence、Notion、内部 Wiki、Git 仓库 README）
- 自动索引更新的文档，保持知识库时效
- 通过 MCP 工具连接支付系统、会员系统实现"Ask → Act"（查询 → 执行）
- 多租户隔离（支付、会员、财务、运营各自知识域）

## 扩展点设计

### 1. 文档爬取预留 (`internal/biz/crawler/`)

> 2026-07-14 补充：Go 侧已落地 `HTTPSource` 和 `FeishuSource` 基础实现，可对单个 HTTP/HTTPS 文档源进行 HEAD 元信息探测、GET 拉取、token/header 注入与大小限制校验。
>
> 2026-07-16 补充：Go 侧已接入知识库文档定时刷新任务表与扫描服务，支持按 cron 扫描到期任务、拉取远程 HTTP / Feishu Wiki 文档并触发重新分块。
> 2026-07-16 补充：定时刷新仅对 URL 来源文档生效，文件上传文档会自动清理 schedule，避免误入远程回源链路。
> 2026-07-16 补充：`/rag/settings` 已对齐 Java 的更完整回显结构，`/rag/v3/chat` 与 `/rag/v3/stop` 已补幂等保护，并新增 `/test/langchain4j/*` 烟囱路由用于调试和联调。
> 2026-07-20 补充：Go 侧已落地 `ConfluenceSource`，可通过 Confluence REST API 拉取页面 storage 内容；后续再扩展 Notion/Git/S3 适配器和更完整的变更监听。

```go
// internal/biz/crawler/source.go
type DocumentSource interface {
    Name() string
    ListDocuments(ctx context.Context) ([]DocumentMeta, error)
    FetchDocument(ctx context.Context, id string) (*Document, error)
    WatchChanges(ctx context.Context, since time.Time) (<-chan ChangeEvent, error)
}

// 计划支持的 source:
// - ConfluenceSource       → REST API（已落地）
// - NotionSource           → Notion API
// - GitRepoSource          → git clone + walk
// - WebCrawlerSource       → HTTP 递归爬取
// - FeishuSource           → 飞书文档 API
// - S3BucketSource         → 对象存储遍历
```

### 2. 多租户隔离

```go
// internal/framework/context/tenant.go
type TenantContext struct {
    TenantID string
    Domain   string // payment / membership / finance / ops
}
```

- 知识库按 tenant_id 隔离
- 意图树按 tenant 绑定
- MCP 工具按 tenant 授权
- Go 侧已先补 tenant 上下文与 middleware 打底，当前仍未引入表级 tenant_id 字段，后续数据隔离需结合表结构再推进。

### 3. MCP 工具扩展 — 支付/会员系统

```go
// internal/biz/mcp_tool/tools/
//   payment_query.go      → 查询支付订单状态
//   payment_refund.go     → 执行退款
//   member_query.go       → 查询会员等级/积分
//   member_grant.go       → 发放权益
```

### 4. 主动式 RAG Agent 循环

```
定时触发
  → DocumentSource.WatchChanges() 发现新文档
    → IngestionPipeline 入库
      → 增量索引到 pgvector
        → 通知订阅方（Webhook / MQ）

用户 Ask 触发
  → Intent 分类（RAG 知识检索 vs MCP 工具调用）
    → RAG 路径：检索 + 生成
    → MCP 路径：LLM 提取参数 → 调用工具 → 格式化结果
```

### 5. 配置预留

在 `configs/config.yaml` 中预留段：

```yaml
agentic:
  crawler:
    enabled: false
    sources: []
    interval: 3600  # 秒
  multi-tenant:
    enabled: false
    domains:
      - payment
      - membership
```

### 6. 当前仍待补齐的能力

> 以下为 2026-07-21 对照 Java 端后确认的 Go 侧缺口，按影响面从高到低排列。

- 余下缺口主要集中在 agent loop、以及更细粒度的 ingestion 节点参数化配置和运维收口能力；Go 侧已经补出基础版 Fetcher / Parser / Enhancer / Enricher / Indexer 节点，但表级 tenant 隔离和更复杂的源监听编排仍可继续补强。
- 2026-07-29 再补一层：知识库文档查询已补 Java `chunksEdited` 回显，按未删除分块 `update_time > create_time + 1s` 判断是否被手工编辑；会话列表默认不再分页截断，`GET /conversations` 返回当前用户全量会话数组，仅 `paged=true` 时走分页对象。
- 2026-07-30 再补一层：知识库文档更新接口已改为空成功返回，对齐 Java `KnowledgeDocumentController.update` 的响应形状，避免前端把更新后的文档对象误当作新增回显。
- 2026-07-30 再补一层：知识库文档分块触发接口已改为空成功返回，对齐 Java `KnowledgeDocumentController.chunk` 的响应形状，避免前端把任务提交提示误当作业务数据。
- 2026-07-30 再补一层：知识库 Chunk 列表已支持 `enabled` 过滤，批量启停在目标状态已全部满足时会返回“无需重复操作”，并且只更新实际需要变化的分块，对齐 Java `KnowledgeChunkServiceImpl` 的重复提交语义。
- 2026-07-30 再补一层：Ingestion Indexer 默认集合回退已补齐，未显式传 `settings.collectionName` 或 `VectorSpaceID` 时会回落到 `rag.default.collection-name`，避免通用流水线在缺少集合参数时直接失败，对齐 Java `IndexerNode` 的默认集合语义。
- 2026-07-29 再补一层：RAG Trace 查询接口已补齐 Java 语义，列表支持 `traceId/conversationId/taskId/status` 过滤，VO 回显补齐 `entryMethod/username/question/ttftMs`，节点回显补齐 `className/methodName`，前台排障页可直接复用。
- 2026-07-28 再补一层：默认意图树初始化已接入启动流程，新增 `app.intent-tree.init-from-factory` 显式开关，默认关闭；开启后启动时按 Java 默认树补齐缺失节点。
- 2026-07-28 再补一层：意图树默认初始化能力已对齐 Java `IntentTreeFactory`，`InitFromFactory(ctx)` 会按 Java 默认树展平 18 个节点并跳过已存在的 `intentCode`。
- 2026-07-28 再补一层：意图树树形查询顺序已对齐 Java `IntentTreeServiceImpl.getFullTree`，树形接口按 `sortOrder ASC, id ASC` 返回同级节点，避免同 `sortOrder` 节点受创建时间影响。
- 2026-07-28 再补一层：意图树更新不可变字段已对齐 Java `IntentNodeUpdateRequest`，更新时忽略 `intentCode/kbId/mcpToolId`，避免路由键、知识库绑定和 MCP 工具绑定被更新接口误改。
- 2026-07-28 再补一层：ingestion `FetcherNode` 已补 Java `SourceType.FEISHU` 执行能力，支持飞书 docx/docs `raw_content` 与普通 URL 鉴权拉取，避免任务创建允许 `feishu` 但运行时不支持。
- 2026-07-26 再补一层：ingestion 条件跳过日志已对齐 Java `NodeResult.skip`，条件未满足会记录 `Skipped: 条件未满足` 成功日志；pipeline condition 运行时也已支持任意 JSON 值。
- 2026-07-26 再补一层：摄取任务 metadata 回写已对齐 Java `buildTaskMetadata` 的主链路，执行器可通过 `TaskExecutionResult.Metadata` 把运行产物合并回任务详情。
- 2026-07-26 再补一层：ingestion VO 回显归一化已对齐 Java 转换层，流水线节点类型、任务来源类型、任务状态、任务节点类型和任务节点状态会规范化输出，兼容历史数据。
- 2026-07-26 再补一层：摄取任务上传文件内容透传已对齐 Java `IngestionTaskServiceImpl.upload`，上传接口会读取文件 bytes/MIME 并传入执行器，知识库 pipeline 上下文也会优先使用请求携带的原始内容。
- 2026-07-26 再补一层：摄取分块策略和任务 metadata 汇总继续对齐 Java，纯文本 fallback 分块会读取 `ChunkerSettings.strategy`，知识库 pipeline 任务会把增强节点生成的 `keywords/questions` 写回任务 metadata。
- 2026-07-26 再补一层：RAG 评测接口继续对齐 Java `EvalController`，现在会把改写、子问题和解析出的意图传入完整检索链路，并将 `retrievedDocIds/retrievedContextDocIds` 优先输出为文档名去扩展名后的业务码。
- 2026-07-26 再补一层：摄取任务节点状态已对齐 Java `resolveNodeStatus`，状态保存前统一小写下划线，`Skipped:` 消息会落为 `skipped`。
- 2026-07-26 再补一层：摄取任务节点顺序已对齐 Java `buildNodeOrderMap`，`node_order` 按 `nextNodeId` 链路计算，避免节点配置入库顺序影响任务节点展示。
- 2026-07-26 再补一层：摄取任务节点输出截断保护已对齐 Java `truncateOutputJson`，节点 `output_json` 超过 1MB 时会截断并追加提示，降低异常大输出写库风险。
- 2026-07-26 再补一层：摄取任务创建参数校验已继续对齐 Java `IngestionTaskServiceImpl.toSource/resolvePipelineId`，任务落库前会拒绝空 `pipelineId`、空文档来源、空 `source.type` 和未知来源类型。
- 2026-07-26 再补一层：摄取流水线节点替换已对齐 Java `physicalDeleteByPipelineId`，更新节点时物理删除旧节点再写入，避免真实唯一约束下同一 `nodeId` 多次更新冲突。
- 2026-07-26 再补一层：摄取流水线创建/更新事务一致性已对齐 Java `@Transactional`，主表写入与节点替换共享同一事务，节点写入失败会整体回滚。
- 2026-07-26 再补一层：摄取流水线节点类型校验已对齐 Java `IngestionNodeType`，创建/更新只接受 `fetcher/parser/enhancer/chunker/enricher/indexer`，支持大小写与横线归一化，未知类型会返回 `未知节点类型: {nodeType}` 并拒绝写入。
- 2026-07-26 再补一层：摄取流水线创建已补 Java 重名提示语义，创建前会 trim 名称并检查未删除流水线是否重名，重复时返回 `流水线名称已存在`。
- 2026-07-26 再补一层：分块日志返回已补齐 Java VO 字段，`chunk-logs` 会回填 pipeline 名称，pipeline 分支会把摄取流水线执行耗时记入 `chunkDuration` 并计算 `otherDuration`；pipeline 模式按 `total - chunk - persist`，普通 chunk 模式按 `total - extract - chunk - embed - persist`。
- 2026-07-26 再补一层：URL 文档定时启用校验已补齐，创建时 `scheduleEnabled=1` 必须提供 `scheduleCron`，更新后最终启用态也必须保留 `sourceLocation/scheduleCron`，避免 Go 侧静默关闭定时配置，继续对齐 Java 上传/更新入口的 schedule 校验。
- 2026-07-26 再补一层：文档上传解析器前置校验已补齐，创建文档前会复用分块阶段 parser registry 校验 MIME，未知扩展不再兜底为纯文本；无解析器支持时拒绝落库，继续对齐 Java 上传入口的 parserSelector 前置拦截。
- 2026-07-26 再补一层：文档 Pipeline 存在性校验已补齐，上传/更新 `processMode=pipeline` 时会查询摄取流水线服务，`pipelineId` 不存在会拒绝落库，继续对齐 Java `ingestionPipelineService.get`。
- 2026-07-26 再补一层：文档处理配置校验已继续对齐 Java，上传/更新会拒绝非法 `processMode`；`processMode=chunk` 或 Java 分块策略 `fixed_size/structure_aware` 会校验 `chunkConfig` JSON 格式与必要字段；更新文档也已补齐 `processMode/pipelineId` 请求字段。
- 2026-07-26 再补一层：文档上传已补 Java Pipeline 模式前置校验，`processMode=pipeline` 会被显式识别并要求 `pipelineId`，缺失时拒绝创建文档，避免静默按 chunk 模式落库。
- 2026-07-26 再补一层：关键词映射列表已按 Java `QueryTermMappingAdminServiceImpl.pageQuery` 语义补齐，支持 `current/page/size/domain/keyword`，关键词匹配 `sourceTerm/targetTerm`，排序改为 `priority ASC, update_time DESC`。
- 2026-07-26 再补一层：意图树 Java 兼容路径返回形状已收口，`POST /intent-tree` 返回新 ID 字符串，`PUT /intent-tree/{id}` 返回空成功；Go 扩展路径 `/intent-tree/nodes` 仍保留完整对象响应。
- 2026-07-26 再补一层：意图节点 `examples` 请求形态已对齐 Java `List<String>`，创建/更新支持字符串数组并按 JSON 数组字符串入库，同时保留历史字符串兼容。
- 2026-07-26 再补一层：意图树创建/更新校验继续对齐 Java，创建时主动拒绝重复 `intentCode`，节点级 `topK` 显式配置时必须大于 0，`enabled` 未传时默认启用且显式 0 仍保留禁用，未传 `topK` 时保留全局默认兜底。
- 2026-07-26 再补一层：意图树知识库绑定继续对齐 Java，创建 TOPIC 级 KB 节点必须指定 `kbId`，传入 `kbId` 时自动用知识库 `collectionName` 回填节点 collection。
- 2026-07-26 再补一层：意图树批量停用/删除已补 Java 层级保护，批量停用会拒绝遗漏未选中的启用后代，批量删除必须选择完整子树，避免留下孤儿子树或半删结构。
- 2026-07-26 再补一层：用户列表已按 Java `UserPageRequest` / `pageQuery` 语义补齐，支持 `current/page/size/keyword`，关键词匹配用户名或角色，排序改为 `update_time DESC`。
- 2026-07-26 再补一层：用户管理已补 Java 默认管理员保护与参数校验，禁止创建 `admin` 用户名、禁止修改/删除默认 `admin` 账号；创建/更新会主动校验用户名重名，角色仅允许 `admin/user`，并在用户名、密码、角色、avatar 上做 trim/非空/归一化，继续对齐 Java `UserServiceImpl`。
- 2026-07-26 再补一层：URL 文档上传已按 Java `KnowledgeDocumentServiceImpl.upload` 主语义补齐，`sourceType=url` 无文件上传时会抓取 `sourceLocation` 的远端文件，识别文件名/类型/大小并写入 collection 级文件存储，后续分块可直接读取原文。
- 2026-07-26 再补一层：用户账号接口已按 Java `AuthController` / `UserController` 继续收口，登录响应补齐 `userId/role/avatar`，当前用户接口补齐 `/user/me` 与 `userId/avatar`，修改密码支持 `currentPassword/newPassword` 并兼容旧 `oldPassword`。
- 2026-07-26 再补一层：手工 Chunk 创建/更新/删除已按 Java 语义收口，创建支持 `content/index/chunkId` 并在文档 `running` 或禁用时拒绝；更新/删除校验文档运行态和 Chunk 归属，更新同步刷新内容 hash 与向量，删除同步软删 Chunk、删除向量并安全递减 `chunk_count`，HTTP 返回空成功。
- 2026-07-25 再补一层：知识库列表与重命名已按 Java 语义收口，列表支持 `name` 模糊过滤、`update_time` 倒序和 `documentCount`；`PUT /knowledge-base/{id}` 可只传 `name` 重命名并返回空成功，已有分块文档时禁止修改 embedding 模型。
- 2026-07-25 再补一层：示例问题接口已按 Java 路由语义收口，`/sample-questions` 返回分页并支持 `keyword`，`/rag/sample-questions` 随机返回 3 条；创建/更新会 trim 并拒绝空白问题，创建返回 ID、更新返回空成功。
- 2026-07-25 再补一层：关键词映射创建/更新已补 Java 参数语义，字段会先 trim，空白 `sourceTerm/targetTerm` 会被拒绝；未传 `matchType/priority/enabled` 时默认 `1/0/1`，并避免 GORM 表默认值把 `priority=0` 覆盖成其它值。
- 2026-07-25 再补一层：认证登出已补 token 撤销能力，JWT 签发时写入 `jti`，`/auth/logout` 会把当前 token 写入 Redis 黑名单直到过期，后续请求不再能复用旧 token。
- 2026-07-25 再补一层：用户管理 `/users` CRUD 已补 `admin` 角色保护，普通登录用户会被拒绝，继续对齐 Java `UserController` 中的 `StpUtil.checkRole("admin")`。
- 2026-07-25 再补一层：Chunk 单条/批量启停已补 Java 运行态保护，文档 `running` 时禁止修改 Chunk 状态；批量启停接口兼容 Java 的可空请求体，空 body 会进入业务校验并返回指定 Chunk 缺失提示。
- 2026-07-25 再补一层：文档源文件下载已按知识库 `collectionName` 读取对象存储中的原始文件，并设置 inline 文件名与准确 Content-Type；CSV / XLS / XLSX 会返回 Java `CONTENT_TYPE_MAP` 对应 MIME，找不到原文件时才回退分块预览文本，对齐 Java `/knowledge-base/docs/{docId}/file` 的直出语义。
- 2026-07-22 再补一层：MCP 参数抽取已支持意图节点 `paramPromptTemplate`，Pipeline 会把解析出的 MCP 意图传入上下文构建器，LLM 抽参时优先使用节点自定义提示词，对齐 Java `executeSingleMcpTool` 的自定义抽参模板链路。
- 2026-07-24 再补一层：知识库文档分块与知识库删除已接入 RocketMQ 事务消息优先路径，Go 侧会在 MQ 可用时发送 `knowledge-document-chunk_topic` / `knowledge-base-cleanup_topic` 事件并由独立 consumer 处理；本地事务回调会负责文档状态更新与知识库软删，topic 回查逻辑也已注册，MQ 不可用时回退到原有同步执行。
- 2026-07-24 再补一层：知识库创建时已补向量空间预创建，`KnowledgeBaseService.Create` 会在落库后调用 `EnsureVectorSpace`，与 Java 的知识空间生命周期更接近。
- 2026-07-24 再补一层：知识库物理空间清理已补到文件存储前缀级删除，Go 侧文件对象会按知识库 collection 归档，`KnowledgeBaseCleanupConsumer` 会真正调用 `DeleteKnowledgeSpace` 清掉对应空间对象。
- 2026-07-24 再补一层：ingestion `IndexerNode` 已支持 Java `IndexerSettings.embeddingModel`，节点 settings 可指定向量模型并通过 `EmbedBatchWithModel` 生成嵌入，保持与知识库侧按模型路由的一致性。
- 2026-07-24 再补一层：ingestion `ParserNode` 已支持 Java `ParserSettings.rules`，会按规则 `mimeType` 校验文档类型，并将匹配规则的 `options` 透传到底层 parser；未配置 source 信息时仍自动补 `sourceFile/documentId/sourceURL/sourceType`。
- 2026-07-22 再补一层：MCP 工具已按 `domains` 做域授权过滤，主服务会结合 request tenant 的 `domain` 过滤可见工具，独立 `mcp-server` 则会按 `X-Tenant-Domain` 收口工具暴露。
- 2026-07-26 再补一层：轻量 LLM 场景已统一走本地 Ollama 优先的 `preferredLLMService`，覆盖不走 RAG 强证据回答的普通回答链路、rewrite、会话摘要、标题生成、MCP 工具选择和参数抽取；当 `ollama` provider 存在但未显式配置候选时，会自动补 `qwen3-local -> qwen3.6:latest`，本地不可用再降级原云端路由。
- 2026-07-22 再补一层：普通回答链路已明确与 RAG 主回答分流，无 KB / MCP 证据时优先走本地 Ollama `qwen3.6:latest`，带证据的 RAG 主回答继续保留云端路由，轻量路径与强能力路径分开。
- 2026-07-22 再补一层：意图歧义澄清只对 KB 候选生效，系统域会先按父链聚合再判断是否要追问；如果用户问题里已经带了明确领域名，或者边界分数经 LLM 复核后不再构成歧义，就直接放行到后续链路。
- 2026-08-05 再补一层：意图歧义澄清与 RAG Trace 默认开关已对齐 Java，`rag.guidance.enabled` / `rag.trace.enabled` 未配置时默认开启，显式 `false` 才关闭；澄清选项默认上限同步为 6。
- 2026-08-05 再补一层：RAG 聊天超时已接入 `rag.default.sse-timeout-ms`，Go 侧默认 5 分钟并可配置，和 Java `SseEmitter` 的默认超时保持一致。
- 2026-08-06 再补一层：RAG 主链路将检索、改写、LLM 流式生成使用的可取消上下文，与会话消息落库、标题读取和 Trace 收尾使用的持久化上下文拆开；SSE 客户端断开或请求取消后，仍会尽力写入用户问题和 Trace 终态，避免无召回兜底场景丢失排障记录。
- 2026-08-05 再补一层：AI 模型基础默认值已对齐 Java，`ai.selection.failure-threshold=2`、`open-duration-ms=30000`、`ai.stream.message-chunk-size=5`，模型候选 `priority` 未配置时默认 100；Go 首包探测超时未配置时默认 60 秒。
- 2026-08-05 再补一层：RAG 检索默认值已对齐 Java，`rag.search.default-top-k=10`、`vector-global.top-k-multiplier=3`、`candidate-budget=100`、`intent-directed.top-k-multiplier=2`、`keyword.top-k-multiplier=2`、`web-search.count=5`、`web-search.timeout-seconds=10`、`fusion.rrf-k=60`、`fusion.rerank-candidate-limit=50`；`rag.rate-limit.global` 也补齐默认值 `50/20/600/200`，显式关闭会绕过聊天队列限流。
- 2026-07-22 再补一层：查询词归一化已补 Redis 缓存与写入侧失效，normalizer 先查按 domain 分桶的缓存，未命中才回源 DB 并回填；关键词映射创建、更新、删除会主动清理对应 domain 缓存，对齐 Java `QueryTermMappingCacheManager`。
- 2026-07-22 再补一层：意图树已补 Redis 缓存与写入侧失效，resolver / guidance 共用缓存型 lister，未命中回源 DB 并回填 7 天；意图节点创建、更新、启停、删除会清理 `ragent:intent:tree`，对齐 Java `IntentTreeCacheManager`。
- 2026-07-22 再补一层：意图分类已补 LLM 主路径，resolver 现在优先用本地优先的 `LLMService` 按叶子节点评分，失败再回落现有启发式打分；多子问题还会做总量裁剪，继续靠近 Java `DefaultIntentClassifier` / `IntentResolver` 的路由语义。
- 2026-07-22 再补一层：检索阶段已接收意图上下文，Pipeline 会把 `SubQuestionIntent` 透传到 intent-aware retriever；多通道检索和 rerank 包装层保留该上下文，意图定向检索可按 KB 节点的 `collectionName/topK` 路由，对齐 Java `RetrievalEngine` 的核心数据流。
- 2026-07-23 再补一层：意图定向检索已优先按 intent collection 做向量召回，使用对应知识库的 `embeddingModel` 生成查询向量，并按节点 `topK * top-k-multiplier` 调用向量检索；低于 `min-intent-score` 的 KB 意图不再触发该通道；依赖缺失、collection 未匹配知识库或向量检索无结果时直接返回空候选，不再用最近分块伪装相关召回，避免错误证据污染主链路。
- 2026-07-23 再补一层：全局向量通道已按 `confidence-threshold` / `single-intent-supplement-threshold` 做兜底控制，高置信意图命中时避免无差别全库召回；无意图、低置信意图或单一中等置信意图时继续启用，并按 `top-k-multiplier` 扩大候选，对齐 Java `VectorGlobalSearchChannel` 的主要启用策略。
- 2026-07-23 再补一层：关键词通道已接入 `mode=both/global/intent` 与 `top-k-multiplier`，`both` 模式会在有 KB 意图时限定到意图 collection、无意图时回退全库，对齐 Java `KeywordSearchChannel` 的检索范围控制。
- 2026-08-10 再补一层：关键词通道改为主链路默认启用，仍支持通过 `rag.search.channels.keyword.enabled=false` 显式关闭；关键词 SQL 从整句 `LIKE` 调整为中英文信号词拆分匹配，并同时匹配文档名和 chunk 内容，查询参数按 SQL 占位符顺序绑定，避免 collectionName 错绑到 score pattern 导致 keyword 结果恒为空；`both` 模式从“只查意图 collection”改为“意图 collection 优先，全库补充”，并按跨知识库 keyword score 排序，避免向量召回走偏时漏掉“扶摇/tag/去重”这类明确词面命中的文档。
- 2026-08-10 再补一层：关键词通道已按保守方案接入 `pg_jieba + ts_rank_cd`。`t_knowledge_vector` 新增 `search_vector`，由文档名 A 权重和 chunk 正文 D 权重组成，并建立 GIN 索引；向量写入和单 chunk 更新后会尽力刷新对应 `search_vector`。检索时会把中英文信号词拼成 `OR` websearch 表达式，再使用 `websearch_to_tsquery('jiebacfg', query)` 与 `ts_rank_cd` 排序；pg_jieba 正常时仍会合并 LIKE 强锚点候选并去重，pg_jieba 或字段不可用时回退原 LIKE 信号词匹配。
- 2026-08-10 再补一层：Rerank 后会保留强关键词锚点。KeywordSearch 会把原始关键词分写入 chunk metadata，`RerankRetriever` 在外部 rerank 结果完全丢掉高分 keyword 候选时，会按阈值把少量 lexical anchor 补回最终上下文前排，防止精排模型把明确命中文档全部淘汰。
- 2026-08-10 再补一层：Pipeline 在进入 LLM Prompt 和输出“依据”前新增最终证据收口。召回候选会按 `rag.search.default-top-k` 截断，排序稳定为 `score/doc_id/chunk_index/chunk_id`；有 KB 意图 collection 时优先保留意图 collection，没有明确意图时优先保留 `kb_name/doc_name/collection_name` 命中问题核心锚点的 collection，再做问题词面相关性过滤。Prompt 也新增“只命中文档标题、目录或链接时不得推断”的拒答约束，减少同问多答和跨知识库引用污染。
- 2026-07-23 再补一层：多通道融合已接入 `rag.search.fusion` 配置，多通道时 RRF 会按 `rrf-k` 重排并按 `rerank-candidate-limit` 截断送入 Rerank 的候选池；单通道时保留原始召回分数与顺序、仅做截断，`strategy=off` 可关闭融合，对齐 Java `FusionPostProcessor` 的两阶段粗排/精排边界。
- 2026-07-23 再补一层：pgvector 全局向量检索已接入 `candidate-budget`，支持跨 collection 单次总预算召回，Go 侧会按 embedding model 分组并在结果中补齐 KB metadata；Milvus 等不支持单次全局检索的后端仍走逐库 fan-out 兜底。
- 2026-07-23 再补一层：pgvector 检索前已补 `hnsw.ef_search=200` 与 `hnsw.iterative_scan=relaxed_order` 查询调优，减少 collection 过滤后的召回不足，继续对齐 Java `PgRetrieverService`。
- 2026-07-23 再补一层：Rerank 后已新增最终候选 metadata 回表富化，按 chunk ID 补齐文档 ID、chunk index 与文档标题，保持原排序不变，对齐 Java `MetadataEnrichmentPostProcessor`。
- 2026-07-22 再补一层：知识库文档定时刷新已补齐 `skipped` 状态收口和锁持有者保护；远端未变化、文档正在分块不会触发文件覆盖或重复分块，调度主状态写回会校验 `lock_owner`，锁已转移时仅记录带锁失效提示的执行日志。
- 2026-07-22 再补一层：知识库文档定时刷新已接入同步分块入口，调度链路会等待分块完成后再写回成功态，异步分块保留给普通上传接口，继续靠近 Java 定时刷新“执行完成再收口”的语义。
- 2026-07-24 再补一层：知识库文档定时刷新后台循环已在 `ragent` 启动时真正运行，并每分钟恢复超出 `rag.knowledge.schedule.running-timeout-minutes` 的 `running` 文档为 `failed`，默认 30 分钟，对齐 Java `KnowledgeDocumentScheduleJob` 的周期扫描与卡死恢复语义。
- 2026-07-24 再补一层：消息反馈已补异步 MQ 优先路径，`POST /conversations/messages/{messageId}/feedback` 与 `DELETE /conversations/messages/{messageId}/feedback` 会优先发送 `message-feedback_topic`，消费者再异步落库到 `t_message_feedback`；取消反馈会写 tombstone，active/cancel 事件都按 `submitTime` 做最终写入保护，MQ 不可用时回退同步写入 / 删除，和 Java `MessageFeedbackController` / `MessageFeedbackServiceImpl` 的主路径继续靠近。
- 2026-07-26 再补一层：RAG 会话记忆窗口已对齐 Java `JdbcConversationMemoryStore.loadHistory`，内部上下文加载会按 `historyKeepTurns * 2` 取最新消息并恢复升序，同时过滤空内容/非 user-assistant 消息并丢弃窗口开头的 assistant，避免长会话使用陈旧或半截上下文。
- 2026-07-26 再补一层：会话消息列表默认返回条数已对齐 Java，`GET /conversations/{conversationId}/messages` 未传 `limit` 时返回全量升序历史，不再截断到 100 条；显式 `limit` 仍保留。
- 2026-07-22 再补一层：文档启停已对齐 Java 语义，`running` 状态禁止切换启用状态；启停文档会同步已有 schedule 的 `enabled / next_run_time`，禁用时保留记录而不是直接删除。
- 2026-07-22 再补一层：文档调度 cron 校验已对齐 Java 的最小周期约束，创建/更新 URL 定时文档时会拒绝过短 cron，并按 cron 计算下一次执行时间。
- 2026-07-22 再补一层：删除文档时会同时清理 schedule 与 schedule_exec 记录，对齐 Java `deleteByDocId` 的删除收口行为。
- 2026-07-22 再补一层：删除文档时会继续软删关联 chunk、硬删 chunk_log，并按知识库 collection 清理向量数据，对齐 Java 删除入口的完整收口链路。
- 2026-07-22 再补一层：删除文档时会继续尝试清理对象存储中的原始文件，失败仅记录 warn，不阻断主删除流程，对齐 Java `deleteStoredFileQuietly` 的语义。
- 2026-07-22 再补一层：知识库文档定时刷新已补齐锁心跳续约，长任务会周期性延长 `lock_until`，避免锁过期后被其它实例抢占，对齐 Java 的 lease / heartbeat 稳态语义。
- 2026-07-22 再补一层：查询词归一化已按 request tenant 的 `domain` 做领域隔离，只加载对应业务域的关键词映射，避免跨域别名规则互相污染。
- 2026-07-22 再补一层：文档更新入口已对齐 Java 的运行态和参数校验，`running` 文档禁止更新，文档名不能为空；URL 来源地址和 schedule 字段会持久化并同步调度表。
- 2026-07-22 再补一层：文档删除入口已补齐运行态保护，`running` 文档禁止删除，对齐 Java 删除入口的状态约束。
- 2026-07-21 再补一层：Go 侧 ingestion 通用引擎已对齐 Java 的节点类型归一化与节点日志回写，后续可继续把更完整的 NodeOutputExtractor / 任务上下文字段补齐。
- 2026-07-21 再补一层：Go 侧 ingestion 节点链基础版已可执行，支持文件/URL 获取、结构化解析、LLM 增强、分块增强与向量索引写入。
- 2026-07-21 再补一层：文档流水线任务已真正接入 core ingestion engine，节点执行结果不再是手工伪造；同时补齐 `chunkSize=-1` 整篇保留语义，对齐 Java 的 whole-document sentinel。
- 2026-07-21 再补一层：结构化表格分块已支持 `rowsPerChunk` 与 key-value `EmbeddingText`，Index/DefaultEngine 向量化时优先使用 `EmbeddingText`，对齐 Java `TableChunker` / `ChunkEmbeddingService` 的关键检索语义。
- 2026-07-21 再补一层：block-aware 分块已支持 heading `outline_path` 元数据传播，以及段落/列表/图片相邻小块打包，继续对齐 Java `HeadingHandler` / `ChunkPacker` 的检索上下文语义。
- 2026-07-21 再补一层：`VectorChunk` 已补齐结构化载荷字段（`BlockType / OutlinePath / SourceBlockIDs / Assets / SectionContext`），向量写入和搜索回读会同步保留这些字段到 metadata / struct，继续对齐 Java `VectorChunk`。
- 2026-07-21 再补一层：列表分块已支持 `maxListItems / listItemsPerChunk`，长列表按项分组且有序列表编号连续，对齐 Java `ListChunker`。
- 2026-07-21 再补一层：`Block` 已补 `ID` 字段，chunker 会把显式 ID 或 fallback ID 写入 `VectorChunk.SourceBlockIDs` / metadata，补齐 Java `Block.id()` 来源追踪链路。
- 2026-07-21 再补一层：`Block / VectorChunk` 已补 Java `Provenance` 对齐字段，CSV/XLSX parser 会写入 `sourceFile`，XLSX 会通过 `workbook.xml` + `workbook.xml.rels` 对齐真实 worksheet 与 sheet 名；普通分块和 `chunkSize=-1` 整篇分块都会在 metadata 中保留 `source_file / sheet_name`。
- 2026-07-21 再补一层：ingestion 条件评估已兼容 Java 常见字符串表达式（字段比较、`#ctx`、`contains` / `matches`、简单 `&&` / `||` / `!`），Fetcher 节点日志 output 也补齐 `rawBytesBase64`。
- 2026-07-21 再补一层：KB 上下文注入已按 Java `DefaultContextFormatter` 语义聚合召回片段，同文档按 `chunk_index` 复原顺序，`doc_name` 去扩展名后作为 `<content source="...">` 内部锚点。
- 2026-07-21 再补一层：MCP 上下文注入已补 Java `CallToolResult` 的 `TextContent / isError` 处理语义，成功文本进入 `<data>`，工具错误集中进入 `<errors>`。
- 2026-07-21 再补一层：Prompt 证据与问题结构已对齐 Java `RAGPromptService` 的核心模板语义，MCP/KB evidence 分别进入 `<tool-data>` / `<documents>`，单问题和多子问题分别进入 `<question>` / `<questions>`。
- 2026-07-21 再补一层：SYSTEM-only 意图已对齐 Java `handleSystemOnly` 语义，系统型节点会跳过 KB 检索并优先使用 `promptTemplate`，未配置模板时回落默认系统提示。
- 2026-07-21 再补一层：SSE `finish` 事件已对齐 Java `CompletionPayload(messageId, title)` 的标题下发语义，新会话或无标题会话返回标题，已有标题会话不重复返回。
- 2026-07-21 再补一层：RAG SSE 完成/取消事件已补齐 `messageId`，助手消息会先落库再返回 `finish` / `cancel`，并携带 thinking 内容与时长，继续对齐 Java `StreamChatEventHandler`。
- 2026-07-21 再补一层：RAG 流式输出已按 `ai.stream.message-chunk-size` 做 rune 级分块发送，和 Java `messageChunkSize` 语义一致。
- 2026-07-21 再补一层：RAG 聊天已接入公平队列限流包装器，超时拒绝会走 `reject + finish + done`，并在可落库时补齐消息与标题，对齐 Java `ChatQueueLimiter`。
- 2026-07-21 已补齐的高优先级项：意图树启发式解析与歧义澄清、检索并发、RAG 评测 `intentLeafIds`、SVG 栅格化到 PNG、XLSX 公式缓存值与超链接内联输出、HTML / XML / PPTX 原生解析与 Tika 兜底顺序修正、MIME 参数归一化与文档来源别名兼容。
- 2026-07-20 已补齐的高优先级项：摄取流水线通用执行引擎、会话标题 LLM 生成、会话删除级联、RAG 评测开关与字段、VLM `maxOutputTokens`、Tika 配置化默认兼容注册。

## 实施优先级

1. 先完成基础 RAG 全链路（阶段 0-7）
2. 再增加 crawler 框架 + Confluence source（阶段 8）
3. 多租户改造（阶段 9）
4. MCP 支付工具接入（阶段 10）
5. Agent 主动循环（阶段 11）
