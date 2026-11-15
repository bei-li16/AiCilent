package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const CtxFormatKey = "request_format"
const CtxBodyKey = "request_body"

// Enforce the multimodal allowance before the first allocation.
const MaxRequestBodySize = 50 << 20

func DetectFormat() gin.HandlerFunc {
	return detectFormat(MaxRequestBodySize)
}

func detectFormat(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}

		// Detect protocol by URL path before inspecting the body. Responses and
		// Anthropic both use an input/messages-style payload, so the endpoint is
		// the unambiguous signal when a client uses the standard API paths.
		format := ""
		path := c.Request.URL.Path
		if path == "/v1/responses" || path == "/responses" || strings.HasSuffix(path, "/responses") {
			format = "responses"
		}
		if path == "/v1/messages" || path == "/messages" || strings.HasSuffix(path, "/messages") {
			format = "anthropic"
		}

		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
				return
			}
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
		if !gjson.ValidBytes(body) && len(body) > 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid JSON request"})
			return
		}
		c.Set(CtxBodyKey, body)

		if format == "" {
			format = detectMarkers(body)
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
	if !gjson.ValidBytes(body) {
		return ""
	}
	return detectMarkers(body)
}

func detectMarkers(body []byte) string {
	// Anthropic sends "system" as a top-level field — either a string or
	// an array of content blocks (e.g., [{"type":"text","text":"..."}]).
	fields := gjson.GetManyBytes(body, "system", "stop_sequences")
	if s := fields[0]; s.Exists() {
		if s.Type == gjson.String || s.IsArray() {
			return "anthropic"
		}
	}
	// "stop_sequences" is Anthropic-specific (OpenAI uses "stop")
	if fields[1].Exists() {
		return "anthropic"
	}

	return ""
}

func GetRequestFormat(c *gin.Context) string {
	f, _ := c.Get(CtxFormatKey)
	s, _ := f.(string)
	return s
}
