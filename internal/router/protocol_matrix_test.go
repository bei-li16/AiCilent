package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"
	"github.com/gin-gonic/gin"
)

// This tests wire preservation/normalization against a loopback upstream, not
// whether SenseNova accepts each native Anthropic thinking setting.
func TestProtocolModelThinkingWireMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	models := []string{"kimi-k3", "glm-5.3", "deepseek-flash"}
	levels := []string{"omitted", "none", "minimal", "low", "medium", "high", "xhigh", "max"}
	protocols := []struct{ name, path, format, auth string }{
		{"chat", "/v1/chat/completions", "openai", "bearer"},
		{"responses", "/v1/responses", "openai", "bearer"},
		{"messages-bearer", "/v1/messages", "anthropic", "bearer"},
		{"messages-api-key", "/v1/messages", "anthropic", "x-api-key"},
	}
	for _, protocol := range protocols {
		settings := levels
		if protocol.format == "anthropic" {
			settings = append(append([]string(nil), levels...), "effort-none", "legacy-none", "both-none")
		}
		for _, model := range models {
			for _, streaming := range []bool{false, true} {
				for _, level := range settings {
					setting := level
					if protocol.format == "anthropic" {
						switch level {
						case "none":
							setting = "thinking-disabled"
						case "minimal":
							setting = "thinking-enabled-budget1024"
						default:
							if level != "omitted" {
								setting = "adaptive-effort-" + level
							}
						}
					}
					t.Run(fmt.Sprintf("%s/%s/stream=%t/%s", protocol.name, model, streaming, setting), func(t *testing.T) {
						input := matrixInput(protocol.name, level, streaming)
						captured := make(chan struct {
							header http.Header
							path   string
							body   map[string]any
						}, 4)
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							var body map[string]any
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								http.Error(w, err.Error(), http.StatusBadRequest)
								return
							}
							captured <- struct {
								header http.Header
								path   string
								body   map[string]any
							}{r.Header.Clone(), r.URL.Path, body}
							matrixReply(w, protocol.format, model, streaming)
						}))
						defer upstream.Close()
						base := "http://token.sensenova.cn"
						if protocol.format == "openai" {
							base += "/v1"
						}
						otherFormat := "openai"
						if protocol.format == "openai" {
							otherFormat = "anthropic"
						}
						cfg := &config.Config{
							Global: config.GlobalConfig{ReasoningEffortOnNone: "max", MaxStreamMinutes: 15},
							Providers: []config.Provider{
								{Name: "wrong-protocol", ModelID: model, Format: otherFormat, APIKey: "wrong-key", BaseURL: base, Priority: 1, Timeout: 5},
								{Name: "selected", ModelID: model, Format: protocol.format, AuthType: protocol.auth, APIKey: "provider-key", BaseURL: base, Priority: 1, Timeout: 5},
							},
						}
						collector := stats.New("")
						engine := NewEngine(cfg, "", io.Discard, io.Discard, collector)
						defer engine.CloseIdleConnections()
						engine.transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
							return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
						}
						router := gin.New()
						router.Use(middleware.Logger(io.Discard), middleware.DetectFormat())
						router.POST(protocol.path, engine.HandleRequest)
						encoded, err := json.Marshal(input)
						if err != nil {
							t.Fatal(err)
						}
						request := httptest.NewRequest(http.MethodPost, protocol.path, bytes.NewReader(encoded))
						request.Header.Set("Content-Type", "application/json")
						request.Header.Set("Authorization", "Bearer client-key-not-for-upstream")
						request.Header.Set("X-Api-Key", "client-key-not-for-upstream")
						response := httptest.NewRecorder()
						router.ServeHTTP(response, request)
						if response.Code != http.StatusOK {
							t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
						}
						if len(captured) != 1 {
							t.Fatalf("upstream calls=%d, want 1", len(captured))
						}
						got := <-captured
						wantPath := "/v1/chat/completions"
						if protocol.format == "anthropic" {
							wantPath = "/v1/messages"
						}
						if got.path != wantPath || got.header.Get("Content-Type") != "application/json" {
							t.Fatalf("path=%s headers=%v", got.path, got.header)
						}
						if id := response.Header().Get("X-Request-ID"); id == "" || got.header.Get("X-Request-ID") != id {
							t.Fatal("missing or mismatched correlation header")
						}
						if protocol.format == "anthropic" && got.header.Get("Anthropic-Version") != "2023-06-01" {
							t.Fatal("missing Anthropic version header regardless of auth mode")
						}
						if protocol.auth == "bearer" {
							if got.header.Get("Authorization") != "Bearer provider-key" || got.header.Get("X-Api-Key") != "" {
								t.Fatal("incorrect bearer credential selection")
							}
						} else if got.header.Get("Authorization") != "" || got.header.Get("X-Api-Key") != "provider-key" || got.header.Get("Anthropic-Version") != "2023-06-01" {
							t.Fatal("incorrect API-key authentication/version header")
						}
						if got.body["model"] != model || got.body["stream"] != streaming {
							t.Fatalf("model/stream changed: %v", got.body)
						}
						if protocol.format == "anthropic" {
							for _, field := range []string{"system", "messages", "tools", "thinking", "output_config", "max_tokens"} {
								want := input[field]
								if level == "none" || level == "effort-none" || level == "legacy-none" || level == "both-none" {
									if field == "thinking" {
										want = map[string]any{"type": "adaptive"}
									} else if field == "output_config" {
										want = map[string]any{"effort": "max"}
									}
								}
								if !reflect.DeepEqual(got.body[field], roundTripJSON(t, want)) {
									t.Fatalf("Anthropic %s changed: got=%v want=%v", field, got.body[field], want)
								}
							}
							if _, exists := got.body["reasoning_effort"]; exists {
								t.Fatal("OpenAI reasoning field injected into native Anthropic request")
							}
						} else {
							wantEffort := level
							if level == "omitted" {
								wantEffort = ""
								if _, exists := got.body["reasoning"]; exists {
									t.Fatal("thinking force-enabled for omitted effort")
								}
							} else {
								if level == "none" {
									wantEffort = "max"
								} else if model == "glm-5.3" {
									switch level {
									case "minimal", "medium":
										wantEffort = "low"
									case "xhigh":
										wantEffort = "max"
									}
								}
								if got.body["reasoning"] != true {
									t.Fatal("SenseNova reasoning flag not enabled")
								}
							}
							if actual, _ := got.body["reasoning_effort"].(string); actual != wantEffort {
								t.Fatalf("effort=%q want=%q", actual, wantEffort)
							}
							messages, _ := got.body["messages"].([]any)
							if len(messages) < 2 || messages[0].(map[string]any)["role"] != "system" {
								t.Fatalf("missing system/developer normalization: %v", messages)
							}
							if _, exists := got.body["input"]; exists {
								t.Fatal("Responses input was not converted to messages")
							}
						}
						if streaming {
							terminal := "[DONE]"
							if protocol.format == "anthropic" {
								terminal = "message_stop"
							} else if protocol.name == "responses" {
								terminal = "response.completed"
							}
							if !strings.Contains(response.Body.String(), terminal) || strings.Contains(response.Body.String(), "stream interrupted") {
								t.Fatalf("invalid completed stream: %s", response.Body.String())
							}
						} else if !strings.Contains(response.Body.String(), "pong") {
							t.Fatalf("missing nonstreaming answer: %s", response.Body.String())
						}
						if snap := collector.Snapshot(); snap.TotalReq != 1 || snap.TotalSuccess != 1 || snap.TotalClientSuccess != 1 || snap.TotalFail != 0 {
							t.Fatal("successful wire exchange counted incorrectly")
						}
					})
				}
			}
		}
	}
}

func matrixInput(protocol, level string, streaming bool) map[string]any {
	body := map[string]any{"model": "P1", "stream": streaming, "max_tokens": 4096}
	if strings.HasPrefix(protocol, "messages") {
		body["system"] = []any{map[string]any{"type": "text", "text": "instructions", "cache_control": map[string]any{"type": "ephemeral"}}}
		body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "ping"}}}}
		body["tools"] = []any{map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object"}}}
		switch level {
		case "omitted":
		case "none":
			body["thinking"] = map[string]any{"type": "disabled"}
		case "effort-none":
			body["output_config"] = map[string]any{"effort": "none"}
		case "legacy-none":
			body["reasoning_effort"] = "none"
		case "both-none":
			body["thinking"] = map[string]any{"type": "disabled"}
			body["output_config"] = map[string]any{"effort": "none"}
		case "minimal":
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": 1024}
		default:
			body["thinking"] = map[string]any{"type": "adaptive"}
			body["output_config"] = map[string]any{"effort": level}
		}
		return body
	}
	body["messages"] = []any{map[string]any{"role": "developer", "content": "instructions"}, map[string]any{"role": "user", "content": "ping"}}
	if protocol == "responses" {
		delete(body, "messages")
		body["instructions"] = "instructions"
		body["input"] = "ping"
		if level != "omitted" {
			body["reasoning"] = map[string]any{"effort": level}
		}
	} else if level != "omitted" {
		body["reasoning_effort"] = level
	}
	return body
}

func roundTripJSON(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func matrixReply(w http.ResponseWriter, format, model string, streaming bool) {
	if !streaming {
		w.Header().Set("Content-Type", "application/json")
		if format == "anthropic" {
			fmt.Fprintf(w, `{"id":"mock","model":%q,"type":"message","role":"assistant","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn"}`, model)
		} else {
			fmt.Fprintf(w, `{"id":"mock","model":%q,"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`, model)
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if format == "anthropic" {
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"mock\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"pong\"}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	} else {
		fmt.Fprintf(w, "data: {\"id\":\"mock\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"pong\"},\"finish_reason\":null}]}\n\n", model)
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}
}
