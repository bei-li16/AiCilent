# AI Proxy — AI 请求转发平台

统一管理多个 AI 供应商，提供单一入口供 OpenCode、Claude Desktop、OpenClaw 等 Agent 工具接入。支持优先级路由、故障转移、重试策略、流式传输、协议自动转换，以及内建监控面板。

---

## 快速开始

### 编译

```bash
go build -o ai-proxy.exe ./cmd/proxy/
```

### 配置

编辑 `config/providers.yaml`，填入你的 API Key：

```yaml
providers:
  - name: my-provider
    vendor: openai
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
```

### 配置 Agent

将 Agent 工具的 `base_url` 指向 `http://localhost:8080`，`model` 字段可选以下模式：

- **Flash**（推荐）— 跳过限流层，直达稳定链路
- **Medium** — 全部供应商，自动降级
- **Max** — 仅最高优先级

---

## 架构概览

```
Agent 请求 → middleware/logger → middleware/detector → router/engine
                                                           │
                                              ┌────────────┼────────────┐
                                              ▼            ▼            ▼
                                          Provider P1  Provider P2  Provider P3
                                          (round-robin + 重试 + 断路器)

tracer 日志 → gin.DefaultWriter → io.MultiWriter(logFile, sseHub) → 监控面板
```

- **router/engine** — 核心引擎：路由匹配、优先级降级、断路器、round-robin、per-provider TryLock、指数退避重试
- **middleware/detector** — 自动检测 OpenAI / Anthropic 请求格式（`/api/*` 跳过）
- **middleware/logger** — 请求日志记录（`/api/*` 跳过）
- **tracer** — 每次请求的结构化追踪日志（PROVIDER / CB / QUEUE / TTFB / SUMMARY）
- **stats** — 统计收集器：per-provider 计数 + 全局 atomic 计数器 + 代理运行状态
- **sse** — SSE Hub：日志实时扇出到 Web 面板客户端

---

## 监控面板

代理启动后，浏览器访问 `http://<host>:8080/`：

- **状态开关** — 一键启用/停用代理（停用时 AI 请求返回 503，管理 API 正常）
- **汇总卡片** — 总请求 / 成功 / 失败 / 成功率
- **供应商表格** — 按 priority 显示每个供应商的请求数、成功率（颜色编码）
- **实时日志** — SSE 流式日志，自动更新，上限 500 行

---

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/v1/chat/completions` | Chat Completions（同步 + 流式） |
| `POST` | `/chat/completions` | 兼容不带 v1 的路径 |
| `POST` | `/v1/messages` | Messages API |
| `GET` | `/health` | 健康检查 `{"status":"ok"}` |
| `GET` | `/` | 监控面板页面 |
| `GET` | `/style.css` | 面板样式表 |
| `GET` | `/app.js` | 面板前端脚本 |
| `GET` | `/api/stats` | 统计快照 JSON（面板每 2s 轮询） |
| `GET` | `/api/logs` | SSE 实时日志流 |
| `POST` | `/api/control` | 启用/停用代理 `{"running":bool}` |

系统自动检测请求体格式（OpenAI / Anthropic），无需手动指定端点协议。

---

## 配置参考

### global

```yaml
global:
  listen_addr: ":8080"
  default_format: openai
  log_file: proxy.log
  cb_threshold: 2           # 连续失败次数阈值
  cb_cooldown: 30            # 熔断冷却时间（秒）
  cb_skip_requests: 10       # 熔断期间跳过请求数
```

### model_routes（可选）

模型名映射，非 Max/Medium/Flash 时生效：

```yaml
model_routes:
  - alias: gpt-4o
    target: sensenova-glm-5.2
  - alias: default
    target: sensenovalyh-deepseek-v4-flash
```

### providers

| 字段 | 说明 |
|------|------|
| `name` | 供应商唯一标识 |
| `vendor` | `openai` 或 `anthropic` |
| `model_id` | 实际调用的模型 ID |
| `api_key` | API Key |
| `base_url` | API 地址 |
| `priority` | 优先级，越小越优先 |
| `format` | `openai` 或 `anthropic` |
| `timeout` | 请求超时秒数，默认 60 |
| `retry.max_retries` | 最大重试次数，默认 3 |
| `retry.retry_interval` | 首次重试间隔秒数，默认 2 |
| `retry.backoff_factor` | 退避因子，默认 2 |

---

## 跨平台编译

```bash
make build-all
```

或手动：

```bash
# Windows
go build -o ai-proxy.exe ./cmd/proxy/

# Linux (amd64)
GOOS=linux GOARCH=amd64 go build -o ai-proxy-linux ./cmd/proxy/

# Linux (ARM64 — 树莓派)
GOOS=linux GOARCH=arm64 go build -o ai-proxy-linux-arm64 ./cmd/proxy/

# macOS Intel
GOOS=darwin GOARCH=amd64 go build -o ai-proxy-macos ./cmd/proxy/

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o ai-proxy-macos-arm64 ./cmd/proxy/
```