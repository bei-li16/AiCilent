# AI 请求转发平台 — 开发计划书

## 1. 项目概述

构建一个轻量级 AI 请求转发网关，统一管理多个 AI 供应商（当前实际使用：腾讯云 sensenova），提供单一入口供本地 Agent 工具（OpenCode、Claude Desktop、OpenClaw 等）接入。核心能力包括**优先级路由**、**故障转移**、**指数退避重试**（含 429 限流重试）、**断路器保护**（状态持久化）、**SSE 流式传输**、**OpenAI ↔ Anthropic 协议自动转换**（含 tool_use/tool_calls/thinking），以及**监控面板**、**实时日志 SSE**、**启用/停用控制**、**请求限流**（per-provider Token Bucket）、**日志自动轮转**、**配置热加载**、**版本号注入**。

### 关键设计原则

- **单文件 exe，零依赖部署**（含 web 面板通过 `go:embed` 嵌入）
- **Per-provider TryLock 并发**：每个供应商一把锁，`TryLock()` 非阻塞，同优先级不同供应商真正并发
- **同优先级内 round-robin 负载均衡**：同一优先级组内多个供应商轮流作为首选，均衡各 API Key 的请求量
- **断路器隔离**：以 priority 为粒度熔断，失败次数达到阈值后自动跳过该组，经过 cooldown 时间或 skip_requests 耗尽后自动半开；**状态持久化到文件**，重启后恢复
- **降级链路**：优先级组从高到低依次尝试，P1 全失败 → P2 → P3 → 503
- **协议转换**：OpenAI ↔ Anthropic 双向转换，支持 tool_use/tool_calls/thinking 等 content block
- **配置热加载**：30s 轮询配置文件，自动重载 provider 列表无需重启
- **请求限流**：per-provider Token Bucket，可配置 RPM + burst
- **日志轮转**：100MB 自动分割，保留 5 个备份
- **版本号注入**：通过 ldflags 注入 Version/Commit/BuildDate，支持 `--version`
- **优雅关闭**：SIGINT/SIGTERM → 10s 超时优雅关闭 HTTP 服务

---

## 2. 项目结构

```
ai-proxy/
├── cmd/
│   └── proxy/
│       └── main.go                  # 入口：加载配置、启动服务 + 优雅关闭
├── internal/
│   ├── version/
│   │   └── version.go               # 版本号注入（ldflags: Version/Commit/BuildDate）
│   ├── server/
│   │   ├── server.go                # Gin 引擎组装、中间件注册、路由挂载（返回 *Instance）
│   │   └── web/                     # 嵌入的监控面板前端
│   │       ├── index.html           # Dashboard 页面
│   │       ├── style.css            # 样式
│   │       └── app.js               # 前端逻辑（轮询 stats + SSE 日志流）
│   ├── config/
│   │   ├── loader.go                # 配置加载/校验/默认值/排序
│   │   └── types.go                 # 配置结构体定义（支持 rate_limit）
│   ├── router/
│   │   ├── engine.go                # 核心引擎：请求分发、重试、转发、断路器、并发控制
│   │   └── matcher.go               # 模型名 → 供应商映射
│   ├── adapter/
│   │   ├── openai.go                # OpenAI 格式 HTTP 调用 + ToolCalls 类型
│   │   ├── anthropic.go             # Anthropic 格式 HTTP 调用
│   │   ├── converter.go             # 协议转换含 tool_use/tool_calls/thinking
│   │   └── errors.go                # APIError + Retryable 判定（含 429）
│   ├── retry/
│   │   └── manager.go               # 指数退避重试管理器（支持 context 取消）
│   ├── ratelimit/
│   │   └── tokenbucket.go           # Token Bucket 限流器（per-provider）
│   ├── rotator/
│   │   └── rotator.go               # 日志轮转（100MB 自动分割，保留 5 个备份）
│   ├── middleware/
│   │   ├── detector.go              # 自动检测请求格式 (OpenAI / Anthropic)
│   │   └── logger.go                # 请求日志中间件
│   ├── sse/
│   │   └── hub.go                   # SSE Hub：日志扇出到多个 Web 客户端
│   ├── stats/
│   │   └── collector.go             # 统计收集器（per-provider + 全局计数）
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
┌──────────────────────────────────────────────────────────────────┐
│                     AI Proxy (:8080)                              │
│                                                                   │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────┐             │
│  │  middleware/  │  │  router/     │  │  retry/    │             │
│  │  detector.go  │  │  engine.go   │  │  manager.go│             │
│  │  logger.go    │  │  matcher.go  │  └────────────┘             │
│  └──────────────┘  └──────┬───────┘                              │
│                           │                                       │
│                    ┌──────▼───────┐                               │
│                    │  adapter/    │                               │
│                    │  converter.go│                               │
│                    │  openai.go   │                               │
│                    │  anthropic.go│                               │
│                    │  errors.go   │                               │
│                    └──────┬───────┘                               │
│                           │                                       │
│                    ┌──────▼───────┐                               │
│                    │  config/     │                               │
│                    │  loader.go   │                               │
│                    │  types.go    │                               │
│                    └──────────────┘                               │
│                                                                   │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────┐     │
│  │  stats/      │  │  sse/        │  │  server/web/       │     │
│  │  collector.go│  │  hub.go      │  │  (Dashboard UI)    │     │
│  └──────┬───────┘  └──────┬───────┘  └─────────┬──────────┘     │
│         │                 │                     │                 │
│         └─────────────────┼─────────────────────┘                 │
│                           │                                       │
│              ┌────────────▼────────────┐                          │
│              │  gin.DefaultWriter      │                          │
│              │  io.MultiWriter(        │                          │
│              │    logFile/os.Stdout,   │                          │
│              │    sseHub              │                          │
│              │  )                     │                          │
│              └─────────────────────────┘                          │
└──────────────────────────────────────────────────────────────────┘
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
| `internal/server/server.go` | Gin 引擎组装、中间件注册、路由挂载；SSE Hub/Stats 初始化；日志 MultiWriter 设置 |
| `internal/server/web/` | 嵌入的监控面板前端（index.html、style.css、app.js），通过 `//go:embed` 编译进二进制 |
| `internal/config/types.go` | 配置结构体定义 |
| `internal/config/loader.go` | 配置加载/校验/默认值/排序 |
| `internal/router/engine.go` | **核心**：请求分发、重试循环、转发逻辑、断路器、优先级队列、round-robin、per-provider TryLock |
| `internal/router/matcher.go` | 模型名 → 供应商映射 |
| `internal/adapter/openai.go` | OpenAI 格式的 HTTP 调用（同步 + 流式） |
| `internal/adapter/anthropic.go` | Anthropic 格式的 HTTP 调用（同步 + 流式） |
| `internal/adapter/converter.go` | 协议转换：请求/响应/SSE 流双向转换 |
| `internal/adapter/errors.go` | APIError 类型 + Retryable 判定 |
| `internal/retry/manager.go` | 指数退避重试管理器（支持 context 取消等待） |
| `internal/middleware/detector.go` | 自动检测请求格式 (OpenAI / Anthropic)；`/api/*` 路径跳过 |
| `internal/middleware/logger.go` | 请求日志中间件；`/api/*` 路径跳过 |
| `internal/sse/hub.go` | SSE Hub：实现 `io.Writer`，日志扇出到 Web 面板（非阻塞 chan fan-out） |
| `internal/stats/collector.go` | 统计收集器：per-provider 计数、全局原子计数器、运行状态 atomic.Bool |
| `internal/tracer/tracer.go` | 结构化追踪日志（每次请求的完整路径记录） |

---

## 4. 请求生命周期

```
Agent 请求
   │
   ├─ 1. middleware/logger     → 记录请求日志（/api/* 跳过）
   ├─ 2. middleware/detector   → 自动识别 OpenAI / Anthropic 格式（/api/* 跳过）
   ├─ 3. router/engine         → 检查 IsRunning() → 503 如果已停用
   │                             解析 model 字段，匹配供应商列表
   │
   ├─ 4. 按 priority 分组，逐组尝试
   │     │
   │     ├─ 4a. 检查断路器：P 组是否熔断？→ 跳过（skip_requests--）
   │     ├─ 4b. 获取优先级队列锁（acquireQueue）
   │     ├─ 4c. defer 释放队列锁 + recover 保护（panic 安全）
   │     │
   │     ├─ 5. 当前组内 round-robin 轮转
   │     │     │
   │     │     ├─ 5a. tryAcquireProvider (TryLock) — 忙则跳过
   │     │     │
   │     │     ├─ 6. retry.Manager 控制重试循环
   │     │     │     │
   │     │     │     ├─ HTTP 500+ → 指数退避等待后重试同供应商
   │     │     │     ├─ HTTP 4xx  → 立即跳过（break），进入下一供应商
   │     │     │     ├─ 超时/取消  → 立即跳过（break），进入下一供应商
   │     │     │     ├─ 流式已响应  → 停止重试，记录组失败
   │     │     │     └─ 成功      → Record(stats) + 关闭断路器 + 释放队列 + 返回
   │     │     │
   │     │     └─ 组内所有供应商失败 → 降级到下一 priority 组
   │     │
   │     └─ 释放队列锁，recordGroupFailure（可能触发断路器打开）
   │
   ├─ 所有供应商失败 → 返回 503
   │
   └─ 每次 provider 尝试后（无论成败）→ stats.Record()
```

### 请求处理流程（engine.go: HandleRequest）

```
HandleRequest(c *gin.Context)
  │
  ├── IsRunning()? → false → 503 "proxy is disabled"
  │
  ├── 读取请求体 body
  ├── 获取 requestFormat (openai / anthropic)
  ├── 提取 modelName
  ├── 检测流式标记 hasStream(body)
  ├── 创建 tracer.Recorder
  ├── getOrderedProviders(modelName) → 按路由模式筛选供应商列表
  │
  ├── groupByPriority(providers) → 按 priority 分组
  │
  └── for each group (从高优先级到低优先级):
        │
        ├── shouldSkipGroup(priority)? → 跳过（断路器打开）
        ├── advanceRRIndex(priority, groupSize) → 轮转起始索引
        │
        ├── for each provider in group (轮转):
        │     ├── tryAcquireProvider(name) → TryLock → 忙则跳过
        │     ├── 创建 retry.Manager
        │     ├── for attempt ≤ maxRetries:
        │     │     ├── forwardRequest() / forwardRequestStream()
        │     │     ├── 成功 → stats.Record(success=true), closeCB, 释放, 返回
        │     │     ├── stats.Record(success=false)
        │     │     ├── Written()? → 流式已响应, 释放, recordGroupFailure, 返回
        │     │     ├── APIError 4xx? → break (不重试)
        │     │     ├── timeout/cancel? → break (不重试)
        │     │     └── 其他错误 → rm.Wait(ctx) 指数退避后继续
        │     └── end provider loop
        │
        ├── 未尝试任何供应商? ("all providers busy") → 继续下一组
        ├── 全失败? → recordGroupFailure → 可能触发断路器打开
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
  └── 成功请求 → 关闭断路器，保持 CLOSED
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
| HTTP 429 Too Many Requests | retryable：等待退避后重试同一供应商（速率限制通常为临时状态） |
| HTTP 4xx (除 429) | 非 retryable：跳过重试，立即切换下一供应商 |
| context.DeadlineExceeded | 跳过重试，立即切换下一供应商 |
| context.Canceled | 跳过重试，立即切换下一供应商 |
| 流式响应已部分写入（`c.Writer.Written()`） | 停止重试，记录组失败，返回 |

### 7.4 重试等待可取消

`rm.Wait(ctx)` 使用 `time.NewTimer` + `select` 监听 `ctx.Done()`，支持请求取消时立即退出等待，不阻塞 goroutine。

---

## 8. 并发控制

### 8.1 优先级队列锁

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

### 8.2 锁释放保障

- `defer` 中检查 `queueHeld` 标志，确保无论成功/失败/panic 都能释放锁
- `recover()` 捕获 panic 后释放锁再重新 panic

### 8.3 Per-Provider TryLock

```go
// engine.go:344-362
func (e *Engine) tryAcquireProvider(name string) bool {
    e.cbMu.Lock()
    mu, ok := e.providerLocks[name]
    if !ok {
        mu = &sync.Mutex{}
        e.providerLocks[name] = mu
    }
    e.cbMu.Unlock()
    return mu.TryLock()  // 非阻塞；false 表示该供应商正在被其他请求使用
}
```

- `providerLocks` 以供应商名为 key，每个供应商一把 `sync.Mutex`
- `TryLock()` 非阻塞：供应商忙时跳过，不排队
- 一个组内所有供应商都忙时 → 日志 "all providers busy"，继续下一组
- 防止同一供应商被多个请求同时打满 TPM

### 8.4 Round-Robin 负载均衡

```go
rrIndex map[int]int  // 每个 priority 的轮转索引

advanceRRIndex(priority, groupLen int) int
  → 返回当前索引，同时将 rrIndex[priority] = (idx + 1) % groupLen
```

### 8.5 锁拆分架构

为减少锁竞争，将单一 `cbMu` 拆分为 4 把独立锁：

| 锁 | 保护的数据 | 访问频率 |
|---|-----------|---------|
| `cbMu` | 断路器状态（failureCount、circuitOpenSince、skipRemaining） | 每个请求最多 3 次 |
| `providerMu` | providerLocks 映射 | 每个 provider 尝试 2 次 |
| `rrMu` | rrIndex（round-robin 索引） | 每个组 1 次 |
| `rlMu` | rateLimiters 映射 | 每个 provider 尝试 1 次（启用限流时） |
| `reloadMu` (RWMutex) | `cfg` 指针 + `matcher` 指针 | 每个请求读取 1 次，热加载写入 1 次 |

**设计原则**：
- 读多写少的数据用 RWMutex（reloadMu）
- 高频低冲突的操作用独立 Mutex（rrMu、providerMu）
- 写操作（断路器等状态变更）用专用锁（cbMu）

### 8.6 共享 HTTP Transport

为优化连接复用，引擎持有共享 `http.Transport`：

```go
transport: &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 10,
    IdleConnTimeout:     90 * time.Second,
}
```

流式请求通过 `transport.Clone()` 获取传输副本并设置 `ResponseHeaderTimeout`，继承底层连接池。

### 8.7 原子操作

| 原子变量 | 用途 |
|----------|------|
| `stats.totalReq` (atomic.Int64) | 全局请求计数器（无锁） |
| `stats.totalSuccess` (atomic.Int64) | 全局成功计数器 |
| `stats.totalFail` (atomic.Int64) | 全局失败计数器 |
| `stats.running` (atomic.Bool) | 代理启用/停用标志，每次请求前读取 |

---

## 9. 监控面板（Dashboard）

### 9.1 架构

```
┌──────────┐    GET /api/stats (每2s)    ┌─────────────┐
│  Web UI  │ ←───────────────────────── │ stats.Collector│
│ (浏览器)  │                              └─────────────┘
│          │    GET /api/logs (SSE)      ┌─────────────┐
│          │ ←───────────────────────── │ sse.Hub      │
└──────────┘                              └─────────────┘
                                                ▲
                                                │ io.Writer
                                          ┌─────┴──────┐
                                          │ tracer     │
                                          │ gin日志    │
                                          └────────────┘
```

### 9.2 统计收集器（internal/stats/collector.go）

```go
type ProviderStats struct {
    Name     string
    ModelID  string
    Priority int
    Total    int64
    Success  int64
    Fail     int64
    Rate     float64    // Success/Total * 100
}

type Collector struct {
    mu           sync.Mutex
    providers    map[string]*ProviderStats
    totalReq     atomic.Int64
    totalSuccess atomic.Int64
    totalFail    atomic.Int64
    running      atomic.Bool     // 代理启用/停用
}
```

- `Record(name, modelID, priority, success)` — 每次 provider 尝试后调用（engine.go:176,187）
- `Snapshot()` — 返回包含所有 provider 统计、全局汇总、运行状态、运行时间的快照
- `SetRunning(v)` / `IsRunning()` — atomic.Bool 控制代理开关

### 9.3 SSE Hub（internal/sse/hub.go）

```go
type Hub struct {
    mu      sync.RWMutex
    clients map[chan string]bool
}

func (h *Hub) Write(p []byte) (int, error)  // 实现 io.Writer，扇出给所有客户端
func (h *Hub) ServeHTTP(w, r)               // SSE 端点，每客户端一个 goroutine
```

- 日志链路：`gin.DefaultWriter = io.MultiWriter(logW, sseHub)`（server.go:28）
- 所有 tracer 输出和 gin 日志均通过此 MultiWriter 写入 → 日志文件 + SSE 实时流
- 每个客户端一个带缓冲（cap=128）的 channel
- 非阻塞发送：`select { case ch <- msg: default: }`，慢客户端不阻塞其他客户端

### 9.4 前端（internal/server/web/）

- 通过 `//go:embed web/*` 编译进二进制，无需外部静态文件
- `index.html`：布局分为标题栏（状态 + 开关）、汇总卡片、供应商表格、实时日志区
- `style.css`：深色主题，简洁 UI
- `app.js`：
  - `fetchStats()`：每 2s `GET /api/stats` 更新汇总卡片和供应商表格
  - `toggleProxy()`：`POST /api/control` 切换代理开关
  - `EventSource('/api/logs')`：SSE 实时日志，颜色编码（绿色=OK、红色=FAIL、蓝色=CB/QUEUE、灰色=SUMMARY）
  - 日志上限 500 行，自动滚动

---

## 10. API 端点

| 方法 | 路径 | 说明 | 中间件处理 |
|------|------|------|-----------|
| `GET` | `/health` | 健康检查 → `{"status":"ok"}` | Logger / DetectFormat 正常执行 |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions（同步 + 流式） | 格式检测 + 日志 |
| `POST` | `/chat/completions` | 同上（兼容不带 v1 的路径） | 同上 |
| `POST` | `/v1/messages` | Anthropic Messages API | 同上 |
| `GET` | `/` | Dashboard 页面（嵌入 HTML） | Logger / DetectFormat 均执行 |
| `GET` | `/style.css` | Dashboard 样式表 | 同上 |
| `GET` | `/app.js` | Dashboard JavaScript | 同上 |
| `GET` | `/api/stats` | 统计快照 JSON（每 2s 轮询） | `/api/` 路径：Logger 不记录、DetectFormat 不解析 body |
| `GET` | `/api/logs` | SSE 实时日志流 | 同上 |
| `POST` | `/api/control` | 启用/停用代理 `{"running":bool}` | 同上 |

系统自动检测请求体格式，无需手动指定端点协议。

---

## 11. 供应商配置

### 11.1 配置结构

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

### 11.2 当前供应商列表

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

## 12. 协议转换

### 12.1 请求转换（同步）

| 方向 | 函数 | 说明 |
|------|------|------|
| OpenAI → Anthropic | `OpenAIRequestToAnthropic()` | system 消息 → 顶层 `system` 字段，其余映射到 `messages` |
| Anthropic → OpenAI | `AnthropicRequestToOpenAI()` | 顶层 `system` → 首条 system 消息，其余映射到 `messages` |

### 12.2 响应转换（同步 + 通用 JSON）

同步响应使用 **通用 JSON 操作** 转换而非结构化 struct 映射，以支持 tool_use/tool_calls：

**Anthropic → OpenAI**：
- `content` 数组中的 text block → `choices[0].message.content`
- `content` 数组中的 tool_use block → `choices[0].message.tool_calls[]`
  - `id`、`name`、`input` → `{id, type:"function", function:{name, arguments}}`
- `stop_reason: "tool_use"` → `finish_reason: "tool_calls"`

**OpenAI → Anthropic**：
- `choices[0].message.tool_calls[]` → content 数组中的 tool_use block
  - `{id, type:"function", function:{name, arguments}}` → `{type:"tool_use", id, name, input}`
- `finish_reason: "tool_calls"` → `stop_reason: "tool_use"`

### 12.3 字段映射

| OpenAI | Anthropic |
|--------|-----------|
| `model` | `model` |
| `messages` (含 system role) | `messages` + `system` |
| `max_tokens` | `max_tokens` |
| `temperature` | `temperature` |
| `top_p` | `top_p` |
| `stream` | `stream` |
| `stop` | `stop_sequences` |
| `tool_calls` | tool_use content block |

### 12.4 FinishReason 映射

| OpenAI | Anthropic |
|--------|-----------|
| `stop` | `end_turn` |
| `length` | `max_tokens` |
| `tool_calls` | `tool_use` |
| `content_filter` | `content_filtered` |

### 12.5 SSE 流式转换（StreamConvertResponse）

逐行读取 SSE 流，根据 `fromFormat` → `toFormat` 方向实时转换。

**Anthropic SSE → OpenAI SSE**（含 tool_use + thinking）：

| Anthropic 事件 | 处理方式 | OpenAI 输出 |
|----------------|---------|-------------|
| `message_start` | 检测 assistant role | `data: {"choices":[{"delta":{"role":"assistant"}}]}` |
| `content_block_start` (type=text) | 记录 block，等待 delta | — |
| `content_block_start` (type=tool_use) | 提取 id/name，分配 tool call index | `data: {"choices":[{"delta":{"tool_calls":[{index,id,type,function:{name}}]}}]}` |
| `content_block_start` (type=thinking) | **跳过**（Claude 非标字段） | — |
| `content_block_delta` (text_delta) | 转发文本内容 | `data: {"choices":[{"delta":{"content":"..."}}]}` |
| `content_block_delta` (input_json_delta) | 转发工具调用参数 | `data: {"choices":[{"delta":{"tool_calls":[{index,function:{arguments}}]}}]}` |
| `content_block_delta` (thinking_delta) | **跳过** | — |
| `content_block_stop` | 无对应事件 | — |
| `message_delta` | 映射 stop_reason + usage | `data: {"choices":[{"delta":{},"finish_reason":"..."}]}` + usage |
| `message_stop` | 流结束 | `data: [DONE]` |

**OpenAI SSE → Anthropic SSE**（含 tool_calls）：

| OpenAI data | 处理方式 | Anthropic 事件 |
|-------------|---------|----------------|
| `delta.role == "assistant"` 首次出现 | 初始化 message | `message_start` |
| `delta.content` 首次非空 | 启动 text block | `content_block_start` (type=text) |
| `delta.content` 后续 | 追加文本 | `content_block_delta` (text_delta) |
| `delta.tool_calls[]` 首次出现 id/name | 启动 tool_use block | `content_block_start` (type=tool_use, id, name) |
| `delta.tool_calls[].arguments` 非空 | 追加工具调用参数 | `content_block_delta` (input_json_delta, partial_json) |
| `finish_reason` 非空 | 关闭所有 active block | `content_block_stop` + `message_delta` |
| `[DONE]` | 流结束 | `message_stop` |

**工具调用状态追踪**：使用 `toolCallAcc` 结构体在流转换期间追踪每个 tool call 的状态（index、id、name、arguments 累加器），支持多个并发工具调用。

---

## 13. 流式响应支持

### 13.1 检测

请求体包含 `"stream": true` 自动启用流式模式（`hasStream(body)` 检测）。

### 13.2 流式处理流程

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

### 13.3 idleTimeoutReader

当流式响应中途卡住时，`idleTimeoutReader` 确保在 `provider.Timeout` 秒内没有新数据时返回超时错误：

```go
type idleTimeoutReader struct {
    r       io.ReadCloser
    timeout time.Duration
    done    chan struct{}
    timer   *time.Timer
}

func newIdleTimeoutReader(r io.ReadCloser, timeout time.Duration) *idleTimeoutReader
```

- 每次 `Read()` 启动一个 goroutine 执行实际读取，通过缓冲 channel 返回结果
- `select` 在读取结果、timer（空闲超时）和 `done`（流关闭信号）间竞争
- `Close()` 关闭 `done` channel + 停止 timer + 关闭底层 reader，goroutine 通过 `select` 检测到 `done` 后退出，**不泄漏**
- timer 在每次读取成功后 reset，确保超时窗口从最后一个字节到达时重新计算

---

## 14. 日志、追踪与监控

### 14.1 日志架构

```
tracer.Log*() → fmt.Fprint(e.logWriter, ...)
                    │
                    ▼
              gin.DefaultWriter
                    │
                    ▼
          io.MultiWriter(logW, sseHub)
              │               │
              ▼               ▼
        os.Stdout +     sse.Hub.Write()
        proxy.log        (扇出给 Web 面板)
```

- `gin.DefaultWriter` 被重设为 `io.MultiWriter(logW, sseHub)`（server.go:28）
- `logWriter()`：`log_file` 为空 → 仅 stdout；有值 → stdout + 文件
- SSE Hub 实现 `io.Writer`，每收到一行日志就非阻塞扇出给所有 SSE 客户端
- 所有 tracer 输出和 gin 日志走同一通道，确保日志顺序

### 14.2 tracer.Recorder 结构化追踪

每次请求创建一个 `Recorder` 实例，记录完整的请求路径：

| 日志标签 | 触发时机 | 内容 |
|----------|----------|------|
| `REQUEST` | 请求进入 | 方法、路径、模型、格式、消息摘要 |
| `ROUTER` | 路由决策 | 过滤模式、匹配供应商列表、降级、跳过 |
| `PROVIDER` | 每次供应商调用 | 优先级、尝试次数、供应商名、延迟、错误信息 |
| `CB` | 断路器操作 | 熔断/跳过/自动关闭/成功关闭/配置 |
| `QUEUE` | 优先级队列操作 | acquire/released/skipped/busy + rr start index |
| `TTFB` | 流式首包到达 | 供应商名、首包延迟、状态码 |
| `SUMMARY` | 请求结束 | 最终结果、成功供应商、rr 轮转索引、总耗时 |

### 14.3 日志输出示例

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

### 14.4 中间件日志静默

- `middleware/logger.go:18-20`：对 `/api/` 前缀路径跳过日志输出
- `middleware/detector.go:17-19`：对 `/api/` 前缀路径跳过 body 读取和格式检测
- 目的：避免监控面板轮询（`GET /api/stats` 每 2s）产生的日志噪音，避免对内部 API 路径执行无意义的 body 解析

---

## 15. 部署与使用

### 15.1 编译

```bash
# Windows
go build -o ai-proxy.exe ./cmd/proxy/

# Linux (amd64)
GOOS=linux GOARCH=amd64 go build -o ai-proxy-linux ./cmd/proxy/

# Linux (ARM64 — 树莓派)
GOOS=linux GOARCH=arm64 go build -o ai-proxy-linux-arm64 ./cmd/proxy/

# macOS (Intel)
GOOS=darwin GOARCH=amd64 go build -o ai-proxy-macos ./cmd/proxy/

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o ai-proxy-macos-arm64 ./cmd/proxy/
```

### 15.2 运行

```bash
ai-proxy.exe --config config/providers.yaml
```

默认监听 `:8080`（可通过 `global.listen_addr` 配置）。日志输出到 stdout 和 `proxy.log`（如果配置）。

### 15.3 监控面板

启动后在浏览器访问 `http://<host>:8080/`：
- 顶部：代理运行状态 + 启用/停用开关 + 运行时间
- 汇总卡片：总请求 / 成功 / 失败 / 成功率
- 供应商表格：每个供应商的请求数、成功/失败、成功率（颜色编码）
- 实时日志：SSE 流式日志，自动更新

### 15.4 Agent 配置

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
| 协议转换 | **自定义通用 JSON 转换** | OpenAI ↔ Anthropic 双向，含 tool_use/tool_calls/thinking |
| 嵌入前端 | **//go:embed** | HTML/CSS/JS 编译进二进制 |
| 实时日志 | **SSE (Server-Sent Events)** | 浏览器 EventSource 消费 |
| 统计收集 | **自定义 Collector** | atomic 计数器 + mutex 保护 map |
| 限流 | **Token Bucket** | per-provider 独立桶，可配置 RPM + burst |
| 日志轮转 | **自定义 Rotator** | 100MB 自动分割，保留 N 个备份 |
| 构建注入 | **ldflags** | Version/Commit/BuildDate 编译时注入 |
| 打包部署 | `go build` / `make build-all` | 单文件 exe，支持交叉编译 |
| 目标部署 | **树莓派 (ARM64 Linux)** | 局域网内统一入口 |

---

## 17. 已修复问题记录

### 17.1 Fix 1 — Panic 死锁 (engine.go)

- **问题**：`defer` 中 `recover()` 在 `if queueHeld` 块内，如果 `queueHeld == false` 时 panic，recover 不执行
- **修复**：将 `recover()` 移到 `if queueHeld` 外部，确保 panic 时始终执行 recover 和队列释放

### 17.2 Fix 2 — 流式 WriteHeader 重复调用 (engine.go)

- **问题**：`forwardRequestStream` 中每次流式失败重试时都会重复 `WriteHeader(http.StatusOK)`
- **修复**：在设置 SSE 响应头前检查 `!c.Writer.Written()`

### 17.3 Fix 3 — 优先级队列锁 (engine.go)

- **问题**：并发请求共享同一优先级组的断路器计数，导致计数错误
- **修复**：新增 `priorityQueues` + `acquireQueue`/`releaseQueue`，每个优先级串行化

### 17.4 Fix 4 — Head-of-Line 阻塞 (retry/manager.go)

- **问题**：`time.Sleep` 在重试等待期间不可取消，即使请求已超时/取消也会阻塞
- **修复**：`time.NewTimer` + `select` 监听 `ctx.Done()`，`Wait()` 接受 `context.Context` 参数

### 17.5 Fix 5 — OpenAI→Anthropic SSE 转换未实现 (converter.go)

- **问题**：`convertOpenAISSETOAnthropic` 返回空字符串，功能未实现
- **修复**：完整实现 `message_start` → `content_block_start` → `content_block_delta` → `content_block_stop` → `message_delta` → `message_stop` 事件流

### 17.6 Fix 6 — Sticky → Round-Robin 负载均衡 (engine.go)

- **问题**：`stickyIndex` 策略让成功供应商一直被命中，同组内多个 API Key 时，一个 Key 用尽配额而另一个闲置
- **修复**：替换为 `rrIndex` + `advanceRRIndex()`，每次请求开始前原子推进索引（mod groupSize），即使首选失败也不影响下次起始位置

### 17.7 Fix 7 — Per-Provider TryLock (engine.go)

- **问题**：组内多个供应商串行尝试，阻塞等待上一个供应商完成，导致高延迟
- **修复**：`tryAcquireProvider` 使用 `TryLock()` 非阻塞获取，供应商忙则跳过，进入下一个

### 17.8 Fix 8 — 监控面板日志噪音 (middleware/*.go)

- **问题**：`GET /api/stats` 每 2s 轮询在日志中产生大量噪音记录
- **修复**：`Logger` 和 `DetectFormat` 中间件对 `/api/` 前缀路径直接 `c.Next()` 返回，跳过日志和 body 解析

### 17.9 其他改进

- `http.NewRequestWithContext`：流式转发使用 context 传播，支持请求取消
- `ResponseHeaderTimeout`：流式请求设置首包超时，避免 DNS/连接阶段无限等待
- `idleTimeoutReader`：流式读取中途超时保护
- 断路器自动关闭日志：cooldown 过期 / skip 耗尽时输出日志
- 断路器模型名日志：CB 事件中记录当前模型名
- `sort.Slice` → `sort.SliceStable`：供应商排序保持原始顺序
- `gin.DefaultWriter` 接 MultiWriter：日志同时输出到文件 + SSE Hub
- 嵌入 `web/` 前端：单文件部署，无外部静态依赖

---

---

## 18. 已修复问题记录（第二阶段）

### 18.1 Fix 1 — 版本号缺失

- **问题**：项目无版本号机制，无法通过命令行查看版本，运维定位困难
- **修复**：新增 `internal/version` 包，`main.go` 添加 `--version` 标志，Makefile 通过 ldflags 注入 Version/Commit/BuildDate

### 18.2 Fix 2 — 429 状态码不重试

- **问题**：`Retryable()` 仅判定 HTTP ≥500 可重试，429 被归为 4xx 不重试，导致限流时直接跳过
- **修复**：将 HTTP 429 加入 retryable 判定（`errors.go`）

### 18.3 Fix 3 — idleTimeoutReader goroutine 泄漏

- **问题**：每次 `Read()` 启动 goroutine，`Close()` 时未通知导致泄漏
- **修复**：新增 `done` channel，`Close()` 关闭 `done` + 停止 timer + 关闭底层 reader，goroutine 通过 `select` 检测后退出

### 18.4 Fix 4 — 日志文件无限增长

- **问题**：`logWriter` 只追加不轮转，生产环境会写满磁盘
- **修复**：新增 `internal/rotator` 包实现基于文件大小的自动轮转（默认 100MB，保留 5 个备份）

### 18.5 Fix 5 — 无请求限流

- **问题**：缺乏对上游 API 的速率保护，可能被 Agent 端突发请求打满 TPM 触发 429
- **修复**：新增 `internal/ratelimit` 包实现 per-provider Token Bucket，`config/types.go` 增加 `RateLimitConfig`（enabled/rpm/burst），引擎在转发前检查限流

### 18.6 Fix 6 — 协议转换不支持 tool_use/tool_calls/thinking

- **问题**：converter.go 只处理 text 类型，工具调用和思考块在转换中被丢弃
- **修复**：`AnthropicContent` 增加 ID/Name/Input/Thinking 字段；`ConvertResponse` 改用通用 JSON 转换；SSE 流转换添加 `content_block_start`(tool_use) + `input_json_delta` + `thinking_delta` 处理；`OpenAIMessage` 增加 ToolCalls 字段

### 18.7 Fix 7 — 无配置热加载

- **问题**：修改 providers.yaml 必须重启进程
- **修复**：`Engine.StartWatcher()` 轮询配置文件修改时间（30s 间隔），`reloadConfig()` 原子替换 cfg/matcher/providerLocks/rateLimiters，无需重启

### 18.8 Fix 8 — 断路器状态不持久化

- **问题**：重启后断路器状态丢失，可能对仍不可用的上游立即重试
- **修复**：`saveCBState()` 在 CB 状态每次变更时写入 `.cb_state.json`，`NewEngine` 中 `loadCBState()` 恢复之前状态

---

## 19. 已修复问题记录（第三阶段 — 全量代码审查）

### 19.1 Bug 1 — loadCBState 调用时机

- **问题**：`NewEngine` 中调用 `loadCBState()` 时 `configPath` 还未设置
- **修复**：`NewEngine` 接受 `configPath` 参数，在构造后再调用 `loadCBState()`

### 19.2 Bug 2 — reloadMu data race

- **问题**：`reloadConfig` 获取 `reloadMu.Lock()` 写入 cfg/matcher，但 `HandleRequest` 等读取方未获取 `RLock()`
- **修复**：`HandleRequest` 入口在 `reloadMu.RLock()` 下捕获 `cfg` 指针 + providers 列表；`shouldSkipGroup`/`recordGroupFailure`/`closeCircuitBreaker` 接受配置参数，不再直接读 `e.cfg`

### 19.3 Bug 3 — ResponseHeaderTimeout 硬编码 30s

- **问题**：流式请求的 `ResponseHeaderTimeout` 固定 30s，忽略 provider timeout
- **修复**：使用 `provider.Timeout` 动态设置，上限 30s

### 19.4 Bug 4 — 流式 http.Client 无连接池

- **问题**：每次 `forwardRequestStream` 创建全新 `http.Client`/`Transport`，无连接复用
- **修复**：引擎持有共享 `http.Transport`（MaxIdleConns=100），流式请求 `transport.Clone()` 继承连接池

### 19.5 优化 1 — cbMu 锁拆分

- **问题**：`cbMu` 同时保护断路器状态 + providerLocks + rateLimiters + rrIndex，高竞争
- **修复**：拆分为 4 把独立锁（cbMu/providerMu/rrMu/rlMu） + cfg/matcher 用 reloadMu RWMutex

### 19.6 优化 2 — StartWatcher goroutine 泄漏

- **问题**：StartWatcher goroutine 无停止机制，服务器关闭后继续运行
- **修复**：接受 `context.Context`，select 监听 `ctx.Done()` 退出

### 19.7 优化 3 — saveCBState 竞态窗口

- **问题**：释放 cbMu 后才写文件，期间 CB 状态可能被修改
- **修复**：`captureCBState()` 在锁内捕获完整快照，锁外异步写文件

### 19.8 清理 — AnthropicResponseToOpenAI 死代码

- **问题**：toolCalls 被正确提取但用 `_ = toolCalls` 丢弃了
- **修复**：补全注入逻辑，消息结构体增加 `OpenAIMessage.ToolCalls` 字段

### 19.9 清理 — msg_ ID 生成

- **问题**：`len(dataStr)%100000` 生成的 ID 不唯一
- **修复**：使用递增计数器 `msg_proxy_N`

### 19.10 清理 — extractBodySnippet 不支持多模态

- **问题**：`first["content"].(string)` 类型断言对数组 content 失败
- **修复**：处理 content 为数组的情况（text/image/tool_use/tool_result）

### 19.11 清理 — Rotator 优雅关闭

- **问题**：Rotator 实例从未关闭，文件句柄泄漏
- **修复**：`server.New` 返回 `*Instance` 结构体暴露 `Rot` 字段，`main.go` 在优雅关闭时调用 `Close()`

### 19.12 清理 — 优雅关闭流程

- **问题**：缺乏 SIGINT/SIGTERM 处理，直接关闭可能丢失数据
- **修复**：`main.go` 使用 `signal.Notify` 捕获退出信号，10s 超时 `http.Server.Shutdown` + 关闭 Rotator

---

## 20. 扩展性考虑

- **插件化供应商适配器**：新增供应商只需实现统一接口（当前已抽象 adapter 层）
- **多租户**：通过请求头区分不同用户的 API Key（阶段二）
- **缓存层**：对 Embedding 等幂等请求做缓存（阶段二）
- **健康检查自动下线**：定期检测供应商可用性，自动移除不可用供应商（阶段二）
- **Dashboard 增强**：历史图表、链路耗时分布、手动降级控制（阶段三）