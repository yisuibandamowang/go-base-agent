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

```go
// internal/biz/crawler/source.go
type DocumentSource interface {
    Name() string
    ListDocuments(ctx context.Context) ([]DocumentMeta, error)
    FetchDocument(ctx context.Context, id string) (*Document, error)
    WatchChanges(ctx context.Context, since time.Time) (<-chan ChangeEvent, error)
}

// 计划支持的 source:
// - ConfluenceSource       → REST API
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

## 实施优先级

1. 先完成基础 RAG 全链路（阶段 0-7）
2. 再增加 crawler 框架 + Confluence source（阶段 8）
3. 多租户改造（阶段 9）
4. MCP 支付工具接入（阶段 10）
5. Agent 主动循环（阶段 11）
