package middleware

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

const CtxRequestedModel = "requested_model"

func RequestStartedAt(c *gin.Context) time.Time {
	value, _ := c.Get(requestStartKey)
	start, _ := value.(time.Time)
	return start
}

// RequestTiming measures the downstream request rather than any single
// upstream attempt. Only model routes use it.
func RequestTiming(log io.Writer, record func(stats.RequestTiming)) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := RequestStartedAt(c)
		if start.IsZero() {
			start = time.Now()
		}
		id := GetReqID(c)
		writer := &timedResponseWriter{
			ResponseWriter: c.Writer,
			onFirst: func(first time.Time) {
				fmt.Fprintf(log, "[%s] [%s] FIRST_RESPONSE | elapsed=%v\n",
					first.Format("2006-01-02 15:04:05.000"), id, first.Sub(start))
			},
		}
		c.Writer = writer
		c.Next()
		end := time.Now()
		c.Set(requestEndKey, end)
		writer.reportFirst()
		timing := stats.RequestTiming{
			RequestID: id, Model: c.GetString(CtxRequestedModel), EndedAt: end, TotalMs: milliseconds(end.Sub(start)),
		}
		if writer.Written() {
			timing.StatusCode = writer.Status()
		}
		if !writer.first.IsZero() {
			ms := milliseconds(writer.first.Sub(start))
			timing.FirstResponseMs = &ms
		}
		record(timing)
	}
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

type timedResponseWriter struct {
	gin.ResponseWriter
	first    time.Time
	onFirst  func(time.Time)
	reported bool
}

func (w *timedResponseWriter) capture(n int, err error) {
	if err == nil && n > 0 && w.first.IsZero() && w.Status() >= http.StatusOK && w.Status() < 300 {
		w.first = time.Now()
	}
}

func (w *timedResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.capture(n, err)
	return n, err
}

func (w *timedResponseWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	w.capture(n, err)
	return n, err
}

func (w *timedResponseWriter) Flush() {
	w.ResponseWriter.Flush()
	w.reportFirst()
}

func (w *timedResponseWriter) reportFirst() {
	if !w.reported && !w.first.IsZero() {
		w.reported = true
		w.onFirst(w.first)
	}
}

func (w *timedResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
