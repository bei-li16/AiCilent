package router

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ai-proxy/internal/config"
)

func TestAnthropicNonePolicy(t *testing.T) {
	for _, model := range []string{"kimi-k3", "glm-5.3", "deepseek-flash"} {
		for _, tc := range []struct {
			name, policy, input, want, from, to string
		}{
			{"disabled max", "max", `{"thinking":{"type":"disabled"}}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "disabled", "max"},
			{"disabled low", "low", `{"thinking":{"type":"disabled"}}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`, "disabled", "low"},
			{"disabled default", "", `{"thinking":{"type":"disabled"}}`, `{}`, "disabled", ""},
			{"native none", "max", `{"output_config":{"effort":"none"}}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "none", "max"},
			{"native none default", "", `{"output_config":{"effort":"none"}}`, `{}`, "none", ""},
			{"native format preserved", "", `{"output_config":{"effort":"none","format":{"type":"json_schema","schema":{"type":"object"}}}}`, `{"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}}`, "none", ""},
			{"legacy none", "max", `{"reasoning_effort":"none"}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "none", "max"},
			{"legacy none default", "", `{"reasoning_effort":"none"}`, `{}`, "none", ""},
			{"native wins legacy", "max", `{"reasoning_effort":"none","output_config":{"effort":"high"}}`, `{"output_config":{"effort":"high"}}`, "none", "high"},
			{"budget preserved", "max", `{"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"none"}}`, `{"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"max"}}`, "none", "max"},
			{"display preserved", "max", `{"thinking":{"type":"disabled","budget_tokens":1024,"display":"summarized"}}`, `{"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"max"}}`, "disabled", "max"},
			{"case and whitespace", "max", `{"thinking":{"type":" DISABLED "},"output_config":{"effort":" NONE "}}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, " NONE ", "max"},
			{"omitted", "max", `{}`, `{}`, "", ""},
			{"explicit level", "max", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}}`, "", ""},
			{"explicit budget", "max", `{"thinking":{"type":"enabled","budget_tokens":1024}}`, `{"thinking":{"type":"enabled","budget_tokens":1024}}`, "", ""},
			{"disabled overrides level", "max", `{"thinking":{"type":"disabled"},"output_config":{"effort":"low"}}`, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "disabled", "max"},
			{"disabled default preserves level", "", `{"thinking":{"type":"disabled"},"output_config":{"effort":"low"}}`, `{"output_config":{"effort":"low"}}`, "disabled", ""},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				p := &config.Provider{ModelID: model, Format: "anthropic", BaseURL: "https://token.sensenova.cn"}
				body := []byte(`{"model":"P1","messages":[{"role":"user","content":"ping"}],"metadata":{"n":9007199254740993},` + strings.TrimPrefix(tc.input, "{"))
				if tc.input == "{}" {
					body = []byte(`{"model":"P1","messages":[{"role":"user","content":"ping"}],"metadata":{"n":9007199254740993}}`)
				}
				originalBody := bytes.Clone(body)
				got, from, to := prepareProviderRequest(body, p, tc.policy)
				if !bytes.Equal(body, originalBody) {
					t.Fatal("policy mutated the original request used by other providers")
				}
				var actual, want map[string]any
				if err := json.Unmarshal(got, &actual); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
					t.Fatal(err)
				}
				if actual["model"] != model || !reflect.DeepEqual(actual["messages"], []any{map[string]any{"role": "user", "content": "ping"}}) || !bytes.Contains(got, []byte("9007199254740993")) {
					t.Fatalf("unrelated request data changed: %s", got)
				}
				delete(actual, "model")
				delete(actual, "messages")
				delete(actual, "metadata")
				if !reflect.DeepEqual(actual, want) || from != tc.from || to != tc.to {
					t.Fatalf("got=%s change=%q -> %q; want=%s change=%q -> %q", got, from, to, tc.want, tc.from, tc.to)
				}
				twice, secondFrom, secondTo := prepareProviderRequest(got, p, tc.policy)
				if !bytes.Equal(got, twice) || secondFrom != "" || secondTo != "" {
					t.Fatalf("not idempotent: %s change=%q -> %q", twice, secondFrom, secondTo)
				}
			})
		}
	}
}

func BenchmarkAnthropicReasoningPolicy(b *testing.B) {
	for _, control := range []string{``, `,"thinking":{"type":"disabled"}`} {
		name := "omitted"
		if control != "" {
			name = "disabled"
		}
		body := []byte(`{"model":"P1","messages":[{"role":"user","content":"` + strings.Repeat("x", 1<<20) + `"}]` + control + `}`)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				normalizeAnthropicReasoning(body, "max")
			}
		})
	}
}
