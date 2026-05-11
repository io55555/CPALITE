package packetcapture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestCaptureFromUsageRecordWaitsForClientResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := InitDefaultInLogDir(t.TempDir()); err != nil {
		t.Fatalf("InitDefaultInLogDir: %v", err)
	}
	defer CloseDefault()
	if err := DefaultStore().SetEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := context.WithValue(logging.WithRequestID(context.Background(), "req-1"), "gin", c)

	CaptureFromUsageRecord(ctx, coreusage.Record{
		Provider:    "groq",
		Model:       "llama-3.1-8b-instant",
		Source:      "groq",
		RequestedAt: time.Now(),
		RawRequest:  "=== 客户端发给CPA的完整数据包 ===\nPOST /v1/chat/completions HTTP/1.1\n\n{}\n\n=== CPA发给供应商的完整数据包 ===\nPOST /openai/v1/chat/completions HTTP/1.1\n\n{}",
		RawResponse: "=== 供应商返回CPA的完整数据包 ===\nHTTP/2.0 200 OK\n\n{}",
	})

	if got, err := DefaultStore().Query(context.Background(), QueryOptions{}); err != nil || len(got) != 0 {
		t.Fatalf("records before client response = %d, err=%v", len(got), err)
	}

	c.Set("USAGE_CLIENT_RESPONSE", "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"ok\":true}")
	FlushPendingRecords(c)

	got, err := DefaultStore().Query(context.Background(), QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records len = %d, want 1", len(got))
	}
	detail, ok, err := DefaultStore().Get(context.Background(), got[0].ID)
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if detail.Packets.ClientResponse == "" {
		t.Fatalf("client response packet missing: %+v", detail.Packets)
	}
}

func TestCaptureFromUsageRecordStoresUnmarkedRawPackets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := InitDefaultInLogDir(t.TempDir()); err != nil {
		t.Fatalf("InitDefaultInLogDir: %v", err)
	}
	defer CloseDefault()
	if err := DefaultStore().SetEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	CaptureFromUsageRecord(context.Background(), coreusage.Record{
		Provider:    "compat",
		Model:       "model-a",
		Source:      "compat",
		RequestedAt: time.Now(),
		RawRequest:  "POST /v1/chat/completions HTTP/1.1\n\n{}",
		RawResponse: "HTTP/1.1 200 OK\n\n{\"ok\":true}",
	})

	got, err := DefaultStore().Query(context.Background(), QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records len = %d, want 1", len(got))
	}
	detail, ok, err := DefaultStore().Get(context.Background(), got[0].ID)
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if detail.Packets.ClientRequest == "" || detail.Packets.UpstreamResponse == "" {
		t.Fatalf("unmarked packets missing: %+v", detail.Packets)
	}
}

func TestRulesWildcardRandomAndOriginalReplacement(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	_, err = store.UpsertRule(context.Background(), Rule{
		Name:        "ua",
		Enabled:     true,
		Packet:      "upstream_request",
		Part:        "header",
		Header:      "User-Agent",
		Operator:    "wildcard",
		Value:       "cli-*",
		Action:      "replace",
		Replacement: "{{random_curl_ua}}",
	})
	if err != nil {
		t.Fatalf("UpsertRule ua: %v", err)
	}
	packet := "POST / HTTP/1.1\nUser-Agent: cli-proxy-openai-compat\n\n{}"
	filtered, errBlock, _ := ApplyRules(context.Background(), Record{Provider: "groq"}, "upstream_request", packet)
	if errBlock != nil {
		t.Fatalf("ApplyRules block: %v", errBlock)
	}
	if got := headerValue(filtered, "User-Agent"); got == "" || got == "cli-proxy-openai-compat" {
		t.Fatalf("User-Agent not replaced: %q", got)
	}

	_, err = store.UpsertRule(context.Background(), Rule{
		Name:        "append",
		Enabled:     true,
		Priority:    1,
		Packet:      "upstream_request",
		Part:        "body_json",
		JSONPath:    "messages.0.content",
		Operator:    "contains",
		Value:       "hello",
		Action:      "replace",
		Replacement: "{original} world",
	})
	if err != nil {
		t.Fatalf("UpsertRule append: %v", err)
	}
	filtered, errBlock, _ = ApplyRules(context.Background(), Record{Provider: "groq"}, "upstream_request", "POST / HTTP/1.1\n\n{\"messages\":[{\"content\":\"hello\"}]}")
	if errBlock != nil {
		t.Fatalf("ApplyRules append block: %v", errBlock)
	}
	if body := PacketBody(filtered); body != "{\"messages\":[{\"content\":\"hello world\"}]}" {
		t.Fatalf("body = %s", body)
	}
}
