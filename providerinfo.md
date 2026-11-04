# 模型列表信息

来源：`GET https://token.sensenova.cn/v1/models`（2026-09-21 实测）  
思考挡位来源：API 报错枚举法实测（2026-09-28），向各模型发送 `reasoning_effort: "invalid"` 触发参数校验错误，从返回信息中提取合法值列表

## 模型总览

| # | 模型ID | 输入模态 | 输出模态 | 上下文 | 最大输出 | 功能特点 |
|---|--------|---------|---------|--------|---------|---------|
| 1 | `kimi-k3` | 文本+**图像** | 文本 | **1M** | 64K | Kimi旗舰思考模型，1M上下文，多模态，工具调用 |
| 2 | `glm-5.2` | 文本 | 文本 | **1M** | **128K** | 旗舰模型，超长任务，工程级上下文，端到端开发流水线 |
| 3 | `glm-5.3` | 文本 | 文本 | **1M** | **128K** | GLM 新一代旗舰（2026-11 新增），1M上下文；思考仅 3 档（low/high/max）且无 none 无法关闭；图片输入不报错但会被静默忽略，勿传图 |
| 4 | `deepseek-v4-pro` | 文本 | 文本 | **1M** | 64K | DeepSeek V4 PRO，1M上下文，工具调用 |
| 5 | `deepseek-v4-flash` | 文本 | 文本 | **1M** | 64K | DeepSeek高性能对话模型，1M上下文，工具调用 |
| 6 | `deepseek-v4.1-flash` | 文本+**图像** | 文本 | **1M** | 64K | DeepSeek V4.1 Flash，多模态高性能对话模型，支持图片理解和工具调用 |
| 7 | `deepseek-flash` | 文本+**图像** | 文本 | **1M** | 64K | DeepSeek Flash，多模态高性能对话模型，支持图片理解和工具调用 |
| 8 | `sensenova-6.8-flash-lite` | 文本+**图像** | 文本 | 262K | 64K | 轻量多模态智能体（6.8版本） |
| 9 | `sensenova-6.7-flash-lite` | 文本+**图像** | 文本 | 262K | 64K | 已自动重定向至 6.8-flash-lite；当前模型列表未单独返回 |
| 10 | `sensenova-u1.5-lite` | 文本 | **图像** | 262K | 64K | 基于U1.5加速版，专用于信息图生成 |
| 11 | `sensenova-u1-fast` | 文本 | **图像** | 262K | 64K | 基于U1加速版，专用于信息图生成 |

---

## 思考挡位（API 实测 2026-09-28）

> 通过向 `/v1/chat/completions` 发送 `reasoning_effort: "invalid"` 触发服务端参数校验报错，从错误信息中提取各模型的合法取值。挡位强度从低到高：`none → minimal → low → medium → high → xhigh → max → ultra`

| 模型ID | 支持的 reasoning_effort | 挡位数 | 可关闭思考 |
|--------|------------------------|--------|-----------|
| `deepseek-v4-pro` | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra` | 8 | ✅ |
| `deepseek-flash` | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra` | 8 | ✅ |
| `deepseek-v4.1-flash` | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` / `ultra` | 8 | ✅ |
| `glm-5.2` | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` | 7 | ✅ |
| `glm-5.3` | `low` / `high` / `max` | 3 | ❌（无 `none`） |
| `kimi-k3` | `none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` | 7 | ✅ |
| `deepseek-v4-flash` | `none` / `low` / `medium` / `high` / `xhigh` | 5 | ✅ |
| `sensenova-6.8-flash-lite` | `none` / `low` / `medium` / `high` / `xhigh` | 5 | ✅ |
| `sensenova-6.7-flash-lite` | 同 6.8（已重定向） | 同 6.8 | 同 6.8 |
| `sensenova-u1.5-lite` | N/A（图像生成模型） | — | — |
| `sensenova-u1-fast` | N/A（图像生成模型） | — | — |

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

- 所有模型定价当前均显示为 **0**（免费），支持 `tools`、`json_mode`、`reasoning`，量化精度 fp8，数据中心位于中国(CN)，业务模式为 tokenplan+metered
- 图像生成类：`sensenova-u1-fast`、`sensenova-u1.5-lite`（仅文本输入、图像输出，使用 `/v1/images/generations` 端点）
- 多模态(图像理解)：`kimi-k3`、`deepseek-v4.1-flash`、`deepseek-flash`、`sensenova-6.7-flash-lite`（已重定向至6.8）、`sensenova-6.8-flash-lite`
- 推理/长上下文类：`deepseek-v4-flash`、`deepseek-v4.1-flash`、`deepseek-v4-pro`、`deepseek-flash`、`glm-5.2`、`glm-5.3`、`kimi-k3`（均为 1M 上下文）

## 变更记录

- 2026-09-22：补充 `deepseek-v4.1-flash` 支持图片输入和多模态图片理解
- 2026-09-21：重新请求 `/v1/models`；新增 `deepseek-v4.1-flash` 和 `deepseek-flash`，二者均为 1M 上下文、64K 最大输出，并支持 `tools`、`json_mode`、`reasoning`
- 2026-09-03：`kimi-k3` 输入模态由"文本+图像"更正为"文本"（API 实测 `input_modalities: ["text"]`）；新增思考模式列
- 2026-09-03：从 `platform.sensenova.cn/docs` 官方文档提取各模型思考等级定义，整理为速查表
- 2026-09-03：`kimi-k3` 输入模态改回"文本+图像"（支持多模态图像理解）
- 2026-09-28：思考挡位数据更新为 API 报错枚举法实测结果，新增 `deepseek-flash`、`deepseek-v4.1-flash` 挡位信息；挡位由原来 4-6 档修正为 5-8 档（新增 `minimal`、`ultra` 等）
- 2026-09-28：`deepseek-flash` 输入模态由"文本"更正为"文本+图像"（实测支持图片输入），补充信息同步更新
- 2026-09-28：更正 `deepseek-v4-pro` 流式推理字段为 `delta.reasoning_content`（原记载 `delta.thinking_content` 经代理链路实测未复现）
- 2026-11-04：新增 `glm-5.3`（1M 上下文、128K 最大输出、fp8；思考挡位实测 `low`/`high`/`max` 共 3 档、无 `none` 无法关思考；接受图片但静默忽略，按纯文本登记；流式推理字段实测为 `delta.reasoning_content`）
