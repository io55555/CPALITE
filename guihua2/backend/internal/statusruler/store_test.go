package statusruler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/breaker"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestApplyRuleActionPersistsOpenAICompatRuntimeState(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "openai-compatibility:test-provider:test-key",
		Provider: "openai-compatibility",
		Label:    "test-provider",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "test-provider",
			"provider_key": "test-provider",
			"api_key":      "sk-test",
			"base_url":     "https://example.invalid/v1",
		},
	}
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "openai-compat-runtime-state.json")
	runtime := Runtime{
		authMgr:          manager,
		runtimeStatePath: statePath,
		persistMu:        &sync.Mutex{},
	}

	applyRuleAction(context.Background(), runtime, breaker.AuthSnapshot{
		AuthID:    auth.ID,
		AuthIndex: auth.EnsureIndex(),
		Provider:  auth.Provider,
	}, Rule{Name: "unauthorized", Action: "disable_auth"})

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read runtime-state: %v", err)
	}

	var payload openAICompatRuntimeStateFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal runtime-state: %v", err)
	}
	if len(payload.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(payload.Entries))
	}
	entry := payload.Entries[0]
	if !entry.Disabled {
		t.Fatalf("entry disabled = false, want true")
	}
	if entry.AuthIndex == "" {
		t.Fatalf("entry auth_index empty")
	}
	if entry.Status != string(coreauth.StatusDisabled) {
		t.Fatalf("entry status = %q, want %q", entry.Status, coreauth.StatusDisabled)
	}
}
