# AI 请求转发平台 — 开发计划书

## 1. 项目概述

构建一个轻量级 AI 请求转发网关，统一管理多个 AI 供应商（当前实际使用：腾讯云 sensenova），提供单一入口供本地 Agent 工具（OpenCode、Claude Desktop、OpenClaw 等）接入。核心能力包括**优先级路由**、**故障转移**、**指数退避重试**、**断路器保护**、**SSE 流式传输**、**OpenAI ↔ Anthropic 协议自动转换**。

### 关键设计原则

- **单文件 exe，零依赖部署**
- **同优先级内 round-robin 负载均衡**：同一优先级组内多个供应商轮流作为首选，均衡各 API Key 的请求量
- **同优先级串行排队**：每个 priority 级别有独立 Mutex，同一时刻只允许一个请求操作该优先级组（防止断路器竞争 + 减少并发压力）
- **断路器隔离**：以 priority 为粒度熔断，失败次数达到阈值后自动跳过该组，经过 cooldown 时间或 skip_requests 耗尽后自动半开
- **降级链路**：优先级组从高到低依次尝试，P1 全失败 → P2 → P3 → 503

---

## 2. 项目结构

```
ai-proxy/
├── cmd/
│   └── proxy/
│       └── main.go                  # 入口：加载配置、启动服务
├── internal/
│   ├── server/
│   │   └── server.go                # Gin 引擎组装、中间件注册、路由挂载
│   ├── config/
│   │   ├── loader.go                # 配置加载/校验/默认值/排序
│   │   └── types.go                 # 配置结构体定义
│   ├── router/
│   │   ├── engine.go                # 核心引擎：请求分发、重试、转发、断路器
│   │   └── matcher.go               # 模型名 → 供应商映射
│   ├── adapter/
│   │   ├── openai.go                # OpenAI 格式 HTTP 调用
│   │   ├── anthropic.go             # Anthropic 格式 HTTP 调用
│   │   ├── converter.go             # 协议转换：请求/响应/SSE 流双向转换
│   │   └── errors.go                # APIError + Retryable 判定
│   ├── retry/
│   │   └── manager.go               # 指数退避重试管理器（支持 context 取消）
│   ├── middleware/
│   │   ├── detector.go              # 自动检测请求格式 (OpenAI / Anthropic)
│   │   └── logger.go                # 请求日志中间件
│   └── tracer/
│       └── tracer.go                # 结构化追踪日志（每次请求完整路径记录）
├── config/
│   └── providers.yaml               # 供应商配置（8 个供应商，2 个 API Key）
├── go.mod / go.sum
├── Makefile
├── README.md
└── AI请求转发平台_开发计划书.md
```

---

## 3. 核心架构

```
                    ┌─────────────────────────────────┐
                    │        Agent 客户端              │
                    │  (OpenCode / Claude Desktop /   )│
                    │         OpenClaw / 自定义)       │
                    └──────────────┬──────────────────┘
                                   │ POST /v1/chat/completions
                                   │ POST /v1/messages
                                   ▼
┌──────────────────────────────────────────────────────┐
│                  AI Proxy (:8080)                     │
│                                                       │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────┐ │
│  │  middleware/  │  │  router/     │  │  retry/    │ │
│  │  detector.go  │  │  engine.go   │  │  manager.go│ │
│  │  logger.go    │  │  matcher.go  │  └────────────┘ │
│  └──────────────┘  └──────┬───────┘                  │
│                           │                          │
│                    ┌──────▼───────┐                   │
│                    │  adapter/    │                   │
│                    │  converter.go│                   │
│                    │  openai.go   │                   │
│                    │  anthropic.go│                   │
│                    │  errors.go   │                   │
│                    └──────┬───────┘                   │
│                           │                          │
│                    ┌──────▼───────┐                   │
│                    │  config/     │                   │
│                    │  loader.go   │                   │
│                    │  types.go    │                   │
│                    └──────────────┘                   │
└──────────────────────────────────────────────────────┘
                           │
              ┌────────────┼────────────────┐
              ▼            ▼                ▼
         ┌────────┐  ┌────────┐      ┌────────┐
         │ 腾讯云  │  │ 腾讯云  │ ...  │ 腾讯云  │
         │ sensen- │  │ sensen- │      │ sensen- │
         │ ova     │  │ ovalyh  │      │ ova     │
         └────────┘  └────────┘      └────────┘
           (P1)         (P1)           (P2/P3)
```

### 包说明

| 包路径 | 职责 |
|--------|------|
| `cmd/proxy/main.go` | 入口：加载配置、启动服务 |
| `internal/server/server.go` | Gin 引擎组装、中间件注册、路由挂载 |
| `internal/config/types.go` | 配置结构体定义 |
| `internal/config/loader.go` | 配置加载/校验/默认值/排序 |
| `internal/router/engine.go` | **核心**：请求分发、重试循环、转发逻辑、断路器、优先级队列 |
| `internal/router/matcher.go` | 模型名 → 供应商映射 |
| `internal/adapter/openai.go` | OpenAI 格式的 HTTP 调用（同步 + 流式） |
| `internal/adapter/anthropic.go` | Anthropic 格式的 HTTP 调用（同步 + 流式） |
| `internal/adapter/converter.go` | 协议转换：请求/响应/SSE 流双向转换 |
| `internal/adapter/errors.go` | APIError 类型 + Retryable 判定 |
| `internal/retry/manager.go` | 指数退避重试管理器（支持 context 取消等待） |
| `internal/middleware/detector.go` | 自动检测请求格式 (OpenAI / Anthropic) |
| `internal/middleware/logger.go` | 请求日志中间件 |
| `internal/tracer/tracer.go` | 结构化追踪日志（每次请求的完整路径记录） |

---

## 4. 请求生命周期

```
Agent 请求
   │
   ├─ 1. middleware/detector   → 自动识别 OpenAI / Anthropic 格式
   ├─ 2. middleware/logger     → 记录请求日志
   ├─ 3. router/engine         → 解析 model 字段，匹配供应商列表
   │
   ├─ 4. 按 priority 分组，逐组尝试
   │     │
   │     ├─ 4a. 检查断路器：P 组是否熔断？→ 跳过（skip_requests--）
   │     ├─ 4b. 获取优先级队列锁（acquireQueue）
   │     ├─ 4c. defer 释放队列锁 + recover 保护（panic 安全）
   │     │
│     ├─ 5. 当前组内 round-robin 轮转
    │     │     │
    │     │     ├─ 6. retry.Manager 控制重试循环
    │     │     │     │
    │     │     │     ├─ HTTP 500+ → 指数退避等待后重试同供应商
    │     │     │     ├─ HTTP 4xx  → 立即跳过（break），进入下一供应商
    │     │     │     ├─ 超时/取消  → 立即跳过（break），进入下一供应商
    │     │     │     ├─ 流式已响应  → 停止重试，记录组失败
    │     │     │     └─ 成功      → 关闭断路器，释放队列，返回
   │     │     │
   │     │     └─ 组内所有供应商失败 → 降级到下一 priority 组
   │     │
   │     └─ 释放队列锁，记录组失败（可能触发断路器打开）
   │
   └─ 所有供应商失败 → 返回 503
```

### 请求处理流程（engine.go: HandleRequest）

```
HandleRequest(c *gin.Context)
  │
  ├── 读取请求体 body
  ├── 获取 requestFormat (openai / anthropic)
  ├── 提取 modelName
  ├── 创建 tracer.Recorder
  ├── getOrderedProviders(modelName) → 按路由模式筛选供应商列表
  │
  ├── groupByPriority(providers) → 按 priority 分组
  │
  └── for each group (从高优先级到低优先级):
        │
        ├── shouldSkipGroup(priority)? → 跳过（断路器打开）
        ├── acquireQueue(priority)     → 获取组锁
        ├── defer releaseQueue + recover
        │
        ├── advanceRRIndex(priority, groupSize) → 起始供应商索引
        ├── for each provider in group (轮转):
        │     ├── 创建 retry.Manager
        │     ├── for attempt ≤ maxRetries:
        │     │     ├── forwardRequest() / forwardRequestStream()
        │     │     ├── 成功 → 关闭 CB, 释放队列, 返回
        │     │     ├── Written()? → 流式已响应, 释放队列, 记录组失败, 返回
        │     │     ├── APIError 4xx? → break (不重试)
        │     │     ├── timeout/cancel? → break (不重试)
        │     │     └── 其他错误 → rm.Wait(ctx) 指数退避后继续
        │     └── end provider loop
        │
        └── 释放队列, recordGroupFailure → 可能触发断路器打开
        │
      end group loop
      │
      └── 返回 503 + lastErr
```

---

## 5. 路由系统

### 5.1 路由模式（Max / Medium / Flash）

通过 `model` 字段的值选择不同的路由策略：

| 模式 | model 值 | 行为 | 典型场景 |
|------|----------|------|----------|
| **Max** | `Max` | **仅限 Priority 1** 供应商（最高优先级） | 需要最强模型，不计成本 |
| **Medium** | `Medium` | **所有**供应商，按 priority 从高到低 | 均衡模式，自动降级 |
| **Flash** | `Flash` | **跳过 Priority 1**，只使用 P2+ | 追求最高可用性，跳过限流层 |

**实际运行数据**：
- P1 (glm-5.2)：TPM 限流 429 为主，空闲后约 33% 成功率，TTFB 2~5s（排队等配额）
- P2 (deepseek-v4-flash)：sk-l7PU 余额耗尽 → 始终 429；sk-Nubs 稳定可用，TTFB 1.9~4.9s
- P3 (u1-fast / 6.7-flash-lite)：当前从未触发（P2 总是成功）

**推荐使用 Flash 模式**获得最高可用性。

### 5.2 供应商匹配顺序（非 Max/Medium/Flash 时）

1. `model_routes` 别名映射（如 `gpt-4o` → `sensenova-glm-5.2`）
2. 请求体 `model` 字段匹配供应商 `model_id`
3. `model_routes` 中的 `default` 映射
4. 按 `priority` 最低的剩余供应商兜底

### 5.3 组内 round-robin 轮转策略

- 同一 priority 组内多个供应商按 **round-robin** 轮流作为首选
- `rrIndex[priority]` 记录下次请求的起始供应商索引
- 每次请求开始前，`advanceRRIndex()` 原子地将索引 +1（mod groupSize）
- 首选失败时，按顺序尝试组内剩余供应商，实现**故障转移 + 负载分散**

```
示例：P1 组 [sensenova-glm-5.2 (sk-A), sensenovalyh-glm-5.2 (sk-B)]

请求1: rr start=0 → 先试 sk-A → 失败 → 再试 sk-B → 成功
请求2: rr start=1 → 先试 sk-B → 失败 → 再试 sk-A → 成功
请求3: rr start=0 → 先试 sk-A → ...
```

对比之前的 sticky 策略（始终从上次成功的供应商开始），round-robin 确保两个 API Key 均等地接收请求，避免一个 Key 过载而另一个空闲。

---

## 6. 断路器（Circuit Breaker）

### 6.1 设计

- **粒度**：按 `priority` 级别隔离，不同优先级独立熔断
- **状态**：CLOSED（正常）→ OPEN（熔断）→ HALF-OPEN（自动恢复探测）
- 实现方式：计数器 + 时间窗口 + 跳过次数

### 6.2 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `threshold` | 2 | 连续失败次数达到此值触发熔断 |
| `cooldown` | 30s | 熔断后冷却时间，到期自动半开 |
| `skip_requests` | 10 | 熔断期间跳过请求数，耗尽后自动半开 |

### 6.3 状态转换

```
CLOSED
  │  连续失败 ≥ threshold
  ├─→ OPEN
  │     │  单个请求检测
  │     ├─ cooldown 未到期 && skip_remaining > 0 → 跳过请求（skip_remaining--）
  │     ├─ cooldown 已到期 → 自动重置为 CLOSED（auto-closed: cooldown expired）
  │     └─ skip_remaining ≤ 0 → 自动重置为 CLOSED（auto-closed: skip count exhausted）
  │
  └── 成功请求 → 重置计数，保持 CLOSED
```

### 6.4 关键代码逻辑

```go
// engine.go:562-589
func (e *Engine) shouldSkipGroup(priority int) bool {
    // 1. failureCount < threshold → 不跳过
    // 2. circuitOpenSince 不存在 → 不跳过
    // 3. cooldown 已过期 → auto-closed, 重置, 不跳过
    // 4. skipRemaining ≤ 0 → auto-closed, 重置, 不跳过
    // 5. 否则 → skipRemaining--, 跳过
}
```

---

## 7. 重试与故障转移

### 7.1 重试管理器（retry/manager.go）

```go
type Manager struct {
    maxRetries    int       // 最大重试次数
    retryInterval int       // 首次重试间隔（秒）
    backoffFactor float64   // 退避因子
    attempt       int       // 当前尝试次数
}
```

### 7.2 指数退避公式

```
第 N 次尝试等待 = retry_interval × backoff_factor^(N-1)
```

默认配置（3 次重试）：2s → 4s → 8s

### 7.3 错误处理策略

| 错误类型 | 行为 |
|----------|------|
| HTTP 5xx | retryable：等待退避后重试同一供应商 |
| HTTP 4xx | 非 retryable：跳过重试，立即切换下一供应商 |
| context.DeadlineExceeded | 跳过重试，立即切换下一供应商 |
| context.Canceled | 跳过重试，立即切换下一供应商 |
| 流式响应已部分写入（`c.Writer.Written()`） | 停止重试，记录组失败，返回 |

### 7.4 重试等待可取消

`rm.Wait(ctx)` 使用 `time.NewTimer` + `select` 监听 `ctx.Done()`，支持请求取消时立即退出等待，不阻塞 goroutine。

---

## 8. 供应商配置

### 8.1 配置结构

完整配置参见 `config/providers.yaml`，当前包含 8 个供应商，2 个 API Key：

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | 是 | 供应商唯一标识名 |
| `vendor` | string | 是 | `openai` 或 `anthropic` |
| `model_id` | string | 是 | 实际调用的模型名 |
| `api_key` | string | 是 | API Key |
| `base_url` | string | 是 | API 地址 |
| `priority` | int | 是 | 优先级（越小越优先） |
| `format` | string | 是 | `openai` 或 `anthropic` |
| `timeout` | int | 否 | 请求超时秒数，默认 **60** |
| `retry.max_retries` | int | 否 | 最大重试次数，默认 **3** |
| `retry.retry_interval` | int | 否 | 首次重试等待秒数，默认 **2** |
| `retry.backoff_factor` | float | 否 | 退避因子，默认 **2** |

### 8.2 当前供应商列表

| 优先级 | 供应商名 | API Key | 模型 ID | 状态 |
|--------|----------|---------|---------|------|
| P1 | sensenova-glm-5.2 | sk-l7PU... | glm-5.2 | ⚠️ TPM 限流，33% 成功率 |
| P1 | sensenovalyh-glm-5.2 | sk-Nubs... | glm-5.2 | ⚠️ TPM 限流，同后端共用配额 |
| P2 | sensenova-deepseek-v4-flash | sk-l7PU... | deepseek-v4-flash | ❌ 余额不足 (insufficient_quota) |
| **P2** | **sensenovalyh-deepseek-v4-flash** | **sk-Nubs...** | **deepseek-v4-flash** | **✅ 稳定可用，TTFB 1.9~4.9s** |
| P3 | sensenova-u1-fast | sk-l7PU... | sensenova-u1-fast | ❌ (图片生成模型，chat 接口 404) |
| P3 | sensenova-6.7-flash-lite | sk-l7PU... | sensenova-6.7-flash-lite | ⚠️ 未验证（未被命中） |
| P3 | sensenovalyh-u1-fast | sk-Nubs... | sensenova-u1-fast | ❌ (图片生成模型，chat 接口 404) |
| P3 | sensenovalyh-6.7-flash-lite | sk-Nubs... | sensenova-6.7-flash-lite | ⚠️ 未验证（未被命中） |

**实际可用链路**：Flash 模式 → P2 (sensenovalyh-deepseek-v4-flash) TTFB 1.9~4.9s，总耗时 4~22s 稳定可用

---

## 9. 协议转换

### 9.1 请求转换（同步）

| 方向 | 函数 | 说明 |
|------|------|------|
| OpenAI → Anthropic | `OpenAIRequestToAnthropic()` | system 消息 → 顶层 `system` 字段，其余映射到 `messages` |
| Anthropic → OpenAI | `AnthropicRequestToOpenAI()` | 顶层 `system` → 首条 system 消息，其余映射到 `messages` |

### 9.2 响应转换（同步）

| 方向 | 函数 | 说明 |
|------|------|------|
| Anthropic → OpenAI | `AnthropicResponseToOpenAI()` | content[0].text → choices[0].message.content |
| OpenAI → Anthropic | `OpenAIResponseToAnthropic()` | choices[0].message.content → content[0].text |

### 9.3 字段映射

| OpenAI | Anthropic |
|--------|-----------|
| `model` | `model` |
| `messages` (含 system role) | `messages` + `system` |
| `max_tokens` | `max_tokens` |
| `temperature` | `temperature` |
| `top_p` | `top_p` |
| `stream` | `stream` |
| `stop` | `stop_sequences` |

### 9.4 FinishReason 映射

| OpenAI | Anthropic |
|--------|-----------|
| `stop` | `end_turn` |
| `length` | `max_tokens` |
| `tool_calls` | `tool_use` |
| `content_filter` | `content_filtered` |

### 9.5 SSE 流式转换（StreamConvertResponse）

逐行读取 SSE 流，根据 `fromFormat` → `toFormat` 方向实时转换：

**Anthropic SSE → OpenAI SSE**：

| Anthropic 事件 | OpenAI 输出 |
|----------------|-------------|
| `message_start` | `data: {"choices":[{"delta":{"role":"assistant"}}]}` |
| `content_block_delta` | `data: {"choices":[{"delta":{"content":"..."}}]}` |
| `message_delta` | `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + usage |
| `message_stop` | `data: [DONE]` |
| `content_block_start` / `content_block_stop` | 忽略（无对应事件） |

**OpenAI SSE → Anthropic SSE**：

| OpenAI data | Anthropic 事件 |
|-------------|----------------|
| `delta.role == "assistant"` 首次出现 | `message_start` + `content_block_start` |
| `delta.content` 非空 | `content_block_delta` |
| `finish_reason` 非空 | `content_block_stop` + `message_delta` |
| `[DONE]` | `message_stop` |

**关键实现细节**：
- `[DONE]` 处理：OpenAI → Anthropic 时，将 `[DONE]` 转换为 `event: message_stop + data: {"type":"message_stop"}`
- 使用 `bufio.Scanner` 逐行解析，buffer 大小 4096/1048576
- 支持 `event:` 前缀透传

---

## 10. 流式响应支持

### 10.1 检测

请求体包含 `"stream": true` 自动启用流式模式（`hasStream(body)` 检测）。

### 10.2 流式处理流程

```
forwardRequestStream()
  │
  ├── setModelInBody(body, provider.ModelID)
  ├── 格式不同？→ ConvertRequest() 转换
  │
  ├── 构建 URL（/chat/completions 或 /messages）
  ├── http.NewRequestWithContext(ctx, ...) — 支持 context 取消
  ├── http.Client{Timeout: 0, ResponseHeaderTimeout: provider.Timeout}
  │   — Timeout: 0 允许无限流式读取
  │   — ResponseHeaderTimeout 确保首包超时
  │
  ├── 检查 StatusCode == 200
  ├── 设置 SSE 响应头（仅一次，通过 Written() 检查）
  ├── TTFB 日志记录
  │
  ├── 创建 idleTimeoutReader（流式读取空闲超时保护）
  ├── 格式相同？→ io.Copy 直传
  └── 格式不同？→ StreamConvertResponse 逐行转换
```

### 10.3 idleTimeoutReader

当流式响应中途卡住时，`idleTimeoutReader` 确保在 `provider.Timeout` 秒内没有新数据时返回超时错误：

```go
type idleTimeoutReader struct {
    r       io.Reader
    timeout time.Duration
    timer   *time.Timer
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
    r.timer.Reset(r.timeout)    // 每次 Read 重置计时器
    n, err := r.r.Read(p)
    // 处理 timer 已过期的情况
    return n, err
}
```

---

## 11. 并发控制

### 11.1 优先级队列锁

每个 priority 级别有独立的 `sync.Mutex`，确保同一时刻只有一个请求在处理该优先级组：

```go
priorityQueues map[int]*sync.Mutex

acquireQueue(priority)  // 获取组锁（可能阻塞）
releaseQueue(priority)  // 释放组锁
```

**设计目的**：
- 防止多个请求同时操作同一优先级组的断路器计数
- 减少并发请求对后端 API 的瞬时冲击
- 请求串行化后，重试和降级行为更可预测

### 11.2 锁释放保障

- `defer` 中检查 `queueHeld` 标志，确保无论成功/失败/panic 都能释放锁
- `recover()` 捕获 panic 后释放锁再重新 panic

### 11.3 断路器互斥锁

`cbMu sync.Mutex` 保护断路器状态（failureCount, circuitOpenSince, skipRemaining），所有读写操作均加锁。

### 11.4 Round-Robin 负载均衡

每个 priority 级别有独立的 `rrIndex`，每次请求开始前原子推进：

```go
rrIndex map[int]int  // 每个 priority 的轮转索引

advanceRRIndex(priority, groupLen int) int
  → 返回当前索引，同时将 rrIndex[priority] = (idx + 1) % groupLen
```

**设计目的**：
- 同一优先级内多个供应商轮流作为首选，均衡 API Key 的请求量
- 避免一个 Key 过载（TPM 打满）而另一个空闲
- 故障转移不受影响：首选失败后仍按顺序尝试组内剩余供应商

**对比 sticky 策略**：

| 策略 | 行为 | 后果 |
|------|------|------|
| Sticky（旧） | 从上次成功的供应商开始 | 稳定供应商一直被命中，其他 Key 闲置 |
| Round-Robin（新） | 每次从下一个供应商开始 | 所有 Key 均等使用 |


---

## 12. 日志与追踪

### 12.1 日志系统

- 使用 `gin.DefaultWriter` 输出，支持控制台 + 文件双重输出（`io.MultiWriter`）
- 日志文件路径通过 `global.log_file` 配置，默认 `proxy.log`

### 12.2 tracer.Recorder 结构化追踪

每次请求创建一个 `Recorder` 实例，记录完整的请求路径：

| 日志标签 | 触发时机 | 内容 |
|----------|----------|------|
| `REQUEST` | 请求进入 | 方法、路径、模型、格式、消息摘要 |
| `ROUTER` | 路由决策 | 过滤模式、匹配供应商列表、降级、跳过 |
| `PROVIDER` | 每次供应商调用 | 优先级、尝试次数、供应商名、延迟、错误信息 |
| `CB` | 断路器操作 | 熔断/跳过/自动关闭/成功关闭 |
| `QUEUE` | 优先级队列操作 | acquire/released + rr start index |
| `TTFB` | 流式首包到达 | 供应商名、首包延迟、状态码 |
| `SUMMARY` | 请求结束 | 最终结果、成功供应商、rr 轮转索引、总耗时 |

### 12.3 日志输出示例

```
[2026-07-14 11:53:25.123] REQUEST  | POST /v1/chat/completions | model=Medium | format=openai | msg=user: 你好
[2026-07-14 11:53:25.124] ROUTER   | mode=Medium filter="all priorities" matched=[sensenova-glm-5.2[P1], ...]
[2026-07-14 11:53:25.202] PROVIDER | [P1][1/4] sensenova-glm-5.2 | FAIL | 78ms | API error (status 429): ...
[2026-07-14 11:53:25.202] ROUTER   | 4xx non-retryable, skip to next provider | sensenova-glm-5.2 [P1]
[2026-07-14 11:53:25.280] PROVIDER | [P1][1/4] sensenovalyh-glm-5.2 | FAIL | 78ms | API error (status 429): ...
[2026-07-14 11:53:25.280] QUEUE    | [P1] | released (all failed)
[2026-07-14 11:53:25.280] ROUTER   | degrade to priority group | P2
[2026-07-14 11:53:25.280] QUEUE    | [P2] | rr start=0 (group size=2)
[2026-07-14 11:53:28.417] TTFB     | sensenovalyh-deepseek-v4-flash[P2] | upstream header=2.28s | status=200
[2026-07-14 11:53:28.418] PROVIDER | [P2][1/4] sensenovalyh-deepseek-v4-flash | OK | stream | 3.137s
[2026-07-14 11:53:28.418] SUMMARY  | model=Medium | format=openai | result=SUCCESS | provider=sensenovalyh-deepseek-v4-flash | rr=1 | total=3.295s
```

---

## 13. API 端点

| 端点 | 请求格式 | 说明 |
|------|----------|------|
| `POST /v1/chat/completions` | OpenAI | Chat Completions（同步 + 流式） |
| `POST /chat/completions` | OpenAI | 同上（兼容不带 v1 的路径） |
| `POST /v1/messages` | Anthropic | Messages API |
| `GET /health` | - | 健康检查 → `{"status": "ok"}` |

系统自动检测请求体格式，无需手动指定端点协议。

---

## 14. 已修复问题记录

### 14.1 Fix 1 — Panic 死锁 (engine.go)

- **问题**：`defer` 中 `recover()` 在 `if queueHeld` 块内，如果 `queueHeld == false` 时 panic，recover 不执行
- **修复**：将 `recover()` 移到 `if queueHeld` 外部，确保 panic 时始终执行 recover 和队列释放

### 14.2 Fix 2 — 流式 WriteHeader 重复调用 (engine.go)

- **问题**：`forwardRequestStream` 中每次流式失败重试时都会重复 `WriteHeader(http.StatusOK)`
- **修复**：在设置 SSE 响应头前检查 `!c.Writer.Written()`

### 14.3 Fix 3 — 优先级队列锁 (engine.go)

- **问题**：并发请求共享同一优先级组的断路器计数，导致计数错误
- **修复**：新增 `priorityQueues` + `acquireQueue`/`releaseQueue`，每个优先级串行化

### 14.4 Fix 4 — Head-of-Line 阻塞 (retry/manager.go)

- **问题**：`time.Sleep` 在重试等待期间不可取消，即使请求已超时/取消也会阻塞
- **修复**：`time.NewTimer` + `select` 监听 `ctx.Done()`，`Wait()` 接受 `context.Context` 参数

### 14.5 Fix 5 — OpenAI→Anthropic SSE 转换未实现 (converter.go)

- **问题**：`convertOpenAISSETOAnthropic` 返回空字符串，功能未实现
- **修复**：完整实现 `message_start` → `content_block_start` → `content_block_delta` → `content_block_stop` → `message_delta` → `message_stop` 事件流

### 14.6 Fix 6 — Sticky → Round-Robin 负载均衡 (engine.go)

- **问题**：`stickyIndex` 策略让成功供应商一直被命中，同组内多个 API Key 时，一个 Key 用尽配额而另一个闲置
- **修复**：替换为 `rrIndex` + `advanceRRIndex()`，每次请求开始前原子推进索引（mod groupSize），即使首选失败也不影响下次起始位置
- **相关变更**：`setStickyIndex` 删除，`getStickyIndex` 替换为 `advanceRRIndex`，QUEUE 日志增加 `rr start=N (group size=M)`，SUMMARY 改为 `rr=N`

### 14.7 其他改进

- `http.NewRequestWithContext`：流式转发使用 context 传播，支持请求取消
- `ResponseHeaderTimeout`：流式请求设置首包超时，避免 DNS/连接阶段无限等待
- `idleTimeoutReader`：流式读取中途超时保护
- 断路器自动关闭日志：cooldown 过期 / skip 耗尽时输出日志
- 断路器模型名日志：CB 事件中记录当前模型名
- `sort.Slice` → `sort.SliceStable`：供应商排序保持原始顺序

---

## 15. 部署与使用

### 15.1 编译

```bash
# Windows
go build -o ai-proxy.exe ./cmd/proxy/

# Linux
GOOS=linux GOARCH=amd64 go build -o ai-proxy-linux ./cmd/proxy/

# macOS (Intel)
GOOS=darwin GOARCH=amd64 go build -o ai-proxy-macos ./cmd/proxy/

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o ai-proxy-macos-arm64 ./cmd/proxy/
```

### 15.2 运行

```bash
ai-proxy.exe --config config/providers.yaml
```

### 15.3 Agent 配置

将 Agent 工具的 `base_url` 指向 `http://localhost:8080`。

**推荐使用 Flash 模式**（在 Agent 的 model 字段填入 `Flash`）：

```
model: Flash
```

---

## 16. 技术栈

| 组件 | 技术方案 | 说明 |
|------|---------|------|
| 运行时 | **Go 1.23+** | 原生编译为单文件 exe，零依赖 |
| HTTP 框架 | **Gin v1.10** | 高性能，社区活跃 |
| 配置解析 | **gopkg.in/yaml.v3** | YAML/JSON 双格式支持 |
| 配置文件 | `providers.yaml` | 主配置文件 |
| 协议转换 | **自定义 Go struct 映射** | OpenAI ↔ Anthropic 格式互转 |
| 打包部署 | `go build` / `make build-all` | 单文件 exe，支持交叉编译 |

---

## 17. 扩展性考虑

- **插件化供应商适配器**：新增供应商只需实现统一接口（当前已抽象 adapter 层）
- **多租户**：通过请求头区分不同用户的 API Key（阶段二）
- **缓存层**：对 Embedding 等幂等请求做缓存（阶段二）
- **Dashboard**：简单的 Web 管理界面（阶段三）
- **健康检查自动下线**：定期检测供应商可用性，自动移除不可用供应商（阶段二）