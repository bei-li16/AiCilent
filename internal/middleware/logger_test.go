package middleware

import (
	"io"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDsAreUniqueAndReturnedToClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldWriter := gin.DefaultWriter
	gin.DefaultWriter = io.Discard
	defer func() { gin.DefaultWriter = oldWriter }()

	const count = 500
	ids := make(chan string, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/health", nil)
			c, _ := gin.CreateTestContext(w)
			c.Request = req
			Logger()(c)
			ids <- GetReqID(c)
			if w.Header().Get("X-Request-ID") == "" {
				t.Errorf("request ID response header is missing")
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool, count)
	for id := range ids {
		if id == "" {
			t.Fatal("request ID is empty")
		}
		if seen[id] {
			t.Fatalf("duplicate request ID %q", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("got %d IDs, want %d", len(seen), count)
	}
}
