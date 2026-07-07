# ragent-go — Go 复刻开发日志

> 从 Java 版 Ragent 用 Go 重写，目标功能 100% 对齐，前端零改动复用。

## 项目概览

| 维度 | 内容 |
|------|------|
| 仓库 | `github.com/nageoffer/ragent-go` |
| Go 版本 | 1.25.5 |
| Web 框架 | Gin |
| ORM | GORM v2 |
| 数据库 | PostgreSQL 16 + pgvector |
| 缓存 | Redis 7 |
| 消息队列 | RocketMQ 5 |
| 对象存储 | S3 兼容 (RustFS) |
| 鉴权 | 自实现 JWT 中间件 (对齐 Sa-Token) |
| 迁移计划 | `../MIGRATION_PLAN.md`（位于 Java 仓库） |

## 阶段进度

| 阶段 | 状态 | 日期 | 产出 / 卡点 |
|------|------|------|------------|
| 0 — 工程骨架 | ✅ 完成 | 2026-07-07 | go mod init、目录骨架、Makefile、CI lint、docker-compose、config.yaml、health 端点 |
| 1 — framework 层 | ⬜ 未开始 | — | — |
| 2 — infra-ai 层 | ⬜ 未开始 | — | — |
| 3 — 知识库 + Pipeline | ⬜ 未开始 | — | — |
| 4 — 检索系统 | ⬜ 未开始 | — | — |
| 5 — 问答系统 | ⬜ 未开始 | — | — |
| 6 — MCP + Admin + 联调 | ⬜ 未开始 | — | — |
| 7 — 收尾 | ⬜ 未开始 | — | — |

## 阶段 0 交付物清单

- [x] `go mod init github.com/nageoffer/ragent-go`
- [x] 目录骨架（cmd/internal/pkg/configs/deploy/scripts/docs/testdata）
- [x] `Makefile`（run/build/test/lint/migrate/fmt/vet/mod/clean）
- [x] `.golangci.yml` golangci-lint 配置
- [x] `deploy/docker-compose.yml`（PG16+pgvector、Redis7、RocketMQ5 namesrv+broker、RustFS）
- [x] `configs/config.yaml`（对齐 Java application.yaml）
- [x] `scripts/migrate.sh`（执行 schema_pg.sql + init_data_pg.sql）
- [x] `cmd/ragent/main.go` — Gin 空服务，`GET /api/ragent/health` → `{"code":"0","data":"ok"}`
- [x] `cmd/mcp-server/main.go` — 占位入口
- [x] `.gitignore`
- [ ] 启动 Go 服务验证 health 端点
- [ ] 抓 Java 版 SSE fixture 到 `testdata/sse/baseline_chat.txt`
