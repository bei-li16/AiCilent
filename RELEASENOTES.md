# Release Notes

各版本发布信息汇总（据 git 提交历史整理），最新版本在前。

## v1.0.20（2026-09-06）

**model_routes 路由增强**

- `target` 支持优先级关键词：`P{n}` / `P{n}up` / `P{n}down` / `Max` / `Flash` / `Medium`，语义与客户端直发该关键词完全一致——仅在选中档位内轮转，不跨档兜底
- 关键词解释优先于供应商名；供应商名 target 保持原有软钉 + 全链兜底行为
- 新增配置校验：target 必须命中供应商名或合法关键词，重复/空 alias、空 target 在启动与热重载时直接报错（此前静默失效，热重载失败会保留旧配置继续服务）
- `default` 路由的 target 同样支持关键词（软展开，其余供应商仍作兜底）
- 修复示例配置引用不存在供应商的问题，并新增 P2 供应商演示跨优先级降级
- 测试扩充至 31+（含 2 个穿透完整请求栈的端到端测试）；发布物新增本文件；二进制改为 Release 资产分发，不再跟踪进 git

## v1.0.19

- README 重构为纯使用指南：Agent 接入配置（OpenCode / Claude Code / Codex）、providerinfo 模型速查参考
- 新增树莓派交互式一键部署脚本 `deploy-rpi-interactive.ps1`
- providerinfo 思考等级说明更新

## v1.0.18

- GUI 启动器默认 `max_stream_minutes` 调整为 15（适配长思考模型的流式输出）
- providerinfo 模型信息更新

## v1.0.17

- 多模态请求透传与 SenseNova `reasoning_effort` 归一化处理
- Responses API 支持不带 `/v1` 前缀的端点（兼容 Codex 客户端）
- 并发连接限制（按上游主机）与流式首事件前故障转移（SSE 提交门）策略完善

## v1.0.16 及更早（核心能力奠基）

- 三协议接入与自动转换：OpenAI Chat Completions / Anthropic Messages / OpenAI Responses（含 tool_use/tool_calls/thinking），格式隔离路由（OpenAI 与 Anthropic 供应商之间不跨格式降级）
- 优先级路由（`P1`~`Pn` 分组降级、`P{n}up`/`P{n}down` 区间）、同组 round-robin、指数退避重试（4xx/5xx 均可重试）、按优先级熔断（状态持久化）
- SSE 流式转发：空闲超时、总时长上限可配、中断注入错误事件
- 监控面板：命中率/延迟曲线、供应商统计表、实时日志 SSE 流、启停控制（loopback 鉴权）
- GUI 启动器（系统托盘、配置编辑器、首次运行引导）与 CLI 双产物，Windows/Linux/macOS x64+ARM64 交叉编译
- 请求体日志分级（off/snippet/full）、配置热加载、令牌桶限流、日志轮转、统计持久化
- 树莓派 systemd 部署指南
