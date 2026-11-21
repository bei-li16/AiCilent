# 模型列表信息

来源：`GET https://token.sensenova.cn/v1/models`（最近实测 2026-11-21，两个 API Key 结果一致；历史观测日期见变更记录）  
思考挡位来源：API 报错枚举法实测（2026-09-28），向各模型发送 `reasoning_effort: "invalid"` 触发参数校验错误，从返回信息中提取合法值列表

## 适用范围与代理策略（2026-11-15）

本文件保留历史模型列表、价格/模态和参数枚举观测，具体日期见各条记录；不是实时供应商文档，也不是所有账号、协议、思考档位的持续可用性保证。接受参数或返回 HTTP 200 不足以证明指定思考等级实际生效。

- 下方 `reasoning_effort` 表来自 OpenAI 兼容接口，不能直接当作 Anthropic `output_config.effort` 或 `thinking` 的合法值表。回环矩阵验证代理如何发包，不证明真实上游接受每种组合。
- 当前代理不会直接透传显式 `none`：OpenAI / Responses 按 `global.reasoning_effort_on_none` 替换档位，未配置时移除该控制；SenseNova OpenAI 存在明确档位时补充 `reasoning: true`。因此表中的「可关闭思考」描述历史直连能力，不是经过代理后 `none` 的保证。
- Anthropic 的显式 `thinking.type: disabled` / `output_config.effort: none` 使用相同策略。配置为 `max` 时写入原生 effort，原关闭状态改为 adaptive；已有启用的手动预算保留。策略留空则省略对应控制，使用上游默认，并非强制关闭。
- SenseNova OpenAI 的 `glm-5.3` 只保留 `low` / `high` / `max`，把 `minimal` / `medium` 映射为 `low`，其他未支持的明确档位映射为 `max`；此映射不用于 Anthropic 原生档位。各账号实际支持情况仍需真实上游验证。

配置、请求头和测试边界以 [README 配置参考](README.md#配置参考) 为准；本代理未提供图像生成 API，后文图像生成模型仅为上游目录记录。

## 模型总览（2026-11-21 实测，当前在列 7 个）

| # | 模型ID | 输入模态 | 输出模态 | 上下文 | 最大输出 | 功能特点 |
|---|--------|---------|---------|--------|---------|---------|
| 1 | `deepseek-v4-flash` | 文本 | 文本 | **1M** | 64K | DeepSeek 高性能对话模型，1M 上下文，工具调用 |
| 2 | `deepseek-flash` | 文本 | 文本 | **1M** | 64K | DeepSeek 高性能对话模型，1M 上下文，工具调用（2026-11-15 起输入模态为纯文本） |
| 3 | `kimi-k3` | 文本 | 文本 | **1M** | 64K | Kimi 旗舰思考模型，1M 上下文，工具调用（2026-11-15 起输入模态为纯文本） |
| 4 | `glm-5.3` | 文本 | 文本 | **1M** | **128K** | GLM 旗舰；思考仅 3 档（low/high/max）且无 none 无法关闭；图片输入不报错但会被静默忽略，勿传图 |
| 5 | `sensenova-6.8-flash` | 文本+**图像** | 文本 | 262K | 64K | 多模态智能体（6.8 版本；2026-11-15 首次出现在列表，9-21 快照中无此模型） |
| 6 | `sensenova-6.8-flash-lite` | 文本+**图像** | 文本 | 262K | 64K | 轻量多模态智能体（6.8 版本） |
| 7 | `sensenova-u1.5-lite` | 文本 | **图像** | 262K | 64K | 基于 U1.5 加速版，专用于信息图生成 |

### 已下架模型（历史记录，仅供对照）

| 模型ID | 下架时间窗 | 原观测记录 |
|--------|-----------|-----------|
| `glm-5.2` | 2026-11-17~11-20 之间（11-17 列表尚在，11-21 已无；直连请求返回 400 `matched user group has no available model instance`） | 1M/128K 文本→文本；思考 7 档 |
| `deepseek-v4-pro` | 9-21 快照后、11-15 前 | 1M/64K；思考 8 档全量 |
| `deepseek-v4.1-flash` | 9-21 快照后、11-15 前 | 1M/64K；文本+图像输入；思考 8 档 |
| `sensenova-6.7-flash-lite` | 9-21 快照后、11-15 前（下架前已重定向至 6.8-flash-lite） | 262K/64K；文本+图像输入 |
| `sensenova-u1-fast` | 9-21 快照后、11-15 前 | 262K/64K；文本→图像输出 |

---

## 思考挡位（API 实测 2026-09-28）

> 通过向 `/v1/chat/completions` 发送 `reasoning_effort: "invalid"` 触发服务端参数校验报错，从错误信息中提取各模型的合法取值。挡位强度从低到高：`none → minimal → low → medium → high → xhigh → max → ultra`

| 模型ID | 支持的 reasoning_effort | 挡位数 | 可关闭思考 |
|--------|------------------------|--------|-----------|
| `deepseek-flash` | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra` | 8 | ✅ |
| `glm-5.3` | `low` / `high` / `max` | 3 | ❌（无 `none`） |
| `kimi-k3` | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` | 7 | ✅ |
| `deepseek-v4-flash` | `none` / `low` / `medium` / `high` / `xhigh` | 5 | ✅ |
| `sensenova-6.8-flash-lite` | `none` / `low` / `medium` / `high` / `xhigh` | 5 | ✅ |
| `sensenova-6.8-flash` | 未实测（2026-11-15 起新出现在列表） | — | — |
| `sensenova-u1.5-lite` | N/A（图像生成模型） | — | — |
| `glm-5.2`（已下架） | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` | 7 | ✅ |
| `deepseek-v4-pro`（已下架） | 8 档全量 | 8 | ✅ |
| `deepseek-v4.1-flash`（已下架） | 与 deepseek-flash 相同，8 档全量 | 8 | ✅ |
| `sensenova-6.7-flash-lite`（已下架） | 同 6.8-flash-lite | 5 | ✅ |
| `sensenova-u1-fast`（已下架） | N/A（图像生成模型） | — | — |

### 注意事项

- **挡位强度排序**：`none` → `minimal` → `low` → `medium` → `high` → `xhigh` → `max` → `ultra`
- **deepseek-v4-flash 与 deepseek-v4-pro 挡位不同**：Flash 仅 5 档（无 minimal/max/ultra），Pro 有 8 档（全量）
- **deepseek-flash / deepseek-v4.1-flash**：与 deepseek-v4-pro 相同，均为 8 档全量
- **glm-5.2 与 kimi-k3**：均为 7 档（无 ultra），挡位完全一致
- **glm-5.3 仅 3 档（low / high / max）且无 `none`**：无法关闭思考，最低档为 `low`（2026-11-04 报错枚举实测）
- **glm-5.3 图片输入会被静默忽略**：不报错、返回 200，但模型自述无多模态输入能力，答案基于纯文本生成——请按纯文本模型使用，勿向其发送图片（2026-11-04 实测）
- 所有模型流式响应的推理字段均为 `delta.reasoning_content`（2026-09-28 经 ai-proxy 链路实测，3 次采样共 80 个 chunk，`thinking_content` 出现 0 次；2026-11-04 补充实测 `glm-5.3` 同为该字段）；非流式同为 `reasoning_content`

---

## 补充信息

- 所有模型定价当前均显示为 **0**（免费），支持 `tools`、`json_mode`、`reasoning`，量化精度 fp8，数据中心位于中国(CN)，业务模式为 tokenplan+metered（2026-11-21 复测未变）
- 图像生成类：`sensenova-u1.5-lite`（仅文本输入、图像输出，使用 `/v1/images/generations` 端点）
- 多模态(图像理解)：仅 `sensenova-6.8-flash`、`sensenova-6.8-flash-lite`（`kimi-k3`、`deepseek-flash` 2026-11-15 起输入模态回归纯文本）
- 推理/长上下文类：`deepseek-v4-flash`、`deepseek-flash`、`glm-5.3`、`kimi-k3`（1M 上下文）；`sensenova-6.8-flash`、`sensenova-6.8-flash-lite`（262K）

## 变更记录

- 2026-11-21：重新请求 `/v1/models`（两个 API Key 结果一致）：列表收敛为 **7 个**。`glm-5.2` 已下架（11-17 尚在、11-20/21 已无；直连请求返回 400）；`kimi-k3`、`deepseek-flash` 输入模态确认为纯文本；`sensenova-6.8-flash` 在列（262K、文本+图像输入）。新增「已下架模型」对照表，移除模型标注见思考挡位表
- 2026-11-15 / 11-17（中间观测）：列表为 8 个（含 `glm-5.2`）；`sensenova-6.8-flash` 已在列；`deepseek-v4-pro`、`deepseek-v4.1-flash`、`sensenova-6.7-flash-lite`、`sensenova-u1-fast` 均已不在列表
- 2026-11-04：新增 `glm-5.3`（1M 上下文、128K 最大输出、fp8；思考挡位实测 `low`/`high`/`max` 共 3 档、无 `none` 无法关思考；接受图片但静默忽略，按纯文本登记；流式推理字段实测为 `delta.reasoning_content`）
- 2026-09-28：思考挡位数据更新为 API 报错枚举法实测结果，新增 `deepseek-flash`、`deepseek-v4.1-flash` 挡位信息；挡位由原来 4-6 档修正为 5-8 档（新增 `minimal`、`ultra` 等）
- 2026-09-28：`deepseek-flash` 输入模态由"文本"更正为"文本+图像"（实测支持图片输入），补充信息同步更新
- 2026-09-28：更正 `deepseek-v4-pro` 流式推理字段为 `delta.reasoning_content`（原记载 `delta.thinking_content` 经代理链路实测未复现）
- 2026-09-22：补充 `deepseek-v4.1-flash` 支持图片输入和多模态图片理解
- 2026-09-21：重新请求 `/v1/models`；新增 `deepseek-v4.1-flash` 和 `deepseek-flash`，二者均为 1M 上下文、64K 最大输出，并支持 `tools`、`json_mode`、`reasoning`
- 2026-09-03：`kimi-k3` 输入模态由"文本+图像"更正为"文本"（API 实测 `input_modalities: ["text"]`）；新增思考模式列
- 2026-09-03：从 `platform.sensenova.cn/docs` 官方文档提取各模型思考等级定义，整理为速查表
- 2026-09-03：`kimi-k3` 输入模态改回"文本+图像"（支持多模态图像理解）
