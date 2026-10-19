package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

// maxRequestBodySize limits the request body to prevent OOM from malicious
// payloads. 50MB leaves room for multimodal requests: a single base64 image
// attachment alone can reach ~5MB, and coding agents attach them on top of
// large conversation history.
const maxRequestBodySize = 50 << 20 // 50 MB

type Engine struct {
	cfg        *config.Config
	configPath string
	matcher    *Matcher

	// Circuit breaker state (cbMu protects)
	cbMu             sync.Mutex
	failureCount     map[int]int
	circuitOpenSince map[int]*time.Time
	skipRemaining    map[int]int
	probeInFlight    map[int]bool
	probeLease       map[int]uint64
	nextProbeLease   uint64

	// Rate limiters (rlMu protects)
	rlMu         sync.Mutex
	rateLimiters map[string]*ratelimit.TokenBucket
	// coolUntil holds per-provider cooldown deadlines set after a 429. A
	// rate-limited account cannot serve again until its TPM/RPM window slides,
	// so retrying it (or picking it for the next request) only wastes latency.
	coolUntil map[string]time.Time

	// Round-robin index (rrMu protects)
	rrMu    sync.Mutex
	rrIndex map[int]int

	// Config reload guard
	reloadMu sync.RWMutex

	// Shared HTTP transport for connection reuse
	transport        *http.Transport
	streamMu         sync.Mutex
	streamTransports map[time.Duration]*http.Transport

	// File write mutex for CB state persistence (prevents concurrent write corruption)
	fileMu sync.Mutex

	// Per-upstream-origin concurrency limiters (connMu protects the map).
	connMu       sync.Mutex
	connLimiters map[string]*hostLimiter

	logWriter  io.Writer
	fileWriter io.Writer
	stats      *stats.Collector
}

func NewEngine(cfg *config.Config, configPath string, logWriter, fileWriter io.Writer, stats *stats.Collector) *Engine {
	if logWriter == nil {
		logWriter = os.Stdout
	}
	if fileWriter == nil {
		fileWriter = logWriter
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
		probeInFlight:    make(map[int]bool),
		probeLease:       make(map[int]uint64),
		rrIndex:          make(map[int]int),
		rateLimiters:     rl,
		coolUntil:        make(map[string]time.Time),
		transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
		streamTransports: make(map[time.Duration]*http.Transport),
		logWriter:        logWriter,
		fileWriter:       fileWriter,
		stats:            stats,
	}
	e.rebuildConnLimiters(cfg)
	e.loadCBState()
	e.logCBConfig()
	return e
}

// logCBConfig prints the circuit-breaker configuration once per load/reload.
// It used to be emitted on every request, which buried the real CB events.
func (e *Engine) logCBConfig() {
	e.reloadMu.RLock()
	g := e.cfg.Global
	e.reloadMu.RUnlock()
	fmt.Fprintf(e.logWriter,
		"[%s] [-] CB       | [CFG] | config | cb_threshold=%d cb_cooldown=%ds cb_skip_requests=%d rate_limit_cooldown=%ds\n",
		time.Now().Format("2006-01-02 15:04:05.000"),
		g.CBThreshold, g.CBCooldown, g.CBSkipRequests, e.rateLimitCooldownSec())
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
						fmt.Fprintf(e.logWriter, "[%s] CONFIG   | hot-reload failed: %v\n",
							time.Now().Format("2006-01-02 15:04:05.000"), err)
					} else {
						fmt.Fprintf(e.logWriter, "[%s] CONFIG   | hot-reloaded from %s\n",
							time.Now().Format("2006-01-02 15:04:05.000"), e.configPath)
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

	// Apply host concurrency changes without interrupting active requests.
	e.rebuildConnLimiters(cfg)

	// Reset RR indexes for any new priority groups
	e.rrMu.Lock()
	for _, p := range cfg.Providers {
		if _, ok := e.rrIndex[p.Priority]; !ok {
			e.rrIndex[p.Priority] = 0
		}
	}
	e.rrMu.Unlock()

	e.logCBConfig()
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
	// Count every accepted client request, including requests that later fail
	// because no provider matches the detected protocol.
	e.stats.RecordClientRequest()

	// Capture the immutable config snapshot before applying model rules. Reloads
	// replace the config pointer, so reading it without this lock is a race.
	e.reloadMu.RLock()
	cfg := e.cfg
	allProviders := e.getOrderedProviders(modelName)
	body, modelTimeout := applyModelDefaults(body, modelName, cfg.ModelRules)
	e.reloadMu.RUnlock()

	streaming := hasStream(body)

	tr := tracer.New(modelName, requestFormat, middleware.GetReqID(c), e.logWriter, e.fileWriter)

	// Request-body log level is read from config (off|snippet|full).
	e.reloadMu.RLock()
	logLevel := e.cfg.Global.LogRequestBody
	e.reloadMu.RUnlock()
	if logLevel == "" {
		logLevel = "snippet"
	}
	tr.LogRequest(c.Request.Method, c.Request.URL.Path, logLevel, buildRequestBodyLog(body, logLevel))

	// Normalize OpenAI-only roles before any format conversion, so both the
	// openai->openai passthrough and the openai->anthropic system hoisting see a
	// body the upstream accepts. This runs after LogRequest on purpose: the log
	// keeps showing the role the client actually sent, which is what makes this
	// class of failure diagnosable.
	body = normalizeDeveloperRole(body)

	// Filter to providers that speak the upstream protocol needed by this
	// request. Responses is an OpenAI client protocol that is adapted to the
	// provider's Chat Completions endpoint, so it uses OpenAI providers.
	providerFormat := upstreamFormat(requestFormat)
	providers := make([]*config.Provider, 0, len(allProviders))
	for _, p := range allProviders {
		if p.Format == providerFormat {
			providers = append(providers, p)
		}
	}
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

		var probeID uint64
		if e.shouldSkipGroup(priority, modelName, tr,
			cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests, false, &probeID) {
			tr.LogQueue(priority, "skipped (circuit breaker)", 0, len(group))
			continue
		}

		completed, lastErr = e.tryGroup(c, body, requestFormat, streaming, group,
			modelTimeout, modelName, tr, cfg, probeID)
		if completed {
			break
		}
	}

	if completed || c.Writer.Written() {
		return
	}

	tr.LogResult(false, "", 0)
	fmt.Fprint(e.logWriter, tr.Dump())
	status := http.StatusServiceUnavailable
	message := "all provider groups unavailable (circuit breaker or local capacity)"
	if lastErr != nil {
		message = "all providers failed: " + lastErr.Error()
		var apiErr *adapter.APIError
		if errors.As(lastErr, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			status = http.StatusTooManyRequests
		}
	}
	c.JSON(status, gin.H{"error": message})
}

// tryGroup attempts every provider in one priority group, honouring each
// provider's retry policy, and reports whether the request is finished.
//
// Client-initiated cancellations are classified here and deliberately kept out
// of the circuit breaker: a client that closes its connection says nothing
// about backend health.
func (e *Engine) tryGroup(c *gin.Context, body []byte, requestFormat string, streaming bool,
	group []*config.Provider, modelTimeout int, modelName string, tr *tracer.Recorder,
	cfg *config.Config, probeID uint64) (bool, error) {

	ctx := c.Request.Context()
	priority := group[0].Priority
	defer func() {
		e.cbMu.Lock()
		if probeID != 0 && e.probeLease[priority] == probeID {
			e.probeInFlight[priority] = false
		}
		e.cbMu.Unlock()
	}()
	startIdx := e.advanceRRIndex(priority, len(group))
	tr.LogQueue(priority, "trying", startIdx, len(group))

	var lastErr error
	upstreamFailed := false
	var lastProvider *config.Provider
	lastIdx := 0

	type candidate struct {
		idx      int
		provider *config.Provider
	}
	candidates := make([]candidate, 0, len(group))
	for i := 0; i < len(group); i++ {
		idx := (startIdx + i) % len(group)
		provider := group[idx]
		candidates = append(candidates, candidate{idx: idx, provider: provider})
	}

	for ci, cand := range candidates {
		idx, provider := cand.idx, cand.provider
		lastProvider, lastIdx = provider, idx
		if ctx.Err() != nil {
			break
		}
		if rlErr := e.checkRateLimit(provider.Name); rlErr != nil {
			lastErr = rlErr
			tr.LogQueue(priority, fmt.Sprintf("provider %s skipping (%v)", provider.Name, rlErr), idx, len(group))
			continue
		}

		if ci > 0 && lastErr != nil {
			tr.LogDegradeProvider(provider.Name, priority, lastErr.Error())
		}

		maxRetries := provider.Retry.MaxRetries
		rm := retry.NewManager(maxRetries, provider.Retry.RetryInterval, provider.Retry.BackoffFactor)
		for rm.ShouldRetry() {
			if ctx.Err() != nil {
				break
			}
			attempt := rm.Attempt()
			start := time.Now()

			var fwdErr error
			if streaming {
				fwdErr = e.forwardRequestStream(c, body, requestFormat, provider, effectiveTimeout(provider.Timeout, modelTimeout), tr)
			} else {
				fwdErr = e.forwardRequest(c, body, requestFormat, provider, effectiveTimeout(provider.Timeout, modelTimeout), middleware.GetReqID(c))
			}

			latency := time.Since(start)

			if fwdErr == nil {
				e.stats.Record(middleware.GetReqID(c), provider.Name, provider.ModelID, priority, true, http.StatusOK, "", latency)
				tr.LogAttempt(provider.Name, priority, attempt, maxRetries+1, true, "", latency)
				e.closeCircuitBreaker(priority, modelName, tr,
					cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests)
				tr.LogResult(true, provider.Name, idx)
				tr.LogQueue(priority, "released", idx, len(group))
				fmt.Fprint(e.logWriter, tr.Dump())
				return true, nil
			}

			var capacityErr *localCapacityError
			if errors.As(fwdErr, &capacityErr) {
				lastErr = fwdErr
				tr.LogQueue(priority, fwdErr.Error(), idx, len(group))
				if isRequestCanceled(c, fwdErr) {
					tr.LogCanceled(provider.Name, idx)
					fmt.Fprint(e.logWriter, tr.Dump())
					return true, nil
				}
				break // no upstream call was made; do not count a provider attempt
			}
			// The client left mid-stream. Not an upstream failure: no failover
			// (nobody to write to) and no circuit-breaker penalty.
			if errors.Is(fwdErr, errClientGoneIncomplete) || isRequestCanceled(c, fwdErr) {
				e.stats.RecordCanceled(middleware.GetReqID(c), provider.Name, provider.ModelID, priority, "client disconnected", latency)
				tr.LogAttemptCanceled(provider.Name, priority, attempt, maxRetries+1, latency)
				tr.LogQueue(priority, "released (client disconnected)", idx, len(group))
				tr.LogCanceled(provider.Name, idx)
				fmt.Fprint(e.logWriter, tr.Dump())
				return true, nil
			}

			statusCode, errMsg := errorDetail(fwdErr)
			e.stats.Record(middleware.GetReqID(c), provider.Name, provider.ModelID, priority, false, statusCode, errMsg, latency)
			tr.LogAttempt(provider.Name, priority, attempt, maxRetries+1, false, fwdErr.Error(), latency)
			lastErr = fwdErr
			upstreamFailed = upstreamFailed || breakerFailure(fwdErr)

			if statusCode == http.StatusTooManyRequests {
				e.markProviderCooldown(provider.Name)
				break // respect this account's cooldown, including the last candidate
			}

			if c.Writer.Written() {
				tr.LogQueue(priority, "released (stream written)", idx, len(group))
				tr.LogResult(false, provider.Name, idx)
				if breakerFailure(fwdErr) {
					e.recordGroupFailure(modelName, priority, tr,
						cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests, probeID)
				}
				fmt.Fprint(e.logWriter, tr.Dump())
				return true, nil
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

		if c.Writer.Written() {
			return true, lastErr
		}
		if ctx.Err() != nil {
			// The client is gone; another provider would have nobody to write
			// to. Leave the final bookkeeping to the block below.
			break
		}
	}

	if !c.Writer.Written() {
		// A client that disappeared while we were backing off is not a backend
		// signal: report it without touching the breaker.
		if ctx.Err() != nil {
			name := ""
			if lastProvider != nil {
				name = lastProvider.Name
			}
			tr.LogQueue(priority, "released (client disconnected)", startIdx, len(group))
			tr.LogCanceled(name, lastIdx)
			fmt.Fprint(e.logWriter, tr.Dump())
			return true, lastErr
		}
		tr.LogQueue(priority, "released (all failed)", startIdx, len(group))
		if upstreamFailed {
			e.recordGroupFailure(modelName, priority, tr,
				cfg.Global.CBThreshold, cfg.Global.CBCooldown, cfg.Global.CBSkipRequests, probeID)
		}
	}
	return false, lastErr
}

// A cooldown or exhausted skip budget permits one probe, never an unbounded
// reset of the breaker. Its outcome decides when the breaker really closes.
func (e *Engine) shouldSkipGroup(priority int, modelName string, tr *tracer.Recorder, threshold, cooldownSec, skipRequests int, _ bool, lease ...*uint64) bool {
	e.cbMu.Lock()
	defer e.cbMu.Unlock()
	openSince := e.circuitOpenSince[priority]
	if openSince == nil || e.failureCount[priority] < threshold {
		return false
	}
	if e.probeInFlight[priority] {
		tr.LogCircuitBreaker(priority, "skip", "condition=probe_in_flight")
		return true
	}
	if time.Since(*openSince) >= time.Duration(cooldownSec)*time.Second || e.skipRemaining[priority] <= 0 {
		e.nextProbeLease++
		e.probeInFlight[priority] = true
		e.probeLease[priority] = e.nextProbeLease
		if len(lease) > 0 && lease[0] != nil {
			*lease[0] = e.nextProbeLease
		}
		tr.LogCircuitBreaker(priority, "half-open", "condition=probe_admitted model="+modelName)
		return false
	}
	e.skipRemaining[priority]--
	tr.LogCircuitBreaker(priority, "skip", fmt.Sprintf("condition=cooldown_pending skip_remaining=%d", e.skipRemaining[priority]))
	return true
}

func breakerFailure(err error) bool {
	var apiErr *adapter.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 408 || apiErr.StatusCode >= 500
	}
	var capacityErr *localCapacityError
	return !errors.As(err, &capacityErr) && !errors.Is(err, errClientGoneIncomplete)
}

func (e *Engine) recordGroupFailure(modelName string, priority int, tr *tracer.Recorder, threshold, cooldownSec, skipRequests int, lease ...uint64) {
	e.cbMu.Lock()

	e.failureCount[priority]++
	failures := e.failureCount[priority]

	wasProbe := len(lease) > 0 && lease[0] != 0 && e.probeLease[priority] == lease[0] && e.probeInFlight[priority]
	if failures >= threshold && (e.circuitOpenSince[priority] == nil || wasProbe) {
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
	e.probeInFlight[priority] = false
	e.probeLease[priority] = 0
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

type hostLimiter struct {
	mu      sync.Mutex
	limit   int
	active  int
	changed chan struct{}
}

func newHostLimiter(limit int) *hostLimiter {
	return &hostLimiter{limit: limit, changed: make(chan struct{})}
}

func (l *hostLimiter) setLimit(limit int) {
	l.mu.Lock()
	if l.limit != limit {
		l.limit = limit
		close(l.changed)
		l.changed = make(chan struct{})
	}
	l.mu.Unlock()
}

func (l *hostLimiter) acquire(ctx context.Context) error {
	for {
		l.mu.Lock()
		if l.limit <= 0 || l.active < l.limit {
			l.active++
			l.mu.Unlock()
			return nil
		}
		changed := l.changed
		l.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *hostLimiter) release() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}

// rebuildConnLimiters applies the strictest positive limit configured for all
// provider entries that point at the same scheme/host/port. Paths such as /v1
// do not create a second pool for the same upstream server.
func (e *Engine) rebuildConnLimiters(cfg *config.Config) {
	desired := make(map[string]int)
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.MaxConcurrent <= 0 {
			continue
		}
		key := upstreamOrigin(p.BaseURL)
		if current, ok := desired[key]; !ok || p.MaxConcurrent < current {
			desired[key] = p.MaxConcurrent
		}
	}

	e.connMu.Lock()
	old := e.connLimiters
	next := make(map[string]*hostLimiter, len(desired))
	for key, limit := range desired {
		limiter := old[key]
		if limiter == nil {
			limiter = newHostLimiter(limit)
		} else {
			limiter.setLimit(limit)
		}
		next[key] = limiter
	}
	for key, limiter := range old {
		if _, ok := next[key]; !ok {
			limiter.setLimit(0)
		}
	}
	e.connLimiters = next
	e.connMu.Unlock()
}

func upstreamOrigin(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

func (e *Engine) acquireConnSlot(ctx context.Context, provider *config.Provider) (*hostLimiter, error) {
	e.connMu.Lock()
	limiter := e.connLimiters[upstreamOrigin(provider.BaseURL)]
	e.connMu.Unlock()
	if limiter == nil {
		return nil, nil
	}
	if err := limiter.acquire(ctx); err != nil {
		return nil, err
	}
	return limiter, nil
}

func (e *Engine) releaseConnSlot(limiter *hostLimiter) {
	if limiter != nil {
		limiter.release()
	}
}

func (e *Engine) streamTransport(timeout int) *http.Transport {
	headerTimeout := time.Duration(timeout) * time.Second

	e.streamMu.Lock()
	defer e.streamMu.Unlock()
	if transport := e.streamTransports[headerTimeout]; transport != nil {
		return transport
	}
	transport := e.transport.Clone()
	transport.ResponseHeaderTimeout = headerTimeout
	e.streamTransports[headerTimeout] = transport
	return transport
}

// checkRateLimit returns nil if the provider is within its rate limit,
// or an error if rate limited. Always returns nil if rate limiting is disabled.
func (e *Engine) checkRateLimit(providerName string) error {
	now := time.Now()
	e.rlMu.Lock()
	until, cooling := e.coolUntil[providerName]
	if cooling && now.Before(until) {
		remain := until.Sub(now)
		e.rlMu.Unlock()
		return &adapter.APIError{
			StatusCode: http.StatusTooManyRequests,
			Message:    fmt.Sprintf("provider %s cooling down after upstream 429 (%v left)", providerName, remain.Round(time.Second)),
		}
	}
	if cooling {
		delete(e.coolUntil, providerName)
	}
	rl := e.rateLimiters[providerName]
	e.rlMu.Unlock()

	if rl == nil {
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

// markProviderCooldown parks a provider for a while after it answered 429.
// Its TPM/RPM window needs time to slide; retrying the same key only adds
// latency and burns the retry budget that another account could use.
func (e *Engine) markProviderCooldown(providerName string) {
	sec := e.rateLimitCooldownSec()
	if sec <= 0 || providerName == "" {
		return
	}
	e.rlMu.Lock()
	e.coolUntil[providerName] = time.Now().Add(time.Duration(sec) * time.Second)
	e.rlMu.Unlock()
}

func (e *Engine) rateLimitCooldownSec() int {
	e.reloadMu.RLock()
	sec := e.cfg.Global.RateLimitCooldown
	e.reloadMu.RUnlock()
	if sec <= 0 {
		return 30
	}
	return sec
}

// isRequestCanceled reports whether err is the cancellation of the downstream
// request context (the client closed the connection), as opposed to our own
// deadline or an upstream failure.
func isRequestCanceled(c *gin.Context, err error) bool {
	ctxErr := c.Request.Context().Err()
	return ctxErr != nil && errors.Is(err, ctxErr)
}

func (e *Engine) advanceRRIndex(priority, groupLen int) int {
	e.rrMu.Lock()
	defer e.rrMu.Unlock()
	idx := e.rrIndex[priority]
	e.rrIndex[priority] = (idx + 1) % groupLen
	return idx
}

func (e *Engine) forwardRequest(c *gin.Context, body []byte, requestFormat string, provider *config.Provider, timeout int, requestID string) error {
	var respBody []byte
	var err error

	if requestFormat != provider.Format {
		converted, err := adapter.ConvertRequest(body, requestFormat, provider.Format)
		if err != nil {
			return fmt.Errorf("convert request: %w", err)
		}
		body = converted
	}

	body = prepareProviderRequest(body, provider)

	requestCtx := c.Request.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(requestCtx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	limiter, err := e.acquireConnSlot(requestCtx, provider)
	if err != nil {
		return &localCapacityError{err: err}
	}
	defer e.releaseConnSlot(limiter)

	if provider.Format == "openai" {
		respBody, err = adapter.CallOpenAIRawContext(requestCtx, provider.BaseURL, provider.APIKey, body, timeout, requestID, e.transport)
	} else {
		respBody, err = adapter.CallAnthropicRawContext(requestCtx, provider.BaseURL, provider.APIKey, provider.AuthType, body, timeout, requestID, e.transport)
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

func upstreamFormat(requestFormat string) string {
	if requestFormat == "responses" {
		return "openai"
	}
	return requestFormat
}

func (e *Engine) forwardRequestStream(c *gin.Context, body []byte, requestFormat string, provider *config.Provider, timeout int, tr *tracer.Recorder) error {
	if requestFormat != provider.Format {
		converted, err := adapter.ConvertRequest(body, requestFormat, provider.Format)
		if err != nil {
			return fmt.Errorf("convert request: %w", err)
		}
		body = converted
	}

	body = prepareProviderRequest(body, provider)

	path := "/chat/completions"
	if provider.Format == "anthropic" {
		path = "/v1/messages"
	}
	url := provider.BaseURL + path

	// Overall stream duration cap, configurable via global.max_stream_minutes
	// (default 3). A stream that trickles data forever would never trigger
	// the idle timeout, so this context acts as a hard safety net.
	e.reloadMu.RLock()
	maxStreamMin := e.cfg.Global.MaxStreamMinutes
	e.reloadMu.RUnlock()
	if maxStreamMin <= 0 {
		maxStreamMin = 3
	}
	streamCtx, streamCancel := context.WithTimeout(c.Request.Context(), time.Duration(maxStreamMin)*time.Minute)
	defer streamCancel()

	httpReq, err := http.NewRequestWithContext(streamCtx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if requestID := middleware.GetReqID(c); requestID != "" {
		httpReq.Header.Set("X-Request-ID", requestID)
	}
	if provider.Format == "openai" {
		httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	} else if provider.AuthType == "bearer" {
		httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	} else {
		httpReq.Header.Set("x-api-key", provider.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}

	slotCtx := streamCtx
	var slotCancel context.CancelFunc
	if timeout > 0 {
		slotCtx, slotCancel = context.WithTimeout(streamCtx, time.Duration(timeout)*time.Second)
		defer slotCancel()
	}
	limiter, err := e.acquireConnSlot(slotCtx, provider)
	if err != nil {
		return &localCapacityError{err: err}
	}
	defer e.releaseConnSlot(limiter)

	// A shared transport reuses TLS/HTTP2 connections and avoids leaving one
	// idle connection pool behind for every completed streaming request.
	client := &http.Client{Transport: e.streamTransport(timeout)}
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
	}

	idleReader := newIdleTimeoutReader(resp.Body, time.Duration(timeout)*time.Second)
	defer idleReader.Close()

	gate := newSSECommitWriter(c.Writer)
	tracker := newTerminalWriter(gate, requestFormat)
	if requestFormat != provider.Format {
		err = adapter.StreamConvertResponse(idleReader, tracker, provider.Format, requestFormat)
	} else {
		_, err = io.Copy(tracker, idleReader)
	}

	if errors.Is(err, errStreamTerminal) {
		err = nil
	}
	if err == nil && !tracker.Terminated() {
		err = tracker.Finish()
		if errors.Is(err, errStreamTerminal) {
			err = nil
		}
		if err == nil && !tracker.Terminated() {
			// Compatibility is limited to a clean EOF after a semantic finish.
			// Never turn a timeout, cancellation, or truncated event into success.
			err = writeStreamTerminator(tracker, requestFormat)
			if errors.Is(err, errStreamTerminal) {
				err = nil
			}
		}
	}
	if err == nil && !gate.Committed() {
		err = fmt.Errorf("upstream stream ended before the first event")
	}
	var writeErr *downstreamWriteError
	if err != nil && (errors.As(err, &writeErr) || isRequestCanceled(c, err)) {
		err = fmt.Errorf("%w: %v", errClientGoneIncomplete, err)
	}
	tr.LogStreamEnd(tracker.Answered(), tracker.Terminated(), err)
	// An upstream failure may be reported inside an already committed stream.
	// A disconnected downstream must not receive an additional error event.
	if err != nil && c.Writer.Written() && !errors.Is(err, errClientGoneIncomplete) {
		errMsg := err.Error()
		if len(errMsg) > 500 {
			errMsg = errMsg[:500] + "..."
		}
		var payload interface{}
		if requestFormat == "responses" {
			payload = map[string]string{"type": "error", "code": "stream_interrupted", "message": "stream interrupted: " + errMsg}
		} else if requestFormat == "anthropic" {
			payload = map[string]interface{}{
				"type": "error",
				"error": map[string]string{
					"type":    "api_error",
					"message": "stream interrupted: " + errMsg,
				},
			}
		} else {
			payload = map[string]interface{}{
				"error": map[string]string{"message": "stream interrupted: " + errMsg},
			}
		}
		payloadJSON, marshalErr := json.Marshal(payload)
		if marshalErr == nil {
			if requestFormat == "anthropic" || requestFormat == "responses" {
				fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", payloadJSON)
			} else {
				fmt.Fprintf(c.Writer, "data: %s\n\n", payloadJSON)
			}
		}
		if f, ok := c.Writer.(http.Flusher); ok {
			f.Flush()
		}
	}

	return err
}

type startTimeContextKey struct{}

var startTimeKey = startTimeContextKey{}

const maxPendingSSEBytes = 1 << 20

// sseCommitWriter buffers upstream output until it contains one complete SSE
// event with a data field. This keeps the downstream response uncommitted when
// an upstream only sends heartbeats, a partial event, or closes immediately,
// allowing the router to retry or fail over to another provider.
type sseCommitWriter struct {
	w         http.ResponseWriter
	pending   []byte
	committed bool
}

func newSSECommitWriter(w http.ResponseWriter) *sseCommitWriter {
	return &sseCommitWriter{w: w}
}

func (w *sseCommitWriter) Committed() bool {
	return w.committed
}

func (w *sseCommitWriter) Write(p []byte) (int, error) {
	if w.committed {
		if err := w.writeAndFlush(p); err != nil {
			return 0, err
		}
		return len(p), nil
	}

	w.pending = append(w.pending, p...)
	for {
		end := sseEventEnd(w.pending)
		if end < 0 {
			if len(w.pending) > maxPendingSSEBytes {
				return 0, fmt.Errorf("first SSE event exceeds %d bytes", maxPendingSSEBytes)
			}
			return len(p), nil
		}

		event := w.pending[:end]
		if sseEventHasData(event) {
			w.committed = true
			if err := w.writeAndFlush(w.pending); err != nil {
				return 0, err
			}
			w.pending = nil
			return len(p), nil
		}

		// Pre-data comments are heartbeats. They should not prevent failover and
		// need not be replayed once a real event arrives.
		w.pending = w.pending[end:]
	}
}

func (w *sseCommitWriter) writeAndFlush(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if n, err := w.w.Write(p); err != nil {
		return err
	} else if n != len(p) {
		return io.ErrShortWrite
	}
	if f, ok := w.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func sseEventEnd(data []byte) int {
	lf := bytes.Index(data, []byte("\n\n"))
	crlf := bytes.Index(data, []byte("\r\n\r\n"))
	switch {
	case lf < 0 && crlf < 0:
		return -1
	case lf < 0:
		return crlf + 4
	case crlf < 0:
		return lf + 2
	case lf < crlf:
		return lf + 2
	default:
		return crlf + 4
	}
}

func sseEventHasData(event []byte) bool {
	normalized := bytes.ReplaceAll(event, []byte("\r\n"), []byte("\n"))
	for _, line := range bytes.Split(normalized, []byte("\n")) {
		line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
		if bytes.Equal(line, []byte("data")) || bytes.HasPrefix(line, []byte("data:")) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────
//  Stream completion tracking
// ─────────────────────────────────────────────────────────────

// idleTimeoutError marks reads that stopped because the upstream went silent.
// It is a distinct type so the router can tell "upstream stalled" apart from
// "client finished" and "we hit the overall stream cap".
type idleTimeoutError struct{ timeout time.Duration }

func (e *idleTimeoutError) Error() string { return fmt.Sprintf("idle timeout after %v", e.timeout) }

func isIdleTimeoutErr(err error) bool {
	var target *idleTimeoutError
	return errors.As(err, &target)
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
		return 0, &idleTimeoutError{timeout: r.timeout}
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
	if providers, ok := e.filterByKeyword(modelName); ok {
		return providers
	}

	matched := make(map[string]bool)
	var result []*config.Provider

	if target, ok := e.matcher.Match(modelName); ok {
		// Keyword targets are hard selections, identical to the client sending
		// the keyword itself, and take precedence over provider names: a
		// provider literally named "P1" or "Max" is shadowed by the keyword.
		if providers, ok := e.filterByKeyword(target); ok {
			return providers
		}
		// Provider-name targets keep the legacy soft-pin semantics: the target
		// provider leads the chain and every other provider stays available as
		// fallback.
		for i := range e.cfg.Providers {
			if e.cfg.Providers[i].Name == target {
				result = append(result, &e.cfg.Providers[i])
				matched[target] = true
				break
			}
		}
	}

	for i := range e.cfg.Providers {
		if p := &e.cfg.Providers[i]; p.ModelID == modelName && !matched[p.Name] {
			result = append(result, p)
			matched[p.Name] = true
		}
	}

	if target, ok := e.matcher.Default(); ok && !matched[target] {
		// The default route resolves through the same rules but stays soft: a
		// keyword expands to its band at the default position, and the
		// remaining providers still follow as fallback.
		if keyword, isKeyword := e.filterByKeyword(target); isKeyword {
			for _, p := range keyword {
				if !matched[p.Name] {
					result = append(result, p)
					matched[p.Name] = true
				}
			}
		} else {
			for i := range e.cfg.Providers {
				if e.cfg.Providers[i].Name == target {
					result = append(result, &e.cfg.Providers[i])
					matched[target] = true
					break
				}
			}
		}
	}

	for i := range e.cfg.Providers {
		if p := &e.cfg.Providers[i]; !matched[p.Name] {
			result = append(result, p)
			matched[p.Name] = true
		}
	}

	return result
}

// filterByKeyword resolves a routing keyword to its provider set: "Max"
// (highest priority only), "Flash" (skip the highest group), "Medium" (all),
// or a "P{n}"/"P{n}up"/"P{n}down" priority band. ok is false when name is not
// a keyword. A valid keyword with no matching providers yields an empty set.
func (e *Engine) filterByKeyword(name string) ([]*config.Provider, bool) {
	switch name {
	case "Max":
		return e.filterProviders(func(p *config.Provider) bool {
			return p.Priority == e.minPriority()
		}), true
	case "Flash":
		return e.filterProviders(func(p *config.Provider) bool {
			return p.Priority > e.minPriority()
		}), true
	case "Medium":
		return e.allProviders(), true
	}
	if pri, mode, ok := config.ParsePriorityKeyword(name); ok {
		minP := e.minPriority()
		return e.filterProviders(func(p *config.Provider) bool {
			switch mode {
			case "only":
				return p.Priority == pri
			case "up":
				return p.Priority >= minP && p.Priority <= pri
			case "down":
				return p.Priority >= pri
			}
			return false
		}), true
	}
	return nil, false
}

func (e *Engine) allProviders() []*config.Provider {
	result := make([]*config.Provider, len(e.cfg.Providers))
	for i := range e.cfg.Providers {
		result[i] = &e.cfg.Providers[i]
	}
	return result
}

func (e *Engine) minPriority() int {
	if len(e.cfg.Providers) == 0 {
		return 0
	}
	return e.cfg.Providers[0].Priority
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

// prepareProviderRequest applies provider-specific compatibility adjustments
// after any request-format conversion and model alias replacement.
func prepareProviderRequest(body []byte, provider *config.Provider) []byte {
	body = setModelInBody(body, provider.ModelID)
	if provider.Format == "openai" && isSensenovaProvider(provider.BaseURL) {
		body = normalizeSensenovaReasoning(body)
	}
	return body
}

func isSensenovaProvider(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.EqualFold(u.Hostname(), "token.sensenova.cn")
}

// normalizeSensenovaReasoning adapts OpenAI-compatible reasoning parameters to
// the Sensenova endpoint. It requires reasoning=true when reasoning_effort is
// present. The valid values are: low, medium, high, xhigh, none. Some models
// (glm-5.2, deepseek-v4-pro, kimi-k3) also accept max; xhigh is universally
// accepted and is NOT remapped to avoid 400 errors on models that reject max.
func normalizeSensenovaReasoning(body []byte) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}

	effort, ok := data["reasoning_effort"].(string)
	if !ok || strings.TrimSpace(effort) == "" {
		return body
	}
	// Sensenova rejects reasoning_effort unless reasoning mode is enabled.
	data["reasoning"] = true

	modified, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return modified
}

// normalizeDeveloperRole rewrites the OpenAI "developer" role to "system" in the
// messages array. OpenAI defines developer as the successor of system, but no
// other vendor is obliged to accept it: Sensenova's glm-5.2 endpoint answers such
// a request with 400 invalid_parameter_error, and because the router degrades
// across every account of a group, one unsupported role fails the whole request
// even when sibling models (deepseek-flash, kimi-k3) tolerate it.
//
// Only the role string is touched; message count, order, content, tool_calls and
// tool_call_id are preserved. The rewrite is idempotent and never fails a request:
// an unparsable body, a missing messages array, or a marshal error leaves the
// original bytes in place.
func normalizeDeveloperRole(body []byte) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}
	messages, ok := data["messages"].([]interface{})
	if !ok {
		return body
	}

	changed := false
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role == "developer" {
			message["role"] = "system"
			changed = true
		}
	}
	if !changed {
		return body
	}

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

func applyModelDefaults(body []byte, modelName string, rules []config.ModelRule) ([]byte, int) {
	var rule *config.ModelRule
	for i := range rules {
		if rules[i].Model == modelName {
			rule = &rules[i]
			break
		}
	}
	if rule == nil || len(rule.Defaults) == 0 {
		return body, 0
	}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return body, 0
	}

	changed := false
	modelTimeout := 0
	for k, v := range rule.Defaults {
		if k == "timeout" {
			if existing, ok := data[k]; ok {
				if n, valid := configNumberAsInt(existing); valid && n > 0 {
					modelTimeout = n
				}
			} else if n, ok := configNumberAsInt(v); ok && n > 0 {
				modelTimeout = n
			}
			continue
		}
		if _, exists := data[k]; !exists {
			data[k] = v
			changed = true
		}
	}

	if !changed {
		return body, modelTimeout
	}

	modified, err := json.Marshal(data)
	if err != nil {
		return body, modelTimeout
	}
	return modified, modelTimeout
}

func configNumberAsInt(value interface{}) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), int64(int(n)) == n
	case uint64:
		return int(n), uint64(int(n)) == n
	case float64:
		return int(n), float64(int(n)) == n
	case float32:
		return int(n), float32(int(n)) == n
	default:
		return 0, false
	}
}

func effectiveTimeout(providerTimeout, modelTimeout int) int {
	if modelTimeout > 0 {
		return modelTimeout
	}
	return providerTimeout
}

// dataURLPattern matches data: URLs carrying base64 payloads (images attached
// by clients). Logged bodies keep a short prefix so the attachment is still
// visible in traces without inflating the log file by megabytes per request.
var dataURLPattern = regexp.MustCompile(`data:image/[a-zA-Z0-9.+-]+;base64,[A-Za-z0-9+/=]+`)

func truncateDataURLs(s string) string {
	return dataURLPattern.ReplaceAllStringFunc(s, func(m string) string {
		const keep = 64
		if len(m) <= keep+32 {
			return m
		}
		return fmt.Sprintf("%s...[base64 truncated, total %d KB]", m[:keep], len(m)/1024)
	})
}

// buildRequestBodyLog renders the request body for logging according to level:
//
//	"off"     → "" (no content)
//	"snippet" → first message's content, truncated to 80 chars
//	"full"    → the entire request body, pretty-printed (model/messages/role/stream/tools/...)
//	            with base64 image data URLs truncated
func buildRequestBodyLog(body []byte, level string) string {
	switch level {
	case "off":
		return ""
	case "full":
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err != nil {
			return truncateDataURLs(string(body)) // fall back to raw if not valid JSON
		}
		return truncateDataURLs(pretty.String())
	default: // "snippet"
		return extractBodySnippet(body)
	}
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
		return "skip highest priority group"
	case "Medium":
		return "all priorities"
	}
	if pri, mode, ok := config.ParsePriorityKeyword(modelName); ok {
		switch mode {
		case "only":
			return fmt.Sprintf("P%d only", pri)
		case "up":
			return fmt.Sprintf("P1..P%d", pri)
		case "down":
			return fmt.Sprintf("P%d..max", pri)
		}
	}
	return "model match"
}

// ─────────────────────────────────────────────────────────────
//  Circuit breaker persistence
// ─────────────────────────────────────────────────────────────

// cbStateJSON is the on-disk format for circuit breaker state.
type cbStateJSON struct {
	FailureCount  map[int]int    `json:"failure_count"`
	SkipRemaining map[int]int    `json:"skip_remaining"`
	CircuitsOpen  map[int]string `json:"circuits_open"` // priority → RFC3339 timestamp
}

// errorDetail extracts an HTTP status code and message from a forwarding error.
// Non-API errors (network/timeout) return statusCode=0 with the error string.
func errorDetail(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if apiErr, ok := err.(*adapter.APIError); ok {
		return apiErr.StatusCode, apiErr.Message
	}
	return 0, err.Error()
}

// CBSnapshot returns a read-only view of every priority's circuit-breaker
// state for the control console. It mirrors shouldSkipGroup's cooldown math
// but never mutates state.
func (e *Engine) CBSnapshot() []stats.CBState {
	e.reloadMu.RLock()
	cooldownSec := e.cfg.Global.CBCooldown
	e.reloadMu.RUnlock()
	cooldown := time.Duration(cooldownSec) * time.Second

	e.cbMu.Lock()
	defer e.cbMu.Unlock()

	// Collect priorities seen across all CB maps.
	seen := make(map[int]struct{})
	for p := range e.failureCount {
		seen[p] = struct{}{}
	}
	for p := range e.circuitOpenSince {
		seen[p] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}

	out := make([]stats.CBState, 0, len(seen))
	for p := range seen {
		openSince := e.circuitOpenSince[p]
		open := openSince != nil && *openSince != (time.Time{})
		rem := 0
		if open {
			elapsed := time.Since(*openSince)
			if left := cooldown - elapsed; left > 0 {
				rem = int(left.Round(time.Second).Seconds())
			}
		}
		out = append(out, stats.CBState{
			Priority:       p,
			Open:           open,
			Failures:       e.failureCount[p],
			CooldownSec:    cooldownSec,
			CooldownRemSec: rem,
			SkipRemaining:  e.skipRemaining[p],
		})
	}
	// Sort by priority ascending for stable UI ordering.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Priority > out[j].Priority; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func (e *Engine) cbStatePath() string {
	dir := filepath.Dir(e.configPath)
	return filepath.Join(dir, ".cb_state.json")
}

func (e *Engine) saveCBState() {
	if e.configPath == "" {
		return
	}
	e.fileMu.Lock()
	defer e.fileMu.Unlock()
	e.cbMu.Lock()
	state := e.captureCBState()
	e.cbMu.Unlock()

	// Write outside the CB lock to minimize hold time.
	// Use temp file + rename for atomic write (prevents concurrent write corruption).
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
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
