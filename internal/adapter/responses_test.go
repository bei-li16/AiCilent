package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesRequestConvertsToChatCompletions(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","instructions":"Be concise","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"max_output_tokens":123,"reasoning":{"effort":"high"},"tool_choice":{"type":"function","name":"lookup"},"tools":[{"type":"function","name":"lookup","description":"Find data","parameters":{"type":"object"}}],"stream":true}`)
	converted, err := ConvertRequest(body, "responses", "openai")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(converted, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5-codex" || got["max_tokens"] != float64(123) || got["stream"] != true {
		t.Fatalf("unexpected top-level conversion: %#v", got)
	}
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", got["reasoning_effort"])
	}
	if _, exists := got["input"]; exists {
		t.Fatal("Responses input leaked into Chat Completions request")
	}
	messages, _ := got["messages"].([]interface{})
	if len(messages) != 2 {
		t.Fatalf("messages length = %d, want 2", len(messages))
	}
	if messages[0].(map[string]interface{})["role"] != "system" || messages[1].(map[string]interface{})["role"] != "user" {
		t.Fatalf("unexpected message roles: %#v", messages)
	}
	tools, _ := got["tools"].([]interface{})
	tool := tools[0].(map[string]interface{})
	if tool["type"] != "function" || tool["function"].(map[string]interface{})["name"] != "lookup" {
		t.Fatalf("unexpected tool conversion: %#v", tool)
	}
	choice := got["tool_choice"].(map[string]interface{})
	if choice["function"].(map[string]interface{})["name"] != "lookup" {
		t.Fatalf("unexpected tool_choice conversion: %#v", choice)
	}
}

func TestChatCompletionsResponseConvertsToResponses(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","created":1700000000,"model":"gpt-5-codex","choices":[{"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}`)
	converted, err := ConvertResponse(body, "openai", "responses", "gpt-5-codex")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(converted, &got); err != nil {
		t.Fatal(err)
	}
	if got["object"] != "response" || got["status"] != "completed" || got["created_at"] != float64(1700000000) {
		t.Fatalf("unexpected response envelope: %#v", got)
	}
	output, _ := got["output"].([]interface{})
	if len(output) != 2 {
		t.Fatalf("output length = %d, want 2", len(output))
	}
	if output[0].(map[string]interface{})["type"] != "message" || output[1].(map[string]interface{})["type"] != "function_call" {
		t.Fatalf("unexpected output items: %#v", output)
	}
	if got["output_text"] != "hello" {
		t.Fatalf("output_text = %#v, want hello", got["output_text"])
	}
}

func TestChatCompletionsSSEConvertsToResponsesEvents(t *testing.T) {
	upstream := strings.NewReader("data: {\"id\":\"chatcmpl-1\",\"created\":1700000000,\"model\":\"gpt-5-codex\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n")
	var output strings.Builder
	if err := StreamConvertResponse(upstream, &output, "openai", "responses"); err != nil {
		t.Fatal(err)
	}
	result := output.String()
	for _, event := range []string{"response.created", "response.output_text.delta", "response.output_text.done", "response.completed"} {
		if !strings.Contains(result, "event: "+event) {
			t.Fatalf("missing %s in converted stream: %s", event, result)
		}
	}
	if !strings.Contains(result, `"delta":"hel"`) || !strings.Contains(result, `"delta":"lo"`) {
		t.Fatalf("text deltas missing from converted stream: %s", result)
	}
}
