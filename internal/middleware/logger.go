package middleware

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
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

		// Prefix is "ACCESS" to distinguish from the tracer's "INBOUND" line,
		// which carries model/format/body. This line carries status + latency.
		fmt.Fprintf(gin.DefaultWriter, "[%s] ACCESS   | %s %s | %d | %v | format=%s\n",
			time.Now().Format("2006-01-02 15:04:05.000"),
			method, path, status, latency, format)
	}
}