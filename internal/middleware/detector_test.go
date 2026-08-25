package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDetectFormatRecognizesResponsesPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(DetectFormat())
	r.POST("/v1/responses", func(c *gin.Context) {
		if got := GetRequestFormat(c); got != "responses" {
			t.Fatalf("request format = %q, want responses", got)
		}
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","input":"hello"}`))
	r.ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}
