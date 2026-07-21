# AI Proxy — AI 请求转发平台

统一管理多个 AI 供应商，提供单一入口供 OpenCode、Claude Desktop、OpenClaw 等 Agent 工具接入。支持优先级路由、故障转移、指数退避重试（含 429 限流重试）、断路器保护（状态持久化）、SSE 流式传输、OpenAI ↔ Anthropic 协议自动转换（含 tool_use/tool_calls/thinking），以及内建监控面板、请求限流、日志轮转、配置热加载。

---

## 快速开始

### 编译

```bash
# 推荐：使用 make（自动注入版本号、commit、build date）
make build

# 或手动编译（版本号显示为 dev）
go build -o ai-proxy.exe ./cmd/proxy/
```

### 配置

复制 `config/providers.example.yaml` 为 `config/providers.yaml`，填入 API Key：

```yaml
global:
  listen_addr: ":8080"
  log_file: proxy.log
  cb_threshold: 2
  cb_cooldown: 30
  cb_skip_requests: 10

providers:
  - name: my-provider
    model_id: gpt-4o
    api_key: sk-xxxxxx
    base_url: https://api.openai.com/v1
    priority: 1
    format: openai
    timeout: 60
    retry:
      max_retries: 3
      retry_interval: 2
      backoff_factor: 2
```

### 启动

```bash
ai-proxy.exe --config config/providers.yaml
ai-proxy.exe --version    # 查看版本信息
```

### 配置 Agent

将 Agent 工具的 `base_url` 指向 `http://localhost:8080`。`model` 字段支持三种路由模式：

| 模式 | model 值 | 行为 | 适用场景 |
|------|----------|------|----------|
| **Flash**（推荐） | `Flash` | 跳过 P1，直达 P2+ | 日常使用，避免 TPM 限流 |
| **Medium** | `Medium` | 全部优先级，自动降级 | 穷尽所有可用供应商 |
| **Max** | `Max` | 仅最高优先级 P1 | 需要最强模型，不计成本 |

也可直接填真实模型名（如 `gpt-4o`），通过 `model_routes` 映射到指定供应商。

---

## 架构概览

```
Agent 请求
  │
  ├─ middleware/logger     请求日志（/api/* 跳过）
  ├─ middleware/detector   自动检测 OpenAI / Anthropic 格式
  │
  └─ router/engine         核心引擎
       │
       ├─ 按 priority 分组，逐组尝试（P1 → P2 → P3）
       │   ├─ 断路器检查：该组是否熔断？→ 跳过
       │   ├─ round-robin + per-provider TryLock（同组并发）
       │   ├─ token bucket 限流检查
       │   └─ 指数退避重试（5xx/429 可重试，4xx 跳过）
       │       └─ 组内全部失败 → recordGroupFailure → CB 计数+1
       │
       └─ adapter 协议转换（OpenAI ↔ Anthropic）+ 上游调用
              │
              └─ tracer 结构化日志 → gin.DefaultWriter → io.MultiWriter(日志文件, SSE Hub)
```

---

## 路由与故障转移

### 优先级降级链路

```
P1 全部供应商失败 → CB failureCount[P1]++ → 降级到 P2
P2 全部供应商失败 → CB failureCount[P2]++ → 降级到 P3
P3 全部供应商失败 → 返回 503
```

每个优先级组有**独立的断路器**。P1 熔断后请求直接跳到 P2，不浪费时间重试 P1。

### 断路器（Circuit Breaker）

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `cb_threshold` | 2 | 连续组失败次数达到此值触发熔断 |
| `cb_cooldown` | 30s | 熔断后冷却时间，到期自动半开探测 |
| `cb_skip_requests` | 10 | 熔断期间跳过的请求数（也可触发自动关闭） |

```
CLOSED（正常）
  │  组失败 ≥ threshold
  ▼
OPEN（熔断，请求直接跳过该组）
  │  cooldown 到期 或 skip_requests 耗尽
  ▼
自动关闭 → CLOSED（半开探测下一个请求）
```

- **状态持久化**：CB 状态保存在 `config/.cb_state.json`，重启后自动恢复
- **原子写入**：使用临时文件 + rename，防止并发写损坏

### 重试策略

| 错误类型 | 行为 |
|----------|------|
| HTTP 5xx | 可重试，等待退避后重试同一供应商 |
| HTTP 429 | 可重试，等待退避后重试同一供应商 |
| HTTP 4xx（除 429） | 不可重试，立即切换下一供应商 |
| 超时/取消 | 不可重试，立即切换 |
| 流式已部分写入 | 停止重试，注入 SSE 错误事件 |

退避公式：`retry_interval × backoff_factor^(N-1)`，默认 2s → 4s → 8s。

---

## 流式传输

- **自动检测**：请求体含 `"stream": true` 自动启用
- **SSE 实时下发**：`flushWriter` 每次 Write 后立即 Flush
- **空闲超时**：`idleTimeoutReader` 在 `provider.Timeout` 秒无数据时返回超时
- **整体超时**：`maxStreamDuration = 3min` 硬上限，防止无限慢速流
- **中途错误**：流式失败时注入 SSE error event，通知客户端截断
- **跨格式转换**：SSE 流逐事件实时转换（Anthropic SSE ↔ OpenAI SSE）

---

## 协议转换

自动检测请求格式（按 URL 路径 + body 字段），支持 OpenAI ↔ Anthropic 双向转换：

| 转换方向 | 处理内容 |
|----------|---------|
| 请求体 | system 消息提取/注入、`stop` ↔ `stop_sequences` 重命名、`tools` 格式转换（`input_schema` ↔ `parameters`）、`tool_choice` 格式转换 |
| 响应体（同步） | content blocks 转换、tool_use ↔ tool_calls、stop_reason ↔ finish_reason 映射 |
| 响应体（流式） | SSE 事件逐条转换，含 tool_use/tool_calls 状态追踪 |

使用通用 JSON map 操作，保留 `response_format`、`seed` 等未知字段原样透传。

---

## 监控面板

浏览器访问 `http://<host>:8080/`：

- **状态开关** — 一键启用/停用代理（停用时请求返回 503）
- **汇总卡片** — 总请求 / 成功 / 失败 / 成功率
- **供应商表格** — 按 priority 显示每个供应商的请求数、成功率
- **实时日志** — SSE 流式日志，自动更新

---

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/v1/chat/completions` | Chat Completions（同步 + 流式） |
| `POST` | `/chat/completions` | 兼容不带 /v1 的路径 |
| `POST` | `/v1/messages` | Anthropic Messages API |
| `GET` | `/health` | 健康检查 `{"status":"ok"}` |
| `GET` | `/` | 监控面板 |
| `GET` | `/api/stats` | 统计快照 JSON |
| `GET` | `/api/logs` | SSE 实时日志流 |
| `POST` | `/api/control` | 启用/停用代理 `{"running":bool}` |

---

## 配置参考

### global

```yaml
global:
  listen_addr: ":8080"       # 监听地址
  log_file: proxy.log        # 日志文件（留空则仅输出到 stdout）
  cb_threshold: 2            # 断路器失败阈值
  cb_cooldown: 30            # 断路器冷却时间（秒）
  cb_skip_requests: 10       # 熔断期间跳过请求数
```

### model_routes（可选）

模型名别名映射，在非 Max/Medium/Flash 模式下生效：

```yaml
model_routes:
  - alias: gpt-4o
    target: sensenova-glm-5.2
  - alias: default          # 特殊键：未匹配任何别名时的兜底
    target: deepseek-v4-flash
```

### providers

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `name` | 是 | — | 供应商唯一标识 |
| `model_id` | 是 | — | 实际调用的模型 ID |
| `api_key` | 是 | — | API Key |
| `base_url` | 是 | — | API 地址（OpenAI 格式含 `/v1`，Anthropic 格式不含 `/v1`，代码自动拼接 `/v1/messages`） |
| `priority` | 是 | — | 优先级（越小越优先） |
| `format` | 是 | — | `openai` 或 `anthropic` |
| `auth_type` | 否 | 自动 | `bearer`（Authorization: Bearer）或 `x-api-key`（+ anthropic-version）；为空时按 format 自动选择：openai→bearer，anthropic→x-api-key |
| `timeout` | 否 | 60 | 请求超时（秒，不能为负） |
| `retry.max_retries` | 否 | 3 | 最大重试次数（不能为负） |
| `retry.retry_interval` | 否 | 2 | 首次重试间隔（秒，不能为负） |
| `retry.backoff_factor` | 否 | 2 | 退避因子（不能为负） |
| `rate_limit.enabled` | 否 | false | 是否启用 per-provider 限流 |
| `rate_limit.rpm` | 否 | 60 | 每分钟允许请求数 |
| `rate_limit.burst` | 否 | 10 | 最大突发请求数 |

### rate_limit 示例

```yaml
providers:
  - name: my-provider
    rate_limit:
      enabled: true
      rpm: 60
      burst: 10
```

---

## 部署

### 树莓派（Linux ARM64）

```bash
# 交叉编译
make build-linux-arm64

# 上传到树莓派
scp ai-proxy-linux-arm64 pi@raspberrypi:~/ai-proxy
scp config/providers.yaml pi@raspberrypi:~/config/providers.yaml

# 运行（或配置 systemd 自启）
~/ai-proxy --config ~/config/providers.yaml
```

> 如需从局域网其他设备通过监控面板控制代理启停，需将 `global.control_allow_remote` 设为 `true`，否则 `/api/control` 仅允许本机访问。

### 跨平台编译

```bash
make build-all          # 全平台
make build-linux-arm64  # 树莓派
make build-linux        # Linux amd64
make build-macos-arm64  # macOS Apple Silicon
make build VERSION=v1.0.0  # 指定版本号
```

---

## 高级功能

| 功能 | 说明 |
|------|------|
| **配置热加载** | 每 30s 检测 `providers.yaml` 变更并自动重载，无需重启。不影响正在处理的请求和断路器状态 |
| **日志轮转** | 日志文件 100MB 自动分割，保留 5 个备份，旧文件自动清理 |
| **CB 状态持久化** | 断路器状态保存在 `.cb_state.json`，重启后自动恢复 |
| **优雅关闭** | 捕获 SIGINT/SIGTERM，等待正在处理的请求最多 10s 后退出，同时停止 config watcher |
| **请求体大小限制** | 10MB 上限，防止恶意大 payload 致 OOM |
| **HTTP Server 超时** | ReadHeaderTimeout=10s、IdleTimeout=120s，防范 Slowloris |
