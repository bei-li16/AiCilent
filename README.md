# AI Proxy — AI 请求转发平台

统一管理多个 AI 供应商，提供单一入口供 OpenCode、Claude Desktop、OpenClaw 等 Agent 工具接入。支持优先级路由、故障转移、重试策略（含 429 限流重试）、流式传输、协议自动转换（含 tool_use/tool_calls）、断路器（状态持久化）、请求限流、日志自动轮转、配置热加载，以及内建监控面板。

---

## 快速开始

### 编译

```bash
# 使用 make（推荐，自动注入版本号）
make build

# 或直接 go build（版本号显示为 dev）
go build -o ai-proxy.exe ./cmd/proxy/

# 查看版本
ai-proxy.exe --version
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
| `rate_limit.enabled` | 是否启用限流，默认 false |
| `rate_limit.rpm` | 每分钟允许的请求数，默认 60 |
| `rate_limit.burst` | 最大突发请求数，默认 10 |

---

## 部署到树莓派 (Linux ARM64)

### 1. 复制文件

```bash
# 将二进制和配置传到树莓派
scp ai-proxy-linux-arm64 pi@raspberrypi:~/ai-proxy
scp config/providers.yaml pi@raspberrypi:~/config/providers.yaml
```

### 2. 安装为 systemd 服务（推荐）

在树莓派上创建 `/etc/systemd/system/ai-proxy.service`：

```ini
[Unit]
Description=AI Proxy
After=network.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi
ExecStart=/home/pi/ai-proxy --config /home/pi/config/providers.yaml
Restart=always
RestartSec=5
# 日志由内置 rotator 管理（100MB 轮转，保留 5 份）
StandardOutput=append:/home/pi/proxy.log
StandardError=inherit

[Install]
WantedBy=multi-user.target
```

启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable ai-proxy
sudo systemctl start ai-proxy
sudo systemctl status ai-proxy
```

### 3. 查看日志

```bash
# 实时日志
sudo journalctl -u ai-proxy -f

# 或直接查看日志文件
tail -f ~/proxy.log
```

### 4. 更新版本

```bash
# 上传新二进制
scp ai-proxy-linux-arm64 pi@raspberrypi:~/ai-proxy-new

# 在树莓派上替换并重启
sudo systemctl stop ai-proxy
mv ~/ai-proxy-new ~/ai-proxy
chmod +x ~/ai-proxy
sudo systemctl start ai-proxy
```

### 5. 验证

```bash
# 检查版本
~/ai-proxy --version

# 健康检查
curl http://localhost:8080/health

# 浏览器打开监控面板
# http://<树莓派IP>:8080/
```

---

## 跨平台编译

```bash
# 一键编译所有平台（版本号自动注入）
make build-all

# 或手动指定版本号
make build VERSION=v1.0.0
```

手动编译（版本号显示为 dev）：

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

如需注入版本号，手动指定 ldflags：

```bash
go build -ldflags "-X ai-proxy/internal/version.Version=v1.0.0 \
  -X ai-proxy/internal/version.Commit=$(git rev-parse --short HEAD) \
  -X ai-proxy/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o ai-proxy.exe ./cmd/proxy/
```

---

## 高级功能

### 请求限流

为每个供应商独立配置速率限制，避免 API Key 被 TPM 限流打满：

```yaml
providers:
  - name: my-provider
    rate_limit:
      enabled: true
      rpm: 60       # 每分钟 60 次请求
      burst: 10      # 最多突发 10 个请求
```

限流使用 Token Bucket 算法，请求超出限制时自动跳过该供应商，尝试同级其他供应商或降级到下一优先级。

### 配置热加载

代理启动后，修改 `providers.yaml` 无需重启。系统每 30 秒自动检测文件变更并热加载。热加载仅更新供应商列表和路由映射，**不影响**正在处理的请求和断路器状态。

### 日志轮转

日志文件默认 100MB 自动分割，保留 5 个备份。日志文件名为 `proxy.log`，轮转后自动命名为 `proxy.log.<时间戳>`，旧文件自动清理。

### 断路器状态持久化

断路器的熔断状态（失败计数、冷却时间、跳过次数）保存在 `config/.cb_state.json`。代理重启后自动恢复，避免对仍不可用的上游立即发起请求。

### 优雅关闭

代理捕获 `SIGINT`/`SIGTERM` 信号，收到后等待正在处理的请求最多 10 秒，然后关闭日志文件后退出。可通过 `Ctrl+C` 或系统管理工具触发。