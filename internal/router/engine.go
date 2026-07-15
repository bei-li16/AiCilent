package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"ai-proxy/internal/adapter"
	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/retry"
	"ai-proxy/internal/tracer"

	"github.com/gin-gonic/gin"
)

type Engine struct {
	cfg              *config.Config
	matcher          *Matcher
	cbMu             sync.Mutex
	failureCount     map[int]int
	circuitOpenSince map[int]*time.Time
	skipRemaining    map[int]int
	priorityQueues   map[int]*sync.Mutex
	rrIndex          map[int]int
	logWriter        io.Writer
}

func NewEngine(cfg *config.Config, logWriter io.Writer) *Engine {
	if logWriter == nil {
		logWriter = os.Stdout
	}
	return &Engine{
		cfg:              cfg,
		matcher:          NewMatcher(cfg.ModelRoutes),
		failureCount:     make(map[int]int),
		circuitOpenSince: make(map[int]*time.Time),
		skipRemaining:    make(map[int]int),
		priorityQueues:   make(map[int]*sync.Mutex),
		rrIndex:          make(map[int]int),
		logWriter:        logWriter,
	}
}

func (e *Engine) HandleRequest(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read request body"})
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	reqStart := time.Now()
	ctx := context.WithValue(c.Request.Context(), startTimeKey, reqStart)
	c.Request = c.Request.WithContext(ctx)

	requestFormat := middleware.GetRequestFormat(c)

	modelName := extractModel(body)
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot extract model from request"})
		return
	}

	streaming := hasStream(body)

	tr := tracer.New(modelName, requestFormat, e.logWriter)

	bodySnippet := extractBodySnippet(body)
	tr.LogRequest(c.Request.Method, c.Request.URL.Path, bodySnippet)

	providers := e.getOrderedProviders(modelName)
	if len(providers) == 0 {
		tr.LogResult(false, "", 0)
		fmt.Fprint(e.logWriter, tr.Dump())
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available provider"})
		return
	}

	{
		names := make([]string, len(providers))
		for i, p := range providers {
			names[i] = fmt.Sprintf("%s[P%d]", p.Name, p.Priority)
		}
		tr.LogRoute(modelName, modeFilter(modelName), names)
	}

	groups := groupByPriority(providers)
	if len(groups) == 0 {
		tr.LogResult(false, "", 0)
		fmt.Fprint(e.logWriter, tr.Dump())
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available provider"})
		return
	}

	// Log CB config for debugging
	tr.LogCircuitBreaker(0, "config",
		fmt.Sprintf("cb_threshold=%d cb_cooldown=%ds cb_skip_requests=%d",
			e.cfg.Global.CBThreshold, e.cfg.Global.CBCooldown, e.cfg.Global.CBSkipRequests))

	var lastErr error
	var completed bool

	for gi, group := range groups {
		if completed || c.Writer.Written() {
			break
		}
		if len(group) == 0 {
			continue
		}

		priority := group[0].Priority

		if gi > 0 {
			reason := fmt.Sprintf("P%d all failed", groups[gi-1][0].Priority)
			if lastErr != nil {
				reason += ": " + lastErr.Error()
			}
			tr.LogDegradeGroup(priority, reason)
		}

		if e.shouldSkipGroup(priority, modelName, tr) {
			tr.LogQueue(priority, "skipped (circuit breaker)", 0, len(group))
			continue
		}

		e.acquireQueue(priority)
		queueHeld := true

		func() {
			defer func() {
				if queueHeld {
					e.releaseQueue(priority)
					queueHeld = false
				}
				if r := recover(); r != nil {
					if queueHeld {
						e.releaseQueue(priority)
						queueHeld = false
					}
					panic(r)
				}
			}()

			startIdx := e.advanceRRIndex(priority, len(group))
			tr.LogQueue(priority, "acquired", startIdx, len(group))

			for i := 0; i < len(group); i++ {
				idx := (startIdx + i) % len(group)
				provider := group[idx]

				if i > 0 {
					tr.LogDegradeProvider(provider.Name, priority, lastErr.Error())
				}

				maxRetries := provider.Retry.MaxRetries
				rm := retry.NewManager(maxRetries, provider.Retry.RetryInterval, provider.Retry.BackoffFactor)
				for rm.ShouldRetry() {
					attempt := rm.Attempt()
					start := time.Now()

					var fwdErr error
					if streaming {
						fwdErr = e.forwardRequestStream(c, body, requestFormat, provider, tr)
					} else {
						fwdErr = e.forwardRequest(c, body, requestFormat, provider)
					}

					latency := time.Since(start)

					if fwdErr == nil {
						tr.LogAttempt(provider.Name, priority, attempt, maxRetries+1, true, "", latency)
						e.closeCircuitBreaker(priority, modelName, tr)
						tr.LogResult(true, provider.Name, startIdx)
						tr.LogQueue(priority, "released", startIdx, len(group))
						queueHeld = false
						e.releaseQueue(priority)
						fmt.Fprint(e.logWriter, tr.Dump())
						completed = true
						return
					}

					tr.LogAttempt(provider.Name, priority, attempt, maxRetries+1, false, fwdErr.Error(), latency)
					lastErr = fwdErr

					if c.Writer.Written() {
						tr.LogQueue(priority, "released (stream written)", startIdx, len(group))
						tr.LogResult(false, provider.Name, startIdx)
						queueHeld = false
						e.releaseQueue(priority)
						e.recordGroupFailure(modelName, priority, tr)
						fmt.Fprint(e.logWriter, tr.Dump())
						completed = true
						return
					}

					if apiErr, ok := fwdErr.(*adapter.APIError); ok && !apiErr.Retryable() {
						tr.LogSkipRetry(provider.Name, priority, apiErr.StatusCode)
						break
					}

					if ctx.Err() != nil {
						break
					}

					if err := rm.Wait(ctx); err != nil {
						break
					}
				}

				if c.Writer.Written() {
					tr.LogQueue(priority, "released (stream written)", startIdx, len(group))
					tr.LogResult(false, provider.Name, startIdx)
					queueHeld = false
					e.releaseQueue(priority)
					e.recordGroupFailure(modelName, priority, tr)
					fmt.Fprint(e.logWriter, tr.Dump())
					completed = true
					return
				}
			}

			tr.LogQueue(priority, "released (all failed)", startIdx, len(group))
			queueHeld = false
			e.releaseQueue(priority)
			e.recordGroupFailure(modelName, priority, tr)
		}()

		if completed {
			break
		}
	}

	if completed || c.Writer.Written() {
		return
	}

	tr.LogResult(false, "", 0)
	fmt.Fprint(e.logWriter, tr.Dump())
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": fmt.Sprintf("all providers failed: %v", lastErr),
	})
}

func (e *Engine) shouldSkipGroup(priority int, modelName string, tr *tracer.Recorder) bool {
	e.cbMu.Lock()
	defer e.cbMu.Unlock()

	threshold := e.cfg.Global.CBThreshold
	failures := e.failureCount[priority]

	if failures < threshold {
		return false
	}

	openSince, exists := e.circuitOpenSince[priority]
	if !exists || openSince == nil {
		return false
	}

	cooldown := time.Duration(e.cfg.Global.CBCooldown) * time.Second
	elapsed := time.Since(*openSince)

	if elapsed >= cooldown {
		e.failureCount[priority] = 0
		e.circuitOpenSince[priority] = nil
		e.skipRemaining[priority] = e.cfg.Global.CBSkipRequests
		tr.LogCircuitBreaker(priority, "auto-closed",
			fmt.Sprintf("condition=cooldown_expired cooldown=%ds elapsed=%v threshold=%d failures=%d",
				e.cfg.Global.CBCooldown, elapsed.Round(time.Second), threshold, failures))
		return false
	}

	if e.skipRemaining[priority] <= 0 {
		e.failureCount[priority] = 0
		e.circuitOpenSince[priority] = nil
		e.skipRemaining[priority] = e.cfg.Global.CBSkipRequests
		tr.LogCircuitBreaker(priority, "auto-closed",
			fmt.Sprintf("condition=skip_requests_exhausted threshold=%d failures=%d",
				threshold, failures))
		return false
	}

	e.skipRemaining[priority]--
	remaining := e.skipRemaining[priority]
	cooldownLeft := cooldown - elapsed
	tr.LogCircuitBreaker(priority, "skip",
		fmt.Sprintf("condition=cooldown_pending_and_skip_remaining>0 cb_threshold=%d failures=%d cooldown_remaining=%v skip_remaining=%d",
			threshold, failures, cooldownLeft.Round(time.Second), remaining))
	return true
}

func (e *Engine) recordGroupFailure(modelName string, priority int, tr *tracer.Recorder) {
	e.cbMu.Lock()
	defer e.cbMu.Unlock()

	e.failureCount[priority]++
	failures := e.failureCount[priority]
	threshold := e.cfg.Global.CBThreshold

	if failures >= threshold && e.circuitOpenSince[priority] == nil {
		now := time.Now()
		e.circuitOpenSince[priority] = &now
		e.skipRemaining[priority] = e.cfg.Global.CBSkipRequests
		tr.LogCircuitBreaker(priority, "opened",
			fmt.Sprintf("condition=failures_reached_threshold model=%s cb_threshold=%d failures=%d/%d cooldown=%ds skip_requests=%d",
				modelName, threshold, failures, threshold, e.cfg.Global.CBCooldown, e.cfg.Global.CBSkipRequests))
	} else {
		tr.LogCircuitBreaker(priority, "failure-count",
			fmt.Sprintf("condition=group_all_failed model=%s failures=%d/%d (threshold=%d)",
				modelName, failures, threshold, threshold))
	}
}

func (e *Engine) closeCircuitBreaker(priority int, modelName string, tr *tracer.Recorder) {
	e.cbMu.Lock()
	defer e.cbMu.Unlock()

	wasOpen := e.circuitOpenSince[priority] != nil
	prevFailures := e.failureCount[priority]

	e.failureCount[priority] = 0
	e.circuitOpenSince[priority] = nil
	e.skipRemaining[priority] = e.cfg.Global.CBSkipRequests

	if wasOpen {
		tr.LogCircuitBreaker(priority, "closed",
			fmt.Sprintf("condition=request_succeeded model=%s previous_failures=%d reset to 0 skip_remaining=%d",
				modelName, prevFailures, e.cfg.Global.CBSkipRequests))
	} else if prevFailures > 0 {
		tr.LogCircuitBreaker(priority, "reset",
			fmt.Sprintf("condition=request_succeeded model=%s previous_failures=%d reset to 0 skip_remaining=%d",
				modelName, prevFailures, e.cfg.Global.CBSkipRequests))
	}
}

func (e *Engine) acquireQueue(priority int) {
	e.cbMu.Lock()
	mu, ok := e.priorityQueues[priority]
	if !ok {
		mu = &sync.Mutex{}
		e.priorityQueues[priority] = mu
	}
	e.cbMu.Unlock()
	mu.Lock()
}

func (e *Engine) releaseQueue(priority int) {
	e.cbMu.Lock()
	mu, ok := e.priorityQueues[priority]
	e.cbMu.Unlock()
	if ok {
		mu.Unlock()
	}
}

func (e *Engine) advanceRRIndex(priority, groupLen int) int {
	e.cbMu.Lock()
	defer e.cbMu.Unlock()
	idx := e.rrIndex[priority]
	e.rrIndex[priority] = (idx + 1) % groupLen
	return idx
}

func (e *Engine) forwardRequest(c *gin.Context, body []byte, requestFormat string, provider *config.Provider) error {
	var respBody []byte
	var err error

	if requestFormat != provider.Format {
		converted, err := adapter.ConvertRequest(body, requestFormat, provider.Format)
		if err != nil {
			return fmt.Errorf("convert request: %w", err)
		}
		body = converted
	}

	body = setModelInBody(body, provider.ModelID)

	if provider.Format == "openai" {
		respBody, err = adapter.CallOpenAIRaw(provider.BaseURL, provider.APIKey, body, provider.Timeout)
	} else {
		respBody, err = adapter.CallAnthropicRaw(provider.BaseURL, provider.APIKey, body, provider.Timeout)
	}

	if err != nil {
		return err
	}

	if provider.Format != requestFormat {
		respBody, err = adapter.ConvertResponse(respBody, provider.Format, requestFormat, provider.ModelID)
		if err != nil {
			return fmt.Errorf("convert response: %w", err)
		}
	}

	c.Data(http.StatusOK, "application/json", respBody)
	return nil
}

func (e *Engine) forwardRequestStream(c *gin.Context, body []byte, requestFormat string, provider *config.Provider, tr *tracer.Recorder) error {
	if requestFormat != provider.Format {
		converted, err := adapter.ConvertRequest(body, requestFormat, provider.Format)
		if err != nil {
			return fmt.Errorf("convert request: %w", err)
		}
		body = converted
	}

	body = setModelInBody(body, provider.ModelID)

	path := "/chat/completions"
	if provider.Format == "anthropic" {
		path = "/messages"
	}
	url := provider.BaseURL + path

	httpReq, err := http.NewRequestWithContext(c.Request.Context(), "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if provider.Format == "openai" {
		httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	} else {
		httpReq.Header.Set("x-api-key", provider.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}

	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	streamStart := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return &adapter.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
	}

	ttfb := time.Since(streamStart)
	tr.LogTTFB(provider.Name, provider.Priority, ttfb, http.StatusOK)

	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.WriteHeader(http.StatusOK)
	}

	timeout := time.Duration(provider.Timeout) * time.Second
	idleReader := &idleTimeoutReader{r: resp.Body, timeout: timeout}

	fw := &flushWriter{w: c.Writer}
	if requestFormat != provider.Format {
		err = adapter.StreamConvertResponse(idleReader, fw, provider.Format, requestFormat)
	} else {
		_, err = io.Copy(fw, idleReader)
	}

	idleReader.Close()
	return err
}

type startTimeContextKey struct{}

var startTimeKey = startTimeContextKey{}

type flushWriter struct {
	w http.ResponseWriter
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if f, ok := fw.w.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

type idleTimeoutReader struct {
	r       io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	if r.timer == nil {
		r.timer = time.NewTimer(r.timeout)
	}
	r.timer.Reset(r.timeout)

	done := make(chan readResult, 1)
	go func() {
		n, err := r.r.Read(p)
		done <- readResult{n, err}
	}()

	select {
	case result := <-done:
		return result.n, result.err
	case <-r.timer.C:
		return 0, fmt.Errorf("idle timeout after %v", r.timeout)
	}
}

func (r *idleTimeoutReader) Close() error {
	if r.timer != nil {
		r.timer.Stop()
	}
	return r.r.Close()
}

type readResult struct {
	n   int
	err error
}

func (e *Engine) getOrderedProviders(modelName string) []*config.Provider {
	switch modelName {
	case "Max":
		return e.filterProviders(func(p *config.Provider) bool {
			return p.Priority == e.cfg.Providers[0].Priority
		})
	case "Flash":
		return e.filterProviders(func(p *config.Provider) bool {
			return p.Priority > e.cfg.Providers[0].Priority
		})
	case "Medium":
		return e.allProviders()
	}

	matched := make(map[string]bool)
	var result []*config.Provider

	if target, ok := e.matcher.Match(modelName); ok {
		for i, p := range e.cfg.Providers {
			if p.Name == target {
				result = append(result, &e.cfg.Providers[i])
				matched[target] = true
				break
			}
		}
	}

	for i, p := range e.cfg.Providers {
		if p.ModelID == modelName && !matched[p.Name] {
			result = append(result, &e.cfg.Providers[i])
			matched[p.Name] = true
		}
	}

	if target, ok := e.matcher.Default(); ok && !matched[target] {
		for i, p := range e.cfg.Providers {
			if p.Name == target {
				result = append(result, &e.cfg.Providers[i])
				matched[target] = true
				break
			}
		}
	}

	for i, p := range e.cfg.Providers {
		if !matched[p.Name] {
			result = append(result, &e.cfg.Providers[i])
			matched[p.Name] = true
		}
	}

	return result
}

func (e *Engine) allProviders() []*config.Provider {
	result := make([]*config.Provider, len(e.cfg.Providers))
	for i := range e.cfg.Providers {
		result[i] = &e.cfg.Providers[i]
	}
	return result
}

func (e *Engine) filterProviders(fn func(*config.Provider) bool) []*config.Provider {
	var result []*config.Provider
	for i := range e.cfg.Providers {
		if fn(&e.cfg.Providers[i]) {
			result = append(result, &e.cfg.Providers[i])
		}
	}
	return result
}

func groupByPriority(providers []*config.Provider) [][]*config.Provider {
	if len(providers) == 0 {
		return nil
	}

	var groups [][]*config.Provider
	current := providers[0].Priority
	start := 0

	for i := 1; i <= len(providers); i++ {
		if i == len(providers) || providers[i].Priority != current {
			group := make([]*config.Provider, i-start)
			copy(group, providers[start:i])
			groups = append(groups, group)
			if i < len(providers) {
				current = providers[i].Priority
				start = i
			}
		}
	}

	return groups
}

func setModelInBody(body []byte, modelID string) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}
	data["model"] = modelID
	modified, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return modified
}

func extractModel(body []byte) string {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}
	if model, ok := data["model"].(string); ok {
		return model
	}
	return ""
}

func hasStream(body []byte) bool {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return false
	}
	if stream, ok := data["stream"].(bool); ok {
		return stream
	}
	return false
}

func extractBodySnippet(body []byte) string {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}
	msgs, ok := data["messages"].([]interface{})
	if !ok || len(msgs) == 0 {
		return ""
	}
	first, ok := msgs[0].(map[string]interface{})
	if !ok {
		return ""
	}
	content, ok := first["content"].(string)
	if !ok {
		return ""
	}
	if len(content) > 80 {
		content = content[:80] + "..."
	}
	role, _ := first["role"].(string)
	return fmt.Sprintf("%s: %s", role, content)
}

func modeFilter(modelName string) string {
	switch modelName {
	case "Max":
		return "highest priority only"
	case "Flash":
		return "skip priority 1"
	case "Medium":
		return "all priorities"
	default:
		return "model match"
	}
}