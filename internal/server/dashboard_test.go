package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-proxy/internal/version"
	"github.com/gin-gonic/gin"
)

func TestEmbeddedDashboardBuildIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", serveWeb("web/index.html", "text/html; charset=utf-8"))
	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("GET", "/", nil))
	body := res.Body.String()
	asset := url.QueryEscape(version.Version + "." + version.Commit + "." + version.BuildDate)
	for _, want := range []string{`content="` + version.Version + `"`, `/style.css?v=` + asset, `/dashboard-core.js?v=` + asset, `/app.js?v=` + asset, `role="tablist"`, `id="confirmDialog"`} {
		if !strings.Contains(body, want) {
			t.Errorf("embedded dashboard missing %q", want)
		}
	}
	if strings.Contains(body, "__DASHBOARD_") {
		t.Fatal("build placeholders remain")
	}
	if res.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal("HTML must revalidate after deployment")
	}
}

func TestEmbeddedDashboardAssets(t *testing.T) {
	for _, name := range []string{"app.js", "dashboard-core.js", "style.css"} {
		t.Run(name, func(t *testing.T) {
			r := gin.New()
			r.GET("/"+name, serveWeb("web/"+name, "text/plain"))
			res := httptest.NewRecorder()
			r.ServeHTTP(res, httptest.NewRequest("GET", "/"+name, nil))
			if res.Code != 200 || res.Body.Len() < 100 {
				t.Fatalf("asset unavailable: %s", name)
			}
		})
	}
}
