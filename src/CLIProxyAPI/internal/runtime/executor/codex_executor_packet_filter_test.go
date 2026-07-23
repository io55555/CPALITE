package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/packetcapture"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexManagerPacketFilter429CooldownFromStreamFailureEvent(t *testing.T) {
	dir := t.TempDir()
	if err := packetcapture.InitDefaultInLogDir(dir); err != nil {
		t.Fatalf("InitDefaultInLogDir: %v", err)
	}
	t.Cleanup(func() { _ = packetcapture.CloseDefault() })
	store := packetcapture.DefaultStore()
	if store == nil {
		t.Fatal("expected packet capture store")
	}
	if _, err := store.UpsertRule(context.Background(), packetcapture.Rule{
		Name:            "codex响应码429冷却24h",
		Enabled:         true,
		RecordHistory:   true,
		Priority:        100,
		Provider:        "codex",
		Packet:          "upstream_response",
		Part:            "status",
		Operator:        "num_eq",
		ValueNumber:     429,
		Action:          "cooldown",
		Target:          "api_key",
		CooldownSeconds: 86400,
	}); err != nil {
		t.Fatalf("UpsertRule: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"status":"failed","error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":86400}}}` + "\n\n"))
	}))
	defer server.Close()

	const authID = "codex-user@example.com.json"
	const model = "gpt-5-codex"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{
		CodexQuotaCooldownBaseSeconds: 86400,
		CodexQuotaCooldownMaxSeconds:  604800,
	})
	manager.RegisterExecutor(NewCodexExecutor(&config.Config{
		CodexQuotaCooldownBaseSeconds: 86400,
		CodexQuotaCooldownMaxSeconds:  604800,
	}))
	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID:       authID,
		FileName: authID,
		Label:    "user@example.com",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			"base_url":  server.URL,
			"api_key":   "codex-token",
			"auth_kind": "oauth",
			"path":      ".cli-proxy-api/" + authID,
		},
		Metadata: map[string]any{
			"email": "user@example.com",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"gpt-5-codex","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected stream 429 error")
	}

	triggers, err := store.ListTriggers(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(triggers) != 1 || triggers[0].Action != "cooldown" || triggers[0].CooldownSeconds != 86400 {
		t.Fatalf("triggers = %+v, want one 86400s cooldown", triggers)
	}
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	if !updated.Unavailable || time.Until(updated.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("auth cooldown = unavailable:%v next:%v status:%s message:%q", updated.Unavailable, updated.NextRetryAfter, updated.Status, updated.StatusMessage)
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("model cooldown = %+v, want about 24h", state)
	}
}
