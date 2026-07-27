package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestRecordAPIRequestClonesDeferredBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body := []byte(`{"model":"original"}`)

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   body,
	})
	body[10] = 'X'

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not captured")
	}
	requests, ok := value.([]logging.DeferredAPIRequest)
	if !ok || len(requests) != 1 {
		t.Fatalf("deferred API requests = %#v, want one request", value)
	}
	captured := string(requests[0]())
	if !strings.Contains(captured, `{"model":"original"}`) {
		t.Fatalf("captured API request = %q, want original body", captured)
	}
}

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestRecordAPIRequestMaterializesAPIRequestWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer secret-token")
	headers.Set("Content-Type", "application/json")
	RecordAPIRequest(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}, UpstreamRequestLog{
		URL:     "https://grok.example/v1/responses",
		Method:  http.MethodPost,
		Headers: headers,
		Body:    []byte(`{"model":"grok-4.5","input":"hi"}`),
		Provider: "xai",
	})

	value, exists := ginCtx.Get(apiRequestKey)
	if !exists {
		t.Fatal("API_REQUEST was not materialized")
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		t.Fatalf("API_REQUEST type = %T", value)
	}
	if !strings.Contains(text, "Upstream URL: https://grok.example/v1/responses") {
		t.Fatalf("API_REQUEST missing upstream url: %s", text)
	}
	if !strings.Contains(text, `"model":"grok-4.5"`) {
		t.Fatalf("API_REQUEST missing body: %s", text)
	}
	if strings.Contains(text, "<missing>") {
		t.Fatalf("API_REQUEST still missing stub: %s", text)
	}
}
