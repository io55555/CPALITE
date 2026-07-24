package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Sanity: gin allows /plugins exact + /plugins/*pluginPath catch-all together,
// and rejects /plugins/:id/config with /plugins/:id/*path (documented panic).
func TestGinPluginRoutePatternsDoNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/plugins", func(c *gin.Context) { c.String(http.StatusOK, "list") })
	g.Any("/plugins/*pluginPath", func(c *gin.Context) {
		c.String(http.StatusOK, c.Param("pluginPath"))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "list" {
		t.Fatalf("list = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v0/management/plugins/grok-manager/status", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "/grok-manager/status" {
		t.Fatalf("plugin path = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v0/management/plugins/foo/config", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "/foo/config" {
		t.Fatalf("config path = %d %q", rec.Code, rec.Body.String())
	}
}
