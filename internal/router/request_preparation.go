package router

import (
	"fmt"
	"strings"

	"ai-proxy/internal/adapter"
	"ai-proxy/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const preparationCacheKey = "provider_preparation_cache"

type preparationKey struct {
	format, model, effort string
	sensenova             bool
}
type preparedBody struct {
	body               []byte
	original, upstream string
	err                error
}

func cachedPreparation(c *gin.Context, body []byte, requestFormat string, p *config.Provider, effort string) preparedBody {
	key := preparationKey{p.Format, p.ModelID, effort, isSensenovaProvider(p.BaseURL)}
	value, enabled := c.Get(preparationCacheKey)
	cache, _ := value.(map[preparationKey]preparedBody)
	if enabled {
		if v, ok := cache[key]; ok {
			return v
		}
	}
	v := preparedBody{body: body}
	if requestFormat != p.Format {
		v.body, v.err = adapter.ConvertRequest(body, requestFormat, p.Format)
		if v.err != nil {
			v.err = fmt.Errorf("convert request: %w", v.err)
		}
	}
	if v.err == nil {
		v.body, v.original, v.upstream = prepareProviderRequest(v.body, p, effort)
	}
	// Retain only the last variant, not a large copy for every fallback model.
	if enabled && cache != nil {
		clear(cache)
		cache[key] = v
	}
	return v
}

// Native disabled thinking is the Anthropic equivalent of explicit OpenAI
// none. Missing thinking/effort and other explicit levels are not defaults.
func normalizeAnthropicReasoning(body []byte, policy string) ([]byte, string, string) {
	fields := gjson.GetManyBytes(body, "output_config.effort", "thinking.type", "reasoning_effort")
	canonical := func(field gjson.Result) string {
		if field.Type == gjson.String {
			return strings.ToLower(strings.TrimSpace(field.Str))
		}
		return ""
	}
	effort, thinking, legacy := canonical(fields[0]), canonical(fields[1]), canonical(fields[2])
	disabled := thinking == "disabled" || thinking == "none"
	legacyNone := legacy == "none"
	if effort != "none" && !disabled && !legacyNone {
		return body, "", ""
	}
	if legacyNone {
		body, _ = sjson.DeleteBytes(body, "reasoning_effort")
	}
	// An explicit native level wins over a legacy OpenAI-only field.
	if legacyNone && effort != "" && effort != "none" && !disabled {
		return body, fields[2].Str, fields[0].Str
	}
	original := fields[0].Str
	if effort != "none" {
		original = fields[1].Str
		if !disabled {
			original = fields[2].Str
		}
	}
	if policy == "" {
		if effort == "none" {
			body, _ = sjson.DeleteBytes(body, "output_config.effort")
			if output := gjson.GetBytes(body, "output_config"); output.IsObject() && len(output.Map()) == 0 {
				body, _ = sjson.DeleteBytes(body, "output_config")
			}
		}
		if disabled {
			body, _ = sjson.DeleteBytes(body, "thinking")
		}
		return body, original, ""
	}
	// The first edit allocates an owned copy; subsequent equal-size replacement
	// can reuse it without mutating the original request used by fallback models.
	body, _ = sjson.SetBytesOptions(body, "output_config.effort", policy, &sjson.Options{Optimistic: true})
	if thinking == "" || disabled {
		body, _ = sjson.SetBytesOptions(body, "thinking.type", "adaptive", &sjson.Options{Optimistic: true, ReplaceInPlace: true})
		if gjson.GetBytes(body, "thinking.budget_tokens").Exists() {
			body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
		}
	}
	return body, original, policy
}

func (e *Engine) pruneStreamTransports(cfg *config.Config) {
	allowed := make(map[int]bool)
	for _, p := range cfg.Providers {
		allowed[p.Timeout] = true
	}
	for _, rule := range cfg.ModelRules {
		if n, ok := configNumberAsInt(rule.Defaults["timeout"]); ok {
			allowed[n] = true
		}
	}
	e.streamMu.Lock()
	defer e.streamMu.Unlock()
	for timeout, transport := range e.streamTransports {
		if !allowed[int(timeout.Seconds())] {
			delete(e.streamTransports, timeout)
			transport.CloseIdleConnections()
		}
	}
}
