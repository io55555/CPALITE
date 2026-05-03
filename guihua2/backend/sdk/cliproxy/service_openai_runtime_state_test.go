package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestServiceApplyOpenAICompatRuntimeStateFromDisk(t *testing.T) {
	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	statePath := filepath.Join(workDir, "openai-compat-runtime-state.json")
	stateJSON := `{
  "version": 1,
  "updated_at": "2026-05-03T12:00:00Z",
  "entries": [
    {
      "auth_id": "openai-compatibility:groq:test",
      "disabled": true,
      "unavailable": true,
      "status": "disabled",
      "status_message": "status-ruler:test-disable"
    }
  ]
}`
	if err := os.WriteFile(statePath, []byte(stateJSON), 0o644); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "openai-compatibility:groq:test",
		Provider: "groq",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_test",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	service := &Service{
		cfg:        &config.Config{},
		configPath: configPath,
		coreManager: manager,
	}

	service.applyOpenAICompatRuntimeStateFromDisk()

	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatalf("expected auth to exist")
	}
	if !got.Disabled || got.Status != coreauth.StatusDisabled {
		t.Fatalf("restored auth disabled=%v status=%q", got.Disabled, got.Status)
	}
	if got.StatusMessage != "status-ruler:test-disable" {
		t.Fatalf("status message = %q", got.StatusMessage)
	}
}
