package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn", "end":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "content_filtered":
		return "content_filter"
	default:
		return reason
	}
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "content_filtered"
	default:
		return reason
	}
}

// ─────────────────────────────────────────────────────────────
//  Non-streaming request/response conversion
// ─────────────────────────────────────────────────────────────

func ConvertRequest(body []byte, fromFormat, toFormat string) ([]byte, error) {
	if fromFormat == toFormat {
		return body, nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal request: %w", err)
	}

	switch {
	case fromFormat == "openai" && toFormat == "anthropic":
		// Extract system message from messages and set as top-level "system"
		if msgs, ok := raw["messages"].([]interface{}); ok {
			var filtered []interface{}
			for _, m := range msgs {
				msg, ok := m.(map[string]interface{})
				if !ok {
					continue
				}
				if role, _ := msg["role"].(string); role == "system" {
					if content, _ := msg["content"].(string); content != "" {
						raw["system"] = content
					}
					continue // drop from messages
				}
				filtered = append(filtered, msg)
			}
			raw["messages"] = filtered
		}
		// Rename stop → stop_sequences
		if stop, ok := raw["stop"]; ok {
			raw["stop_sequences"] = stop
			delete(raw, "stop")
		}
		// Convert tools: OpenAI [{type:"function",function:{name,parameters}}] → Anthropic [{name,input_schema}]
		convertOpenAIToolsToAnthropic(raw)

	case fromFormat == "anthropic" && toFormat == "openai":
		// Prepend system as a system message
		if sys, ok := raw["system"]; ok {
			if sysStr, ok := sys.(string); ok && sysStr != "" {
				var msgs []interface{}
				msgs = append(msgs, map[string]interface{}{
					"role":    "system",
					"content": sysStr,
				})
				if existing, ok := raw["messages"].([]interface{}); ok {
					msgs = append(msgs, existing...)
				}
				raw["messages"] = msgs
			}
			delete(raw, "system")
		}
		// Rename stop_sequences → stop
		if ss, ok := raw["stop_sequences"]; ok {
			raw["stop"] = ss
			delete(raw, "stop_sequences")
		}
		// Convert tools: Anthropic [{name,input_schema}] → OpenAI [{type:"function",function:{name,parameters}}]
		convertAnthropicToolsToOpenAI(raw)

	default:
		return nil, fmt.Errorf("unsupported format conversion: %s -> %s", fromFormat, toFormat)
	}

	return json.Marshal(raw)
}

// convertOpenAIToolsToAnthropic converts tools array in-place:
// OpenAI: [{type:"function", function:{name, description, parameters}}]
// Anthropic: [{name, description, input_schema}]
func convertOpenAIToolsToAnthropic(raw map[string]interface{}) {
	tools, ok := raw["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return
	}
	converted := make([]interface{}, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]interface{})
		if !ok {
			// Already in Anthropic format or unknown structure, keep as-is
			converted = append(converted, tool)
			continue
		}
		entry := map[string]interface{}{}
		if name, ok := fn["name"]; ok {
			entry["name"] = name
		}
		if desc, ok := fn["description"]; ok {
			entry["description"] = desc
		}
		if params, ok := fn["parameters"]; ok {
			entry["input_schema"] = params
		}
		converted = append(converted, entry)
	}
	raw["tools"] = converted

	// Convert tool_choice
	// OpenAI: {"type":"function","function":{"name":"..."}}
	// Anthropic: {"type":"tool","name":"..."}
	if tc, ok := raw["tool_choice"].(map[string]interface{}); ok {
		if tcType, _ := tc["type"].(string); tcType == "function" {
			if fn, ok := tc["function"].(map[string]interface{}); ok {
				if name, ok := fn["name"].(string); ok {
					raw["tool_choice"] = map[string]interface{}{
						"type": "tool",
						"name": name,
					}
				}
			}
		}
	}
}

// convertAnthropicToolsToOpenAI converts tools array in-place:
// Anthropic: [{name, description, input_schema}]
// OpenAI: [{type:"function", function:{name, description, parameters}}]
func convertAnthropicToolsToOpenAI(raw map[string]interface{}) {
	tools, ok := raw["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return
	}
	converted := make([]interface{}, 0, len(tools))
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		// Already in OpenAI format?
		if _, hasType := tool["type"]; hasType {
			converted = append(converted, tool)
			continue
		}
		fn := map[string]interface{}{}
		if name, ok := tool["name"]; ok {
			fn["name"] = name
		}
		if desc, ok := tool["description"]; ok {
			fn["description"] = desc
		}
		if schema, ok := tool["input_schema"]; ok {
			fn["parameters"] = schema
		}
		converted = append(converted, map[string]interface{}{
			"type":     "function",
			"function": fn,
		})
	}
	raw["tools"] = converted

	// Convert tool_choice
	// Anthropic: {"type":"tool","name":"..."}
	// OpenAI: {"type":"function","function":{"name":"..."}}
	if tc, ok := raw["tool_choice"].(map[string]interface{}); ok {
		if tcType, _ := tc["type"].(string); tcType == "tool" {
			if name, ok := tc["name"].(string); ok {
				raw["tool_choice"] = map[string]interface{}{
					"type": "function",
					"function": map[string]interface{}{
						"name": name,
					},
				}
			}
		}
	}
}

func ConvertResponse(body []byte, fromFormat, toFormat, model string) ([]byte, error) {
	if fromFormat == toFormat {
		return body, nil
	}

	if fromFormat == "anthropic" && toFormat == "openai" {
		// Use generic JSON manipulation to handle tool_use blocks
		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("unmarshal anthropic response: %w", err)
		}

		openAIResp := map[string]interface{}{
			"id":     raw["id"],
			"object": "chat.completion",
			"model":  model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "",
					},
					"finish_reason": "stop",
				},
			},
		}

		// Map stop_reason
		if stopReason, ok := raw["stop_reason"].(string); ok {
			openAIResp["choices"].([]map[string]interface{})[0]["finish_reason"] = mapStopReason(stopReason)
		}

		// Process content blocks
		content, _ := raw["content"].([]interface{})
		var textContent string
		var toolCalls []map[string]interface{}

		for _, c := range content {
			block, _ := c.(map[string]interface{})
			if block == nil {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "text":
				text, _ := block["text"].(string)
				textContent += text
			case "tool_use":
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				input := block["input"]
				inputJSON, _ := json.Marshal(input)
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   id,
					"type": "function",
					"function": map[string]interface{}{
						"name":      name,
						"arguments": string(inputJSON),
					},
				})
			}
		}

		msg := openAIResp["choices"].([]map[string]interface{})[0]["message"].(map[string]interface{})
		msg["content"] = textContent
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}

		// Map usage
		if usage, ok := raw["usage"].(map[string]interface{}); ok {
			inputTokens, _ := usage["input_tokens"].(float64)
			outputTokens, _ := usage["output_tokens"].(float64)
			openAIResp["usage"] = map[string]interface{}{
				"prompt_tokens":     int(inputTokens),
				"completion_tokens": int(outputTokens),
				"total_tokens":      int(inputTokens + outputTokens),
			}
		}

		return json.Marshal(openAIResp)
	}

	if fromFormat == "openai" && toFormat == "anthropic" {
		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("unmarshal openai response: %w", err)
		}

		anthropicResp := map[string]interface{}{
			"id":           raw["id"],
			"type":         "message",
			"role":         "assistant",
			"model":        model,
			"content":      []interface{}{},
			"stop_reason":  "end_turn",
			"stop_sequence": nil,
		}

		// Map finish_reason
		choices, _ := raw["choices"].([]interface{})
		if len(choices) > 0 {
			choice, _ := choices[0].(map[string]interface{})
			if finishReason, ok := choice["finish_reason"].(string); ok {
				anthropicResp["stop_reason"] = mapFinishReason(finishReason)
			}

			message, _ := choice["message"].(map[string]interface{})
			if message != nil {
				var content []interface{}

				// Text content
				if text, ok := message["content"].(string); ok && text != "" {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": text,
					})
				}

				// Tool calls
				if toolCalls, ok := message["tool_calls"].([]interface{}); ok {
					for _, tc := range toolCalls {
						tcMap, _ := tc.(map[string]interface{})
						if tcMap == nil {
							continue
						}
						id, _ := tcMap["id"].(string)
						tcType, _ := tcMap["type"].(string)
						funcMap, _ := tcMap["function"].(map[string]interface{})

						if tcType == "function" && funcMap != nil {
							name, _ := funcMap["name"].(string)
							argsStr, _ := funcMap["arguments"].(string)

							// Parse arguments as JSON
							var argsJSON interface{}
							json.Unmarshal([]byte(argsStr), &argsJSON)

							content = append(content, map[string]interface{}{
								"type": "tool_use",
								"id":   id,
								"name": name,
								"input": argsJSON,
							})
						}
					}
				}

				anthropicResp["content"] = content
			}
		}

		// Map usage
		if usage, ok := raw["usage"].(map[string]interface{}); ok {
			promptTokens, _ := usage["prompt_tokens"].(float64)
			completionTokens, _ := usage["completion_tokens"].(float64)
			anthropicResp["usage"] = map[string]interface{}{
				"input_tokens":  int(promptTokens),
				"output_tokens": int(completionTokens),
			}
		}

		return json.Marshal(anthropicResp)
	}

	return nil, fmt.Errorf("unsupported format conversion: %s -> %s", fromFormat, toFormat)
}

// ─────────────────────────────────────────────────────────────
//  Streaming SSE conversion
// ─────────────────────────────────────────────────────────────

func StreamConvertResponse(src io.Reader, dst io.Writer, fromFormat, toFormat string) error {
	if fromFormat == toFormat {
		_, err := io.Copy(dst, src)
		return err
	}

	if fromFormat == "anthropic" && toFormat == "openai" {
		return convertAnthropicSSEToOpenAI(src, dst)
	}
	if fromFormat == "openai" && toFormat == "anthropic" {
		return convertOpenAISSETOAnthropic(src, dst)
	}

	return fmt.Errorf("unsupported stream conversion: %s -> %s", fromFormat, toFormat)
}

// ─────────────────────────────────────────────────────────────
//  Anthropic SSE → OpenAI SSE
// ─────────────────────────────────────────────────────────────

type blockTracker struct {
	index int    // OpenAI tool_call index (not Anthropic block index)
	typ   string // "text", "tool_use", "thinking"
}

func convertAnthropicSSEToOpenAI(src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 4096), 1048576)

	var currentEvent string
	// Track content blocks by index
	blocks := make(map[int]blockTracker)
	// Track the next tool call index for OpenAI format
	nextToolIdx := 0

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "[DONE]" {
			writeSSEDone(dst)
			continue
		}

		switch currentEvent {
		case "message_start":
			var msg struct {
				Type    string `json:"type"`
				Message struct {
					ID   string `json:"id"`
					Role string `json:"role"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(dataStr), &msg); err == nil && msg.Message.Role != "" {
				oaChunk := map[string]interface{}{
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]string{"role": "assistant", "content": ""},
						},
					},
				}
				writeSSEData(dst, oaChunk)
			}

		case "content_block_start":
			var block struct {
				Index        int             `json:"index"`
				ContentBlock json.RawMessage `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(dataStr), &block); err != nil {
				continue
			}

			// Determine block type by parsing the content_block
			var header struct {
				Type string `json:"type"`
			}
			json.Unmarshal(block.ContentBlock, &header)

			switch header.Type {
			case "text":
				blocks[block.Index] = blockTracker{index: -1, typ: header.Type}

			case "tool_use":
				var toolBlock struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				json.Unmarshal(block.ContentBlock, &toolBlock)

				tcIdx := nextToolIdx
				nextToolIdx++
				// Store the OpenAI tool call index, NOT the Anthropic block index
				blocks[block.Index] = blockTracker{index: tcIdx, typ: header.Type}

				oaChunk := map[string]interface{}{
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{
								"tool_calls": []map[string]interface{}{
									{
										"index": tcIdx,
										"id":    toolBlock.ID,
										"type":  "function",
										"function": map[string]interface{}{
											"name":      toolBlock.Name,
											"arguments": "",
										},
									},
								},
							},
						},
					},
				}
				writeSSEData(dst, oaChunk)

			case "thinking":
				// Skip thinking blocks in OpenAI output (Claude-specific)
				// Optionally could emit as text content
			}

		case "content_block_delta":
			var delta struct {
				Index int             `json:"index"`
				Delta json.RawMessage `json:"delta"`
			}
			if err := json.Unmarshal([]byte(dataStr), &delta); err != nil {
				continue
			}

			var deltaType struct {
				Type string `json:"type"`
			}
			json.Unmarshal(delta.Delta, &deltaType)

			switch deltaType.Type {
			case "text_delta":
				var textDelta struct {
					Text string `json:"text"`
				}
				json.Unmarshal(delta.Delta, &textDelta)
				if textDelta.Text != "" {
					oaChunk := map[string]interface{}{
						"choices": []map[string]interface{}{
							{
								"index": 0,
								"delta": map[string]string{"content": textDelta.Text},
							},
						},
					}
					writeSSEData(dst, oaChunk)
				}

			case "input_json_delta":
				var jsonDelta struct {
					PartialJSON string `json:"partial_json"`
				}
				json.Unmarshal(delta.Delta, &jsonDelta)
				if jsonDelta.PartialJSON != "" {
					// Find the tool call index for this block
					bt := blocks[delta.Index]
					// We need to know which tool call index this corresponds to
					// For simplicity, use block index as tool call index
					oaChunk := map[string]interface{}{
						"choices": []map[string]interface{}{
							{
								"index": 0,
								"delta": map[string]interface{}{
									"tool_calls": []map[string]interface{}{
										{
											"index": bt.index,
											"function": map[string]string{
												"arguments": jsonDelta.PartialJSON,
											},
										},
									},
								},
							},
						},
					}
					writeSSEData(dst, oaChunk)
				}

			case "thinking_delta":
				// Skip thinking delta in OpenAI output
			}

		case "content_block_stop":
			// Nothing to emit for OpenAI format

		case "message_delta":
			var delta struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(dataStr), &delta); err != nil {
				continue
			}

			finishReason := mapStopReason(delta.Delta.StopReason)
			choice := map[string]interface{}{
				"index": 0,
				"delta": map[string]string{},
			}
			if finishReason != "" {
				choice["finish_reason"] = finishReason
			}
			oaChunk := map[string]interface{}{
				"choices": []map[string]interface{}{choice},
			}
			if delta.Usage != nil {
				oaChunk["usage"] = map[string]int{
					"prompt_tokens":     delta.Usage.InputTokens,
					"completion_tokens": delta.Usage.OutputTokens,
					"total_tokens":      delta.Usage.InputTokens + delta.Usage.OutputTokens,
				}
			}
			writeSSEData(dst, oaChunk)

		case "message_stop":
			writeSSEDone(dst)
		}
	}
	return scanner.Err()
}

// ─────────────────────────────────────────────────────────────
//  OpenAI SSE → Anthropic SSE
// ─────────────────────────────────────────────────────────────

func convertOpenAISSETOAnthropic(src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 4096), 1048576)

	var roleWritten bool
	var contentBlockStarted bool
	var toolCallAccumulators []*toolCallAcc // track active tool calls
	contentBlockIndex := 0
	msgCounter := 0

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "[DONE]" {
			writeAnthropicEvent(dst, "message_stop", map[string]string{"type": "message_stop"})
			continue
		}

		var chunk struct {
			Choices []struct {
				Index        int             `json:"index"`
				Delta        json.RawMessage `json:"delta"`
				FinishReason *string         `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil || len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		var delta map[string]interface{}
		json.Unmarshal(choice.Delta, &delta)

		// Handle role: emit message_start + content_block_start for text
		if role, ok := delta["role"].(string); ok && !roleWritten {
			roleWritten = true
			msgCounter++
			msgID := fmt.Sprintf("msg_proxy_%d", msgCounter)
			writeAnthropicEvent(dst, "message_start", map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":      msgID,
					"type":    "message",
					"role":    role,
					"content": []interface{}{},
				},
			})
		}

		// Handle tool_calls
		if tcRaw, ok := delta["tool_calls"].([]interface{}); ok {
			for _, tc := range tcRaw {
				tcMap, _ := tc.(map[string]interface{})
				if tcMap == nil {
					continue
				}

				tcIdxFloat, ok := tcMap["index"].(float64)
			if !ok {
				continue
			}
			tcIdx := int(tcIdxFloat)

				// Find or create accumulator for this tool call index
				var acc *toolCallAcc
				for _, a := range toolCallAccumulators {
					if a.index == tcIdx {
						acc = a
						break
					}
				}
				if acc == nil {
					acc = &toolCallAcc{index: tcIdx}
					toolCallAccumulators = append(toolCallAccumulators, acc)
				}

				// Track accumulators per tool call index
				if id, ok := tcMap["id"].(string); ok && id != "" {
					acc.id = id
				}
				if tcType, ok := tcMap["type"].(string); ok && tcType != "" {
					acc.tcType = tcType
				}
				if funcMap, ok := tcMap["function"].(map[string]interface{}); ok {
					if name, ok := funcMap["name"].(string); ok && name != "" {
						acc.name = name
					}
					if args, ok := funcMap["arguments"].(string); ok && args != "" {
						acc.args += args
					}
				}

				// Emit content_block_start for tool_use on first sighting of this tool call
				if !acc.started {
					acc.started = true
					acc.blockIndex = contentBlockIndex
					contentBlockIndex++

					writeAnthropicEvent(dst, "content_block_start", map[string]interface{}{
						"type":  "content_block_start",
						"index": acc.blockIndex,
						"content_block": map[string]interface{}{
							"type": "tool_use",
							"id":   acc.id,
							"name": acc.name,
							"input": map[string]interface{}{},
						},
					})
				}

				// Emit content_block_delta for input_json
				if acc.args != "" {
					writeAnthropicEvent(dst, "content_block_delta", map[string]interface{}{
						"type":  "content_block_delta",
						"index": acc.blockIndex,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": acc.args,
						},
					})
				}
			}
		}

		// Handle text content
		if content, ok := delta["content"].(string); ok && content != "" {
			if !contentBlockStarted {
				contentBlockStarted = true
				writeAnthropicEvent(dst, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				})
				contentBlockIndex++
			}
			writeAnthropicEvent(dst, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": contentBlockIndex - 1,
				"delta": map[string]string{
					"type": "text_delta",
					"text": content,
				},
			})
		}

		// Handle finish_reason — emit content_block_stop and message_delta
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			if contentBlockStarted {
				writeAnthropicEvent(dst, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": contentBlockIndex - 1,
				})
				contentBlockStarted = false
			}

			// Stop tool call accumulators
			for _, acc := range toolCallAccumulators {
				if acc.started {
					writeAnthropicEvent(dst, "content_block_stop", map[string]interface{}{
						"type":  "content_block_stop",
						"index": acc.blockIndex,
					})
					acc.started = false
				}
			}

			writeAnthropicEvent(dst, "message_delta", map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]string{
					"stop_reason": mapFinishReason(*choice.FinishReason),
				},
			})
		}
	}
	return scanner.Err()
}

// toolCallAcc tracks the state of an in-progress tool call during SSE conversion
type toolCallAcc struct {
	index      int
	blockIndex int
	started    bool
	id         string
	tcType     string
	name       string
	args       string
}

// ─────────────────────────────────────────────────────────────
//  SSE write helpers
// ─────────────────────────────────────────────────────────────

// writeSSEData writes a "data:" SSE line. Write errors are intentionally
// ignored — client disconnects are detected via streamCtx cancellation
// in the idleTimeoutReader, which stops the upstream read loop.
func writeSSEData(w io.Writer, data interface{}) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "data: %s\n\n", string(b))
}

func writeSSEDone(w io.Writer) {
	fmt.Fprintf(w, "data: [DONE]\n\n")
}

func writeAnthropicEvent(w io.Writer, event string, data interface{}) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", string(b))
}