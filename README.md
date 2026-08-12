# AI Proxy — AI 请求转发平台

统一管理多个 AI 供应商，提供单一入口供 OpenCode、Claude Desktop、OpenClaw 等 Agent 工具接入。支持优先级路由、故障转移、指数退避重试（含 429 限流重试）、断路器保护（状态持久化）、SSE 流式传输、OpenAI ↔ Anthropic 协议自动转换（含 tool_use/tool_calls/thinking），以及内建监控面板、GUI 启动器、请求限流、日志轮转、配置热加载。

---

## 快速开始

### 编译

```bash
# CLI 版本（纯 Go，无 CGO 依赖）
make build-cli
# 或手动编译（版本号显示为 dev）
go build -o ai-proxy.exe ./cmd/proxy/

# GUI 版本（需要 CGO + MinGW-w64，Windows 下需安装 GCC）
make build-gui
# 或手动编译
go build -ldflags "-H=windowsgui" -tags gui -o ai-proxy-gui.exe ./cmd/launcher/
```

| 产物 | 说明 | 大小 |
|------|------|------|
| `ai-proxy.exe` | CLI 版本，命令行启动，适合服务器/无头环境 | ~13 MB |
| `ai-proxy-gui.exe` | GUI 版本，含系统托盘、配置编辑器、实时日志，适合桌面 | ~59 MB |

### 配置

GUI 版本可直接发布单个 `ai-proxy-gui.exe`。首次启动会以 exe 所在目录为根目录自动创建：

```text
config/providers.yaml
proxy.log
```

默认配置已嵌入 exe；首次启动后通过“编辑配置”填写供应商 API Key。升级启动时，GUI 会为已有 YAML 补充新版本缺失的配置字段，但不会覆盖已有供应商、API Key 或已明确设置的值。日志路径也不依赖启动时的工作目录。

CLI 版本仍需复制 `config/providers.example.yaml` 为 `config/providers.yaml`，填入 API Key：

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
    max_concurrent: 2
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
| **Flash**（推荐） | `Flash` | 跳过当前最高优先级组 | 日常使用，避免最高优先级组的 TPM 限流 |
| **Medium** | `Medium` | 全部优先级，自动降级 | 穷尽所有可用供应商 |
| **Max** | `Max` | 仅当前最高优先级组 | 需要最强模型，不计成本 |

也可直接填真实模型名（如 `gpt-4o`），通过 `model_routes` 映射到指定供应商。

---

## GUI 启动器

`ai-proxy-gui.exe` 提供完整的桌面图形管理界面，无需命令行即可控制代理服务。基于 [Fyne](https://fyne.io) v2.8 构建。

### 启动

双击 `ai-proxy-gui.exe` 即可。程序启动后：

- 自动在 exe 所在目录下创建 `config/providers.yaml`（首次运行）或迁移旧配置（升级）
- 自动启动代理服务并最小化到系统托盘
- 关闭窗口不会退出程序，仅隐藏到托盘；右键托盘菜单选择"退出"才会停止服务

### 主界面

```
┌─────────────────────────────────────────┐
│ ● 运行中   监听 :8080   已运行 2h 15m   3 个供应商 │
├─────────────────────────────────────────┤
│ [▶ 启动] [⏹ 停止] [↻ 重启] [📊 打开面板] [⚙ 编辑配置] │
├─────────────────────────────────────────┤
│ 快速设置                                  │
│  监听地址: [:8080        ]   日志级别: [snippet▼]    │
│  远程控制: [☑]              熔断阈值: [2         ]   │
│  冷却(秒): [30        ]     探测请求: [10        ]   │
│  默认路由: [sensenova-glm-5.2▼]                      │
│  [保存并重启]                                         │
├─────────────────────────────────────────┤
│ 最近日志                    [☑ 自动跟随] [清空]     │
│ ┌─────────────────────────────────────┐ │
│ │ [reqID] 200 ← sensenova-glm-5.2     │ │
│ │ [reqID] 429 ← sensenovalyh (retry)  │ │
│ │ ...                                  │ │
│ └─────────────────────────────────────┘ │
└─────────────────────────────────────────┘
```

| 区域 | 功能 |
|------|------|
| **状态栏** | 实时显示运行状态、监听地址、运行时长、供应商数量 |
| **控制按钮** | 启动 / 停止 / 重启代理服务；打开浏览器监控面板；打开配置编辑器 |
| **快速设置** | 修改监听地址、日志级别（off/snippet/full）、远程控制开关、断路器参数、默认路由目标，保存后自动重启 |
| **日志窗口** | SSE 实时日志流（最近 200 行），支持自动跟随滚动和清空 |

### 配置编辑器

点击"⚙ 编辑配置"打开独立窗口，可可视化管理供应商：

- **供应商列表**：显示每个供应商的优先级、名称、模型 ID、格式，支持编辑和删除
- **添加/编辑表单**：填写名称、模型 ID、API Key、Base URL、格式（openai/anthropic）、优先级、超时
- **保存并重启**：写入 `config/providers.yaml` 并立即重启服务
- **打开 YAML**：用系统记事本直接编辑原始 YAML 文件

### 系统托盘

GUI 版本在系统托盘显示图标，右键菜单：

- 启动 / 停止 / 重启服务
- 打开监控面板 / 编辑配置
- 显示窗口 / 退出

### 首次运行与配置迁移

- **首次运行**：以 exe 所在目录为根目录自动创建 `config/providers.yaml`，内嵌默认配置模板，弹出提示引导填写 API Key
- **升级迁移**：检测到旧版配置缺少新字段时自动补充默认值（如 `default_format`、`log_file`、`auth_type`、`rate_limit` 等），不覆盖已有供应商和 API Key
- **日志路径**：相对路径以 `config/providers.yaml` 所在目录为基准解析

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
       │   ├─ round-robin + 上游主机并发队列（同组并发）
       │   ├─ token bucket 限流检查
       │   └─ 指数退避重试（全部上游 4xx/5xx 按 YAML 重试）
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
| HTTP 4xx（全部状态码） | 可重试，按 YAML 配置的退避时间重试同一供应商 |
| 超时/取消 | 不可重试，立即切换 |
| 流式已部分写入 | 停止重试，注入 SSE 错误事件 |

退避公式：`retry_interval × backoff_factor^(N-1)`，默认 2s → 4s → 8s。

---

## 流式传输

- **自动检测**：请求体含 `"stream": true` 自动启用
- **SSE 实时下发**：收到首个完整 `data:` 事件后提交响应，此后每次写入立即 Flush
- **空闲超时**：`idleTimeoutReader` 在 `provider.Timeout` 秒无数据时返回超时
- **整体超时**：`global.max_stream_minutes` 控制流式总时长上限（默认 3 分钟），超过则中断流并注入 SSE error event
- **中途错误**：流式失败时注入 SSE error event，通知客户端截断
- **首事件前故障转移**：收到上游 200 后延迟提交下游响应头；首个完整 `data:` 事件前断开、心跳或空流可继续重试/降级
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
- **最近上游尝试** — 显示请求 ID、重试/降级结果和耗时，可与日志中的同一 ID 关联

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
| `GET` | `/api/version` | 当前版本、commit 和编译时间 |
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
  max_stream_minutes: 3       # 流式传输总时长上限（分钟），0 或留空则默认 3
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

### model_rules（可选）

按客户端模型名补充请求参数。已有字段不会被覆盖；`timeout` 是代理到上游的超时时间（秒），不会作为未知字段转发给上游：

```yaml
model_rules:
  - model: gpt-4o
    defaults:
      temperature: 0.2
      max_tokens: 4096
      timeout: 45
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
| `max_concurrent` | 否 | 0 | 同一上游 scheme/host/port 的最大并发请求数；共享主机取最小正值，0 表示不限 |
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

#### 1. 交叉编译

```bash
make build-linux-arm64
```

#### 2. 上传到树莓派

```bash
scp ai-proxy-linux-arm64 pi@raspberrypi:~/ai-proxy
scp config/providers.yaml    pi@raspberrypi:~/config/providers.yaml
ssh pi@raspberrypi 'chmod +x ~/ai-proxy'
```

#### 3. 配置 systemd 服务

> 不配置 systemd 时直接在 SSH 终端运行 `~/ai-proxy`，关闭 SSH 后进程会被 `SIGHUP` 杀掉。systemd 可实现开机自启、断连不挂、崩溃自动重启。

创建服务文件：

```bash
sudo nano /etc/systemd/system/ai-proxy.service
```

写入以下内容：

```ini
[Unit]
Description=AI Proxy — AI 请求转发平台
After=network.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi
ExecStart=/home/pi/ai-proxy --config /home/pi/config/providers.yaml
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

#### 4. 启用并启动

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ai-proxy    # 开机自启 + 立即启动
sudo systemctl status ai-proxy          # 查看运行状态
curl http://localhost:8080/health       # 验证服务
```

#### 5. 日常管理

```bash
sudo systemctl restart ai-proxy         # 重启（更新二进制后）
sudo systemctl stop ai-proxy            # 停止
sudo systemctl start ai-proxy           # 启动
journalctl -u ai-proxy -f               # 查看实时日志
```

#### 6. 客户端配置

局域网其他设备将 `base_url` 指向 `http://<树莓派IP>:8080`（如 `http://192.168.1.5:8080`）。`listen_addr: ":8080"` 默认绑定所有网卡，无需额外配置。

> 如需从局域网其他设备通过监控面板控制代理启停，需将 `global.control_allow_remote` 设为 `true`，否则 `/api/control` 仅允许本机访问。

### 跨平台编译

```bash
make build-cli          # Windows CLI（= make build）
make build-gui          # Windows GUI（需 CGO + GCC）
make build-linux-arm64  # 树莓派 / Linux ARM64
make build-linux        # Linux amd64
make build-macos-arm64  # macOS Apple Silicon
make build-all          # 全平台 CLI
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
