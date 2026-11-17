package stream

import (
	"bytes"
	"time"

	"github.com/tidwall/gjson"
)

// UsageMeter observes the upstream protocol before any response conversion.
// Token counts are cumulative within a stream, so later updates replace values.
type UsageMeter struct {
	input, output, cacheRead, cacheWrite int64
	inputSeen, outputSeen, cacheSeen     bool
	FirstOutput, LastOutput              time.Time
}

type TokenCounts struct {
	Input, Output, CachedInput, CacheEligible int64
}

func (m *UsageMeter) Counts(format string) (TokenCounts, bool) {
	counts := TokenCounts{Input: m.input, Output: m.output, CachedInput: m.cacheRead}
	if format == "anthropic" {
		counts.Input += m.cacheRead + m.cacheWrite
	}
	if m.cacheSeen && counts.CachedInput <= counts.Input {
		counts.CacheEligible = counts.Input
	} else {
		counts.CachedInput = 0
	}
	return counts, m.inputSeen && m.outputSeen && counts.Input+counts.Output > 0
}

func readCount(field gjson.Result, dest *int64, seen *bool) {
	if field.Type == gjson.Number && field.Num >= 0 && field.Num <= 1e12 && field.Num == float64(field.Int()) {
		*dest = field.Int()
		*seen = true
	}
}

func (m *UsageMeter) readObject(usage gjson.Result, format string) {
	if !usage.IsObject() {
		return
	}
	if format == "anthropic" {
		readCount(usage.Get("input_tokens"), &m.input, &m.inputSeen)
		readCount(usage.Get("output_tokens"), &m.output, &m.outputSeen)
		readCount(usage.Get("cache_read_input_tokens"), &m.cacheRead, &m.cacheSeen)
		// Some upstreams report writes separately from cache hits.
		var writeSeen bool
		readCount(usage.Get("cache_creation_input_tokens"), &m.cacheWrite, &writeSeen)
		return
	}
	readCount(usage.Get("prompt_tokens"), &m.input, &m.inputSeen)
	readCount(usage.Get("completion_tokens"), &m.output, &m.outputSeen)
	cache := usage.Get("prompt_tokens_details.cached_tokens")
	if !cache.Exists() {
		cache = usage.Get("prompt_cache_hit_tokens")
	}
	readCount(cache, &m.cacheRead, &m.cacheSeen)
}

func (m *UsageMeter) ObserveJSON(body []byte, format string) {
	m.readObject(gjson.GetBytes(body, "usage"), format)
}

func (m *UsageMeter) ObserveEvent(event Event, format string, at time.Time) {
	if !event.HasData {
		return
	}
	data := event.Data
	generated := false
	if format == "anthropic" {
		kind := event.Type
		if kind == "" {
			kind = gjson.GetBytes(data, "type").String()
		}
		switch kind {
		case "message_start":
			m.readObject(gjson.GetBytes(data, "message.usage"), format)
		case "message_delta":
			m.ObserveJSON(data, format)
		case "content_block_delta":
			for _, field := range gjson.GetManyBytes(data, "delta.text", "delta.thinking", "delta.partial_json") {
				generated = generated || field.Str != ""
			}
		case "content_block_start":
			block := gjson.GetBytes(data, "content_block")
			generated = block.Get("type").Str == "tool_use" || block.Get("text").Str != "" || block.Get("thinking").Str != ""
		}
	} else if format == "openai" {
		if bytes.Contains(data, []byte(`"usage"`)) {
			m.ObserveJSON(data, format)
		}
		gjson.GetBytes(data, "choices").ForEach(func(_, choice gjson.Result) bool {
			delta := choice.Get("delta")
			generated = delta.Get("content").Str != "" || delta.Get("reasoning_content").Str != "" ||
				delta.Get("reasoning").Str != "" || len(delta.Get("tool_calls").Array()) > 0 || delta.Get("function_call").IsObject()
			return !generated
		})
	}
	if generated {
		if m.FirstOutput.IsZero() {
			m.FirstOutput = at
		}
		m.LastOutput = at
	}
}

func IsUsageOnly(event Event) bool {
	if !event.HasData || !bytes.Contains(event.Data, []byte(`"usage"`)) {
		return false
	}
	choices := gjson.GetBytes(event.Data, "choices")
	return gjson.GetBytes(event.Data, "usage").IsObject() &&
		(!choices.Exists() || (choices.IsArray() && len(choices.Array()) == 0))
}
