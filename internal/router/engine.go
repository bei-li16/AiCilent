package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ai-proxy/internal/adapter"
	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/ratelimit"
	"ai-proxy/internal/retry"
	"ai-proxy/internal/stats"
	"ai-proxy/internal/tracer"

	"github.com/gin-gonic/gin"
)

// maxStreamDuration caps total streaming duration to prevent indefinitely
// slow upstream streams from holding connections forever.
const maxStreamDuration = 3 * time.Minute

// maxRequestBodySize limits the request body to prevent OOM from malicious
// payloads. 10MB is generous for chat completions with long conversation history.
const maxRequestBodySize = 10 << 20 // 10 MB

type Engine struct {
	cfg              *config.Config
	configPath       string
	matcher          *Matcher

	// Circuit breaker state (cbMu protects)
	cbMu             sync.Mutex
	failureCount     map[int]int
	circuitOpenSince map[int]*time.Time
	skipRemaining    map[int]int

	// Provider concurrency locks (providerMu protects)
	providerMu       sync.Mutex
	providerLocks    map[string]*sync.Mutex

	// Rate limiters (rlMu protects)
	rlMu             sync.Mutex
	rateLimiters     map[string]*ratelimit.TokenBucket

	// Round-robin index (rrMu protects)
	rrMu             sync.Mutex
	rrIndex          map[int]int

	// Config reload guard
	reloadMu         sync.RWMutex

	// Shared HTTP transport for connection reuse
	transport *http.Transport

	// File write mutex for CB state persistence (prevents concurrent write corruption)
	fileMu sync.Mutex

	logWriter io.Writer
	stats     *stats.Collector
}

func NewEngine(cfg *config.Config, configPath string, logWriter io.Writer, stats *stats.Collector) *Engine {
	if logWriter == nil {
		logWriter = os.Stdout
	}

	rl := make(map[string]*ratelimit.TokenBucket, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.RateLimit.Enabled {
			rps := p.RateLimit.RPM / 60.0
			rl[p.Name] = ratelimit.New(rps, p.RateLimit.Burst)
		}
	}

	e := &Engine{
		cfg:              cfg,
		configPath:       configPath,
		matcher:          NewMatcher(cfg.ModelRoutes),
		failureCount:     make(map[int]int),
		circuitOpenSince: make(map[int]*time.Time),
		skipRemaining:    make(map[int]int),
		providerLocks:    make(map[string]*sync.Mutex),
		rrIndex:          make(map[int]int),
		rateLimiters:     rl,
		transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
		logWriter: logWriter,
		stats:    stats,
	}
	e.loadCBState()
	return e
}

// StartWatcher starts a goroutine that polls the config file for changes
// and hot-reloads the provider configuration.
func (e *Engine) StartWatcher(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}

	var lastMod time.Time
	if fi, err := os.Stat(e.configPath); err == nil {
		lastMod = fi.ModTime()
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fi, err := os.Stat(e.configPath)
				if err != nil {
					continue
				}
				if fi.ModTime().After(lastMod) {
					lastMod = fi.ModTime()
					if err := e.reloadConfig(); err != nil {
						fmt.Fprintf(e.logWriter, "[CONFIG] hot-reload failed: %v\n", err)
					} else {
						fmt.Fprintf(e.logWriter, "[CONFIG] hot-reloaded from %s\n", e.configPath)
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}


func (e *Engine) reloadConfig() error {
	cfg, err := config.Load(e.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	e.reloadMu.Lock()
	e.cfg = cfg
	e.matcher = NewMatcher(cfg.ModelRoutes)
	e.reloadMu.Unlock()

	// Keep existing provider locks for stable names
	oldLocks := make(map[string]*sync.Mutex)
	e.providerMu.Lock()
	for k, v := range e.providerLocks {
		oldLocks[k] = v
	}
	newLocks := make(map[string]*sync.Mutex, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if old, ok := oldLocks[p.Name]; ok {
			newLocks[p.Name] = old
		} else {
			newLocks[p.Name] = &sync.Mutex{}
		}
	}
	e.providerLocks = newLocks
	e.providerMu.Unlock()

	// Rebuild rate limiters
	e.rlMu.Lock()
	newRL := make(map[string]*ratelimit.TokenBucket, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if p.RateLimit.Enabled {
			rps := p.RateLimit.RPM / 60.0
			if old, ok := e.rateLimiters[p.Name]; ok {
				old.SetRate(rps, p.RateLimit.Burst)
				newRL[p.Name] = old
			} else {
				newRL[p.Name] = ratelimit.New(rps, p.RateLimit.Burst)
			}
		}
	}
	e.rateLimiters = newRL
	e.rlMu.Unlock()

	// Reset RR indexes for any new priority groups
	e.rrMu.Lock()
	for _, p := range cfg.Providers {
		if _, ok := e.rrIndex[p.Priority]; !ok {
			e.rrIndex[p.Priority] = 0
		}
	}
	e.rrMu.Unlock()

	return nil
}

func (e *Engine) HandleRequest(c *gin.Context) {
	if !e.stats.IsRunning() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "proxy is disabled"})
		return
	}

	// Limit request body to 10MB to prevent OOM from malicious large payloads.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodySize)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large or unreadable"})
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

	// Capture config pointer under read lock for consistency.
	// Once captured, the pointed-to struct is never mutated by reloadConfig.
	e.reloadMu.RLock()
	providers := e.getOrderedProviders(modelName)
	cfg := e.cfg
	e.reloadMu.RUnlock()
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
			cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests))

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

		if e.shouldSkipGroup(priority, modelName, tr,
				cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests) {
			tr.LogQueue(priority, "skipped (circuit breaker)", 0, len(group))
			continue
		}

		startIdx := e.advanceRRIndex(priority, len(group))
		tr.LogQueue(priority, "trying", startIdx, len(group))

		anyTried := false

		for i := 0; i < len(group); i++ {
			idx := (startIdx + i) % len(group)
			provider := group[idx]

			if !e.tryAcquireProvider(provider.Name) {
				tr.LogQueue(priority, fmt.Sprintf("provider %s busy, skipped", provider.Name), idx, len(group))
				continue
			}
			anyTried = true

			if i > 0 && lastErr != nil {
				tr.LogDegradeProvider(provider.Name, priority, lastErr.Error())
			}

			// Check rate limit before attempting
			if rlErr := e.checkRateLimit(provider.Name); rlErr != nil {
				tr.LogQueue(priority, fmt.Sprintf("provider %s rate limited, skipping", provider.Name), idx, len(group))
				e.releaseProvider(provider.Name)
				continue
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
					e.stats.Record(provider.Name, provider.ModelID, priority, true)
					tr.LogAttempt(provider.Name, priority, attempt, maxRetries+1, true, "", latency)
					e.closeCircuitBreaker(priority, modelName, tr,
			cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests)
					tr.LogResult(true, provider.Name, idx)
					tr.LogQueue(priority, "released", idx, len(group))
					e.releaseProvider(provider.Name)
					fmt.Fprint(e.logWriter, tr.Dump())
					completed = true
					break
				}

				e.stats.Record(provider.Name, provider.ModelID, priority, false)
					tr.LogAttempt(provider.Name, priority, attempt, maxRetries+1, false, fwdErr.Error(), latency)
				lastErr = fwdErr

				if c.Writer.Written() {
					tr.LogQueue(priority, "released (stream written)", idx, len(group))
					tr.LogResult(false, provider.Name, idx)
					e.releaseProvider(provider.Name)
					e.recordGroupFailure(modelName, priority, tr,
		cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests)
					fmt.Fprint(e.logWriter, tr.Dump())
					completed = true
					break
				}

				if apiErr, ok := fwdErr.(*adapter.APIError); ok && !apiErr.Retryable() {
					tr.LogSkipRetry(provider.Name, priority, apiErr.StatusCode)
					break
				}

				if ctx.Err() != nil {
					break
				}

				// Only wait if there is a next retry (skip after final attempt)
				if attempt < maxRetries+1 {
					if err := rm.Wait(ctx); err != nil {
						break
					}
				}
			}

			if completed {
				break
			}

			e.releaseProvider(provider.Name)

			if c.Writer.Written() {
				break
			}
		}

		if !anyTried {
			tr.LogQueue(priority, "all providers busy", startIdx, len(group))
			continue
		}

		if !completed && !c.Writer.Written() {
			tr.LogQueue(priority, "released (all failed)", startIdx, len(group))
			e.recordGroupFailure(modelName, priority, tr,
		cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests)
		}

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

func (e *Engine) shouldSkipGroup(priority int, modelName string, tr *tracer.Recorder, threshold, cooldownSec, skipRequests int) bool {
	e.cbMu.Lock()
	defer e.cbMu.Unlock()

	failures := e.failureCount[priority]

	if failures < threshold {
		return false
	}

	openSince, exists := e.circuitOpenSince[priority]
	if !exists || openSince == nil {
		return false
	}

	cooldown := time.Duration(cooldownSec) * time.Second
	elapsed := time.Since(*openSince)

	if elapsed >= cooldown {
		e.failureCount[priority] = 0
		e.circuitOpenSince[priority] = nil
		e.skipRemaining[priority] = skipRequests
		tr.LogCircuitBreaker(priority, "auto-closed",
			fmt.Sprintf("condition=cooldown_expired cooldown=%ds elapsed=%v threshold=%d failures=%d",
				cooldownSec, elapsed.Round(time.Second), threshold, failures))
		return false
	}

	if e.skipRemaining[priority] <= 0 {
		e.failureCount[priority] = 0
		e.circuitOpenSince[priority] = nil
		e.skipRemaining[priority] = skipRequests
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

func (e *Engine) recordGroupFailure(modelName string, priority int, tr *tracer.Recorder, threshold, cooldownSec, skipRequests int) {
	e.cbMu.Lock()

	e.failureCount[priority]++
	failures := e.failureCount[priority]

	if failures >= threshold && e.circuitOpenSince[priority] == nil {
		now := time.Now()
		e.circuitOpenSince[priority] = &now
		e.skipRemaining[priority] = skipRequests
		tr.LogCircuitBreaker(priority, "opened",
			fmt.Sprintf("condition=failures_reached_threshold model=%s cb_threshold=%d failures=%d/%d cooldown=%ds skip_requests=%d",
				modelName, threshold, failures, threshold, cooldownSec, skipRequests))
	} else {
		tr.LogCircuitBreaker(priority, "failure-count",
			fmt.Sprintf("condition=group_all_failed model=%s failures=%d/%d (threshold=%d)",
				modelName, failures, threshold, threshold))
	}
	e.cbMu.Unlock()

	e.saveCBState()
}

func (e *Engine) closeCircuitBreaker(priority int, modelName string, tr *tracer.Recorder, threshold, cooldownSec, skipRequests int) {
	e.cbMu.Lock()

	wasOpen := e.circuitOpenSince[priority] != nil
	prevFailures := e.failureCount[priority]

	e.failureCount[priority] = 0
	e.circuitOpenSince[priority] = nil
	e.skipRemaining[priority] = skipRequests

	if wasOpen {
		tr.LogCircuitBreaker(priority, "closed",
			fmt.Sprintf("condition=request_succeeded model=%s previous_failures=%d reset to 0 skip_remaining=%d",
				modelName, prevFailures, skipRequests))
	} else if prevFailures > 0 {
		tr.LogCircuitBreaker(priority, "reset",
			fmt.Sprintf("condition=request_succeeded model=%s previous_failures=%d reset to 0 skip_remaining=%d",
				modelName, prevFailures, skipRequests))
	}
	e.cbMu.Unlock()

	e.saveCBState()
}

func (e *Engine) tryAcquireProvider(name string) bool {
	e.providerMu.Lock()
	mu, ok := e.providerLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		e.providerLocks[name] = mu
	}
	e.providerMu.Unlock()
	return mu.TryLock()
}

func (e *Engine) releaseProvider(name string) {
	e.providerMu.Lock()
	mu, ok := e.providerLocks[name]
	e.providerMu.Unlock()
	if ok {
		mu.Unlock()
	}
}

// checkRateLimit returns nil if the provider is within its rate limit,
// or an error if rate limited. Always returns nil if rate limiting is disabled.
func (e *Engine) checkRateLimit(providerName string) error {
	e.rlMu.Lock()
	rl, ok := e.rateLimiters[providerName]
	e.rlMu.Unlock()
	if !ok || rl == nil {
		return nil
	}
	if !rl.Allow(providerName) {
		return &adapter.APIError{
			StatusCode: http.StatusTooManyRequests,
			Message:    fmt.Sprintf("rate limit exceeded for provider %s", providerName),
		}
	}
	return nil
}

func (e *Engine) advanceRRIndex(priority, groupLen int) int {
	e.rrMu.Lock()
	defer e.rrMu.Unlock()
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
		respBody, err = adapter.CallOpenAIRaw(provider.BaseURL, provider.APIKey, body, provider.Timeout, e.transport)
	} else {
		respBody, err = adapter.CallAnthropicRaw(provider.BaseURL, provider.APIKey, provider.AuthType, body, provider.Timeout, e.transport)
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
		path = "/v1/messages"
	}
	url := provider.BaseURL + path

	// Overall stream duration cap: 3 minutes. The idleTimeoutReader handles
	// per-gap stalls, but a stream that trickles data forever would never
	// trigger it. This context acts as a hard safety net.
	streamCtx, streamCancel := context.WithTimeout(c.Request.Context(), maxStreamDuration)
	defer streamCancel()

	httpReq, err := http.NewRequestWithContext(streamCtx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if provider.Format == "openai" {
		httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	} else if provider.AuthType == "bearer" {
		httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	} else {
		httpReq.Header.Set("x-api-key", provider.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}

	respHeaderTimeout := time.Duration(provider.Timeout) * time.Second
	if respHeaderTimeout > 30*time.Second {
		respHeaderTimeout = 30 * time.Second // cap at 30s for header wait
	}
	clone := e.transport.Clone()
	clone.ResponseHeaderTimeout = respHeaderTimeout
	client := &http.Client{Transport: clone}
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
	idleReader := newIdleTimeoutReader(resp.Body, timeout)
	defer idleReader.Close()

	fw := &flushWriter{w: c.Writer}
	if requestFormat != provider.Format {
		err = adapter.StreamConvertResponse(idleReader, fw, provider.Format, requestFormat)
	} else {
		_, err = io.Copy(fw, idleReader)
	}

	// If the stream failed mid-way (headers already written), inject an
	// SSE error event so the client knows the stream was truncated.
	if err != nil && c.Writer.Written() {
		errMsg := err.Error()
		if len(errMsg) > 500 {
			errMsg = errMsg[:500] + "..."
		}
		if requestFormat == "anthropic" {
			fmt.Fprintf(c.Writer, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"stream interrupted: %s\"}}\n\n", errMsg)
		} else {
			fmt.Fprintf(c.Writer, "data: {\"error\":{\"message\":\"stream interrupted: %s\"}}\n\n", errMsg)
		}
		if f, ok := c.Writer.(http.Flusher); ok {
			f.Flush()
		}
	}

	return err
}

type startTimeContextKey struct{}

var startTimeKey = startTimeContextKey{}

type flushWriter struct {
	w http.ResponseWriter
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if err == nil {
		if f, ok := fw.w.(http.Flusher); ok {
			f.Flush()
		}
	}
	return n, err
}

type idleTimeoutReader struct {
	r         io.ReadCloser
	timeout   time.Duration
	done      chan struct{}
	timer     *time.Timer
	closeOnce sync.Once
}

// newIdleTimeoutReader wraps an io.ReadCloser with an idle timeout.
// If no data arrives within timeout, reads return an error.
// Close is safe to call multiple times (sync.Once).
func newIdleTimeoutReader(r io.ReadCloser, timeout time.Duration) *idleTimeoutReader {
	return &idleTimeoutReader{
		r:       r,
		timeout: timeout,
		done:    make(chan struct{}),
		timer:   time.NewTimer(timeout),
	}
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	type readResult struct {
		n   int
		err error
	}
	resultCh := make(chan readResult, 1)

	// Drain any stale timer value before resetting (per time.Timer docs).
	if !r.timer.Stop() {
		select {
		case <-r.timer.C:
		default:
		}
	}
	r.timer.Reset(r.timeout)

	// Use an internal buffer to avoid data race: the goroutine may write to p
	// after Read returns (on timeout). Copy to caller's buffer only on success.
	buf := make([]byte, len(p))
	go func() {
		n, err := r.r.Read(buf)
		select {
		case resultCh <- readResult{n, err}:
		case <-r.done:
		}
	}()

	select {
	case result := <-resultCh:
		copy(p, buf[:result.n])
		return result.n, result.err
	case <-r.timer.C:
		return 0, fmt.Errorf("idle timeout after %v", r.timeout)
	case <-r.done:
		return 0, io.ErrClosedPipe
	}
}

func (r *idleTimeoutReader) Close() error {
	var err error
	r.closeOnce.Do(func() {
		close(r.done)
		r.timer.Stop()
		err = r.r.Close()
	})
	return err
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
	role, _ := first["role"].(string)

	// Handle string content
	if content, ok := first["content"].(string); ok {
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		return fmt.Sprintf("%s: %s", role, content)
	}

	// Handle array content (multimodal — images, tools, etc.)
	if contentArr, ok := first["content"].([]interface{}); ok && len(contentArr) > 0 {
		parts := make([]string, 0, len(contentArr))
		for _, c := range contentArr {
			block, _ := c.(map[string]interface{})
			if block == nil {
				continue
			}
			switch block["type"] {
			case "text":
				if text, _ := block["text"].(string); text != "" {
					parts = append(parts, text)
				}
			case "image_url", "image":
				parts = append(parts, "[image]")
			case "tool_use":
				parts = append(parts, "[tool_use]")
			case "tool_result":
				parts = append(parts, "[tool_result]")
			default:
				parts = append(parts, fmt.Sprintf("[%s]", block["type"]))
			}
		}
		snippet := strings.Join(parts, " ")
		if len(snippet) > 80 {
			snippet = snippet[:80] + "..."
		}
		return fmt.Sprintf("%s: %s", role, snippet)
	}

	return fmt.Sprintf("%s: [non-text content]", role)
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

// ─────────────────────────────────────────────────────────────
//  Circuit breaker persistence
// ─────────────────────────────────────────────────────────────

// cbStateJSON is the on-disk format for circuit breaker state.
type cbStateJSON struct {
	FailureCount     map[int]int    `json:"failure_count"`
	SkipRemaining    map[int]int    `json:"skip_remaining"`
	CircuitsOpen     map[int]string `json:"circuits_open"` // priority → RFC3339 timestamp
}

func (e *Engine) cbStatePath() string {
	dir := filepath.Dir(e.configPath)
	return filepath.Join(dir, ".cb_state.json")
}

func (e *Engine) saveCBState() {
	if e.configPath == "" {
		return
	}
	e.cbMu.Lock()
	state := e.captureCBState()
	e.cbMu.Unlock()

	// Write outside the CB lock to minimize hold time.
	// Use temp file + rename for atomic write (prevents concurrent write corruption).
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	e.fileMu.Lock()
	defer e.fileMu.Unlock()
	path := e.cbStatePath()
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return
	}
	os.Rename(tmpPath, path)
}

// captureCBState must be called with e.cbMu held.
func (e *Engine) captureCBState() cbStateJSON {
	fc := make(map[int]int, len(e.failureCount))
	for k, v := range e.failureCount {
		fc[k] = v
	}
	sr := make(map[int]int, len(e.skipRemaining))
	for k, v := range e.skipRemaining {
		sr[k] = v
	}
	co := make(map[int]string)
	for k, v := range e.circuitOpenSince {
		if v != nil {
			co[k] = v.Format(time.RFC3339)
		}
	}
	return cbStateJSON{
		FailureCount:  fc,
		SkipRemaining: sr,
		CircuitsOpen:  co,
	}
}

func (e *Engine) loadCBState() {
	if e.configPath == "" {
		return
	}
	data, err := os.ReadFile(e.cbStatePath())
	if err != nil {
		return // no state file yet
	}

	var state cbStateJSON
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}

	e.cbMu.Lock()
	defer e.cbMu.Unlock()

	for k, v := range state.FailureCount {
		if v > 0 {
			e.failureCount[k] = v
		}
	}
	for k, v := range state.SkipRemaining {
		e.skipRemaining[k] = v
	}
	for k, v := range state.CircuitsOpen {
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			e.circuitOpenSince[k] = &t
		}
	}
}