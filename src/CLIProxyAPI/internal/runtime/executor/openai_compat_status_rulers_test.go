package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/openai_compat_state"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenAICompatExecutorAppliesStatusRulers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"organization_restricted","message":"restricted"}}`))
	}))
	defer server.Close()

	if err := openai_compat_state.InitDefault(filepath.Join(t.TempDir(), "state.db")); err != nil {
		t.Fatalf("init state: %v", err)
	}
	defer func() { _ = openai_compat_state.CloseDefault() }()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "groq",
				BaseURL: server.URL,
				StatusRulers: []config.OpenAICompatibilityStatusRuler{
					{
						Name: "organization restricted",
						When: config.OpenAICompatibilityStatusRulerWhen{
							Status:     http.StatusBadRequest,
							JSONPath:   "error.code",
							JSONEquals: "organization_restricted",
						},
						Action: "disable",
					},
				},
			},
		},
	}
	exec := NewOpenAICompatExecutor("groq", cfg)
	auth := &cliproxyauth.Auth{
		Provider: "groq",
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"base_url":     server.URL,
			"api_key":      "test-key",
		},
	}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "llama",
		Payload: []byte(`{"model":"llama","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatal("expected upstream error")
	}

	st, ok := openai_compat_state.DefaultService().Get("groq", "test-key")
	if !ok {
		t.Fatal("expected key state")
	}
	if st.Enabled || st.Status != openai_compat_state.StatusDisabled {
		t.Fatalf("state = enabled:%v status:%s, want disabled", st.Enabled, st.Status)
	}
	if !strings.Contains(st.RawResponse, "organization_restricted") || !strings.Contains(st.RawRequest, "POST /chat/completions") {
		t.Fatalf("raw packets not recorded: request=%q response=%q", st.RawRequest, st.RawResponse)
	}
}

func TestOpenAICompatExecutorStatusRulerReturnStopsRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model \"qwen3.5:122b\" not found","type":"not_found_error"}}`))
	}))
	defer server.Close()

	if err := openai_compat_state.InitDefault(filepath.Join(t.TempDir(), "state.db")); err != nil {
		t.Fatalf("init state: %v", err)
	}
	defer func() { _ = openai_compat_state.CloseDefault() }()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "ollama",
				BaseURL: server.URL,
				StatusRulers: []config.OpenAICompatibilityStatusRuler{
					{
						Name: "model not found",
						When: config.OpenAICompatibilityStatusRulerWhen{
							Status:       http.StatusNotFound,
							JSONPath:     "error.message",
							JSONContains: "model",
							BodyContains: "not found",
						},
						Action:        "return",
						ClientStatus:  http.StatusNotFound,
						ClientMessage: "model not found",
					},
				},
			},
		},
	}
	exec := NewOpenAICompatExecutor("ollama", cfg)
	auth := &cliproxyauth.Auth{
		Provider: "ollama",
		Attributes: map[string]string{
			"compat_name":  "ollama",
			"provider_key": "ollama",
			"base_url":     server.URL,
			"api_key":      "test-key",
		},
	}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen3.5:122b",
		Payload: []byte(`{"model":"qwen3.5:122b","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatal("expected upstream error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %#v, want 404", err)
	}
	if got := err.Error(); got != "model not found" {
		t.Fatalf("error = %q, want model not found", got)
	}
	stopRetry, ok := err.(interface{ StopRetry() bool })
	if !ok || !stopRetry.StopRetry() {
		t.Fatalf("expected StopRetry error, got %#v", err)
	}
	authFault, ok := err.(interface{ AuthFault() bool })
	if !ok || authFault.AuthFault() {
		t.Fatalf("expected non-auth-fault terminal error, got %#v", err)
	}
	st, ok := openai_compat_state.DefaultService().Get("ollama", "test-key")
	if ok && (!st.Enabled || st.Status == openai_compat_state.StatusDisabled) {
		t.Fatalf("terminal return should not disable key: %#v", st)
	}
}
