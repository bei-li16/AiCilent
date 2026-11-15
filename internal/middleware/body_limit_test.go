package middleware

import (
	"github.com/gin-gonic/gin"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type countingBody struct {
	io.Reader
	read int
}

func (r *countingBody) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	r.read += n
	return n, err
}
func (*countingBody) Close() error { return nil }

func TestDetectionBoundsFirstReadAndCachesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(detectFormat(64))
	r.POST("/chat/completions", func(c *gin.Context) {
		b, _ := c.Get(CtxBodyKey)
		if len(b.([]byte)) == 0 {
			t.Error("missing body cache")
		}
		c.Status(200)
	})
	for _, length := range []int64{1000, -1} {
		body := &countingBody{Reader: strings.NewReader(strings.Repeat("x", 1000))}
		req := httptest.NewRequest("POST", "/chat/completions", nil)
		req.Body = body
		req.ContentLength = length
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		if res.Code != 413 || body.read > 65 {
			t.Fatalf("status=%d read=%d", res.Code, body.read)
		}
	}
	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{"model":"a"}`)))
	if res.Code != 200 {
		t.Fatal(res.Code)
	}
}
