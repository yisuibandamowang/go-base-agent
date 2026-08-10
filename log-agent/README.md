# log-agent

`log-agent` 是独立于主 RAG 问答界面的会员日志排查工具。当前 MVP 只接入 K8S 日志读取能力，底层复用本机 Codex skill:

```text
/Users/mima0000/.codex/skills/member-k8s-pod-log-read/scripts/read_pod_logs.mjs
```

## 目录

```text
log-agent/
├── backend/    # Go + Gin 后端，封装日志 helper
└── frontend/   # 独立前端静态服务
```

## 后端启动

```bash
go run ./log-agent/backend
```

默认监听：

```text
http://localhost:9108
```

环境变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `LOG_AGENT_ADDRESS` | `:9108` | 后端监听地址 |
| `LOG_AGENT_LOG_READER_NODE_PATH` | `node` | Node.js 可执行文件 |
| `LOG_AGENT_LOG_READER_SCRIPT_PATH` | `/Users/mima0000/.codex/skills/member-k8s-pod-log-read/scripts/read_pod_logs.mjs` | 日志 helper 脚本 |
| `LOG_AGENT_LOG_READER_TIMEOUT_MS` | `15000` | 单次查询超时 |
| `LOG_AGENT_LOG_READER_ALLOWED_ENVS` | `test,test2,test3,test4,regress,online` | 允许读取的环境 |
| `LOG_AGENT_LOG_READER_MAX_CONCURRENCY` | `4` | 全环境、全服务等批量查询时的并发 job 数 |
| `QIHOO360_API_KEY` | 空 | 360 智脑 OpenAI 兼容接口 API Key |
| `LOG_AGENT_ANALYZER_BASE_URL` | `https://api.360.cn/v1` | 360 智脑 OpenAI 兼容接口地址 |
| `BAILIAN_API_KEY` / `DASHSCOPE_API_KEY` | 空 | 阿里云百炼 OpenAI 兼容接口兜底 API Key |
| `LOG_AGENT_ANALYZER_BAILIAN_BASE_URL` | `https://dashscope.aliyuncs.com` | 阿里云百炼接口地址 |
| `LOG_AGENT_ANALYZER_CODE_REPO_PATH` | `/Users/work_project/360/member` | 会员微服务代码仓库路径 |

K8S OpenAPI 凭据仍沿用现有 skill 的约定：

```text
MEMBER_K8S_TEST_AK / MEMBER_K8S_TEST_SK
```

或本机凭据文件：

```text
~/.codex/credentials/member_k8s_test_deploy.env
```

## 前端启动

```bash
cd log-agent/frontend
npm run dev
```

默认监听：

```text
http://localhost:5178
```

前端默认调用：

```text
http://localhost:9108/api/log-agent/logs/search/stream
```

## API

### 健康检查

```bash
curl http://localhost:9108/api/log-agent/health
```

### 日志查询

```bash
curl -X POST http://localhost:9108/api/log-agent/logs/search \
  -H 'Content-Type: application/json' \
  -d '{
    "service": "pay",
    "env": "test2",
    "at": "2026-08-10 15:30:00",
    "before_minutes": 2,
    "after_minutes": 1,
    "keywords": ["PayCenterFailed"],
    "question": "订单支付成功但会员未到账，帮我定位可能原因",
    "code_repo_path": "/Users/work_project/360/member",
    "include_critical": true
  }'
```

### 流式日志查询

前端查询默认调用流式接口：

```text
POST /api/log-agent/logs/search/stream
```

后端使用 Gin `c.Stream()` 输出 SSE 事件，先返回日志查询进度和日志结果，再继续输出代码线索与模型分析增量。关键阶段会输出 `trace_id` 日志，便于定位请求卡在日志 helper、代码检索还是模型调用。

前端“停止”按钮会取消当前这一次流式请求；后端收到请求取消后会终止对应的日志 helper 子进程，日志中会出现 `log helper canceled`。`log helper started` 中的 `max_duration` 只是本次 helper 的最大允许执行时长，不代表已经超时；真正超时会打印 `log helper timeout`。

全环境、全服务、全 Pod 查询：

```bash
curl -X POST http://localhost:9108/api/log-agent/logs/search \
  -H 'Content-Type: application/json' \
  -d '{
    "service": "all",
    "env": "all",
    "keywords": ["order_123"],
    "all_pods": true,
    "include_critical": true
  }'
```

`env=all` 会包含 `online`。全选查询必须提供 `keywords` 或 `regexes`，避免无条件读取所有服务日志。

`keywords` 支持字段值写法。比如输入：

```text
qihoo_id=3523031789
```

后端会同时查询：

- 原始 `qihoo_id=3523031789`
- 兼容 JSON/URL 参数形式的字段值正则

这类写法默认不会自动附加纯值宽搜，避免返回其他字段里包含同一数字的日志。如果需要跨服务按纯值串链路，可以另起一行显式输入 `3523031789`。

智能分析会在日志查询后结合代码线索调用模型服务。代码目录可在前端“代码目录”输入框按请求覆盖；未填写时使用后端配置的 `LOG_AGENT_ANALYZER_CODE_REPO_PATH`。当前模型路由为代码内硬编码降级策略：

```text
360 智脑 codex-ccmax/gpt-5.5
-> 360 智脑 deepseek/deepseek-v4-flash-internal
-> 360 智脑 deepseek/deepseek-v4-pro
-> 360 智脑 deepseek/deepseek-v4-flash
-> 阿里云百炼 qwen3-max
```

未配置 `QIHOO360_API_KEY` 时会直接尝试阿里云百炼兜底；未配置 `BAILIAN_API_KEY` / `DASHSCOPE_API_KEY` 时则无法使用百炼兜底。每个路由尝试都会输出 provider、model 和失败原因，便于定位模型渠道问题。

### 下拉选项

```bash
curl http://localhost:9108/api/log-agent/options
```

## 安全边界

- 当前版本只读 K8S 日志，不支持部署、重启、exec 任意命令或数据库写操作。
- 后端只把结构化参数转换为 helper 参数，不接受用户传入 shell 命令。
- `online` 环境仍走同一套只读限制，建议查询时使用明确时间窗口和关键词。
- helper 输出已做敏感字段脱敏；后端不会记录 AK/SK、cookie、token 或签名。
