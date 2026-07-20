package tracer

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Recorder struct {
	mu           sync.Mutex
	logWriter    io.Writer
	model        string
	format       string
	startTime    time.Time
	resultStatus string // "SUCCESS" or "FAIL" (empty = not yet set)
	resultProv   string
	resultRR     int
}

func New(model, format string, logWriter io.Writer) *Recorder {
	if logWriter == nil {
		logWriter = os.Stdout
	}
	return &Recorder{
		model:     model,
		format:    format,
		startTime: time.Now(),
		logWriter: logWriter,
	}
}

func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

func (r *Recorder) LogRequest(method, path string, bodySnippet string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := fmt.Sprintf("[%s] REQUEST  | %s %s | model=%s | format=%s | msg=%s\n",
		timestamp(), method, path, r.model, r.format, bodySnippet)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogRoute(mode string, filter string, matched []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := fmt.Sprintf("[%s] ROUTER   | mode=%s filter=\"%s\" matched=[%s]\n",
		timestamp(), mode, filter, strings.Join(matched, ", "))
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogAttempt(providerName string, priority, attemptNum, maxRetries int, success bool, errMsg string, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := "OK"
	if !success {
		status = "FAIL"
	}
	detail := ""
	if success {
		detail = fmt.Sprintf("%v", latency)
	} else {
		detail = fmt.Sprintf("%v | %s", latency, errMsg)
	}

	msg := fmt.Sprintf("[%s] PROVIDER | [P%d][%d/%d] %s | %s | %s\n",
		timestamp(), priority, attemptNum, maxRetries, providerName, status, detail)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogSkipRetry(providerName string, priority int, statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := fmt.Sprintf("[%s] ROUTER   | non-retryable status=%d | skip retry | %s [P%d]\n",
		timestamp(), statusCode, providerName, priority)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogDegradeProvider(providerName string, priority int, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := fmt.Sprintf("[%s] ROUTER   | degrade to next provider | %s [P%d] | reason=%s\n",
		timestamp(), providerName, priority, reason)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogDegradeGroup(priority int, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := fmt.Sprintf("[%s] ROUTER   | degrade to priority group | P%d | reason=%s\n",
		timestamp(), priority, reason)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogQueue(priority int, action string, rrIdx int, groupSize int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := fmt.Sprintf("[%s] QUEUE    | [P%d] | %s | rr=%d size=%d\n",
		timestamp(), priority, action, rrIdx, groupSize)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogCircuitBreaker(priority int, action string, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pLabel := fmt.Sprintf("P%d", priority)
	if priority == 0 {
		pLabel = "CFG"
	}
	msg := fmt.Sprintf("[%s] CB       | [%s] | %s | %s\n",
		timestamp(), pLabel, action, detail)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogTTFB(providerName string, priority int, latency time.Duration, statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg := fmt.Sprintf("[%s] TTFB     | %s[P%d] | upstream header=%v | status=%d\n",
		timestamp(), providerName, priority, latency, statusCode)
	fmt.Fprint(r.logWriter, msg)
}

func (r *Recorder) LogResult(success bool, providerName string, rrIdx int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resultStatus = "SUCCESS"
	if !success {
		r.resultStatus = "FAIL"
	}
	r.resultProv = providerName
	r.resultRR = rrIdx
}

func (r *Recorder) Dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	total := time.Since(r.startTime)
	status := r.resultStatus
	if status == "" {
		status = "FAIL"
	}

	return fmt.Sprintf("[%s] SUMMARY  | model=%s | format=%s | result=%s | provider=%s | rr=%d | total=%v\n",
		timestamp(), r.model, r.format, status, r.resultProv, r.resultRR, total)
}
