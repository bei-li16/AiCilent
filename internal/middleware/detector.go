package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const CtxFormatKey = "request_format"

func DetectFormat() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}

		// Detect by URL path: /v1/messages or /messages → anthropic
		format := ""
		path := c.Request.URL.Path
		if path == "/v1/messages" || path == "/messages" || strings.HasSuffix(path, "/messages") {
			format = "anthropic"
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

		if format == "" {
			format = detect(body)
		}
		if format == "" {
			format = "openai"
		}
		c.Set(CtxFormatKey, format)
		c.Next()
	}
}

// detect inspects the request body to determine if it's an Anthropic-format request.
// Anthropic-specific markers: top-level "system" (string or array), "stop_sequences".
// OpenAI and Anthropic both have "messages", so that alone is insufficient.
func detect(body []byte) string {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	// Anthropic sends "system" as a top-level field — either a string or
	// an array of content blocks (e.g., [{"type":"text","text":"..."}]).
	if s, ok := data["system"]; ok {
		switch s.(type) {
		case string, []interface{}:
			return "anthropic"
		}
	}
	// "stop_sequences" is Anthropic-specific (OpenAI uses "stop")
	if _, ok := data["stop_sequences"]; ok {
		return "anthropic"
	}

	return ""
}

func GetRequestFormat(c *gin.Context) string {
	f, _ := c.Get(CtxFormatKey)
	s, _ := f.(string)
	return s
}