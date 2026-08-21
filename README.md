# go-base-agent

> 用 Go 重写的 Ragent 企业级 RAG 智能体平台，目标是与 Java 版保持接口、表结构和前端交互一致，前端可直接复用。

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)

---

## 目录

- [项目简介](#项目简介)
- [核心能力](#核心能力)
- [技术栈](#技术栈)
- [架构概览](#架构概览)
- [快速开始](#快速开始)
- [配置说明](#配置说明)
- [项目结构](#项目结构)
- [API 概览](#api-概览)
- [开发指南](#开发指南)
- [阶段进度](#阶段进度)
- [路线图](#路线图)
- [文档索引](#文档索引)
- [License](#license)

---

## 项目简介

`go-base-agent` 是 [Ragent](https://github.com/nageoffer/ragent) 的 Go 重写版本，围绕企业级 RAG 场景构建，保留了原有的 HTTP 契约、SSE 流式交互、数据库表结构和前端调用方式。

这个仓库的目标很明确：

- 与 Java 版接口对齐
- 与 Java 版表结构对齐
- 与 Java 版前端交互对齐
- 以 Go 的方式把基础设施层、模型路由层和业务层重新组织起来

## 核心能力

- 知识库管理：创建、查询、更新、删除知识库
- 文档入库：上传、解析、分块、向量化、入库
- RAG 问答：SSE 流式输出、查询改写、多问句拆分、术语归一化、意图路由、多通道融合检索、重排序、记忆压缩，并按 KB / MCP / 混合场景优先读取对应提示词槽位；检索会区分知识定向未命中与全局回退，只有真实召回归属的定向意图参与提示词选择；KB 场景会额外注入引用规则提示
- 知识图谱：`rag.graph.type=lightrag` 时启用 LightRAG 图谱后端，并可通过 `rag.search.channels.graph.enabled` 参与检索召回；图谱按 KB 意图置信度共享全局/定向作用域，定向时剔除失效 Collection 并扩大召回预算；同时向量写入、文档删除和知识库清理会 best-effort 同步图谱数据，后台提供 `/admin/kg/graph` 与 `/admin/kg/labels` 用于图谱浏览
- 模型路由：多 provider 候选、首包探测、故障切换、三态熔断和 chat 档位候选/超时预算；RAG 主回答优先走云端候选，rewrite、摘要、标题和 MCP 选择/抽参、普通非 RAG 回答优先走本地 Ollama `qwen3.6:latest`，本地不可用再降级云端路由
- 管理后台：仪表盘、追踪、示例问题、用户管理、审计日志
- 会话增强：标题生成、摘要压缩和推荐追问生成
- 系统设置快照：`/rag/settings` 返回引擎、后端选型、检索管线、AI 档位（含 chat tiers）、SSE 超时与上传配置的只读展示
- MCP：主服务可接入远程 MCP Server，按问题选择相关工具、提取参数并注入工具结果；独立进程暴露知识库、销售、天气、联网搜索等工具能力
- 联调工具：提供 LangChain4j 兼容测试路由，图片分析在配置 VLM 时调用真实多模态模型，未配置时降级 demo 响应
- 体验环境：`app.demo-mode=true` 时主服务切为只读模式，GET 查询放行，写操作和 SSE 问答会返回体验环境拒绝结果
- 可观测性：trace_id、链路追踪、健康检查、指标探针

## 技术栈

| 组件 | 选型 |
|------|------|
| 语言 | Go 1.25.8 |
| Web 框架 | Gin |
| ORM | GORM v2 |
| 数据库 | PostgreSQL 16 + pgvector |
| 缓存 | Redis 7 + go-redis/v9 |
| 消息队列 | RocketMQ 5 + `apache/rocketmq-client-go/v2` |
| 对象存储 | S3 兼容 RustFS + `aws-sdk-go-v2` 体系 |
| 配置管理 | `spf13/viper` + `.env` + `${VAR:default}` 占位符 |
| 日志 | `log/slog` |
| 鉴权 | 自实现 JWT 中间件 |

## 架构概览

```text
cmd/initializer/main.go # 预检/清理/初始化入口
cmd/ragent/main.go       # 主服务入口
cmd/mcp-server/main.go   # MCP Server 入口

internal/
├── framework/           # 基础设施层
│   ├── config/          # 配置加载
│   ├── db/              # 数据库连接与分页
│   ├── middleware/      # recover / trace / auth / db / tenant
│   ├── mq/              # RocketMQ 抽象
│   ├── lock/            # 分布式锁
│   ├── ratelimit/       # 公平队列限流
│   ├── sse/             # SSE 发送封装
│   ├── trace/           # 链路追踪
│   └── convention/      # 统一响应体
├── infra/               # 模型路由与 provider 适配
│   ├── chat/
│   ├── embedding/
│   ├── rerank/
│   └── model/
└── biz/                 # 业务域
    ├── user/
    ├── knowledge/
    ├── conversation/
    ├── ingestion/
    ├── intent_tree/
    ├── admin/
    ├── audit/
    ├── rag/
    ├── crawler/
    └── mcp_tool/
```

## 快速开始

### 1. 启动基础设施

```bash
docker compose -f deploy/docker-compose.yml up -d
```

默认会启动：

- PostgreSQL 16 + pgvector
- Redis 7
- RocketMQ NameServer / Broker
- RustFS

### 2. 初始化数据库

```bash
make migrate
```

会执行 `cmd/initializer migrate`，依次灌入数据库结构和初始数据。

### 2.1 清理初始化数据

```bash
make cleanup-db
```

会执行 `cmd/initializer cleanup`，用于清空企业知识库初始化数据和相关业务表，保留表结构。

### 2.2 运行预检

```bash
make preflight
```

会检查 PostgreSQL、Redis、健康接口、管理员登录状态和 `/rag/settings` 中声明的后端类型，并拒绝近 30 分钟内存在文档分块、RAG trace、摄取任务或活动流的初始化/清理操作，当前由 `cmd/initializer preflight` 执行。

### 2.3 初始化企业知识库

```bash
make initialize
```

`AGENT_TYPE_DIR` 可覆盖 Makefile 中的默认数据集目录，例如：

```bash
make initialize AGENT_TYPE_DIR=/path/to/agent-type-dir
```

初始化流程会依次执行预检、清理、知识库创建、文档上传与分块、意图树、示例问题、结果校验和预热。
正式初始化要求数据集目录提供 `checksums.sha256`，启动前会校验目录内声明文件的 SHA-256；开发测试场景直接调用初始化包时仍保持兼容。
意图 properties 支持 Java 数据集的 `mcp-tool-id`、`prompt-snippet-file`、`prompt-template-file` 和 `param-prompt-template-file` 字段。
知识库 properties 支持 `knowledge-base.<ref>.ingestion-spec`，初始化上传时会透传文档级 `fast/fidelity` 解析档位及 `maxChars`、`overlapChars`、`rowsPerChunk`、`toleranceFactor` 分块预算；服务端归一化后写入 `t_knowledge_document.ingestion_spec` 并在实际解析、分块时使用。现有 `chunkStrategy/chunkConfig` 仍保留兼容。
初始化结果校验还会确认每个绑定知识库的意图节点实际包含对应 Collection，避免意图树创建成功但检索范围错误。
默认同名文档会按 Java 初始化器行为替换，单份文档分块最多等待 `20m`，每 `3s` 轮询一次；可用
`--replace-existing=false`、`--document-timeout` 和 `--document-poll-interval` 调整。只查看计划时加
`--dry-run`，跳过模型预热时加 `--skip-warmup`。
预热每轮默认最多尝试 3 次，SSE 必须收到非空回答、`finish`（`NORMAL` 且含消息 ID）和 `done`；回答完成后会补生成推荐追问，追问失败不影响主回答数据。
可用 `--warmup-max-attempts`、`--warmup-retry-interval`、`--warmup-interval` 调整预热节奏，使用 `--warmup-shuffle-seed` 固定演示问题顺序以便复现。

### 3. 配置环境

```bash
cp .env.example .env
```

按需填写数据库、Redis、RocketMQ、对象存储和模型 API Key。

### 4. 启动主服务

```bash
make run
```

默认监听 `http://localhost:9091`。

### 5. 启动 MCP Server

```bash
go run ./cmd/mcp-server
```

默认监听 `http://localhost:9099`。
若设置了 `YDC_API_KEY`，MCP Server 会额外启用联网搜索工具 `youcom_search`。

### 6. 基本验证

```bash
curl http://localhost:9091/api/ragent/health
curl http://localhost:9091/health
curl http://localhost:9091/readyz
curl http://localhost:9091/metrics
```

## 配置说明

### 文件分工

| 文件 | 用途 |
|------|------|
| `.env` | 本地环境变量，不提交 |
| `configs/config.yaml` | 本地业务配置，不提交 |
| `.env.example` | 环境变量模板，提交 |
| `configs/config.example.yaml` | 配置模板，提交 |

### 加载顺序

`config.Load()` 的加载链如下：

1. `godotenv` 读取 `.env`
2. 读取 `configs/config.yaml`
3. 执行 `${VAR}` / `${VAR:default}` 占位符展开
4. 使用 `viper` 反序列化到配置结构体

### 常用配置项

| 配置项 | 说明 |
|------|------|
| `database` | PostgreSQL 连接信息 |
| `redis` | Redis 连接信息 |
| `rocketmq` | RocketMQ NameServer 与 producer 配置 |
| `milvus` | 预留向量库切换配置 |
| `rag.engine.type` | 执行架构档位，默认 `workflow`；可显式切换为 `agent`，用于区分编排管线与 ReAct 主智能体模式，agent 模式下主回答优先使用 `AGENT_MAIN` 槽位 |
| `rag.vector.type` | 默认 `pg`，预留 `milvus` 切换 |
| `rag.graph` | 知识图谱后端配置；`type=none` 时不启用，`type=lightrag` 时注册 LightRAG 客户端、图谱检索通道，并在向量写入/文档删除/知识库清理时 best-effort 同步图谱 |
| `rag.search` | RAG 多通道检索配置；默认 TopK、通道倍数、通道级超时、融合 RRF 与 rerank 候选上限按 Java `SearchChannelProperties` 补齐默认值；`rag.search.channels.graph.enabled=true` 需要同时开启 `rag.graph.type=lightrag` |
| `rag.query-rewrite` | 查询改写配置；主链路会先按 `t_query_term_mapping` 做启用规则的术语归一化，启用时走 LLM 改写+拆分，并支持指代消解、省略续问及非查询轮次保护，关闭时走规则拆分 |
| `rag.citation.enabled` | 知识库回答行内引用开关；开启后模型上下文使用内部文档标识，最终回答中的标识会转换为 `[N](#cite-N)`，默认关闭 |
| `rag.memory` | 会话记忆配置；默认保留最近 `8` 轮、累计 `9` 轮后生成最长 `400` 字摘要；存在消息历史时会保留最新摘要作为系统消息，并按 Java 版窗口策略截取最近消息；摘要压缩异步执行并用 Redis 锁串行化，配置会校验 `summary-start-turns > history-keep-turns` |
| `rag.rate-limit.global` | RAG 聊天全局并发队列限流；默认启用并按 Java 使用 `max-concurrent=50`、`max-wait-seconds=20`、`lease-seconds=600`、`poll-interval-ms=200`；超时会返回 `reject + finish + done` SSE |
| `rag.mcp.servers` | 远程 MCP Server 列表；主服务启动时执行 `initialize` / `tools/list` 并注册远端工具，问答时会先按 tenant/domain 过滤可见工具再选择调用 |
| `app.intent-tree.init-from-factory` | 默认 `false`；开启后主服务启动时会按 Java `IntentTreeFactory` 初始化默认意图树，已存在的 `intentCode` 会跳过；仅在 `rag.engine.type` 未显式配置时作为兼容兜底 |
| `ai.stream.message-chunk-size` | SSE 消息分块粒度；主链路会按 rune 数批量发送 `message` 事件 |
| `ai.providers.*` | Chat / Embedding / Rerank provider 配置 |
| `ai.chat.default-tier` / `ai.chat.deep-thinking-tier` / `ai.chat.tiers` | Chat 档位路由配置；按档位候选顺序和 `timeout-ms` 执行，深度思考请求使用 `deep-thinking-tier`，档位配置不完整时兼容旧的模型候选排序 |
| `sa-token` | JWT 认证配置，包含 token 名称和过期时间 |
| `rustfs` | 对象存储配置 |

> 认证配置节点以代码中的 `sa-token` 为准。

## 项目结构

```text
configs/                 # 配置模板
deploy/                  # docker-compose
docs/                    # 设计文档、阶段文档、契约文档
internal/framework/      # 基础设施层
internal/infra/          # AI provider 与路由实现
internal/biz/            # 业务代码
resources/database/      # schema、初始化数据与清理脚本
resources/prompts/       # Prompt 模板
scripts/                 # 迁移、清理、预检与验证脚本
cmd/ragent/              # 主服务
cmd/mcp-server/          # MCP Server
```

## API 概览

完整契约见 [`docs/API_CONTRACT.md`](./docs/API_CONTRACT.md)。

### 基础与探针

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/ragent/health` | 健康检查 |
| GET | `/health` `/healthz` `/live` `/livez` `/ready` `/readyz` | 探针 |
| GET | `/metrics` | 指标 |

### 主要业务接口

| 模块 | 典型路径 |
|------|----------|
| 鉴权 | `/api/ragent/auth/login`、`/api/ragent/auth/logout`、`/api/ragent/auth/current-user` |
| 用户 | `/api/ragent/users`、`/api/ragent/user/password` |
| 会话 | `/api/ragent/conversations`、`/api/ragent/conversations/:conversationId/messages` |
| 知识库 | `/api/ragent/knowledge-base`、`/api/ragent/knowledge-base/docs/ingestion-spec-schema`、`/api/ragent/knowledge-base/:id/docs/upload`（支持本地文件、远程 URL、内部 URL） |
| 意图树 | `/api/ragent/intent-tree/*`、`/api/ragent/mappings*` |
| 智能体 | `/api/ragent/agents*`、`/api/ragent/admin/agents*` |
| 管理后台 | `/api/ragent/admin/*`、`/api/ragent/biz-change-logs*` |
| 入库任务 | `/api/ragent/ingestion/pipelines*`、`/api/ragent/ingestion/tasks*` |
| RAG 流式问答 | `/rag/v3/chat`、`/rag/v3/stop`（支持配置默认代码仓库路径 `rag.code.repo-path`，也支持请求参数 `codeRepoPath` 手动覆盖） |
| MCP | `:9099` 的 JSON-RPC 端点 |

## 开发指南

### 常用命令

```bash
make run              # 启动主应用
make build            # 构建主应用和 MCP Server
make test             # 单测 + race
make test-integration # 集成测试
make lint             # golangci-lint
make fmt              # gofumpt + goimports
make vet              # go vet
make mod              # go mod tidy
make migrate          # 执行数据库迁移
```

### 开发约定

- 错误使用 `fmt.Errorf("failed to xxx: %w", err)` 包装
- 涉及 I/O 的逻辑必须传递 `context.Context`
- 日志必须携带 `trace_id`
- 公开函数必须补 GoDoc 注释
- 数据库表名和字段名保持与 Java 版一致

## 阶段进度

当前仓库的主线阶段已经完成：

- 阶段 0：工程骨架
- 阶段 1：framework 层
- 阶段 2：infra-ai 层
- 阶段 3：基础 RAG 主链路
- 阶段 4：完整能力包与检索闭环
- 阶段 5：知识库与文档入库 Pipeline
- 阶段 6：MCP Server + Admin + 联调
- 阶段 7：收尾与文档整理

更细的演进记录见 [`docs/DEVLOG.md`](./docs/DEVLOG.md)。

## 路线图

下一阶段重点是 Agentic RAG：

1. 文档爬取接入：Confluence / Notion / Git / 飞书
2. 多租户隔离：按业务域拆分知识与检索
3. MCP 工具扩展：订单、退款、会员查询等
4. 主动循环：定时抓取、自动入库、变更通知

详细规划见 [`docs/AGENTIC_RAG_PLAN.md`](./docs/AGENTIC_RAG_PLAN.md)。

## 文档索引

- [`docs/STARTUP.md`](./docs/STARTUP.md)
- [`docs/API_CONTRACT.md`](./docs/API_CONTRACT.md)
- [`docs/DEVLOG.md`](./docs/DEVLOG.md)
- [`docs/AGENTIC_RAG_PLAN.md`](./docs/AGENTIC_RAG_PLAN.md)

## License

[Apache License 2.0](LICENSE)
