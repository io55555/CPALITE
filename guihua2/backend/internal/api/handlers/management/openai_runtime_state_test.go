package management

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
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

func TestOpenAICompatRuntimeStateOmitsActiveEntries(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	cfg := &config.Config{}

	manager := coreauth.NewManager(nil, nil, nil)
	activeAuth := &coreauth.Auth{
		ID:       "openai-compatibility:groq:active",
		Provider: "groq",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_active",
		},
	}
	disabledAuth := &coreauth.Auth{
		ID:         "openai-compatibility:groq:disabled",
		Provider:   "groq",
		Status:     coreauth.StatusDisabled,
		Disabled:   true,
		Unavailable: true,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_disabled",
		},
	}
	if _, err := manager.Register(context.Background(), activeAuth); err != nil {
		t.Fatalf("register active auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), disabledAuth); err != nil {
		t.Fatalf("register disabled auth: %v", err)
	}

	h := NewHandler(cfg, configPath, manager)
	h.openAICompatStateMu.Lock()
	h.openAICompatStateApplied = true
	h.openAICompatStateMu.Unlock()
	if err := h.persistOpenAICompatRuntimeState(); err != nil {
		t.Fatalf("persist runtime state: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(workDir, openAICompatRuntimeStateFileName))
	if err != nil {
		t.Fatalf("read runtime state file: %v", err)
	}

	var stateFile openAICompatRuntimeStateFile
	if err := json.Unmarshal(raw, &stateFile); err != nil {
		t.Fatalf("unmarshal runtime state file: %v", err)
	}
	if len(stateFile.Entries) != 1 {
		t.Fatalf("entries len=%d, want 1", len(stateFile.Entries))
	}
	if stateFile.Entries[0].AuthID != disabledAuth.ID {
		t.Fatalf("persisted auth id=%q, want %q", stateFile.Entries[0].AuthID, disabledAuth.ID)
	}
}

func TestOpenAICompatRuntimeStateWaitsForMatchingAuthBeforeApplying(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	cfg := &config.Config{}

	sourceManager := coreauth.NewManager(nil, nil, nil)
	sourceAuth := &coreauth.Auth{
		ID:         "openai-compatibility:groq:delayed",
		Provider:   "groq",
		Status:     coreauth.StatusDisabled,
		Disabled:   true,
		Unavailable: true,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_delayed",
		},
	}
	if _, err := sourceManager.Register(context.Background(), sourceAuth); err != nil {
		t.Fatalf("register source auth: %v", err)
	}
	sourceHandler := NewHandler(cfg, configPath, sourceManager)
	if err := sourceHandler.persistOpenAICompatRuntimeState(); err != nil {
		t.Fatalf("persist runtime state: %v", err)
	}

	reloadedManager := coreauth.NewManager(nil, nil, nil)
	reloadedHandler := NewHandler(cfg, configPath, reloadedManager)
	if reloadedHandler.openAICompatStateApplied {
		t.Fatal("runtime state should stay unapplied when no auth matched yet")
	}

	reloadedAuth := &coreauth.Auth{
		ID:       sourceAuth.ID,
		Provider: "groq",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_delayed",
		},
	}
	if _, err := reloadedManager.Register(context.Background(), reloadedAuth); err != nil {
		t.Fatalf("register delayed auth: %v", err)
	}

	reloadedHandler.applyOpenAICompatRuntimeState()

	got, ok := reloadedManager.GetByID(sourceAuth.ID)
	if !ok || got == nil {
		t.Fatalf("expected reloaded auth")
	}
	if !got.Disabled || got.Status != coreauth.StatusDisabled {
		t.Fatalf("reloaded auth disabled=%v status=%q, want true/%q", got.Disabled, got.Status, coreauth.StatusDisabled)
	}
}

func TestOpenAICompatibilityWithAuthIndexTriggersPendingRuntimeStateApply(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "groq",
				BaseURL: "https://example.invalid/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "gsk_pending", ProxyURL: "direct"},
				},
			},
		},
	}

	sourceManager := coreauth.NewManager(nil, nil, nil)
	sourceAuth := &coreauth.Auth{
		ID:         "openai-compatibility:groq:pending",
		Provider:   "groq",
		Status:     coreauth.StatusDisabled,
		Disabled:   true,
		Unavailable: true,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_pending",
		},
	}
	if _, err := sourceManager.Register(context.Background(), sourceAuth); err != nil {
		t.Fatalf("register source auth: %v", err)
	}
	sourceHandler := NewHandler(cfg, configPath, sourceManager)
	if err := sourceHandler.persistOpenAICompatRuntimeState(); err != nil {
		t.Fatalf("persist runtime state: %v", err)
	}

	reloadedManager := coreauth.NewManager(nil, nil, nil)
	idGen := synthesizer.NewStableIDGenerator()
	authID, _ := idGen.Next("openai-compatibility:groq", "gsk_pending", "https://example.invalid/v1", "direct")
	reloadedAuth := &coreauth.Auth{
		ID:       authID,
		Provider: "groq",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_pending",
		},
	}
	if _, err := reloadedManager.Register(context.Background(), reloadedAuth); err != nil {
		t.Fatalf("register reloaded auth: %v", err)
	}

	reloadedHandler := NewHandler(cfg, configPath, reloadedManager)
	reloadedHandler.openAICompatStateMu.Lock()
	reloadedHandler.openAICompatStateApplied = false
	reloadedHandler.openAICompatStateMu.Unlock()

	gotProviders := reloadedHandler.openAICompatibilityWithAuthIndex()
	if len(gotProviders) != 1 || len(gotProviders[0].APIKeyEntries) != 1 {
		t.Fatalf("unexpected providers payload: %#v", gotProviders)
	}
	if !gotProviders[0].APIKeyEntries[0].Disabled {
		t.Fatalf("expected disabled state in response, got %#v", gotProviders[0].APIKeyEntries[0])
	}

	gotAuth, ok := reloadedManager.GetByID(authID)
	if !ok || gotAuth == nil {
		t.Fatalf("expected reloaded auth")
	}
	if !gotAuth.Disabled || gotAuth.Status != coreauth.StatusDisabled {
		t.Fatalf("reloaded auth disabled=%v status=%q, want true/%q", gotAuth.Disabled, gotAuth.Status, coreauth.StatusDisabled)
	}
}

func TestPatchOpenAICompatRuntimeStateEnableClearsLastError(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:         "openai-compatibility:groq:enable-clear",
		Provider:   "groq",
		Status:     coreauth.StatusDisabled,
		Disabled:   true,
		Unavailable: true,
		Index:      "auth-enable-clear",
		StatusMessage: "manual_disable",
		NextRetryAfter: time.Now().Add(time.Minute),
		LastError: &coreauth.Error{
			Message:    "unauthorized",
			HTTPStatus: 401,
		},
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "gsk_enable_clear",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), manager)
	got := h.findAuthByIndex("auth-enable-clear")
	if got == nil {
		t.Fatal("expected auth by index")
	}

	got.Disabled = false
	got.Unavailable = false
	got.Status = coreauth.StatusActive
	got.StatusMessage = ""
	got.NextRetryAfter = time.Time{}
	got.LastError = nil

	if _, err := manager.Update(context.Background(), got); err != nil {
		t.Fatalf("update auth: %v", err)
	}
	if err := h.persistOpenAICompatRuntimeState(); err != nil {
		t.Fatalf("persist runtime state: %v", err)
	}

	path := filepath.Join(filepath.Dir(h.openAICompatRuntimeStatePath()), openAICompatRuntimeStateFileName)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("runtime state file should be removed or empty-default after enable, stat err=%v", err)
	}
}
