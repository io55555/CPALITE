package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/packetcapture"
)

func TestPacketCaptureMiddlewareIndependentOfRequestLogger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	t.Cleanup(func() { _ = packetcapture.CloseDefault() })
	if err := packetcapture.InitDefaultInLogDir(dir); err != nil {
		t.Fatalf("init packet capture store: %v", err)
	}
	store := packetcapture.DefaultStore()
	if store == nil {
		t.Fatal("packet capture store is nil")
	}
	if err := store.SetEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable packet capture: %v", err)
	}

	engine := gin.New()
	engine.Use(PacketCaptureMiddleware())
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set("API_REQUEST", []byte("POST /openai/v1/chat/completions HTTP/1.1\nUser-Agent: test\n\n{\"model\":\"llama\"}"))
		c.Set("API_RESPONSE", []byte("HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"id\":\"ok\"}"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}

	items, err := store.Query(context.Background(), packetcapture.QueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("query captures: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("capture count = %d, want 1", len(items))
	}
	record, ok, err := store.Get(context.Background(), items[0].ID)
	if err != nil || !ok {
		t.Fatalf("get capture ok=%t err=%v", ok, err)
	}
	if !strings.Contains(record.Packets.ClientRequest, `{"model":"llama"}`) {
		t.Fatalf("client request packet missing body: %q", record.Packets.ClientRequest)
	}
	if !strings.Contains(record.Packets.ClientResponse, `{"ok":true}`) {
		t.Fatalf("client response packet missing body: %q", record.Packets.ClientResponse)
	}
}
