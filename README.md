# AI Proxy — AI 请求转发平台

统一管理多个 AI 供应商，提供单一入口供 OpenCode、Claude Desktop、OpenClaw、Codex 等 Agent 工具接入。支持优先级路由、按账号负载调度、故障转移、指数退避重试（含 429 限流重试）、断路器保护（状态持久化）、SSE 流式传输、OpenAI Chat Completions / Responses 兼容和 Anthropic Messages，以及内建监控面板、GUI 启动器、可选请求限流、日志轮转和配置热加载。

Chat Completions 和 Responses 选择 `format: openai` 的上游，Responses 转换到上游 Chat Completions；Messages 选择 `format: anthropic` 的上游。正常路由不跨协议兜底，需要使用两种入口时请分别配置两种格式的供应商。

---

## 快速开始

### 配置

**GUI 版本**：双击 `ai-proxy-gui.exe` 即可，无需额外配置。首次启动会自动创建 `config/providers.yaml` 和 `proxy.log`，内置默认模板，通过"编辑配置"填写 API Key 即可。升级时自动补充新增字段，不覆盖已有供应商和 API Key。

**CLI 版本**：将 `config/providers.example.yaml` 复制为 `config/providers.yaml`，替换占位 URL 和 API Key。模板演示 P1 / `kimi-k3`、P2 / `glm-5.3`、P3 / `deepseek-flash` 的双协议配置；模型名须与实际上游一致。完整字段说明见[配置参考](#配置参考)。

配置中的相对日志路径以配置文件所在目录解析，模板的 `../proxy.log` 指向项目根目录。推理接口没有客户端 API Key 鉴权，监控读取接口也没有登录保护；仅本机使用请监听 `127.0.0.1:8080`，跨设备部署请使用可信内网或额外的访问控制，不要直接暴露公网。

### 启动

```bash
ai-proxy.exe --config config/providers.yaml
ai-proxy.exe --version    # 查看版本信息
```

**Windows 后台 CLI**：将 `start-cli.bat`、`stop-cli.bat` 和共用的 `cli.ps1` 与 `ai-proxy.exe` 放在同一目录，使用该目录下的 `config/providers.yaml`。双击 `start-cli.bat` 启动，双击 `stop-cli.bat` 停止。监控面板默认在 `http://127.0.0.1:8080/`。若已在终端启动，先在原终端按 Ctrl+C 退出，再用 BAT 启动。

### 配置 Agent

将 Agent 工具的 `base_url` 指向 `http://localhost:8080`，`model` 字段填写路由模式或真实模型名。

OpenAI 兼容客户端可用 `GET /v1/models`（或 `GET /models`）发现模型。列表取自当前配置，包含去重后的 OpenAI 上游模型 ID、适用的路由别名和可用的 `P1` / `P2` 等优先级入口。

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
      "name": "glm-5.3",
      "attachment": false,
      "modalities": {
        "input": ["text"],
        "output": ["text"]
      },
      "variants": {
        "low": { "reasoning": true, "reasoningEffort": "low" },
        "high": { "reasoning": true, "reasoningEffort": "high" },
        "max": { "reasoning": true, "reasoningEffort": "max" }
      }
    },
    "P3": {
      "name": "deepseek-flash",
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
    }
  }
}
```

> `model` 键（P1/P2/P3）即路由模式，对应代理的优先级选择。`apiKey` 留空，代理使用 YAML 中配置的供应商密钥。上述客户端配置仅作示例，模态和思考参数需与实际账号接口能力匹配；`none` 受代理策略影响，不保证关闭思考。历史模型观测见 [`providerinfo.md`](providerinfo.md)，不能视为所有协议和档位的真实上游验收。

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
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "P3",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": "P3",
    "ANTHROPIC_MODEL": "P1",
    "API_TIMEOUT_MS": "3000000",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  }
}
```

> 此示例将 Fable → P1、Opus → P2、Sonnet / Haiku → P3；仅使用 YAML 中已配置的优先级。`ANTHROPIC_AUTH_TOKEN` 填任意值，代理使用 YAML 中配置的供应商密钥。

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
| `P{n}down` | P{n} ~ 最低优先级 | 从指定级别向下穷尽所有更便宜的供应商 |

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
- **添加/编辑表单**：名称、模型 ID、API Key（编辑时留空则保留原密钥）、Base URL、格式（openai/anthropic）、优先级、超时
- **保存并重启**：写入 `config/providers.yaml` 并立即重启
- **打开 YAML**：用系统文本编辑器直接编辑原始配置文件

### 系统托盘

右键菜单：启动 / 停止 / 重启服务、打开监控面板、编辑配置、显示窗口、退出

### 首次运行与配置迁移

- **首次运行**：以 exe 所在目录为根目录自动创建 `config/providers.yaml`，内嵌默认模板，弹出提示引导填写 API Key
- **升级迁移**：自动检测缺失字段并补充默认值，不覆盖已有供应商和 API Key；移除已废弃的 `max_concurrent`。CLI 不改写配置，兼容读取带有此旧字段的 YAML，但该字段不再生效。
- **日志路径**：相对路径以 `config/providers.yaml` 所在目录为基准解析

## 监控面板

浏览器访问 `http://<host>:8080/`：

- **状态开关** — 一键启用/停用代理（停用时请求返回 503）
- **请求与上游质量** — 分别统计客户端请求、上游尝试、成功率、失败和取消。取消尝试不计入成功率分母。
- **Token 统计** — 工作区默认页按模型汇总上游实际报告的用量，提供 52 周活跃度、今日模型占比和分小时或分日趋势。趋势悬停显示总消耗、模型明细及曲线聚焦点。顶部「清空 Token 统计」可独立清空用量历史。
- **成功率和耗时趋势** — 分别多选汇总和当前优先级曲线。成功率按本曲线最近 N 次有效上游尝试滚动计算，默认 50；面板可在 20～100 之间按步长 10 调整，并在当前浏览器记住设置。取消不计入，不足 N 次按实际样本数计算。查看范围只裁剪点，不重新累计或重置窗口；汇总与各 Px 独立计算。耗时图例显示各曲线当前范围内所有保留成功样本的算术平均与样本数，不是最后一次耗时；汇总只取汇总序列，各 Px 只取自身序列，不受其他曲线显隐影响，不对各 Px 的均值再求平均。下方 P50/P95 仍为所选样本的分位数，选中汇总时不重复计入各 Px。每条曲线最多显示 240 点，成功率额外保留最多 99 条不绘制的预热结果，保证左端窗口可正确重算；最近 50/100 按客户端请求序号筛选显示点，不是滑动窗口大小。全部模式下各曲线保留的历史跨度可能不同，不代表完整历史。
- **优先级与熔断** — 按当前配置显示各优先级的熔断状态、失败次数和冷却倒计时。
- **供应商统计** — 支持按模型或优先级分组和筛选，表头可切换升降序，默认按模型 ID 升序。展示最近尝试时间、最近连续失败次数、有效成功率和平均耗时。
- **请求追踪** — 按请求 ID 汇总最近的上游尝试，可查看供应商、模型、状态码和耗时，并跳转到相关日志。
- **原始日志** — 按 Px 徽标、模型、供应商排列，已识别模型的行持续显示对应 Px 底色；P1–P3 固定配色，新增 Px 自动生成稳定颜色。区分实际限流、切换目标和跳过对象，支持按模型、优先级、结果及级别筛选，点击行展开原文。

完成事件分别显示绿色「上游成功」（一次账号尝试成功）、蓝色「路由成功」（本次路由选得成功结果）、青色「流式完成」（协议结束标记已完成）。它们是同一请求的不同阶段，可能只相差毫秒，不是三次独立成功。`TTFB` 是收到上游响应头，`FIRST_RESPONSE` 是首次向客户端写出非空的 HTTP 2xx 正文，可能是元数据事件而非首个文本 token；请求 ID 用于关联各阶段。

桌面宽屏同时显示趋势和排障工作区，供应商表格和日志在各自区域内滚动。窄屏自动使用纵向布局。

---

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/v1/chat/completions` | Chat Completions（同步 + 流式） |
| `POST` | `/chat/completions` | 兼容不带 /v1 的路径 |
| `POST` | `/v1/messages` | Anthropic Messages API |
| `POST` | `/v1/responses` | OpenAI Responses API，转换到上游 Chat Completions（同步 + 流式） |
| `POST` | `/responses` | Responses API 兼容路径（部分客户端不带 `/v1`） |
| `GET` | `/v1/models`、`/models` | 当前 OpenAI 上游模型、路由别名及优先级入口 |
| `GET` | `/health` | 健康检查 `{"status":"ok"}` |
| `GET` | `/` | 监控面板 |
| `GET` | `/api/stats` | 统计快照 JSON |
| `GET` | `/api/usage` | Token 用量快照，含近一年日汇总和今日小时明细 |
| `GET` | `/api/version` | 当前版本、commit 和编译时间 |
| `GET` | `/api/logs` | SSE 实时日志流 |
| `POST` | `/api/control` | 启用/停用代理 `{"running":bool}` |
| `POST` | `/api/stats/reset` | 清零请求和供应商统计，受 `control_allow_remote` 保护 |
| `POST` | `/api/logs/clear` | 清空当前日志文件和实时日志缓冲，受 `control_allow_remote` 保护 |
| `POST` | `/api/usage/reset` | 清空 Token 统计，受 `control_allow_remote` 保护 |

网页「清空当前视图」只影响当前浏览器。管理操作「清空日志」同时清空当前日志文件和实时缓冲，已打开的面板重连后会清掉旧记录。面板检测到新构建后会自动加载更新的页面资源。日志轮转备份和累计统计不能替代逐条请求历史。

---

## 配置参考

完整配置模板及字段注释见 [`config/providers.example.yaml`](config/providers.example.yaml)。

### 思考参数与请求头

OpenAI 与 Anthropic 共用 `global.reasoning_effort_on_none` 策略。OpenAI 的显式 `reasoning_effort: none`，以及 Anthropic 的 `thinking.type: disabled`、`output_config.effort: none` 均按此策略处理；Anthropic 请求中误用的 `reasoning_effort: none` 也会转换为原生字段并移除该 OpenAI 字段。完全未指定思考参数时不触发策略。

例如配置 `reasoning_effort_on_none: max`：OpenAI 写入 `reasoning_effort: max`；Anthropic 写入 `output_config.effort: max`，原来的关闭状态改为 `thinking.type: adaptive`，已有启用的手动预算则保留。留空时，OpenAI 省略 `none` 字段并保留已有 `reasoning`；Anthropic 省略 `none` 档位和显式关闭控制，交由上游默认决定，而不是保证关闭思考。其他明确档位仍遵守原有处理逻辑。可选值为 `minimal`、`low`、`medium`、`high`、`xhigh`、`max`，请填写所用上游支持的值，尤其不要假定 Anthropic 接口支持 `minimal`。原始日志可用「思考等级调整」筛选实际发生的映射和移除。

Responses 的 `reasoning.effort` 转换后执行相同 OpenAI 策略。SenseNova OpenAI 上游存在显式档位时补充 `reasoning: true`；其中 `glm-5.3` 将 `minimal` / `medium` 映射为 `low`，将 `xhigh` 等其他未支持档位映射为 `max`，`low` / `high` / `max` 保留。完全不指定档位时不会因此强制启用；Anthropic 原生档位不套用此 OpenAI 专用映射。

代理按所选 provider 重建上游请求头，不转发客户端密钥。OpenAI 使用 `Authorization: Bearer <api_key>`；Anthropic 按 `auth_type` 选择 Bearer 或 `x-api-key`，两者在流式和非流式请求中均发送 `anthropic-version: 2023-06-01`。两种格式均设置 JSON Content-Type 和请求 ID。`anthropic-beta` 等任意客户端扩展头不会自动透传，因此不能据此承诺所有 Claude 客户端扩展能力兼容。

### 超时边界

以下数值以模板的 `timeout: 60`、`max_stream_minutes: 15` 为例；每次重试或切换账号重新计时。

| 阶段 | 当前时限及含义 |
|------|----------------|
| 客户端请求头 | `ReadHeaderTimeout = 10s` |
| 客户端上传正文 | 无独立读取时限；正文大小限制为 50 MiB |
| 非流式上游请求 | 单次 `timeout = 60s`，含连接、响应头和正文读取，不包含所有重试及下游写入 |
| 流式上游响应头 | `ResponseHeaderTimeout = 60s`，从上游请求正文写完后开始，不包含连接及上传 |
| 2xx 流式正文的第一次、后续读取 | 等待上游原始正文数据的空闲时限为 60s；包括心跳在内的任意字节会重置，下游背压不计入 |
| 流式非 2xx 错误正文 | 最多读取 64 KiB，受单次流式总时限约束，不单独套用上述读空闲计时器 |
| 首个有效 SSE / 首个文本 token | 没有独立时限，不能把上面的读空闲时限当作首 token 时限 |
| 单次流式上游尝试 | 15 分钟，含连接、响应头及读流；取消上游 context。省略或 `<=0` 时默认 3 分钟 |
| 跨重试、跨账号的整个客户端请求 | 没有统一总时限；客户端取消后停止后续尝试 |
| 向客户端写响应 | 未设置独立写入时限；取消上游不保证解除阻塞的下游写入 |
| HTTP 保活连接空闲 | `IdleTimeout = 120s`，不是一次推理的执行时限 |

模型规则按客户端原始 `model` 精确匹配，只补充缺失字段；`defaults.timeout` 是代理使用的特殊默认值，不注入上游 JSON，生效后替代 provider 的 `timeout`，不改变上述计时范围，也不改变 `max_stream_minutes`。客户端原有字段不被规则覆盖。

### 重试、限流与兼容

- `max_retries` 是首次尝试之外的重试次数；`3` 表示最多 4 次真实尝试。省略或设为 `0` 仍默认 3，负数校验失败，不能用 `0` 关闭重试。`retry_interval` / `backoff_factor` 省略或为 `0` 时分别默认 2 秒 / 2。
- 同一账号先按 retry 配置重试；第 n 次重试的开始间隔目标为 `retry_interval * backoff_factor^(n-1)`，失败响应耗时已计入，只等待剩余时间。429 重试耗尽后才按最新负载转其他账号；已向客户端写出正文后不会把另一个账号的响应拼接进原流。
- `rate_limit_cooldown` 省略或 `<=0` 关闭；正数仅为重试耗尽仍返回 429 的 provider name 设置跨请求冷却，不代表上游真实配额重置时间，不自动共用到同账号其他模型。
- 可选 `rate_limit.enabled` 默认关闭；开启后使用按 provider name 独立的 RPM / burst 令牌桶。选择候选时令牌不足则跳过，不排队，也不逐次限制该候选内部重试。RPM 是请求速率，不是并发容量；调度状态共享和本地限流桶共享是两回事。
- 断路器按优先级组隔离，并非每账号/每协议独立；组失败中包含 408、5xx 或非 HTTP 错误（如网络、协议错误）才累积，纯 429 或客户端取消不触发该惩罚。熔断后冷却时间到达或跳过计数耗尽即可放行半开探测，同组同时最多一个探测，成功关闭熔断。
- 现有 YAML 不要求新增字段。废弃 `max_concurrent` 不再限制容量；新调度没有全局/单账号并发上限，也没有等待队列，详见[账号调度](#账号调度)。

---

## 部署

### 本地（Windows）

**GUI 版本**：双击 `ai-proxy-gui.exe`，自动创建配置并启动服务，最小化到系统托盘。

**CLI 版本**：将 `config/providers.example.yaml` 复制为 `config/providers.yaml`，替换占位 URL 和 API Key，然后启动：

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

**注意：此交互脚本会覆盖服务器配置并删除上述状态/轮转日志，不适合需要保留历史的常规升级。** 常规升级应先备份二进制、生产 YAML、统计及日志，停止服务后仅替换已核对 SHA-256 的 `ai-proxy`，保留 `config/providers.yaml`、`.stats.json`、`.stats.json.archive/`、`.cb_state.json` 和全部日志，再启动服务。不要把示例配置覆盖到生产。启动后同时核对 `systemctl status ai-proxy`、`/health`、`/api/version` 和运行中二进制摘要，确保服务实际启动的是新文件。

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
| **统计持久化** | 请求、供应商和 Token 统计每 30s 自动保存到 `.stats.json`，重启后恢复。Token 历史保留每日汇总，小时明细只保留当天数据 |
| **优雅关闭** | 捕获 SIGINT/SIGTERM，等待正在处理的请求最多 10s 后退出，同时停止 config watcher 并落盘统计 |
| **请求体大小限制** | 50 MiB 上限（含多模态 base64），不等于整个进程内存上限 |
| **HTTP Server 超时** | ReadHeaderTimeout=10s、IdleTimeout=120s；没有完整请求正文/写入总时限，详见超时边界 |
| **账号调度** | OpenAI（含 Responses）与 Anthropic 独立池；候选组内优先选择当前并发最少的账号，持平时选择最久未尝试的账号；不再设置主机并发上限 |
| **限流** | 每供应商独立令牌桶限流，支持热重载动态调整速率 |

### 账号调度

账号状态按「上游协议池 + scheme/host/port + API Key」识别；同池内同一 Key 的多个模型共享并发数和最近尝试时间，OpenAI 与 Anthropic 则完全独立。账号选择与并发预占原子完成，未尝试过的账号在并发持平时优先；每次真实尝试更新开始时间，成功、失败、取消和流式结束都会归还计数，重试退避期间不占用并发。状态保存在内存中，重启后重新开始；热加载保留仍在使用的账号状态。

每个新请求和转出后的选择都按最新负载重新评估，不因上一次成功而粘住账号。仍遵守现有 Px/别名路由、协议筛选及每账号 retry 配置；429 重试耗尽后转出，且本次组内不再尝试同账号的其他别名配置。所有账号都有在途请求时仍选并发最小者，不设置容量上限或等待队列。旧配置中的 `max_concurrent` 不再生效，GUI 迁移或保存配置时会移除该字段。

请求 ID 只标识一次 HTTP 请求及其内部尝试，不标识聊天会话。当前没有 session-to-account 映射或会话粘连；同一会话的后续请求也独立选账号。Responses 兼容转换会移除 `conversation` 和 `previous_response_id`，不会替客户端恢复上游保存的会话状态，客户端需提供所需的上下文。

### 运行时边界与性能验证

- 请求正文在首次读取前限制为 50 MiB，非流式上游响应也限制为 50 MiB；错误正文只保留前 64 KiB。请求 JSON 的定点修改保留未修改字段及大整数，不重建整个消息对象树。
- 不可信上游 SSE 单事件仍限制为 1 MiB。Responses 兼容层最多累积 4 MiB 文本与工具参数、128 个工具调用；其自行生成的下游事件使用独立的 64 MiB 有限额度，避免长输出的正常结束事件被 1 MiB 上游额度误拒绝。达到累积上限会报告错误，不伪造成功。
- 流式空闲检测复用一个计时器，不再为每次读取创建 goroutine、channel 和复制缓冲区；下游背压时间不算上游读取空闲。保留单次尝试的超时与重试时序；没有新增整个客户端请求总时限。
- 日志历史使用环形缓存，慢订阅者的分发缺失通过 SSE `gap` 事件计数；网页和 GUI 批量刷新。统计缓存按数据变化失效，冷却倒计时与运行时长仍实时更新。
- 无变化的 CB/统计状态不重复写盘。关闭时先结束日志订阅、等待请求收尾，再等待统计保存完成；超时强制关闭连接也不会跳过最终保存。
- 热重载每 30 秒比较内容摘要，失败保留上次有效配置并继续重试，支持 mtime 不变的文件替换。废弃传输缓存及时清理，移除的供应商统计存入 `.stats.json.archive/`，重新启用时恢复；全局计数不被删除，清零后不恢复旧归档。

隔离压测工具：`go build -o perfprobe ./cmd/perfprobe`。在 Linux 上运行 `./perfprobe -binary /absolute/path/ai-proxy -work /absolute/path/disposable-result-dir`，会启动仅回环可访问的模拟上游及代理子进程，使用虚拟 Key 和独立配置/统计，输出 `report.json`，包含 CPU 时间、RSS/峰值、写入量、首响及完成延迟。不会读取生产 YAML 或发起外部模型推理；对比时使用相同文件系统、请求量和并发度，勿把不同负载的现场 RSS 直接作为优化幅度。

压测使用模拟上游，不能代替真实模型的延迟、配额和思考能力验收。单请求/缓存有界也不等于进程 RSS 有固定上限；无限并发仍可放大总内存。CPU、分配量和写盘减少不保证 RSS 同比例下降，应分别记录空闲 RSS、负载峰值、CPU 时间、错误率及延迟分位数。

### 测试与验证范围

```bash
go test ./...
go test -race ./internal/...
go vet ./...
node --test internal/server/webtest/dashboard-core.test.cjs
# GUI 需 CGO 和 C 编译器，Windows 可使用 MinGW-w64
go test -tags gui ./cmd/launcher
# 只运行回环协议/模型/思考档位矩阵及 none 策略测试
go test ./internal/router -run 'TestProtocolModelThinkingWireMatrix|TestAnthropicNonePolicy' -count=1
```

回环矩阵覆盖三个模型标识、流式/非流式、Chat / Responses / Messages，以及 Anthropic Bearer / x-api-key 认证和各测试档位；验证的是代理请求头、字段转换和流完整性，不是上游接受全部组合。没有完成真实上游全部模型、思考档位、15 个账号的穷举验收。只看 HTTP 200、首次写出或模型名字符串通过测试，均不足以证明模型实际执行了指定思考等级。

历史模型观测见 [`providerinfo.md`](providerinfo.md)，版本变更与发布状态见 [`RELEASENOTES.md`](RELEASENOTES.md)。旧开发计划和历史修复记录不作为当前调度/超时的接口约定；当前行为以本文、配置模板及对应回归测试为准。
