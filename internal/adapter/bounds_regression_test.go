package adapter

import (
	"ai-proxy/internal/stream"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestBoundedErrorBody(t *testing.T) {
	got := ReadErrorBody(strings.NewReader(strings.Repeat("x", MaxErrorBodyBytes*2)))
	if !strings.HasSuffix(got, "[truncated]") || len(got) > MaxErrorBodyBytes+32 {
		t.Fatal("unbounded error diagnostic")
	}
}

func TestAnthropicToolArgumentsAreDeltas(t *testing.T) {
	var src bytes.Buffer
	for i, args := range []string{`{"a":`, `1`, `}`} {
		tool := map[string]interface{}{"index": 0, "function": map[string]string{"arguments": args}}
		if i == 0 {
			tool["id"] = "tool"
			tool["function"].(map[string]string)["name"] = "test"
		}
		b, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"index": 0, "delta": map[string]interface{}{"tool_calls": []interface{}{tool}}}}})
		src.WriteString("data: " + string(b) + "\n\n")
	}
	src.WriteString("data: [DONE]\n\n")
	var dst bytes.Buffer
	if err := StreamConvertResponse(&src, &dst, "openai", "anthropic"); err != nil {
		t.Fatal(err)
	}
	decoder := stream.NewDecoder(&dst)
	var args strings.Builder
	for {
		e, err := decoder.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var p struct {
			Delta struct {
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		_ = json.Unmarshal(e.Data, &p)
		args.WriteString(p.Delta.PartialJSON)
	}
	if args.String() != `{"a":1}` {
		t.Fatal(args.String())
	}
}

func TestResponsesAccumulatorHasFiniteBudget(t *testing.T) {
	s := &responsesStreamState{}
	if !s.retain(strings.Repeat("x", 4<<20)) || s.retain("x") || s.err == nil {
		t.Fatal("output accumulation was not bounded")
	}
}

func TestRequestConversionPreservesLargeNumbers(t *testing.T) {
	got, err := ConvertRequest([]byte(`{"model":"test","input":"hi","metadata":{"integer":9007199254740993},"tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"x":{"const":9007199254740993}}}}]}`), "responses", "openai")
	if err != nil || bytes.Count(got, []byte("9007199254740993")) != 2 {
		t.Fatalf("number changed: %s (%v)", got, err)
	}
}

func TestRequestConversionRejectsTrailingJSON(t *testing.T) {
	for _, suffix := range []string{`{}`, `invalid`} {
		if _, err := ConvertRequest([]byte(`{"input":"hi"}`+suffix), "responses", "openai"); err == nil {
			t.Fatalf("accepted trailing JSON %q", suffix)
		}
	}
}
