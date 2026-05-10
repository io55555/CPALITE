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
