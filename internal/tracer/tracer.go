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
	logWriter    io.Writer // combined writer (file + SSE)
	fileWriter   io.Writer // file-only writer (full request bodies land here, never SSE)
	model        string
	format       string
	startTime    time.Time
	resultStatus string // "SUCCESS" or "FAIL" (empty = not yet set)
	resultProv   string
	resultRR     int
}

// New builds a Recorder. logWriter receives every log line (file + SSE).
// fileWriter is the file-only sink used for full request bodies so that large
// bodies are persisted without flooding the live SSE log stream.
func New(model, format string, logWriter, fileWriter io.Writer) *Recorder {
	if logWriter == nil {
		logWriter = os.Stdout
	}
	if fileWriter == nil {
		fileWriter = logWriter
	}
	return &Recorder{
		model:      model,
		format:     format,
		startTime:  time.Now(),
		logWriter:  logWriter,
		fileWriter: fileWriter,
	}
}

func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

// LogRequest logs the inbound request line. The level controls how the body
// is rendered:
//   "off"     — body omitted entirely
//   "snippet" — body is a short single-line snippet (printed as msg=<snippet>)
//   "full"    — the full pretty-printed request structure is written to the
//               file-only sink (NOT the SSE stream, to avoid flooding the live
//               console with potentially huge bodies). The SSE/header line gets
//               a placeholder so the live console still shows a request arrived.
func (r *Recorder) LogRequest(method, path, level, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Prefix is "INBOUND" to distinguish from the gin access log's "REQUEST" line.
	header := fmt.Sprintf("[%s] INBOUND  | %s %s | model=%s | format=%s",
		timestamp(), method, path, r.model, r.format)

	switch level {
	case "off":
		fmt.Fprintf(r.logWriter, "%s\n", header)
	case "full":
		// Header + placeholder go to the combined stream (visible live).
		fmt.Fprintf(r.logWriter, "%s | body=<full logged to file>\n", header)
		// The full body goes to the file only — never the SSE stream.
		fmt.Fprintf(r.fileWriter, "%s | body:\n%s\n", header, body)
	default: // "snippet"
		fmt.Fprintf(r.logWriter, "%s | msg=%s\n", header, body)
	}
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
