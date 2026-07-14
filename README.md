# AI Proxy — AI 请求转发平台

统一管理多个 AI 供应商，提供单一入口供 OpenCode、Claude Desktop、OpenClaw 等 Agent 工具接入。支持优先级路由、故障转移、重试策略、流式传输和协议自动转换。

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

## API 端点

| 端点 | 说明 |
|------|------|
| `POST /v1/chat/completions` | Chat Completions（同步 + 流式） |
| `POST /chat/completions` | 兼容不带 v1 的路径 |
| `POST /v1/messages` | Messages API |
| `GET /health` | 健康检查 |

系统自动检测请求体格式（OpenAI / Anthropic），无需手动指定。

---

## 跨平台编译

```bash
make build-all
```

或手动：

```bash
# Windows
go build -o ai-proxy.exe ./cmd/proxy/

# Linux
GOOS=linux GOARCH=amd64 go build -o ai-proxy-linux ./cmd/proxy/

# macOS Intel
GOOS=darwin GOARCH=amd64 go build -o ai-proxy-macos ./cmd/proxy/

# macOS Apple Silicon
GOOS=darwin GOARCH=arm64 go build -o ai-proxy-macos-arm64 ./cmd/proxy/
```