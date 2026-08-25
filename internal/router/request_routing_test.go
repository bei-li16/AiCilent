package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

func TestHandleRequestCorrelatesConcurrentProviderResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const requestCount = 8

	started := make(chan struct{}, requestCount)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Session string `json:"session"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		started <- struct{}{}
		<-release

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%q,"session":%q}`, request.Session, request.Session)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Global: config.GlobalConfig{CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1},
		Providers: []config.Provider{{
			Name:     "shared-provider",
			ModelID:  "m",
			APIKey:   "key",
			BaseURL:  upstream.URL + "/v1",
			Priority: 1,
			Format:   "openai",
			Timeout:  5,
		}},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	router := gin.New()
	router.Use(middleware.DetectFormat())
	router.POST("/chat/completions", e.HandleRequest)

	type result struct {
		session string
		status  int
		body    string
	}
	results := make(chan result, requestCount)
	var wg sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		session := fmt.Sprintf("session-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestBody := fmt.Sprintf(`{"model":"m","session":%q}`, session)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(requestBody))
			router.ServeHTTP(recorder, request)
			results <- result{session: session, status: recorder.Code, body: recorder.Body.String()}
		}()
	}

	for i := 0; i < requestCount; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("not all concurrent requests reached the provider")
		}
	}
	close(release)
	wg.Wait()
	close(results)

	for result := range results {
		if result.status != http.StatusOK {
			t.Fatalf("session %s status = %d, body = %s", result.session, result.status, result.body)
		}
		var response struct {
			ID      string `json:"id"`
			Session string `json:"session"`
		}
		if err := json.Unmarshal([]byte(result.body), &response); err != nil {
			t.Fatalf("session %s returned invalid JSON %q: %v", result.session, result.body, err)
		}
		if response.ID != result.session || response.Session != result.session {
			t.Fatalf("session %s received response for %s: %s", result.session, response.Session, result.body)
		}
	}
}
