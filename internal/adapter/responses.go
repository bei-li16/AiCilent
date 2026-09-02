package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// convertResponsesRequestToOpenAI adapts the OpenAI Responses request shape
// to the Chat Completions shape accepted by the existing OpenAI adapters.
// Unsupported Responses-only execution controls are deliberately removed;
// provider routing and retry behavior remain owned by the proxy.
func convertResponsesRequestToOpenAI(raw map[string]interface{}) ([]byte, error) {
	input, hasInput := raw["input"]
	instructions := raw["instructions"]
	maxOutputTokens := raw["max_output_tokens"]

	delete(raw, "input")
	delete(raw, "instructions")
	delete(raw, "max_output_tokens")
	delete(raw, "previous_response_id")
	delete(raw, "conversation")
	delete(raw, "store")
	delete(raw, "background")
	delete(raw, "include")
	delete(raw, "truncation")
	delete(raw, "prompt_cache_key")
	delete(raw, "safety_identifier")
	delete(raw, "service_tier")
	delete(raw, "text")
	if reasoning, ok := raw["reasoning"].(map[string]interface{}); ok {
		if effort, ok := reasoning["effort"].(string); ok && effort != "" {
			raw["reasoning_effort"] = effort
		}
	}
	delete(raw, "reasoning")

	if maxOutputTokens != nil {
		raw["max_tokens"] = maxOutputTokens
	}

	messages := make([]interface{}, 0)
	if instructions != nil {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": responsesContentToChatContent(instructions),
		})
	}
	if hasInput {
		messages = append(messages, responsesInputToMessages(input)...)
	}
	if len(messages) > 0 {
		raw["messages"] = messages
	} else if _, ok := raw["messages"]; !ok {
		raw["messages"] = []interface{}{}
	}

	convertResponsesToolsToOpenAI(raw)
	convertResponsesToolChoiceToOpenAI(raw)
	return json.Marshal(raw)
}

func responsesInputToMessages(input interface{}) []interface{} {
	switch value := input.(type) {
	case string:
		return []interface{}{map[string]interface{}{"role": "user", "content": value}}
	case []interface{}:
		messages := make([]interface{}, 0, len(value))
		for _, item := range value {
			converted := responsesInputItemToMessages(item)
			messages = append(messages, converted...)
		}
		return messages
	case map[string]interface{}:
		return responsesInputItemToMessages(value)
	default:
		return nil
	}
}

func responsesInputItemToMessages(item interface{}) []interface{} {
	entry, ok := item.(map[string]interface{})
	if !ok {
		return nil
	}

	typ, _ := entry["type"].(string)
	switch typ {
	case "function_call":
		id, _ := entry["call_id"].(string)
		if id == "" {
			id, _ = entry["id"].(string)
		}
		name, _ := entry["name"].(string)
		arguments, _ := entry["arguments"].(string)
		return []interface{}{map[string]interface{}{
			"role": "assistant",
			"tool_calls": []interface{}{map[string]interface{}{
				"id":   id,
				"type": "function",
				"function": map[string]interface{}{
					"name":      name,
					"arguments": arguments,
				},
			}},
		}}
	case "function_call_output":
		id, _ := entry["call_id"].(string)
		output := entry["output"]
		return []interface{}{map[string]interface{}{
			"role":         "tool",
			"tool_call_id": id,
			"content":      responsesContentToChatContent(output),
		}}
	case "reasoning", "item_reference":
		return nil
	}

	role, _ := entry["role"].(string)
	if role == "" {
		role = "user"
	}
	if role == "developer" {
		role = "system"
	}
	content, exists := entry["content"]
	if !exists {
		content = entry["text"]
	}
	return []interface{}{map[string]interface{}{
		"role":    role,
		"content": responsesContentToChatContent(content),
	}}
}

func responsesContentToChatContent(content interface{}) interface{} {
	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case []interface{}:
		parts := make([]interface{}, 0, len(value))
		for _, rawPart := range value {
			part, ok := rawPart.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := part["type"].(string)
			switch typ {
			case "input_text", "output_text", "text":
				text, _ := part["text"].(string)
				parts = append(parts, map[string]interface{}{"type": "text", "text": text})
			case "input_image":
				if img := inputImageToChatImage(part); img != nil {
					parts = append(parts, img)
				}
			}
		}
		if len(parts) == 1 {
			if part, ok := parts[0].(map[string]interface{}); ok && part["type"] == "text" {
				return part["text"]
			}
		}
		return parts
	default:
		return fmt.Sprint(value)
	}
}

// inputImageToChatImage converts a Responses input_image part into the Chat
// Completions image_url shape. image_url arrives as a plain string from most
// Responses clients but as an object {"url": ...} from some Chat-style ones;
// both are accepted, and detail is preserved when present. Returns nil when
// the part carries no usable URL.
func inputImageToChatImage(part map[string]interface{}) map[string]interface{} {
	var imageURL string
	switch v := part["image_url"].(type) {
	case string:
		imageURL = v
	case map[string]interface{}:
		imageURL, _ = v["url"].(string)
	}
	if imageURL == "" {
		return nil
	}
	image := map[string]interface{}{"url": imageURL}
	if detail, ok := part["detail"].(string); ok && detail != "" {
		image["detail"] = detail
	}
	return map[string]interface{}{
		"type":      "image_url",
		"image_url": image,
	}
}

func convertResponsesToolsToOpenAI(raw map[string]interface{}) {
	tools, ok := raw["tools"].([]interface{})
	if !ok {
		return
	}
	converted := make([]interface{}, 0, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]interface{})
		if !ok {
			continue
		}
		if typ, _ := tool["type"].(string); typ != "function" {
			// Chat Completions providers cannot execute Responses-hosted tools
			// such as web_search; omit those instead of sending invalid JSON.
			continue
		}
		if _, alreadyNested := tool["function"]; alreadyNested {
			converted = append(converted, tool)
			continue
		}
		fn := map[string]interface{}{}
		for _, key := range []string{"name", "description", "parameters", "strict"} {
			if value, exists := tool[key]; exists {
				if key == "parameters" {
					fn[key] = value
				} else {
					fn[key] = value
				}
			}
		}
		converted = append(converted, map[string]interface{}{
			"type":     "function",
			"function": fn,
		})
	}
	raw["tools"] = converted
}

func convertResponsesToolChoiceToOpenAI(raw map[string]interface{}) {
	choice, ok := raw["tool_choice"].(map[string]interface{})
	if !ok {
		return
	}
	if typ, _ := choice["type"].(string); typ != "function" {
		return
	}
	if name, ok := choice["name"].(string); ok && name != "" {
		raw["tool_choice"] = map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": name},
		}
	}
}

func convertOpenAIResponseToResponses(body []byte, model string) ([]byte, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal chat response: %w", err)
	}

	responseID, _ := raw["id"].(string)
	if responseID == "" {
		responseID = "resp_proxy"
	}
	createdAt := int64(0)
	if created, ok := raw["created"].(float64); ok {
		createdAt = int64(created)
	}
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}
	if model == "" {
		model, _ = raw["model"].(string)
	}

	output, outputText := responsesOutputFromChoices(raw["choices"])
	response := map[string]interface{}{
		"id":          responseID,
		"object":      "response",
		"created_at":  createdAt,
		"status":      "completed",
		"model":       model,
		"output":      output,
		"output_text": outputText,
	}
	if usage := responsesUsage(raw["usage"]); usage != nil {
		response["usage"] = usage
	}
	return json.Marshal(response)
}

func responsesOutputFromChoices(rawChoices interface{}) ([]interface{}, string) {
	choices, _ := rawChoices.([]interface{})
	if len(choices) == 0 {
		return []interface{}{}, ""
	}
	choice, _ := choices[0].(map[string]interface{})
	message, _ := choice["message"].(map[string]interface{})
	if message == nil {
		return []interface{}{}, ""
	}

	output := make([]interface{}, 0, 2)
	var outputText strings.Builder
	if content, ok := message["content"].(string); ok && content != "" {
		outputText.WriteString(content)
		output = append(output, map[string]interface{}{
			"id":     "msg_proxy",
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []interface{}{map[string]interface{}{
				"type":        "output_text",
				"text":        content,
				"annotations": []interface{}{},
			}},
		})
	}
	if toolCalls, ok := message["tool_calls"].([]interface{}); ok {
		for _, rawToolCall := range toolCalls {
			toolCall, _ := rawToolCall.(map[string]interface{})
			if toolCall == nil {
				continue
			}
			fn, _ := toolCall["function"].(map[string]interface{})
			if fn == nil {
				continue
			}
			id, _ := toolCall["id"].(string)
			name, _ := fn["name"].(string)
			arguments, _ := fn["arguments"].(string)
			output = append(output, map[string]interface{}{
				"id":        id,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   id,
				"name":      name,
				"arguments": arguments,
			})
		}
	}
	return output, outputText.String()
}

func responsesUsage(rawUsage interface{}) map[string]interface{} {
	usage, _ := rawUsage.(map[string]interface{})
	if usage == nil {
		return nil
	}
	input, _ := usage["prompt_tokens"].(float64)
	if input == 0 {
		input, _ = usage["input_tokens"].(float64)
	}
	output, _ := usage["completion_tokens"].(float64)
	if output == 0 {
		output, _ = usage["output_tokens"].(float64)
	}
	total, _ := usage["total_tokens"].(float64)
	if total == 0 {
		total = input + output
	}
	return map[string]interface{}{
		"input_tokens":  int(input),
		"output_tokens": int(output),
		"total_tokens":  int(total),
	}
}

type responsesStreamState struct {
	responseID      string
	model           string
	createdAt       int64
	sequenceNumber  int
	nextOutputIndex int
	message         map[string]interface{}
	messageIndex    int
	messageText     strings.Builder
	contentStarted  bool
	functionCalls   map[int]*responsesStreamFunction
	output          []interface{}
	usage           map[string]interface{}
}

type responsesStreamFunction struct {
	index       int
	outputIndex int
	item        map[string]interface{}
	arguments   strings.Builder
	started     bool
}

func convertOpenAISSEToResponses(src io.Reader, dst io.Writer) error {
	state := &responsesStreamState{functionCalls: make(map[int]*responsesStreamFunction)}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 4096), 1048576)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			state.finish(dst)
			continue
		}
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		state.consumeChunk(chunk, dst)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if state.responseID != "" {
		state.finish(dst)
	}
	return nil
}

func (s *responsesStreamState) consumeChunk(chunk map[string]interface{}, dst io.Writer) {
	if s.responseID == "" {
		s.responseID, _ = chunk["id"].(string)
		if s.responseID == "" {
			s.responseID = "resp_proxy"
		}
		s.model, _ = chunk["model"].(string)
		s.createdAt = int64FromNumber(chunk["created"])
		if s.createdAt == 0 {
			s.createdAt = time.Now().Unix()
		}
		s.writeEvent(dst, "response.created", map[string]interface{}{"response": s.baseResponse("in_progress")})
		s.writeEvent(dst, "response.in_progress", map[string]interface{}{"response": s.baseResponse("in_progress")})
	}
	if rawUsage, ok := chunk["usage"].(map[string]interface{}); ok {
		s.usage = responsesUsage(rawUsage)
	}
	choices, _ := chunk["choices"].([]interface{})
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]interface{})
	delta, _ := choice["delta"].(map[string]interface{})
	if delta == nil {
		return
	}
	if _, ok := delta["role"]; ok {
		s.ensureMessage(dst)
	}
	if content, ok := delta["content"].(string); ok && content != "" {
		s.ensureMessage(dst)
		if !s.contentStarted {
			s.contentStarted = true
			s.writeEvent(dst, "response.content_part.added", map[string]interface{}{
				"item_id": s.message["id"], "output_index": s.messageIndex, "content_index": 0,
				"part": map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}, "logprobs": []interface{}{}},
			})
		}
		s.messageText.WriteString(content)
		s.writeEvent(dst, "response.output_text.delta", map[string]interface{}{
			"item_id": s.message["id"], "output_index": s.messageIndex, "content_index": 0,
			"delta": content, "logprobs": []interface{}{},
		})
	}
	if rawTools, ok := delta["tool_calls"].([]interface{}); ok {
		for _, rawTool := range rawTools {
			s.consumeToolCall(rawTool, dst)
		}
	}
	if finish, _ := choice["finish_reason"].(string); finish != "" {
		s.closeMessage(dst)
		for _, function := range s.functionCalls {
			s.closeFunction(dst, function)
		}
	}
}

func (s *responsesStreamState) ensureMessage(dst io.Writer) {
	if s.message != nil {
		return
	}
	s.messageIndex = s.nextOutputIndex
	s.nextOutputIndex++
	s.message = map[string]interface{}{"id": "msg_" + s.responseID, "type": "message", "status": "in_progress", "role": "assistant", "content": []interface{}{}}
	s.output = append(s.output, s.message)
	s.writeEvent(dst, "response.output_item.added", map[string]interface{}{"output_index": s.messageIndex, "item": s.message})
}

func (s *responsesStreamState) consumeToolCall(rawTool interface{}, dst io.Writer) {
	tool, _ := rawTool.(map[string]interface{})
	if tool == nil {
		return
	}
	index := intFromNumber(tool["index"])
	function := s.functionCalls[index]
	if function == nil {
		function = &responsesStreamFunction{index: index, outputIndex: s.nextOutputIndex}
		s.nextOutputIndex++
		id, _ := tool["id"].(string)
		if id == "" {
			id = fmt.Sprintf("call_%s_%d", s.responseID, index)
		}
		function.item = map[string]interface{}{"id": id, "type": "function_call", "status": "in_progress", "call_id": id, "name": "", "arguments": ""}
		function.started = true
		s.functionCalls[index] = function
		s.output = append(s.output, function.item)
		s.writeEvent(dst, "response.output_item.added", map[string]interface{}{"output_index": function.outputIndex, "item": function.item})
	}
	fn, _ := tool["function"].(map[string]interface{})
	if fn == nil {
		return
	}
	if name, ok := fn["name"].(string); ok && name != "" {
		function.item["name"] = name
	}
	if args, ok := fn["arguments"].(string); ok && args != "" {
		function.arguments.WriteString(args)
		function.item["arguments"] = function.arguments.String()
		s.writeEvent(dst, "response.function_call_arguments.delta", map[string]interface{}{
			"item_id": function.item["id"], "output_index": function.outputIndex, "delta": args,
		})
	}
}

func (s *responsesStreamState) closeMessage(dst io.Writer) {
	if s.message == nil || s.message["status"] == "completed" {
		return
	}
	if s.contentStarted {
		s.writeEvent(dst, "response.output_text.done", map[string]interface{}{"item_id": s.message["id"], "output_index": s.messageIndex, "content_index": 0, "text": s.messageText.String()})
		content := []interface{}{map[string]interface{}{"type": "output_text", "text": s.messageText.String(), "annotations": []interface{}{}, "logprobs": []interface{}{}}}
		s.message["content"] = content
		s.writeEvent(dst, "response.content_part.done", map[string]interface{}{"item_id": s.message["id"], "output_index": s.messageIndex, "content_index": 0, "part": content[0]})
	}
	s.message["status"] = "completed"
	s.writeEvent(dst, "response.output_item.done", map[string]interface{}{"output_index": s.messageIndex, "item": s.message})
}

func (s *responsesStreamState) closeFunction(dst io.Writer, function *responsesStreamFunction) {
	if function == nil || !function.started {
		return
	}
	function.started = false
	function.item["status"] = "completed"
	s.writeEvent(dst, "response.function_call_arguments.done", map[string]interface{}{"item_id": function.item["id"], "output_index": function.outputIndex, "name": function.item["name"], "arguments": function.arguments.String()})
	s.writeEvent(dst, "response.output_item.done", map[string]interface{}{"output_index": function.outputIndex, "item": function.item})
}

func (s *responsesStreamState) finish(dst io.Writer) {
	if s.responseID == "" || s.output == nil {
		return
	}
	s.closeMessage(dst)
	for _, function := range s.functionCalls {
		s.closeFunction(dst, function)
	}
	response := s.baseResponse("completed")
	response["output"] = s.output
	response["output_text"] = s.messageText.String()
	if s.usage != nil {
		response["usage"] = s.usage
	}
	s.writeEvent(dst, "response.completed", map[string]interface{}{"response": response})
	s.responseID = ""
}

func (s *responsesStreamState) baseResponse(status string) map[string]interface{} {
	return map[string]interface{}{"id": s.responseID, "object": "response", "created_at": s.createdAt, "status": status, "model": s.model, "output": []interface{}{}}
}

func (s *responsesStreamState) writeEvent(dst io.Writer, event string, payload map[string]interface{}) {
	payload["type"] = event
	payload["sequence_number"] = s.sequenceNumber
	s.sequenceNumber++
	b, _ := json.Marshal(payload)
	fmt.Fprintf(dst, "event: %s\ndata: %s\n\n", event, b)
}

func int64FromNumber(value interface{}) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	case json.Number:
		parsed, _ := strconv.ParseInt(string(number), 10, 64)
		return parsed
	default:
		return 0
	}
}

func intFromNumber(value interface{}) int {
	return int(int64FromNumber(value))
}
