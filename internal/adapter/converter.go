package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func OpenAIRequestToAnthropic(openAIReq OpenAIRequest) AnthropicRequest {
	anthropicReq := AnthropicRequest{
		Model:         openAIReq.Model,
		MaxTokens:     openAIReq.MaxTokens,
		Temperature:   openAIReq.Temperature,
		TopP:          openAIReq.TopP,
		Stream:        openAIReq.Stream,
		StopSequences: openAIReq.Stop,
	}

	var systemMsg string
	for _, msg := range openAIReq.Messages {
		if msg.Role == "system" {
			systemMsg = msg.Content
			continue
		}
		anthropicReq.Messages = append(anthropicReq.Messages, AnthropicMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	anthropicReq.System = systemMsg

	return anthropicReq
}

func AnthropicResponseToOpenAI(anthropicResp AnthropicResponse, model string) OpenAIResponse {
	oaResp := OpenAIResponse{
		ID:     anthropicResp.ID,
		Object: "chat.completion",
		Model:  model,
		Choices: []Choice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: "",
				},
				FinishReason: mapStopReason(anthropicResp.StopReason),
			},
		},
	}

	if len(anthropicResp.Content) > 0 {
		oaResp.Choices[0].Message.Content = anthropicResp.Content[0].Text
	}

	if anthropicResp.Usage != nil {
		oaResp.Usage = Usage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		}
	}

	return oaResp
}

func AnthropicRequestToOpenAI(anthropicReq AnthropicRequest) OpenAIRequest {
	oaReq := OpenAIRequest{
		Model:       anthropicReq.Model,
		MaxTokens:   anthropicReq.MaxTokens,
		Temperature: anthropicReq.Temperature,
		TopP:        anthropicReq.TopP,
		Stream:      anthropicReq.Stream,
		Stop:        anthropicReq.StopSequences,
	}

	if anthropicReq.System != "" {
		oaReq.Messages = append(oaReq.Messages, OpenAIMessage{
			Role:    "system",
			Content: anthropicReq.System,
		})
	}
	for _, msg := range anthropicReq.Messages {
		oaReq.Messages = append(oaReq.Messages, OpenAIMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return oaReq
}

func OpenAIResponseToAnthropic(oaResp OpenAIResponse, model string) AnthropicResponse {
	anthropicResp := AnthropicResponse{
		ID:    oaResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
		StopReason: mapFinishReason(oaResp.Choices[0].FinishReason),
		Usage: &AnthropicUsage{
			InputTokens:  oaResp.Usage.PromptTokens,
			OutputTokens: oaResp.Usage.CompletionTokens,
		},
	}

	if len(oaResp.Choices) > 0 {
		anthropicResp.Content = []AnthropicContent{
			{
				Type: "text",
				Text: oaResp.Choices[0].Message.Content,
			},
		}
	}

	return anthropicResp
}

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

func ConvertRequest(body []byte, fromFormat, toFormat string) ([]byte, error) {
	if fromFormat == toFormat {
		return body, nil
	}

	if fromFormat == "openai" && toFormat == "anthropic" {
		var oaReq OpenAIRequest
		if err := json.Unmarshal(body, &oaReq); err != nil {
			return nil, fmt.Errorf("unmarshal openai request: %w", err)
		}
		anthropicReq := OpenAIRequestToAnthropic(oaReq)
		return json.Marshal(anthropicReq)
	}

	if fromFormat == "anthropic" && toFormat == "openai" {
		var anthropicReq AnthropicRequest
		if err := json.Unmarshal(body, &anthropicReq); err != nil {
			return nil, fmt.Errorf("unmarshal anthropic request: %w", err)
		}
		oaReq := AnthropicRequestToOpenAI(anthropicReq)
		return json.Marshal(oaReq)
	}

	return nil, fmt.Errorf("unsupported format conversion: %s -> %s", fromFormat, toFormat)
}

func ConvertResponse(body []byte, fromFormat, toFormat, model string) ([]byte, error) {
	if fromFormat == toFormat {
		return body, nil
	}

	if fromFormat == "anthropic" && toFormat == "openai" {
		var anthropicResp AnthropicResponse
		if err := json.Unmarshal(body, &anthropicResp); err != nil {
			return nil, fmt.Errorf("unmarshal anthropic response: %w", err)
		}
		oaResp := AnthropicResponseToOpenAI(anthropicResp, model)
		return json.Marshal(oaResp)
	}

	if fromFormat == "openai" && toFormat == "anthropic" {
		var oaResp OpenAIResponse
		if err := json.Unmarshal(body, &oaResp); err != nil {
			return nil, fmt.Errorf("unmarshal openai response: %w", err)
		}
		anthropicResp := OpenAIResponseToAnthropic(oaResp, model)
		return json.Marshal(anthropicResp)
	}

	return nil, fmt.Errorf("unsupported format conversion: %s -> %s", fromFormat, toFormat)
}

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

func convertAnthropicSSEToOpenAI(src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 4096), 1048576)

	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "[DONE]" {
				continue
			}

			switch currentEvent {
			case "message_start":
				var msg struct {
					Type  string `json:"type"`
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

			case "content_block_delta":
				var delta struct {
					Delta struct {
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(dataStr), &delta); err == nil && delta.Delta.Text != "" {
					oaChunk := map[string]interface{}{
						"choices": []map[string]interface{}{
							{
								"index": 0,
								"delta": map[string]string{"content": delta.Delta.Text},
							},
						},
					}
					writeSSEData(dst, oaChunk)
				}

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
				if err := json.Unmarshal([]byte(dataStr), &delta); err == nil {
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
				}

			case "message_stop":
				writeSSEDone(dst)
			}
		}
	}
	return scanner.Err()
}

func convertOpenAISSETOAnthropic(src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 4096), 1048576)

	var roleWritten bool
	var contentBlockStarted bool
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
				Index        int `json:"index"`
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

		if role, ok := delta["role"].(string); ok && !roleWritten {
			roleWritten = true
			msgID := fmt.Sprintf("msg_%d", len(dataStr)%100000)
			writeAnthropicEvent(dst, "message_start", map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":   msgID,
					"type": "message",
					"role": role,
					"content": []interface{}{},
				},
			})
			writeAnthropicEvent(dst, "content_block_start", map[string]interface{}{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]interface{}{
					"type": "text",
					"text": "",
				},
			})
			contentBlockStarted = true
		}

		if content, ok := delta["content"].(string); ok && content != "" {
			writeAnthropicEvent(dst, "content_block_delta", map[string]interface{}{
				"type": "content_block_delta",
				"index": 0,
				"delta": map[string]string{
					"type": "text_delta",
					"text": content,
				},
			})
		}

		if choice.FinishReason != nil && *choice.FinishReason != "" {
			if contentBlockStarted {
				writeAnthropicEvent(dst, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": 0,
				})
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