package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

// TestNormalizeDeveloperRoleRewritesOnlyDeveloperRole pins the blast radius of
// the rewrite: developer becomes system, every other role and all other message
// fields stay exactly as the client sent them.
func TestNormalizeDeveloperRoleRewritesOnlyDeveloperRole(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[
		{"role":"developer","content":"instructions"},
		{"role":"system","content":"sys"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"out"}
	],"stream":false}`)

	got := normalizeDeveloperRole(body)

	var decoded struct {
		Model    string                   `json:"model"`
		Stream   bool                     `json:"stream"`
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("normalized body is not valid JSON: %v", err)
	}

	if decoded.Model != "glm-5.2" || decoded.Stream {
		t.Fatalf("unrelated top-level fields changed: model=%q stream=%v", decoded.Model, decoded.Stream)
	}
	if len(decoded.Messages) != 5 {
		t.Fatalf("message count = %d, want 5 (the rewrite must not add or drop messages)", len(decoded.Messages))
	}

	roles := make([]string, 0, len(decoded.Messages))
	for _, message := range decoded.Messages {
		role, _ := message["role"].(string)
		roles = append(roles, role)
	}
	want := []string{"system", "system", "user", "assistant", "tool"}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}

	if decoded.Messages[0]["content"] != "instructions" {
		t.Fatalf("developer content = %#v, want instructions", decoded.Messages[0]["content"])
	}
	if _, exists := decoded.Messages[3]["tool_calls"]; !exists {
		t.Fatal("assistant tool_calls were dropped")
	}
	if decoded.Messages[4]["tool_call_id"] != "call_1" {
		t.Fatalf("tool_call_id = %#v, want call_1", decoded.Messages[4]["tool_call_id"])
	}
}

func TestNormalizeDeveloperRoleIsIdempotent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"developer","content":"a"},{"role":"user","content":"b"}]}`)

	once := normalizeDeveloperRole(body)
	twice := normalizeDeveloperRole(once)

	if string(once) != string(twice) {
		t.Fatalf("second pass changed the body:\n once = %s\n twice = %s", once, twice)
	}
}

// TestNormalizeDeveloperRoleLeavesUnchangedBodies covers the inputs where the
// function must return the original bytes rather than risk breaking a request.
func TestNormalizeDeveloperRoleLeavesUnchangedBodies(t *testing.T) {
	cases := map[string]string{
		"invalid json":       `{"messages":[`,
		"no messages key":    `{"model":"glm-5.2","input":"hi"}`,
		"messages not array": `{"messages":"not-an-array"}`,
		"empty messages":     `{"messages":[]}`,
		"non-object entry":   `{"messages":["not-an-object"]}`,
		"role is not string": `{"messages":[{"role":123}]}`,
		"missing role":       `{"messages":[{"content":"hi"}]}`,
		"no developer role":  `{"messages":[{"role":"system","content":"hi"},{"role":"user","content":"yo"}]}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if got := normalizeDeveloperRole([]byte(body)); string(got) != body {
				t.Fatalf("body was modified: got %s, want %s", got, body)
			}
		})
	}
}

// TestHandleRequestNormalizesDeveloperRoleForUpstream is the regression test for
// the production failure: a client sending role "developer" made every glm-5.2
// account answer 400 invalid_parameter_error. It asserts what the upstream
// actually receives, so a misplaced hook point fails here.
func TestHandleRequestNormalizesDeveloperRoleForUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var (
		mu       sync.Mutex
		received string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = string(raw)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Global: config.GlobalConfig{CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1},
		Providers: []config.Provider{{
			Name:     "sensenova1-glm-5.2",
			ModelID:  "glm-5.2",
			APIKey:   "key",
			BaseURL:  upstream.URL + "/v1",
			Priority: 1,
			Format:   "openai",
			Timeout:  5,
		}},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	router := gin.New()
	router.Use(middleware.DetectFormat())
	router.POST("/chat/completions", e.HandleRequest)

	// Same shape as the request that failed in production.
	requestBody := `{"model":"glm-5.2","messages":[
		{"role":"developer","content":"You are an AI agent powered by DeepSeek Harness."},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"result"}
	]}`

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(requestBody))
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("client status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	mu.Lock()
	upstreamBody := received
	mu.Unlock()
	if upstreamBody == "" {
		t.Fatal("upstream received no request")
	}

	var decoded struct {
		Model    string                   `json:"model"`
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &decoded); err != nil {
		t.Fatalf("upstream body is not JSON: %v (%s)", err, upstreamBody)
	}
	if decoded.Model != "glm-5.2" {
		t.Fatalf("upstream model = %q, want glm-5.2", decoded.Model)
	}

	roles := make([]string, 0, len(decoded.Messages))
	for _, message := range decoded.Messages {
		role, _ := message["role"].(string)
		roles = append(roles, role)
	}
	want := []string{"system", "user", "assistant", "tool"}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("upstream roles = %v, want %v", roles, want)
	}
	if decoded.Messages[0]["content"] != "You are an AI agent powered by DeepSeek Harness." {
		t.Fatalf("upstream first content = %#v", decoded.Messages[0]["content"])
	}
}
