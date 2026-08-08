package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const CtxReqIDKey = "req_id"

var reqCounter uint64

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		reqID := newRequestID()
		c.Set(CtxReqIDKey, reqID)
		c.Header("X-Request-ID", reqID)

		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		if strings.HasPrefix(path, "/api/") {
			return
		}

		latency := time.Since(start)
		status := c.Writer.Status()
		method := c.Request.Method
		format := GetRequestFormat(c)

		fmt.Fprintf(gin.DefaultWriter, "[%s] [%s] ACCESS   | %s %s | %d | %v | format=%s\n",
			time.Now().Format("2006-01-02 15:04:05.000"),
			reqID,
			method, path, status, latency, format)
	}
}

func newRequestID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("%016x-%016x", uint64(time.Now().UnixNano()), atomic.AddUint64(&reqCounter, 1))
}

func GetReqID(c *gin.Context) string {
	v, _ := c.Get(CtxReqIDKey)
	s, _ := v.(string)
	return s
}
