package management

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestOpenAICompatRuntimeStatePersistsAndReloads(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	cfg := &config.Config{}

	manager := coreauth.NewManager(nil, nil, nil)
	disabledAuth := &coreauth.Auth{
		ID:         "openai-compatibility:groq:test",
		Provider:   "groq",
		Status:     coreauth.StatusDisabled,
		Disabled:   true,
		Unavailable: true,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_test_12345678",
		},
		LastError: &coreauth.Error{
			Message:    "organization_restricted",
			HTTPStatus: 400,
		},
	}
	if _, err := manager.Register(context.Background(), disabledAuth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandler(cfg, configPath, manager)
	if err := h.persistOpenAICompatRuntimeState(); err != nil {
		t.Fatalf("persist runtime state: %v", err)
	}

	reloadedManager := coreauth.NewManager(nil, nil, nil)
	activeAuth := &coreauth.Auth{
		ID:       disabledAuth.ID,
		Provider: "groq",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_test_12345678",
		},
	}
	if _, err := reloadedManager.Register(context.Background(), activeAuth); err != nil {
		t.Fatalf("register reloaded auth: %v", err)
	}

	reloadedHandler := NewHandler(cfg, configPath, reloadedManager)
	got, ok := reloadedManager.GetByID(disabledAuth.ID)
	if !ok || got == nil {
		t.Fatalf("expected reloaded auth")
	}
	if !got.Disabled || got.Status != coreauth.StatusDisabled {
		t.Fatalf("reloaded auth disabled=%v status=%q, want true/%q", got.Disabled, got.Status, coreauth.StatusDisabled)
	}
	if got.LastError == nil || got.LastError.Message != "organization_restricted" {
		t.Fatalf("reloaded last error = %#v", got.LastError)
	}
	_ = reloadedHandler
}
