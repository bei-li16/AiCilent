# 模型列表信息

来源：`GET https://token.sensenova.cn/v1/models`（2026-09-03 实测）  
思考等级来源：`platform.sensenova.cn/docs` 官方文档（2026-09-03 提取）

## 模型总览

| # | 模型ID | 输入模态 | 输出模态 | 上下文 | 最大输出 | 功能特点 |
|---|--------|---------|---------|--------|---------|---------|
| 1 | `kimi-k3` | 文本+**图像** | 文本 | **1M** | 64K | Kimi旗舰思考模型，1M上下文，多模态，工具调用 |
| 2 | `glm-5.2` | 文本 | 文本 | **1M** | **128K** | 旗舰模型，超长任务，工程级上下文，端到端开发流水线 |
| 3 | `deepseek-v4-pro` | 文本 | 文本 | **1M** | 64K | DeepSeek V4 PRO，1M上下文，工具调用 |
| 4 | `deepseek-v4-flash` | 文本 | 文本 | **1M** | 64K | DeepSeek高性能对话模型，1M上下文，工具调用 |
| 5 | `sensenova-6.8-flash-lite` | 文本+**图像** | 文本 | 262K | 64K | 轻量多模态智能体（6.8版本） |
| 6 | `sensenova-6.7-flash-lite` | 文本+**图像** | 文本 | 262K | 64K | 已自动重定向至 6.8-flash-lite |
| 7 | `sensenova-u1.5-lite` | 文本 | **图像** | 262K | 64K | 基于U1.5加速版，专用于信息图生成 |
| 8 | `sensenova-u1-fast` | 文本 | **图像** | 262K | 64K | 基于U1加速版，专用于信息图生成 |

---

## 思考等级（SenseNova 平台）

> 信息提取自 `platform.sensenova.cn/docs` 官方文档。SenseNova 平台对各模型参数进行了**归一化处理**，与模型原厂（Zhipu/DeepSeek/Moonshot）的官方文档可能不同，以 SenseNova 平台文档为准。

| 模型ID | 支持的 reasoning_effort | 默认值 | 响应字段 | 可关闭思考 | thinking.type |
|--------|------------------------|--------|---------|-----------|---------------|
| `kimi-k3` | `low` / `high` / `max` | `max` | `reasoning_content` | ❌ | ❌ 不使用 |
| `glm-5.2` | `low` / `medium` / `high` / `none` | `medium` | `reasoning_content` | ✅ | ❌ 未提及 |
| `deepseek-v4-pro` | `low` / `high` / `max` / `none`（`medium`→`high`，`xhigh`→`high`） | `high` | `thinking_content` | ✅ | ✅ enabled/disabled |
| `deepseek-v4-flash` | `low` / `medium` / `high` / `none` | `medium` | `reasoning_content` | ✅ | ❌ 未提及 |
| `sensenova-6.8-flash-lite` | `low` / `medium` / `high` / `none` | `medium` | `reasoning_content` | ✅ | ❌ 未提及 |
| `sensenova-6.7-flash-lite` | 同 6.8（已重定向） | 同 6.8 | 同 6.8 | 同 6.8 | 同 6.8 |
| `sensenova-u1.5-lite` | N/A（图像生成模型） | N/A | N/A | N/A | N/A |
| `sensenova-u1-fast` | N/A（图像生成模型） | N/A | N/A | N/A | N/A |

### 注意事项

- **V4 Flash 与 V4 Pro 参数不同**：Flash 仅支持 4 档（low/medium/high/none），默认 `medium`；Pro 支持 6 档（含 xhigh/max），默认 `high`，其中 `medium` 和 `xhigh` 映射为 `high`
- **GLM-5.2 与原厂差异**：智谱官方文档支持 7 档（max/xhigh/high/medium/low/minimal/none），SenseNova 归一化为 4 档（low/medium/high/none），默认 `medium` 而非 `max`
- `deepseek-v4-pro` 流式响应中推理字段为 `delta.thinking_content`，其余模型为 `delta.reasoning_content`（非流式为 `reasoning_content`）

---

## 补充信息

- 所有模型定价当前均显示为 **0**（免费），支持 `tools`、`json_mode`、`reasoning`，量化精度 fp8，数据中心位于中国(CN)，业务模式为 tokenplan+metered
- 图像生成类：`sensenova-u1-fast`、`sensenova-u1.5-lite`（仅文本输入、图像输出，使用 `/v1/images/generations` 端点）
- 多模态(图像理解)：`kimi-k3`、`sensenova-6.7-flash-lite`（已重定向至6.8）、`sensenova-6.8-flash-lite`
- 推理/长上下文类：`deepseek-v4-flash`、`deepseek-v4-pro`、`glm-5.2`、`kimi-k3`（均为 1M 上下文）

## 变更记录

- 2026-09-03：`kimi-k3` 输入模态由"文本+图像"更正为"文本"（API 实测 `input_modalities: ["text"]`）；新增思考模式列
- 2026-09-03：从 `platform.sensenova.cn/docs` 官方文档提取各模型思考等级定义，整理为速查表
- 2026-09-03：`kimi-k3` 输入模态改回"文本+图像"（支持多模态图像理解）
