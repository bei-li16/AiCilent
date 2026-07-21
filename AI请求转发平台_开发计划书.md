# AI 请求转发平台 — 开发计划书

## 1. 项目概述

构建一个轻量级 AI 请求转发网关，统一管理多个 AI 供应商（当前实际使用：腾讯云 sensenova），提供单一入口供本地 Agent 工具（OpenCode、Claude Desktop、OpenClaw 等）接入。核心能力包括**优先级路由**、**故障转移**、**指数退避重试**（含 429 限流重试）、**断路器保护**（状态持久化、原子写入）、**SSE 流式传输**（3min 硬超时 + idleTimeoutReader + 中途错误事件注入）、**OpenAI ↔ Anthropic 协议自动转换**（含 tool_use/tool_calls/thinking，通用 JSON map 保留未知字段），以及**监控面板**（含命中率曲线、熔断器状态条、连续失败/错误类型/平均延迟列）、**实时日志 SSE**（含子串过滤、离线指示）、**请求内容日志分级**（`log_request_body`：off/snippet/full，full 仅落盘不进 SSE）、**统计持久化**（`.stats.json`）、**启用/停用控制**（loopback 鉴权）、**请求限流**（per-provider Token Bucket）、**日志自动轮转**、**配置热加载**、**版本号注入**。

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
│   │   ├── openai.go                # OpenAI 格式 HTTP 调用（CallOpenAIRaw，共享 transport）
│   │   ├── anthropic.go             # Anthropic 格式 HTTP 调用（CallAnthropicRaw，共享 transport）
│   │   ├── converter.go             # 协议转换含 tool_use/tool_calls/thinking（通用 JSON map 转换）
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
| HTTP 429 Too Many Requests | retryable：等待退避后重试同一供应商（速率限制通常为临时状态，TPM 配额会逐步恢复） |
| HTTP 4xx (除 429) | 非 retryable：跳过重试，立即切换下一供应商 |
| context.DeadlineExceeded | 跳过重试，立即切换下一供应商 |
| context.Canceled | 跳过重试，立即切换下一供应商 |
| 流式响应已部分写入（`c.Writer.Written()`） | 停止重试，注入 SSE 错误事件，记录组失败，返回 |

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

为减少锁竞争，将单一 `cbMu` 拆分为 5 把独立锁 + 1 把文件锁：

| 锁 | 保护的数据 | 访问频率 |
|---|-----------|---------|
| `cbMu` | 断路器状态（failureCount、circuitOpenSince、skipRemaining） | 每个请求最多 3 次 |
| `providerMu` | providerLocks 映射 | 每个 provider 尝试 2 次 |
| `rrMu` | rrIndex（round-robin 索引） | 每个组 1 次 |
| `rlMu` | rateLimiters 映射 | 每个 provider 尝试 1 次（启用限流时） |
| `reloadMu` (RWMutex) | `cfg` 指针 + `matcher` 指针 | 每个请求读取 1 次，热加载写入 1 次 |
| `fileMu` | CB 状态文件写入（防止并发写损坏） | 每次组失败/成功 1 次 |

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
    Name            string
    ModelID         string
    Priority        int
    Total           int64
    Success         int64
    Fail            int64
    Rate            float64    // Success/Total * 100
    ConsecutiveFail int64      // 当前连续失败数，成功后归零
    LastErrType     string    // 最近错误类型（ClassifyError 归类）
    LastErr         string    // 最近错误简述（截断 120 字符）
    LatencySumMs    float64   // 累计延迟(ms)
    LatencyCount    int64
    LatencyAvgMs    float64   // 平均延迟(ms)
    LastLatencyMs   float64
}

type CBState struct {        // 只读熔断器快照，供 /api/stats
    Priority, Open, Failures, CooldownSec, CooldownRemSec, SkipRemaining
}

type HitRateCurve struct {   // 命中率曲线一条
    Priority int       // 0 = 全部请求
    Points   []float64 // 每个=最近 hitWindowSize(50) 次尝试的成功率(%)
}

type Collector struct {
    mu            sync.Mutex
    providers     map[string]*ProviderStats
    totalReq      atomic.Int64   // 上游尝试次数（含重试/降级）
    totalClient   atomic.Int64   // 客户端请求数
    totalSuccess  atomic.Int64
    totalFail     atomic.Int64
    running       atomic.Bool
    overallWin    rollingHit       // 全局滚动 50 窗口
    prioWin       map[int]*rollingHit // per-priority 滚动窗口
    overallSeries []float64        // 全局曲线点序列（≤240）
    prioSeries    map[int][]float64 // per-priority 曲线序列
}
```

- `Record(name, modelID, priority, success, statusCode, errMsg, latency)` — 每次 provider 尝试后调用；分类错误、维护连续失败、累计延迟，并 push 进对应滚动窗口/曲线序列
- `RecordClientRequest()` — 每入站请求一次（engine 在确认有可用 provider 后调用）
- `Snapshot()` — 返回 providers 统计、全局汇总（含 total_client_req）、运行状态、运行时间、`CB`、`Curves`
- `ClassifyError(statusCode, msg)` — 429→tpm_limit/rpm_exhausted/plan_exhausted、404→not_found、401/403→auth_error、408/504→timeout、≥500→upstream_error、0→network_error
- `SetRunning(v)` / `IsRunning()` — atomic.Bool 控制代理开关
- `Save()` / `load()` / `StartAutoSave(stop, interval)` — 持久化到 `.stats.json`（仅累计计数；连续性/窗口/曲线为运行态，重启归零）

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
- `index.html` 面板顺序：标题栏（实时连接徽标 + 状态 + 开关 + 运行时间）→ 汇总卡片（客户端请求/上游尝试/成功/失败/成功率）→ **命中率曲线**（SVG 多折线）→ **熔断器状态条** → 供应商表格（连续失败/平均延迟/最近错误列）→ 实时日志区（过滤输入框 + 暂停/清空）
- `style.css`：深色主题，优先级色码（P1 橙/P2 蓝/P3 灰），成功率三色阈值，CB 状态条/曲线图例/徽标三态
- `app.js`：
  - `fetchStats()`：每 2s `GET /api/stats` 更新汇总卡片、供应商表格、CB 状态条、命中率曲线
  - `renderChart(curves)`：SVG 多折线，右对齐（最新点对到右边缘），Y 轴 0/50/100%
  - `toggleProxy()`：乐观更新 UI + 失败回退 + 防抖
  - `EventSource('/api/logs')`：SSE 实时日志，颜色编码（绿=OK、红=FAIL、蓝=INBOUND/CB/QUEUE/ROUTER、灰=ACCESS/SUMMARY），`onopen/onerror` 驱动实时徽标
  - 日志子串过滤（大小写不敏感，存量+新行）+ 暂停冻结 + 500 行 DOM 上限自动滚动
  - `fetchStats` 连续失败 ≥2 → 显示"统计中断"

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
| `GET` | `/api/stats` | 统计快照 JSON（每 2s 轮询）：providers + 全局汇总(含 total_client_req) + `cb`(熔断快照) + `curves`(命中率曲线) | `/api/` 路径：Logger 不记录、DetectFormat 不解析 body |
| `GET` | `/api/logs` | SSE 实时日志流（combined writer：短行；full body 不进此流） | 同上 |
| `POST` | `/api/control` | 启用/停用代理 `{"running":bool}`；`control_allow_remote=false` 时仅 loopback 可调用（403 拒绝远程） | 同上 |

系统自动检测请求体格式，无需手动指定端点协议。

---

## 11. 供应商配置

### 11.1 配置结构

完整配置参见 `config/providers.yaml`（gitignored，含真实 key）。当前包含 24 个供应商（12 openai + 12 anthropic，3 个 API Key，`sensenovalyh2-*` 一组覆盖 P1/P2/P3 两种格式）。

**global 段**：

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `listen_addr` | string | `:8080` | 监听地址 |
| `default_format` | string | `openai` | （已废弃，探测以 URL/body 为准） |
| `log_file` | string | — | 日志文件路径 |
| `log_request_body` | string | `snippet` | 请求内容日志级别：off / snippet / full（full 整段 body 仅落盘不进 SSE；热重载） |
| `cb_threshold` | int | 3 | 熔断阈值 |
| `cb_cooldown` | int | 10 | 熔断冷却秒数 |
| `cb_skip_requests` | int | 1 | 熔断恢复跳过请求数 |
| `control_allow_remote` | bool | false | `/api/control` 是否允许远程访问，默认仅 loopback（**不支持热重载**） |

**provider 段**：
|------|------|------|------|
| `name` | string | 是 | 供应商唯一标识名 |
| `model_id` | string | 是 | 实际调用的模型名 |
| `api_key` | string | 是 | API Key |
| `base_url` | string | 是 | API 地址 |
| `priority` | int | 是 | 优先级（越小越优先） |
| `format` | string | 是 | `openai` 或 `anthropic` |
| `timeout` | int | 否 | 请求超时秒数，默认 **60** |
| `retry.max_retries` | int | 否 | 最大重试次数，默认 **3**（不能为负） |
| `retry.retry_interval` | int | 否 | 首次重试等待秒数，默认 **2**（不能为负） |
| `retry.backoff_factor` | float | 否 | 退避因子，默认 **2**（不能为负） |
| `rate_limit.enabled` | bool | 否 | 是否启用 per-provider 限流，默认 **false** |
| `rate_limit.rpm` | float | 否 | 每分钟请求数，默认 **60** |
| `rate_limit.burst` | int | 否 | 最大突发请求数，默认 **10** |

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

### 12.1 请求转换（同步 — 通用 JSON 保留未知字段）

`ConvertRequest()` 使用 **通用 JSON map 操作** 而非结构化 struct 映射，以保留所有未知字段（`tools`、`tool_choice`、`response_format`、`seed` 等）：

| 方向 | 转换操作 |
|------|---------|
| OpenAI → Anthropic | 提取 `messages` 中 role=system 的消息 → 顶层 `system` 字段；重命名 `stop` → `stop_sequences`；其余字段原样保留 |
| Anthropic → OpenAI | 顶层 `system` → 首条 system message；重命名 `stop_sequences` → `stop`；其余字段原样保留 |

**关键改进**：相比旧的 struct 映射方式，map 方式不会丢弃 `tools`、`tool_choice`、`response_format` 等高级字段，确保跨格式转换时功能完整。

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

**工具调用状态追踪**：
- Anthropic→OpenAI 方向：使用 `blockTracker` 存储 **OpenAI tool_call index**（非 Anthropic block index），确保 `content_block_delta` 中的 `input_json_delta` 发送到正确的 tool_call 索引
- OpenAI→Anthropic 方向：使用 `toolCallAcc` 结构体追踪每个 tool call 的状态（index、id、name、arguments 累加器），支持多个并发工具调用

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
  ├── streamCtx = context.WithTimeout(ctx, 3min)  — 流式整体超时硬上限
  ├── http.NewRequestWithContext(streamCtx, ...)
  ├── transport.Clone() + ResponseHeaderTimeout(≤30s)
  │   — ResponseHeaderTimeout 确保首包超时
  │   — streamCtx 3min 硬上限防止无限慢速流
  │
  ├── 检查 StatusCode == 200
  ├── 设置 SSE 响应头（仅一次，通过 Written() 检查）
  ├── TTFB 日志记录
  │
  ├── defer idleReader.Close()  — 确保资源清理
  ├── 创建 idleTimeoutReader（流式读取空闲超时保护）
  ├── 格式相同？→ io.Copy 直传
  ├── 格式不同？→ StreamConvertResponse 逐行转换
  │
  └── 流式失败且已 Written？→ 注入 SSE error event 通知客户端截断
```

### 13.3 流式整体超时（maxStreamDuration）

`maxStreamDuration = 3 * time.Minute` 作为流式传输的硬上限。`idleTimeoutReader` 处理每段间隔的停顿，但如果上游持续滴漏数据（每 `timeout-ε` 秒一个字节），`idleTimeoutReader` 的 timer 会不断 reset，永不触发。`streamCtx` 作为安全网，3 分钟后强制取消上游请求。

### 13.4 idleTimeoutReader

当流式响应中途卡住时，`idleTimeoutReader` 确保在 `provider.Timeout` 秒内没有新数据时返回超时错误：

```go
type idleTimeoutReader struct {
    r         io.ReadCloser
    timeout   time.Duration
    done      chan struct{}
    timer     *time.Timer
    closeOnce sync.Once  // 安全的多次 Close
}
```

- 每次 `Read()` 启动一个 goroutine 执行实际读取（使用**内部缓冲**避免数据竞争），通过缓冲 channel 返回结果
- `Read()` 前先 `timer.Stop()` + 排空 stale channel value，再 `Reset()`（遵循 `time.Timer` 文档要求）
- goroutine 读取到内部 `buf`，仅在正常返回时 `copy(p, buf)` 到调用者缓冲，避免 timer 超时后 goroutine 仍写入 `p` 的数据竞争
- `select` 在读取结果、timer（空闲超时）和 `done`（流关闭信号）间竞争
- `Close()` 使用 `sync.Once` 确保安全多次调用不 panic，`defer idleReader.Close()` 确保资源清理

---

## 14. 日志、追踪与监控

### 14.1 日志架构

```
tracer.Log*() → fmt.Fprint(...)
                   │
       combinedW ◄──┘  (gin.DefaultWriter = io.MultiWriter(fileW, sseHub))
            │                          │
            ▼                          ▼
        proxy.log                  sse.Hub.Write()
   (rotator 轮转)                 (扇出给 Web 面板，仅短行)

full body（log_request_body=full）→ fmt.Fprint(fileWriter, ...)  → 仅 proxy.log，不进 SSE
```

- `gin.DefaultWriter` 被重设为 `io.MultiWriter(fileW, sseHub)`（server.go）：`fileW` 为 rotator（`log_file` 非空）或 stdout
- SSE Hub 实现 `io.Writer`，每收到一行日志就非阻塞扇出给所有 SSE 客户端
- `full` 模式整段请求体只写 `fileW`（仅文件），header+占位行进 combined（SSE 可见），避免大 body 冲垮控制台
- 所有 tracer 输出和 gin 日志走同一通道，确保日志顺序

### 14.2 tracer.Recorder 结构化追踪

每次请求创建一个 `Recorder` 实例，记录完整的请求路径。Recorder 仅存储最终结果字段（`resultStatus`/`resultProv`/`resultRR`），不积累 entries 切片，零额外内存分配：

| 日志标签 | 触发时机 | 内容 |
|----------|----------|------|
| `INBOUND` | 请求进入 | 方法、路径、模型、格式、请求内容（off/snippet/full 三档；full 整段 body 仅落盘不进 SSE） |
| `ACCESS` | 请求结束（middleware） | 方法、路径、状态码、耗时、格式（与 INBOUND 区分，避免双 REQUEST 同流冲突） |
| `ROUTER` | 路由决策 | 过滤模式、匹配供应商列表、降级、跳过 |
| `PROVIDER` | 每次供应商调用 | 优先级、尝试次数、供应商名、延迟、错误信息 |
| `CB` | 断路器操作 | 熔断/跳过/自动关闭/成功关闭/配置 |
| `QUEUE` | 优先级队列操作 | trying/released/skipped/busy + rr 索引 + 组大小 |
| `TTFB` | 流式首包到达 | 供应商名、首包延迟、状态码 |
| `SUMMARY` | 请求结束 | 最终结果、成功供应商、rr 轮转索引、总耗时（由 `Dump()` 输出） |

**优化**：移除了旧的 `entries []entry` 切片（每个 Log* 方法都 append，仅在 `Dump()` 中扫描一次后丢弃），改为 `LogResult()` 直接存储最终结果到字段，`Dump()` 直接读取，零切片分配。

### 14.3 日志输出示例

```
[2026-07-21 21:15:52.900] INBOUND  | POST /v1/messages | model=Max | format=anthropic | body=<full logged to file>
[2026-07-21 21:15:52.900] ROUTER   | mode=Max filter="highest priority only" matched=[sensenova-glm-5.2-anthropic[P1], ...]
[2026-07-21 21:12:14.219] PROVIDER | [P1][1/5] sensenovalyh-glm-5.2-anthropic | FAIL | 677.6642ms | API error (status 429): ...TPM limit 5000000...
[2026-07-21 21:12:21.640] PROVIDER | [P1][1/5] sensenovalyh2-glm-5.2-anthropic | FAIL | 767.3346ms | API error (status 429): ...rpm exhausted
[2026-07-21 21:13:21.856] PROVIDER | [P1][5/5] sensenova-glm-5.2-anthropic | OK | 46.5643112s
[2026-07-21 21:13:21.857] SUMMARY  | model=Max | format=anthropic | result=SUCCESS | provider=sensenova-glm-5.2-anthropic | rr=2 | total=46.7s
[2026-07-21 21:13:21.857] ACCESS   | POST /v1/messages | 200 | 46.7s | format=anthropic
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
- 顶部：实时连接徽标 + 代理运行状态 + 启用/停用开关 + 运行时间
- 汇总卡片：客户端请求 / 上游尝试 / 成功 / 失败 / 成功率
- 命中率曲线：滚动 50 次平均成功率，全部 + 各优先级多曲线（右对齐）
- 熔断器状态条：每优先级 open/正常 + 剩余冷却
- 供应商表格：尝试/成功/失败/成功率/连续失败/平均延迟/最近错误类型
- 实时日志：SSE 流式日志（子串过滤 + 暂停 + 离线指示），自动更新

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

## 20. 已修复问题记录（第四阶段 — 第一轮全量审查修复）

### 20.1 Bug — `_ = cancel` watcher 泄漏（server.go）
- **问题**：config watcher 的 `context.CancelFunc` 被丢弃（`_ = cancel`），优雅关闭时 watcher goroutine 永不退出
- **修复**：`Instance` 结构体增加 `cancelCtx` 字段，新增 `StopWatcher()` 方法，`main.go` 在优雅关闭时调用

### 20.2 Bug — nil 传入 io.MultiWriter 导致 panic（server.go）
- **问题**：`log_file` 为空时 `logWriter()` 返回 nil `*rotator.Rotator`，作为非 nil `io.Writer` 接口传入 `io.MultiWriter`，首请求触发 nil 指针 panic
- **修复**：`rot != nil` 时用 `io.MultiWriter(rot, sseHub)`，否则用 `io.MultiWriter(os.Stdout, sseHub)`

### 20.3 Bug — Anthropic 格式探测失效（detector.go）
- **问题**：`detect()` 检测 `data["messages"]` 返回 openai（Anthropic 也有 messages），`data["anthropic_version"]` 是 HTTP header 不是 body 字段，永不命中。所有请求被识别为 openai
- **修复**：按 URL 路径探测（`/v1/messages` → anthropic）+ body 字段探测（`system` 为 string 或 array、`stop_sequences`）；`GetRequestFormat` 改为安全类型断言

### 20.4 保持 — 429 作为 retryable（errors.go）
- **设计决策**：429（Too Many Requests）标记为 retryable，与 5xx 一样等待退避后重试同一供应商。TPM 限速是临时状态，配额会逐步恢复，重试可以命中恢复后的窗口
- **退避策略**：默认 `maxRetries=3`，退避 `retry_interval × backoff_factor^(N-1)` = 2s → 4s → 8s，最多 4 次尝试
- **CB 配合**：如果同一优先级组所有供应商都连续 429 导致组失败，断路器会累积失败计数，达到 threshold 后熔断整个优先级组，后续请求直接跳到更低优先级组

### 20.5 Bug — SUMMARY 日志 rr= 报起始索引非实际命中索引（engine.go）
- **问题**：`LogResult(true, provider.Name, startIdx)` 传的是组起始索引 `startIdx`，而非实际命中的 `idx`。组内第 2 个供应商成功时 SUMMARY 显示 `rr=0` 而非 `rr=1`
- **修复**：所有 `LogResult`/`LogQueue` 调用传 `idx` 而非 `startIdx`

### 20.6 Bug — 流式无整体超时（engine.go）
- **问题**：流式请求仅设 `ResponseHeaderTimeout`（≤30s）和 per-idle-gap `idleTimeoutReader`，无整体 `Timeout`。上游持续滴漏数据的流可无限运行（日志已观测 2m12s）
- **修复**：新增 `maxStreamDuration = 3min` 硬上限，用 `context.WithTimeout` 包裹流式请求

### 20.7 清理 — 死配置字段 + 死代码（types.go, loader.go, openai.go, anthropic.go, converter.go）
- **问题**：`DefaultFormat`、`HealthCheckInterval`、`Vendor` 字段定义但从未被读；`CallOpenAI`、`CallAnthropic`、`OpenAIResponseToAnthropic`、`AnthropicResponseToOpenAI`、`OpenAIResponse`、`Choice`、`Usage` 等死代码
- **修复**：移除所有死字段和死函数；清理 example yaml 中的 `vendor:` 和 `default_format:` 字段

### 20.8 优化 — 非流式不共享 transport（openai.go, anthropic.go）
- **问题**：非流式 `postOpenAI`/`postAnthropic` 每次新建 `http.Client{Timeout:...}` 不共享 transport，无连接复用
- **修复**：`CallOpenAIRaw`/`CallAnthropicRaw` 增加 `transport http.RoundTripper` 参数，引擎传入共享 `e.transport`

---

## 21. 已修复问题记录（第五阶段 — 第二轮全量审查修复）

### 21.1 [CRASH] 未检查类型断言（converter.go）
- **问题**：`tcMap["index"].(float64)` 未用 comma-ok 模式，nil 或非 float64 值会 panic 崩溃整个进程
- **修复**：改为 comma-ok 模式 + `continue`

### 21.2 [BUG] Tool Call 索引不匹配（converter.go）
- **问题**：Anthropic→OpenAI SSE 转换中，`blockTracker.index` 存储的是 Anthropic block index，而非 OpenAI tool_call index。`input_json_delta` 发送到错误的 tool_call 索引，客户端丢失工具参数
- **修复**：`blockTracker` 存储 OpenAI tool_call index（`tcIdx`），text block 存 -1

### 21.3 [BUG] ConvertRequest 丢失高级字段（converter.go）
- **问题**：`ConvertRequest` 使用 typed struct（`OpenAIRequest`/`AnthropicRequest`）仅含 7 个字段，`tools`、`tool_choice`、`response_format`、`seed` 等被静默丢弃
- **修复**：改为通用 JSON map 操作，仅转换已知字段（system 提取、stop↔stop_sequences），其余字段原样保留

### 21.4 [BUG] Anthropic array system 探测失败（detector.go）
- **问题**：Anthropic API 支持 `system` 为 string 或 array，但 `detect()` 仅检查 `.(string)`，array 形式被误判为 openai
- **修复**：`switch s.(type) { case string, []interface{}: return "anthropic" }`

### 21.5 [SECURITY] 无 http.Server 超时 — Slowloris 漏洞（main.go）
- **问题**：`http.Server{}` 未设 `ReadHeaderTimeout`/`IdleTimeout`，slowloris 攻击可耗尽文件描述符
- **修复**：设 `ReadHeaderTimeout: 10s`、`IdleTimeout: 120s`（不设 ReadTimeout/WriteTimeout 因流式需要）

### 21.6 [BUG] CB 状态文件并发写损坏（engine.go）
- **问题**：`os.WriteFile` 非原子写，多 goroutine 并发写可交错损坏 JSON 文件，重启后 `loadCBState` 静默失败丢失状态
- **修复**：新增 `fileMu` 互斥锁 + 临时文件 + `os.Rename` 原子写入

### 21.7 [BUG] idleTimeoutReader 双 Close panic（engine.go）
- **问题**：`close(r.done)` 在第二次 `Close()` 时 panic；`Close()` 非 defer，panic 时资源泄漏
- **修复**：`sync.Once` 确保安全多次 Close + `defer idleReader.Close()`

### 21.8 [BUG] Timer 未排空 stale value（engine.go）
- **问题**：`r.timer.Reset(r.timeout)` 未先排空 `r.timer.C` 中的 stale value，可能导致 spurious 超时
- **修复**：`if !r.timer.Stop() { select { case <-r.timer.C: default: } }` 再 `Reset()`

### 21.9 [BUG] idleTimeoutReader 缓冲数据竞争（engine.go）
- **问题**：timer 超时后 goroutine 仍写入调用者 `p` 缓冲，造成数据竞争
- **修复**：goroutine 读取到内部 `buf`，仅正常返回时 `copy(p, buf)`

### 21.10 [优化] tracer entries 切片浪费内存（tracer.go）
- **问题**：每个 `Log*` 方法 append 到 `entries` 切片，仅 `Dump()` 扫描一次后丢弃，每请求 20+ entries 纯浪费
- **修复**：移除 `entries` 切片和 `entry` 结构体，`LogResult` 直接存储最终结果到字段，`Dump()` 直接读取；移除 "acquired" 死分支

### 21.11 [BUG] 流式中途失败无错误事件（engine.go）
- **问题**：流式在已 `WriteHeader(200)` 后失败，客户端收到截断的 SSE 流无任何错误指示，可能 hang 或误判为完整响应
- **修复**：`forwardRequestStream` 返回错误且 `c.Writer.Written()` 为 true 时，注入 SSE error event（OpenAI/Anthropic 格式分别处理）

### 21.12 [BUG] flushWriter 写失败仍 Flush（engine.go）
- **问题**：`fw.w.Write(p)` 返回错误后仍调用 `Flush()`，浪费工作
- **修复**：`if err == nil { f.Flush() }`

### 21.13 [SECURITY] 无请求体大小限制（engine.go）
- **问题**：`io.ReadAll(c.Request.Body)` 无限制，恶意大 payload 可致 OOM
- **修复**：`http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)` 限制 10MB

### 21.14 [BUG] 配置校验缺失负值检查（loader.go）
- **问题**：`max_retries: -5` 等负值不报错，负 `retry_interval` 致 timer 立即触发
- **修复**：`validate()` 增加 `MaxRetries`/`RetryInterval`/`BackoffFactor`/`Timeout` 负值校验

---

## 22. 已修复问题记录（第六阶段 — 控制台与日志增强）

本阶段围绕"请求内容可控打印"与"控制台可观测性"做了一次集中增强，跨 10 个文件。按功能分组记录如下。

### 22.1 [FEATURE] 请求内容日志分级（`log_request_body`）
- **新增**：`GlobalConfig.LogRequestBody`（yaml `log_request_body`），三档：
  - `off` — REQUEST 行不含请求内容
  - `snippet`（默认）— `msg=` 首条消息前 80 字符（保持旧行为）
  - `full` — 打印完整请求结构（model/messages/role/stream/tools 等全部字段），pretty-printed
- **实现**：`engine.go` 读 `cfg.Global.LogRequestBody`（带 `reloadMu` RLock，兼容热重载，默认 `snippet`），经新增 `buildRequestBodyLog(body, level)`（`full` 用 `json.Indent` 美化）交给 `tracer.LogRequest`。
- **热重载**：改完无需重启，下一次 30s 轮询生效（日志已观测 ~30s 后切换）。

### 22.2 [BUG] 双 "REQUEST" 日志源同流冲突
- **问题**：`middleware/logger.go`（access 日志）与 `tracer.LogRequest`（入站日志）都用 `REQUEST` 前缀，但 schema 不同（一个带 status+latency，一个带 model+format+body），在同一 SSE 流/文件中交错，前端无法区分。
- **修复**：tracer 入站行前缀 `REQUEST` → `INBOUND`；middleware access 行前缀 `REQUEST` → `ACCESS`。`app.js` `logLineClass` 适配：`INBOUND`→info(蓝)，`ACCESS`→muted(灰)。

### 22.3 [FEATURE] `full` 模式 body 不进 SSE 流
- **问题**：`full` 模式整段 pretty JSON（含完整系统提示+历史+tools，单条可达数十 KB）经 `gin.DefaultWriter`（= MultiWriter(file, sseHub)）同时推给所有浏览器，会冲垮控制台，且 500 行 DOM 上限按节点数算、一个大 body 只算 1 节点，无法及时淘汰。
- **修复**：拆分双 writer —— `fileW`（仅文件，rotator）、`combinedW`（文件+SSE）。`gin.DefaultWriter = combinedW`（短行仍进 SSE）；`NewEngine` 增 `fileWriter` 参数，`tracer.New` 接受 `(logWriter, fileWriter)`。`LogRequest` 的 `full` 分支：header+占位行 `body=<full logged to file>` 进 combined（SSE 可见），整段 body 只写 `fileWriter`（不进 SSE）。

### 22.4 [SECURITY] `/api/control` 启停开关鉴权
- **问题**：kill switch 裸奔，任何能访问 :8080 的来源都能 POST 停掉代理。
- **修复**：`GlobalConfig.ControlAllowRemote`（yaml `control_allow_remote`，默认 `false`）。`/api/control` handler 在 `ControlAllowRemote=false` 时仅允许 loopback（`127.0.0.1/::1`，新增 `isLoopback` helper 基于 `net.SplitHostPort`+`net.ParseIP().IsLoopback()`），否则 403。设 `true` 才允许远程管理。
- **限制**：该字段是启动期安全设置，闭包捕获启动时 `cfg`，**不支持热重载**（其余 global 项仍走热重载）。

### 22.5 [FEATURE] 统计持久化（`.stats.json`）
- **问题**：CB 状态持久化（`.cb_state.json`）但 stats 全在内存，重启后累计计数丢失，不一致。
- **修复**：`stats.Collector` 增 `statsPath`，新增 `Save()`/`load()`（原子写 temp+rename，复用 `.stats.json`）。`New(statsPath)` 启动时 load；`StartAutoSave(stop, 30s)` goroutine 每 30s 存盘并在 stop 关闭时做最后一次 Save。`server.go` 计算路径 `filepath.Join(dir, ".stats.json")` 并启动 auto-save；`main.go` 优雅关闭流程加 `srv.SaveStats()`。`Instance` 暴露 `SaveStats()` + `statsStop` channel。
- **保留语义**：只持久化累计计数（total/success/fail/latency），`ConsecutiveFail`/`LastErr*`/rolling 窗口与曲线为纯运行态，重启归零（连续性无法跨重启推断）。

### 22.6 [FEATURE] 客户端请求数 vs 上游尝试数
- **问题**：`Record()` 按 provider 尝试计数，一个客户端请求重试 5 次 + 降级 2 个 provider 会计 7 次，UI 写"总请求"误导。
- **修复**：`Collector` 增 `totalClient atomic.Int64` + `RecordClientRequest()`；`engine.go` 在确认有可用 provider 后调一次（每入站请求一次）。`Snapshot` 增 `TotalClientReq`。前端摘要卡 4→5：拆出"客户端请求"+"上游尝试"。

### 22.7 [FEATURE] 连续失败数 + 最近错误类型
- **问题**：stats 表只有成功/失败数，无法一眼看出哪些 provider 100% 失败（如 `*-u1-fast-anthropic` 全 404），需手动读日志。
- **修复**：`Record` 签名扩展为 `(name, modelID, priority, success, statusCode, errMsg, latency)`。`ProviderStats` 增 `ConsecutiveFail`（成功归零）、`LastErrType`、`LastErr`（截断 120 字符）。新增 `ClassifyError(statusCode, msg)`：429→`tpm_limit`/`rpm_exhausted`/`plan_exhausted`、404→`not_found`、401/403→`auth_error`、408/504→`timeout`、≥500→`upstream_error`、0→`network_error`、其余 `http_<code>`。`engine.go` 新增 `errorDetail(fwdErr)` 从 `*adapter.APIError` 取 StatusCode+Message（非 APIError 返回 0 + err.Error()），成功传 `200,""`。前端表格加"连续失败""最近错误"两列。

### 22.8 [FEATURE] 熔断器状态可视化
- **问题**：CB 的 open/cooldown/skip 逻辑丰富但只埋在日志里，控制台看不到当前态。
- **修复**：stats 包定义 `CBState{Priority,Open,Failures,CooldownSec,CooldownRemSec,SkipRemaining}`；`Engine.CBSnapshot()` 只读（`reloadMu` RLock 取 `CBCooldown` + `cbMu` 锁内算 `cooldown_remaining`，镜像 `shouldSkipGroup` 的 cooldown 数学但不改状态），按 priority 升序排序。`/api/stats` handler 合并 `snap.CB = engine.CBSnapshot()`（单次轮询）。前端新增"熔断器状态"区，按熔断中(红)/正常(绿)渲染状态条 + 剩余冷却秒数。

### 22.9 [FEATURE] 延迟指标
- **问题**：stats 只有计数无延迟，选优先级最该看延迟却看不到。
- **修复**：`Record` 带 `latency time.Duration`（engine 在 `latency := time.Since(start)` 后传入）。`ProviderStats` 增 `LatencySumMs`/`LatencyCount`/`LatencyAvgMs`/`LastLatencyMs`。前端表格加"平均延迟"列，`fmtLatency` 自适应 ms/s。

### 22.10 [FEATURE] 命中率曲线（滚动 50 次平均）
- **需求**：在"请求数面板之后、供应商统计之前"加命中率曲线；每个点=当前请求往前 50 次命中情况的平均数，不足有多少算多少；包含每个优先级一条 + 全部请求一条。
- **实现**：
  - stats 包：`HitRateCurve{Priority int(0=全部), Points []float64}`，常量 `hitWindowSize=50`/`hitSeriesCap=240`。`rollingHit` 固定 50 槽环形缓冲 + O(1) 成功计数 → `rate()` 即"最近 50 次成功率"（分母为实际长度，不足 50 按实际算）。`pushSeries` 满 240 后左移覆写（有界数组，无泄漏）。
  - `Collector` 增 `overallWin`/`prioWin map`/`overallSeries`/`prioSeries map`。`Record()` 末尾在 `c.mu` 下 push 进对应优先级窗口 + 全局窗口，把当前 rolling-50 成功率作为一点追加进对应序列。
  - `Snapshot.Curves`：Priority 0(全部) 在前，其余按优先级升序；序列浅拷贝返回避免竞态。
  - 前端 `renderChart(curves)`：SVG 多折线，Y 轴 0/50/100% 网格，**右对齐**（各曲线最新点都对到右边缘，不同点数在时间上对齐），颜色与优先级表一致（全部=绿、P1=橙、P2=蓝、P3=灰），动态图例。接入 `fetchStats` 2s 轮询。
- **粒度说明**：点按"上游尝试"粒度（含重试/降级），因成功/失败只在 `Record` 时可知，且能反映重试导致的抖动。曲线不持久化，重启后从空开始随新请求建立。

### 22.11 [FEATURE] 日志面板过滤/搜索
- **实现**：前端日志头加过滤输入框 + 暂停/清空按钮。客户端子串过滤（大小写不敏感），对存量行 `display` 切换 + 对新到达行按当前过滤词预先 `display:none`。新增"暂停"按钮冻结追加（`paused` 标志）。

### 22.12 [FEATURE] 离线指示
- **实现**：新增 `live-badge`。SSE `onopen`→"● 实时"(绿)，`onerror`→"● 重连中"(黄)；`fetchStats` 连续失败 ≥2→"统计中断"(红)。`toggleProxy` 失败时复用徽标闪现"操作失败（已回退）"2s。

### 22.13 [FEATURE] toggle 乐观反馈
- **问题**：原 `toggleProxy` fire-and-forget，失败时开关状态错位直到下次轮询。
- **修复**：乐观更新 UI（立即反映状态），`toggleBusy` 防抖防重复触发，`.then` 校验 HTTP 状态，`.catch` 回退开关+状态+闪现提示，`.finally` 释放 busy。轮询时若 `toggleBusy` 进行中则不覆盖乐观态。

### 22.14 [REFACTOR] writer 拆分与签名变更
- `server.go`：`fileW`(仅文件) / `combinedW`(文件+SSE) 双 writer；`gin.DefaultWriter = combinedW`；`NewEngine(cfg, configPath, combinedW, fileW, statsCollector)`。
- `engine.go`：`Engine` 增 `fileWriter` 字段；`tracer.New(model, format, e.logWriter, e.fileWriter)`。
- `tracer.go`：`Recorder` 增 `fileWriter`；`New(model, format, logWriter, fileWriter)`。
- 这些是内部 wiring 的破坏性签名变更（仅 `server.go` 调用，无第三方影响）。

### 22.15 配置/数据文件变更汇总
- `config/providers.yaml`（gitignored）：`global` 段新增 `log_request_body: snippet` 与 `control_allow_remote: false`。供应商由 8 个扩展到 24 个（12 openai + 12 anthropic，3 个 API Key，新增 `sensenovalyh2-*` 一组覆盖 P1/P2/P3 两种格式）。
- 新增运行态文件 `.stats.json`（与 `.cb_state.json` 同目录，gitignored）。

---

## 23. 扩展性考虑

- **共享 TPM 池感知限流**：当前限流器以 provider name 为 key，同账号多供应商各自独立桶。可增加 `account`/`quota_group` 字段让限流跨供应商共享
- **单元测试**：当前无 `*_test.go` 文件。converter.go（复杂状态机）、retry manager、token bucket、CB 持久化、stats 持久化/rollingHit 为优先测试目标
- **`/api/*` 端点认证**：`/api/control` 已加 loopback 守卫（`control_allow_remote`），但 `/api/stats`、`/api/logs` 仍只读无认证。暴露到公网时需加 API key middleware，并让 `control_allow_remote` 支持热重载
- **命中率曲线持久化**：当前 rolling 窗口/曲线为运行态，重启归零。可持久化最近 50 次原始结果以在重启后立即恢复曲线
- **插件化供应商适配器**：新增供应商只需实现统一接口（当前已抽象 adapter 层）
- **健康检查自动下线**：定期检测供应商可用性，自动移除不可用供应商（如 `*-u1-fast-anthropic` 全 404 的死 provider）
- **Dashboard 增强**：历史图表、链路耗时分布、手动降级控制
- **热加载 CB/RR 状态清理**：热加载更换 priority 供应商后，旧 priority 的 CB 计数和 RR 索引仍残留（当前为设计意图，CB 按 priority 粒度隔离）