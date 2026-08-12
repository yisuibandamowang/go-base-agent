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

后端会先加载默认值，再读取可选 YAML 配置文件，最后允许环境变量覆盖。默认配置文件路径是 `log-agent/config.yaml`；也可以用 `LOG_AGENT_CONFIG_FILE=/path/to/config.yaml` 指定。真实 `log-agent/config.yaml` 不提交，示例见 `log-agent/config.example.yaml`。

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `LOG_AGENT_CONFIG_FILE` | 空 | 指定 log-agent YAML 配置文件路径 |
| `LOG_AGENT_ADDRESS` | `:9108` | 后端监听地址 |
| `LOG_AGENT_LOG_READER_NODE_PATH` | `node` | Node.js 可执行文件 |
| `LOG_AGENT_LOG_READER_SCRIPT_PATH` | `/Users/mima0000/.codex/skills/member-k8s-pod-log-read/scripts/read_pod_logs.mjs` | 会员日志 helper 脚本 |
| `LOG_AGENT_LOG_READER_FUYAO_SCRIPT_PATH` | `/Users/work_project/360/ad-platform-bot/.codex/skills/ad-platform-runtime-readonly/scripts/k8s_pod_logs.mjs` | 扶摇日志 helper 脚本 |
| `LOG_AGENT_LOG_READER_FUYAO_WORK_DIR` | `/Users/work_project/360/ad-platform-bot` | 扶摇日志 helper 工作目录 |
| `LOG_AGENT_LOG_READER_TIMEOUT_MS` | `120000` | 单次查询超时；扶摇按 Deployment 扫多 Pod 时需要更长 websocket 等待时间 |
| `LOG_AGENT_LOG_READER_ALLOWED_ENVS` | `test,test2,test3,test4,regress,online` | 允许读取的环境 |
| `LOG_AGENT_LOG_READER_MAX_CONCURRENCY` | `4` | 全环境、全服务等批量查询时的并发 job 数 |
| `QIHOO360_API_KEY` | 空 | 360 智脑 OpenAI 兼容接口 API Key |
| `LOG_AGENT_ANALYZER_BASE_URL` | `https://api.360.cn/v1` | 360 智脑 OpenAI 兼容接口地址 |
| `BAILIAN_API_KEY` / `DASHSCOPE_API_KEY` | 空 | 阿里云百炼 OpenAI 兼容接口兜底 API Key |
| `LOG_AGENT_ANALYZER_BAILIAN_BASE_URL` | `https://dashscope.aliyuncs.com` | 阿里云百炼接口地址 |
| `LOG_AGENT_ANALYZER_CODE_REPO_PATH` | `/Users/work_project/360/member` | 会员微服务代码仓库路径；扶摇项目未填写代码目录时默认使用 `/Users/work_project/360/ad-platform-bot` |
| `LOG_AGENT_SQL_ENABLE` | `false` | 是否启用诊断链路内部 SQL 辅助查询；默认关闭，不影响现有日志链路 |
| `LOG_AGENT_SQL_DIALECT` | `postgres` | SQL 方言，当前支持 `postgres` / `sqlite` |
| `LOG_AGENT_SQL_DSN` | 空 | 可选固定只读数据库连接串；诊断链路优先从代码仓库解析数据库直连配置 |
| `LOG_AGENT_SQL_TIMEOUT_MS` | `3000` | 单次 SQL 查询超时 |
| `LOG_AGENT_SQL_MAX_ROWS` | `50` | SQL 查询最大返回行数，后端会强制补 `LIMIT` |
SSH 配置建议放在 `log-agent/config.yaml`：

```yaml
sql:
  enable: true
  ssh_profiles:
    member:
      enable: true
      host: chenhongyi
    fuyao:
      enable: true
      host: chenhongyi
```

这里的 `host` 是本机 `~/.ssh/config` 里的 Host 快捷名。后端会执行类似 `ssh -N -L 本地端口:代码解析出的数据库地址:数据库端口 chenhongyi` 的命令，`HostName`、`User`、`Port`、`IdentityFile` 等全部交给 OpenSSH 配置处理。不要提交真实 `log-agent/config.yaml`。

会员 K8S OpenAPI 凭据仍沿用现有 skill 的约定：

```text
MEMBER_K8S_TEST_AK / MEMBER_K8S_TEST_SK
```

或本机凭据文件：

```text
~/.codex/credentials/member_k8s_test_deploy.env
```

扶摇项目优先使用 `/Users/work_project/360/ad-platform-bot/.codex/skills/ad-platform-runtime-readonly` 下的只读日志 helper，凭据沿用该 skill 的约定：

```text
AD_PLATFORM_K8S_AK / AD_PLATFORM_K8S_SK
```

或本机凭据文件：

```text
~/.codex/credentials/ad_platform_k8s.env
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
    "project": "member",
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

`project` 不传时默认使用 `member`，也就是 K8S AppID `1586` 的会员项目。查询扶摇项目时，在前端“项目”下拉选择 `5658 扶摇项目`，代码目录会默认切换到 `/Users/work_project/360/ad-platform-bot`，也可以在输入框里手动覆盖。

扶摇默认按 webmember 的日志链路查询，也就是在 `ad-platform-test`、`ad-platform-regress`、`ad-platform-online` 中读取 `/home/log/webmember/webmember.log`。例如：

```json
{
  "project": "fuyao",
  "deployment": "ad-platform-online",
  "keywords": ["order_123"],
  "all_pods": true
}
```

扶摇项目如果不选具体 Deployment，后端会按环境自动映射到 webmember Deployment，避免底层 helper 在 `regress/online` 上无法通过 `service+env` 自动推导 Deployment。`env=all` 时会依次查询 `ad-platform-test`、`ad-platform-regress`、`ad-platform-online`。

### 流式日志查询

前端查询默认调用流式接口：

```text
POST /api/log-agent/logs/search/stream
```

后端使用 Gin `c.Stream()` 输出 SSE 事件，先返回日志查询进度和日志结果，再继续输出代码线索与模型分析增量。关键阶段会输出 `trace_id` 日志，便于定位请求卡在日志 helper、代码检索还是模型调用。

前端“停止”按钮会取消当前这一次流式请求；后端收到请求取消后会终止对应的日志 helper 子进程，日志中会出现 `log helper canceled`。`log helper started` 中的 `max_duration` 只是本次 helper 的最大允许执行时长，不代表已经超时；真正超时会打印 `log helper timeout`。扶摇 webmember 未指定具体 Pod 时会默认使用 `--all-pods`，避免只抽中一个 Pod 导致漏掉线上实例日志；后端会汇总 helper 返回的每个 Pod 结果并展示实际命中的 Pod。

后端托管的前端静态资源会返回 `Cache-Control: no-store`，`index.html` 也会给 `app.js` 带版本号，避免浏览器继续使用旧展示逻辑。

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

- 远端 helper 用字段值 `3523031789` 召回候选日志
- 后端再按 `qihoo_id` 字段做严格匹配，兼容普通 JSON 和转义 JSON

多行 `field=value` 会在后端做 AND 过滤，避免把只命中其中一个字段的日志展示出来。如果需要跨服务按纯值串链路，可以另起一行显式输入 `3523031789`。

智能分析会在日志查询后结合代码线索调用模型服务。代码目录可在前端“代码目录”输入框按请求覆盖；未填写时 member 使用后端配置的 `LOG_AGENT_ANALYZER_CODE_REPO_PATH`，fuyao 使用 `/Users/work_project/360/ad-platform-bot`。当前模型路由为代码内硬编码降级策略：

```text
360 智脑 codex-ccmax/gpt-5.5
-> 360 智脑 deepseek/deepseek-v4-flash-internal
-> 360 智脑 deepseek/deepseek-v4-pro
-> 360 智脑 deepseek/deepseek-v4-flash
-> 阿里云百炼 qwen3-max
```

未配置 `QIHOO360_API_KEY` 时会直接尝试阿里云百炼兜底；未配置 `BAILIAN_API_KEY` / `DASHSCOPE_API_KEY` 时则无法使用百炼兜底。每个路由尝试都会输出 provider、model 和失败原因，便于定位模型渠道问题。

分析前会先做通用的确定性日志解析，再把结构化事实放入模型 prompt。后端会解析普通 JSON 日志、嵌套 `msg` JSON 和 `aivip_extjson` 这类二次转义 JSON，优先提取 `level`、`ts`、`caller`、`msg`、`error`、`status`、`topic`、`event_id`、`qid`、`qihoo_id`、`mid`、`order_id`、`product`、`medium`、`logidurl`、`bd_vid` 等字段，并识别 `消费进入`、`处理失败`、`消费成功` 等流程阶段。模型必须先基于这些确定性事实，再结合代码线索解释原因。

当前已覆盖扶摇 webmember 百度转化事件的增强结论：当日志出现 `conversion event baidu bd_vid or logidurl is empty` 时，后端会解析原始消息里的 `aivip_extjson`，明确输出 `bd_vid_present` 和 `logidurl_present`，用于判断到底缺少 `bd_vid` 还是 `logidurl`。当只返回 `[HandleConversionEventQbusMessage] handle failed` 时，分析也会结合代码顺序确认消费已经进入 handler，避免误判为“消费进入未检索到”。代码线索检索会优先使用日志里的明确错误信息、业务 `msg` 和 handler 名，减少被 `caller`、`msg` 等通用日志字段带偏。

### SQL 辅助排查

SQL 是链路排查的内部辅助能力，默认关闭，不会影响现有 `/api/log-agent/logs/search` 和 `/api/log-agent/logs/search/stream` 主链路。前端不传数据库地址、端口、用户名、密码或任意 SQL；后端在诊断过程中根据日志、代码线索和代码仓库里的 datasource 配置决定是否查库。

启用后仍只允许只读 `SELECT`，后端会拒绝多语句和 `INSERT`、`UPDATE`、`DELETE`、`ALTER`、`DROP`、`TRUNCATE` 等非只读操作，并自动追加 `LIMIT`。如果配置了项目级 SSH profile，后端会按需建立临时 tunnel 到代码里解析出的数据库 `host:port`；未配置 SSH profile 时会尝试直连代码中的数据库地址。

诊断链路会先从日志事实检索代码链路和写库点，再用这些代码证据推断表名与过滤字段，而不是要求前端手动填表。比如扶摇 `HandleConversionEventQbusMessage` 这类 Kafka 事件，后端会结合 `service/conversion_event.go`、`ReportWithMonitorRetry`、`ad_media_report_monitor_log` 的代码线索推断目标表；如果表结构里只有 `kafka_event_id` 而日志里提到的是 `event_id`, 后端会自动把条件映射到 `kafka_event_id` 再查库。

### Beta 链路排查

数据库辅助排查走独立 Beta 流式接口，不复用现有日志查询入口：

```text
POST /api/log-agent/diagnosis/search/stream
```

这个接口的执行顺序是：先复用现有日志检索，再检索代码链路和写库点，然后执行可选 SQL 查询，最后把日志、代码证据和数据库结果一起交给模型生成诊断结论。未启用 SQL 时会输出 `db_query_result` 事件并提示 `SQL 助手未启用`。

当未显式填写 SQL 条件时，Beta 链路会做最小自动推断：

- `Keywords` 中的 `field=value` 会转成 SQL 过滤条件，例如 `order_id=order_123`。
- 如果只填了普通关键词，问题描述里包含“订单”时会按 `order_id` 查询；包含 `kafka`、`事件` 或 `event_id` 时会按 `event_id` 查询。
- 如果日志里已经返回结构化字段，后端会优先提取 `order_id`、`event_id`、`qid`、`qihoo_id`、`mid` 等主标识中的一个作为数据库过滤条件。
- 如果未填写 `SQL Table`，后端会基于代码目录扫描 `TableName()` 和 GORM `column:` 标签，选择匹配过滤字段的候选表。
- 数据库 `host`、`port`、`database`、`username`、`password` 会从代码仓库中的 `application.yml`、`bootstrap.yml`、`.properties`、Go/Java/XML 等文件扫描 PostgreSQL datasource 配置得到。

前端页面中普通“查询”仍调用现有日志流式接口；“链路排查”按钮调用 Beta 接口，但不会携带数据库连接信息或 SQL 文本。

### 下拉选项

```bash
curl http://localhost:9108/api/log-agent/options
```

## 安全边界

- 当前版本只读 K8S 日志，不支持部署、重启、exec 任意命令或数据库写操作；SQL 助手只允许只读查询。
- 后端只把结构化参数转换为 helper 参数，不接受用户传入 shell 命令。
- `online` 环境仍走同一套只读限制，建议查询时使用明确时间窗口和关键词。
- helper 输出已做敏感字段脱敏；后端不会记录 AK/SK、cookie、token 或签名。
