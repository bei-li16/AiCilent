# AI Proxy — AI 请求转发平台

统一管理多个 AI 供应商，提供单一入口供 OpenCode、Claude Desktop、OpenClaw、Codex 等 Agent 工具接入。支持优先级路由、故障转移、指数退避重试（含 429 限流重试）、断路器保护（状态持久化）、SSE 流式传输、OpenAI Chat Completions / Responses、Anthropic Messages 协议转换（含 tool_use/tool_calls/thinking），以及内建监控面板、GUI 启动器、请求限流、日志轮转、配置热加载。

---

## 快速开始

### 配置

**GUI 版本**：双击 `ai-proxy-gui.exe` 即可，无需额外配置。首次启动会自动创建 `config/providers.yaml` 和 `proxy.log`，内置默认模板，通过"编辑配置"填写 API Key 即可。升级时自动补充新增字段，不覆盖已有供应商和 API Key。

**CLI 版本**：将 `config/providers.example.yaml` 复制为 `config/providers.yaml`，填入 API Key。完整字段说明见[配置参考](#配置参考)。

### 启动

```bash
ai-proxy.exe --config config/providers.yaml
ai-proxy.exe --version    # 查看版本信息
```

### 配置 Agent

将 Agent 工具的 `base_url` 指向 `http://localhost:8080`，`model` 字段填写路由模式或真实模型名。

#### OpenCode

```json
{
  "npm": "@ai-sdk/openai-compatible",
  "options": {
    "baseURL": "http://localhost:8080",
    "apiKey": "",
    "setCacheKey": true
  },
  "models": {
    "P1": {
      "name": "kimi-k3",
      "attachment": true,
      "modalities": {
        "input": ["text", "image"],
        "output": ["text"]
      },
      "variants": {
        "low": { "reasoning": true, "reasoningEffort": "low" },
        "high": { "reasoning": true, "reasoningEffort": "high" },
        "max": { "reasoning": true, "reasoningEffort": "max" }
      }
    },
    "P2": {
      "name": "glm-5.2",
      "attachment": false,
      "modalities": {
        "input": ["text"],
        "output": ["text"]
      },
      "variants": {
        "low": { "reasoning": true, "reasoningEffort": "low" },
        "medium": { "reasoning": true, "reasoningEffort": "medium" },
        "high": { "reasoning": true, "reasoningEffort": "high" },
        "none": { "reasoning": true, "reasoningEffort": "none" }
      }
    },
    "P3": {
      "name": "deepseek-v4-pro",
      "attachment": false,
      "modalities": {
        "input": ["text"],
        "output": ["text"]
      },
      "variants": {
        "low": { "reasoning": true, "reasoningEffort": "low" },
        "high": { "reasoning": true, "reasoningEffort": "high" },
        "max": { "reasoning": true, "reasoningEffort": "max" },
        "none": { "reasoning": true, "reasoningEffort": "none" }
      }
    },
    "P4": {
      "name": "deepseek-v4-flash",
      "attachment": false,
      "modalities": {
        "input": ["text"],
        "output": ["text"]
      },
      "variants": {
        "low": { "reasoning": true, "reasoningEffort": "low" },
        "medium": { "reasoning": true, "reasoningEffort": "medium" },
        "high": { "reasoning": true, "reasoningEffort": "high" },
        "xhigh": { "reasoning": true, "reasoningEffort": "xhigh" },
        "none": { "reasoning": true, "reasoningEffort": "none" }
      }
    }
  }
}
```

> `model` 键（P1/P2/P3/P4）即路由模式，对应代理的优先级选择。`apiKey` 留空，代理使用 YAML 中配置的供应商密钥。各模型的输入输出模态、思考等级等参数详见 [`providerinfo.md`](providerinfo.md)。

#### Claude Code

```json
{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "proxy",
    "ANTHROPIC_BASE_URL": "http://localhost:8080",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "P1",
    "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": "P1",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "P2",
    "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "P2",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "P3",
    "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "P3",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "P4",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": "P4",
    "ANTHROPIC_MODEL": "P1",
    "API_TIMEOUT_MS": "3000000",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
```

> Fable → P1（最强）、Opus → P2、Sonnet → P3、Haiku → P4。`ANTHROPIC_AUTH_TOKEN` 填任意值，代理使用 YAML 中配置的供应商密钥。

#### Codex

```toml
model_provider = "custom"
model = "P1"

[model_providers.custom]
name = "custom"
base_url = "http://localhost:8080"
```

> `model` 填路由模式（P1/P2/P3/P4/Flash/Max/Medium 等）。

#### 其他 Agent

兼容 OpenAI API 格式的工具（如 Cline、Roo Code、Continue 等）：

1. **URL**：`http://localhost:8080`
2. **模型 ID**：填路由模式或真实模型名，详见下方[模型 ID 路由](#模型-id-路由)
3. **输入输出模态**：文本/图像，详见 [`providerinfo.md`](providerinfo.md)
4. **思考等级**：`low`/`medium`/`high`/`xhigh`/`max`/`none`，各模型支持等级不同，详见 [`providerinfo.md`](providerinfo.md)

> `api_key` 填任意值，代理使用 YAML 中配置的供应商密钥。

#### 模型 ID 路由

| model 值 | 命中供应商 | 说明 |
|----------|-----------|------|
| `Max` | 仅最高优先级组（如 P1） | 用最强模型，不计成本 |
| `Flash`（推荐） | 跳过最高优先级组（P2 起） | 日常使用，避免顶级供应商 TPM 限流 |
| `Medium` | 全部优先级 | 穷尽所有可用供应商 |
| `P{n}` | 仅 P{n} 组 | 只用指定优先级的供应商 |
| `P{n}up` | P1 ~ P{n} | 从最高优先级到指定级别，含中间所有组 |
| `P{ndown}` | P{n} ~ 最低优先级 | 从指定级别向下穷尽所有更便宜的供应商 |

例如配置了 P1~P4 四组供应商，则 `P2up` 命中 P1+P2，`P2down` 命中 P2+P3+P4，`P3` 仅命中 P3。

`model_routes` 的 `target` 除供应商 `name` 外，也可填上述关键词（如 `P1`、`Flash`、`P2down`），别名命中后行为与客户端直发该关键词完全一致（硬性档位，档外不兜底）；填供应商名则保持软钉 + 全链兜底。target 同时匹配关键词和供应商名时**按关键词解释**（档位优先）；既不是供应商名也不是关键词时启动/热重载报错。

也可直接填真实模型名（如 `gpt-4o`），通过 `model_routes` 映射到指定供应商。

---

## GUI 启动器

`ai-proxy-gui.exe` 基于 [Fyne](https://fyne.io) v2 构建，提供桌面图形管理界面。

### 启动

双击 `ai-proxy-gui.exe` 即可。程序自动启动代理服务并最小化到系统托盘。关闭窗口仅隐藏到托盘，右键托盘菜单选择"退出"才会停止服务。

### 主界面

- **状态栏**：运行状态、监听地址、运行时长、供应商数量（每 2 秒刷新）
- **控制按钮**：启动 / 停止 / 重启 / 打开监控面板 / 编辑配置
- **快速设置**：监听地址、日志级别（off/snippet/full）、远程控制开关、断路器参数（阈值/冷却/探测）、流式时长上限、默认路由，保存后自动重启
- **日志窗口**：SSE 实时日志流，支持自动跟随和清空

### 配置编辑器

点击"⚙ 编辑配置"打开独立窗口，可视化管理供应商：

- **供应商列表**：显示优先级、名称、模型 ID、格式，支持编辑和删除
- **添加/编辑表单**：名称、模型 ID、API Key（编辑时留空则保留原密钥）、Base URL、格式（openai/anthropic）、优先级、超时、主机最大并发
- **保存并重启**：写入 `config/providers.yaml` 并立即重启
- **打开 YAML**：用系统文本编辑器直接编辑原始配置文件

### 系统托盘

右键菜单：启动 / 停止 / 重启服务、打开监控面板、编辑配置、显示窗口、退出

### 首次运行与配置迁移

- **首次运行**：以 exe 所在目录为根目录自动创建 `config/providers.yaml`，内嵌默认模板，弹出提示引导填写 API Key
- **升级迁移**：自动检测缺失字段并补充默认值，不覆盖已有供应商和 API Key
- **日志路径**：相对路径以 `config/providers.yaml` 所在目录为基准解析

## 监控面板

浏览器访问 `http://<host>:8080/`：

- **状态开关** — 一键启用/停用代理（停用时请求返回 503）
- **汇总卡片** — 客户端请求 / 上游尝试 / 成功 / 失败 / 成功率
- **命中率/延迟图表** — 按优先级分组的滚动命中率曲线 + 延迟曲线
- **断路器状态** — 各优先级组的熔断状态、失败次数、冷却倒计时
- **供应商统计表** — 请求数、成功率、连续失败数、平均延迟、最后错误类型，支持清零基线
- **最近上游尝试** — 请求 ID、供应商、结果、状态码、耗时，可按 ID 关联日志
- **实时日志** — SSE 流式日志，支持过滤、暂停、清空，按类型着色

---

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/v1/chat/completions` | Chat Completions（同步 + 流式） |
| `POST` | `/chat/completions` | 兼容不带 /v1 的路径 |
| `POST` | `/v1/messages` | Anthropic Messages API |
| `POST` | `/v1/responses` | OpenAI Responses API，转换到上游 Chat Completions（同步 + 流式） |
| `POST` | `/responses` | Responses API 兼容路径（部分客户端不带 `/v1`） |
| `GET` | `/health` | 健康检查 `{"status":"ok"}` |
| `GET` | `/` | 监控面板 |
| `GET` | `/api/stats` | 统计快照 JSON |
| `GET` | `/api/version` | 当前版本、commit 和编译时间 |
| `GET` | `/api/logs` | SSE 实时日志流 |
| `POST` | `/api/control` | 启用/停用代理 `{"running":bool}` |

---

## 配置参考

完整配置模板及字段注释见 [`config/providers.example.yaml`](config/providers.example.yaml)。

---

## 部署

### 本地（Windows）

**GUI 版本**：双击 `ai-proxy-gui.exe`，自动创建配置并启动服务，最小化到系统托盘。

**CLI 版本**：将 `config/providers.example.yaml` 复制为 `config/providers.yaml` 并填入 API Key，然后启动：

```bash
ai-proxy.exe --config config/providers.yaml
ai-proxy.exe --version    # 查看版本信息
```

### 树莓派（Linux ARM64）

使用 `deploy-rpi-interactive.ps1` 一键部署（需 Posh-SSH 模块，首次自动安装）：

```powershell
.\deploy-rpi-interactive.ps1
```

脚本与二进制均可从 Release 页下载，放到同一目录即可运行。脚本交互式输入树莓派用户名、IP 和密码（部署目录 `/home/<用户名>/Project/ai-proxy` 自动跟随用户名），自动完成：上传二进制和配置、清理旧状态文件（`.cb_state.json`、`.stats.json`、`proxy.log.*`）、重启服务、验证版本和健康检查。

目录布局：

```
deploy-rpi-interactive.ps1
ai-proxy-linux-arm64        # 从 Release 页下载
config/providers.yaml       # 必需：providers.example.yaml 填好 API Key 后改名
```

> 首次部署需手动配置 systemd 服务以实现开机自启和崩溃重启，详见脚本注释。局域网设备将 `base_url` 指向 `http://<树莓派IP>:8080`。如需远程控制代理启停，将 `global.control_allow_remote` 设为 `true`。

### 发布 Release

> 以下为 AI 助手打 tag 和创建 Release 的提示词参考，版本号和说明由用户确认。

```bash
# 1. 打 tag 并推送（版本号询问用户）
git tag <version> -m "<version>: 简短说明"
git push origin <version>

# 2. 用 GitHub API 创建 Release，上传资产：
#    6 个二进制 + README.md + config/providers.example.yaml
#    + RELEASENOTES.md + deploy-rpi-interactive.ps1
```

> 版本号和 Release 说明由用户确认。Release 资产包含可执行文件、README、示例配置、发布说明和部署脚本；二进制不跟踪进 git。各版本变更见 [`RELEASENOTES.md`](RELEASENOTES.md)。

---

## 高级功能

| 功能 | 说明 |
|------|------|
| **配置热加载** | 每 30s 轮询 `providers.yaml` 变更并自动重载，无需重启。不影响正在处理的请求和断路器状态 |
| **日志轮转** | 日志文件 100MB 自动分割，保留 5 个备份，旧文件自动清理 |
| **CB 状态持久化** | 断路器状态保存在 `.cb_state.json`，重启后自动恢复 |
| **统计持久化** | 供应商统计数据每 30s 自动保存到 `.stats.json`，重启后恢复累计计数 |
| **优雅关闭** | 捕获 SIGINT/SIGTERM，等待正在处理的请求最多 10s 后退出，同时停止 config watcher 并落盘统计 |
| **请求体大小限制** | 50MB 上限，防止恶意大 payload 致 OOM（支持多模态 base64 图片） |
| **HTTP Server 超时** | ReadHeaderTimeout=10s、IdleTimeout=120s，防范 Slowloris |
| **并发限制** | 按上游主机 scheme/host/port 分组，信号量控制同主机最大并发请求数，共享主机取最小正值 |
| **限流** | 每供应商独立令牌桶限流，支持热重载动态调整速率 |
