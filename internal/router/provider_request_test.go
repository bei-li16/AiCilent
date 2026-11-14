package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

func TestPrepareProviderRequestNormalizesSensenovaReasoning(t *testing.T) {
	provider := &config.Provider{
		ModelID: "glm-5.2",
		Format:  "openai",
		BaseURL: "https://token.sensenova.cn/v1",
	}

	got, from, to := prepareProviderRequest([]byte(`{"model":"Max","reasoning_effort":"xhigh"}`), provider, "")
	if from != "" || to != "" {
		t.Fatalf("unexpected effort change %q → %q", from, to)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "glm-5.2" {
		t.Fatalf("model = %#v, want glm-5.2", body["model"])
	}
	if body["reasoning_effort"] != "xhigh" {
		t.Fatalf("reasoning_effort = %#v, want xhigh (xhigh must not be remapped; models that reject max would 400)", body["reasoning_effort"])
	}
	if body["reasoning"] != true {
		t.Fatalf("reasoning = %#v, want true", body["reasoning"])
	}
}

func TestPrepareProviderRequestLeavesNonSensenovaReasoningValue(t *testing.T) {
	provider := &config.Provider{
		ModelID: "other-model",
		Format:  "openai",
		BaseURL: "https://api.example.com/v1",
	}

	got, from, to := prepareProviderRequest([]byte(`{"model":"Max","reasoning_effort":"xhigh"}`), provider, "")
	if from != "" || to != "" {
		t.Fatalf("unexpected effort change %q → %q", from, to)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "other-model" {
		t.Fatalf("model = %#v, want other-model", body["model"])
	}
	if body["reasoning_effort"] != "xhigh" {
		t.Fatalf("reasoning_effort = %#v, want xhigh", body["reasoning_effort"])
	}
	if _, exists := body["reasoning"]; exists {
		t.Fatal("non-Sensenova provider should not receive a reasoning field")
	}
}

func TestPrepareProviderRequestDropsSensenovaNoneReasoningEffort(t *testing.T) {
	provider := &config.Provider{
		ModelID: "glm-5.3",
		Format:  "openai",
		BaseURL: "https://token.sensenova.cn/v1",
	}

	got, from, to := prepareProviderRequest([]byte(`{"model":"P2","reasoning_effort":" none "}`), provider, "")
	if from != " none " || to != "" {
		t.Fatalf("effort change = %q → %q, want removal", from, to)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatal("reasoning_effort \"none\" must be dropped: glm-5.3 rejects it with 400")
	}
	if _, exists := body["reasoning"]; exists {
		t.Fatal("reasoning must not be force-enabled when reasoning_effort is dropped")
	}
	if body["model"] != "glm-5.3" {
		t.Fatalf("model = %#v, want glm-5.3", body["model"])
	}
}

func TestPrepareProviderRequestMapsGLM53Effort(t *testing.T) {
	provider := &config.Provider{
		ModelID: "glm-5.3",
		Format:  "openai",
		BaseURL: "https://token.sensenova.cn/v1",
	}
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"minimal", "low"}, {"medium", "low"}, {"xhigh", "max"},
		{"ultra", "max"}, {"unexpected", "max"}, {" HIGH ", "high"},
		{"low", "low"}, {"high", "high"}, {"max", "max"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"model": "P2", "reasoning_effort": tc.input})
			if err != nil {
				t.Fatal(err)
			}
			got, from, to := prepareProviderRequest(payload, provider, "")
			var body map[string]interface{}
			if err := json.Unmarshal(got, &body); err != nil {
				t.Fatal(err)
			}
			if body["reasoning_effort"] != tc.want || body["reasoning"] != true {
				t.Fatalf("upstream body = %s, want effort %q and reasoning enabled", got, tc.want)
			}
			if tc.input == tc.want {
				if from != "" || to != "" {
					t.Fatalf("unexpected effort change %q → %q", from, to)
				}
			} else if from != tc.input || to != tc.want {
				t.Fatalf("effort change = %q → %q, want %q → %q", from, to, tc.input, tc.want)
			}
		})
	}
}

func TestPrepareProviderRequestReasoningEffortOnNone(t *testing.T) {
	for _, tc := range []struct {
		name, url, policy, input, from, to string
		wantEffort, wantReasoning          any
	}{
		{"SenseNova override", "https://token.sensenova.cn/v1", "low", " NONE ", " NONE ", "low", "low", true},
		{"SenseNova max override", "https://token.sensenova.cn/v1", "max", "none", "none", "max", "max", true},
		{"Other upstream override", "https://api.example.com/v1", "medium", "none", "none", "medium", "medium", false},
		{"Other upstream default", "https://api.example.com/v1", "", "none", "none", "", nil, false},
		{"Explicit effort", "https://token.sensenova.cn/v1", "max", "medium", "medium", "low", "low", true},
		{"Absent effort", "https://token.sensenova.cn/v1", "low", "", "", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &config.Provider{
				ModelID: "glm-5.3", Format: "openai", BaseURL: tc.url,
			}
			input := map[string]any{"model": "P2", "reasoning": false}
			if tc.input != "" {
				input["reasoning_effort"] = tc.input
			}
			payload, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			got, from, to := prepareProviderRequest(payload, provider, tc.policy)
			var body map[string]any
			if err := json.Unmarshal(got, &body); err != nil {
				t.Fatal(err)
			}
			effort, present := body["reasoning_effort"]
			if effort != tc.wantEffort || present != (tc.wantEffort != nil) || body["reasoning"] != tc.wantReasoning {
				t.Fatalf("upstream body = %s, want effort %v and reasoning %v", got, tc.wantEffort, tc.wantReasoning)
			}
			if from != tc.from || to != tc.to {
				t.Fatalf("effort change = %q → %q, want %q → %q", from, to, tc.from, tc.to)
			}
		})
	}
}

func TestReasoningEffortChangeLoggedDuringForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		payload    string
		wantEffort any
		wantLog    string
		streaming  bool
		nonePolicy string
	}{
		{"chat", `{"model":"P2","reasoning_effort":"medium"}`, "low", `from="medium" | to=low`, false, ""},
		{"stream", `{"model":"P2","stream":true,"reasoning_effort":"none"}`, nil, `from="none" | to=removed`, true, ""},
		{"stream with override", `{"model":"P2","stream":true,"reasoning_effort":"none"}`, "low", `from="none" | to=low`, true, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode upstream body: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				effort, exists := body["reasoning_effort"]
				if effort != tc.wantEffort || exists != (tc.wantEffort != nil) {
					t.Errorf("upstream effort = %v, present=%t, want %v", effort, exists, tc.wantEffort)
				}
				if tc.streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = io.WriteString(w, `{"id":"ok","choices":[]}`)
			}))
			t.Cleanup(upstream.Close)

			var log bytes.Buffer
			cfg := &config.Config{
				Global: config.GlobalConfig{ReasoningEffortOnNone: tc.nonePolicy},
				Providers: []config.Provider{{
					Name: "account", ModelID: "glm-5.3", Format: "openai", BaseURL: "http://token.sensenova.cn/v1", Priority: 2, Timeout: 5,
				}},
			}
			engine := NewEngine(cfg, "", &log, &log, stats.New(""))
			engine.transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			router := gin.New()
			router.Use(middleware.Logger(&log), middleware.DetectFormat())
			router.POST("/v1/chat/completions", engine.HandleRequest)

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.payload)))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
			}
			id := response.Header().Get("X-Request-ID")
			want := "[" + id + "] REASONING_EFFORT | [P2] account | model=glm-5.3 | " + tc.wantLog
			entry := strings.Index(log.String(), want)
			attempt := strings.Index(log.String(), "["+id+"] PROVIDER")
			if id == "" || entry < 0 || attempt <= entry || strings.Count(log.String(), " REASONING_EFFORT ") != 1 {
				t.Fatalf("expected one effort event before the attempt for %s:\n%s", id, log.String())
			}
		})
	}
}
