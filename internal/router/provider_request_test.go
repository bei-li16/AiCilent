package router

import (
	"encoding/json"
	"testing"

	"ai-proxy/internal/config"
)

func TestPrepareProviderRequestNormalizesSensenovaReasoning(t *testing.T) {
	provider := &config.Provider{
		ModelID: "glm-5.2",
		Format:  "openai",
		BaseURL: "https://token.sensenova.cn/v1",
	}

	got := prepareProviderRequest([]byte(`{"model":"Max","reasoning_effort":"xhigh"}`), provider)
	var body map[string]interface{}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "glm-5.2" {
		t.Fatalf("model = %#v, want glm-5.2", body["model"])
	}
	if body["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", body["reasoning_effort"])
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

	got := prepareProviderRequest([]byte(`{"model":"Max","reasoning_effort":"xhigh"}`), provider)
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
