package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

const CtxFormatKey = "request_format"

func DetectFormat() gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

		format := detect(body)
		if format == "" {
			format = "openai"
		}
		c.Set(CtxFormatKey, format)
		c.Next()
	}
}

func detect(body []byte) string {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	if _, ok := data["messages"]; ok {
		return "openai"
	}
	if _, ok := data["anthropic_version"]; ok {
		return "anthropic"
	}

	return ""
}

func GetRequestFormat(c *gin.Context) string {
	f, _ := c.Get(CtxFormatKey)
	return f.(string)
}