package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFiles_IncludesProjectIDFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "antigravity-user@example.com-project-a.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"antigravity","email":"user@example.com","project_id":"project-a"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type":       "antigravity",
			"email":      "user@example.com",
			"project_id": "project-a",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	if got := entry["project_id"]; got != "project-a" {
		t.Fatalf("expected project_id %q, got %#v", "project-a", got)
	}
}

func TestListAuthFilesFromDisk_IncludesProjectID(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "antigravity-user@example.com-project-a.json")
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"antigravity","email":"user@example.com","project_id":"project-a"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := firstAuthFileEntry(t, h)
	if got := entry["project_id"]; got != "project-a" {
		t.Fatalf("expected project_id %q, got %#v", "project-a", got)
	}
}

func TestListAuthFilesFromDisk_ClassifiesGeminiCLIAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "user@example.com-project-a.json")
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"gemini","email":"user@example.com","project_id":"project-a"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := firstAuthFileEntry(t, h)
	if got := entry["type"]; got != "gemini" {
		t.Fatalf("expected type gemini, got %#v", got)
	}
	if got := entry["project_id"]; got != "project-a" {
		t.Fatalf("expected project_id %q, got %#v", "project-a", got)
	}
	if got := entry["account_type"]; got != "oauth" {
		t.Fatalf("expected account_type oauth, got %#v", got)
	}
	if got := entry["account"]; got != "user@example.com" {
		t.Fatalf("expected account email, got %#v", got)
	}
}

func TestListAuthFiles_IncludesWebsocketsFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-user@example.com-pro.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","email":"user@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":       filePath,
			"websockets": "true",
		},
		Metadata: map[string]any{
			"type": "codex",
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	if got := entry["websockets"]; got != true {
		t.Fatalf("expected websockets true, got %#v", got)
	}
}

func TestListAuthFiles_IncludesModelCooldownFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-user@example.com-pro.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","email":"user@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	cooldownUntil := time.Now().Add(3 * time.Hour).UTC().Round(time.Second)
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Status:   coreauth.StatusError,
		Attributes: map[string]string{
			"path": filePath,
		},
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-5-codex": {
				Status:         coreauth.StatusError,
				StatusMessage:  "packet filter matched: 429 cooldown",
				Unavailable:    true,
				NextRetryAfter: cooldownUntil,
				Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: cooldownUntil},
				UpdatedAt:      time.Now().UTC(),
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	if got := entry["cooldown_model"]; got != "gpt-5-codex" {
		t.Fatalf("expected cooldown_model gpt-5-codex, got %#v", got)
	}
	if _, ok := entry["cooldown_until"].(string); !ok {
		t.Fatalf("expected cooldown_until string, got %#v", entry["cooldown_until"])
	}
	if _, ok := entry["next_retry_after"].(string); !ok {
		t.Fatalf("expected aggregated next_retry_after string, got %#v", entry["next_retry_after"])
	}
	modelStates, ok := entry["model_states"].(map[string]any)
	if !ok {
		t.Fatalf("expected model_states object, got %#v", entry["model_states"])
	}
	if _, ok := modelStates["gpt-5-codex"].(map[string]any); !ok {
		t.Fatalf("expected gpt-5-codex model state, got %#v", modelStates)
	}
}

func TestListAuthFiles_IncludesXAICooldownFromManager(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "xai-user.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"xai","auth_kind":"oauth","access_token":"token","refresh_token":"refresh"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	cooldownUntil := time.Now().Add(24 * time.Hour).UTC().Round(time.Second)
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "xai",
		Status:   coreauth.StatusError,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"path":      filePath,
		},
		Metadata: map[string]any{
			"type":          "xai",
			"access_token":  "token",
			"refresh_token": "refresh",
		},
		ModelStates: map[string]*coreauth.ModelState{
			"grok-4": {
				Status:         coreauth.StatusError,
				StatusMessage:  "packet filter matched: 429 cooldown",
				Unavailable:    true,
				NextRetryAfter: cooldownUntil,
				Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: cooldownUntil},
				UpdatedAt:      time.Now().UTC(),
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	entry := firstAuthFileEntry(t, h)
	if got := entry["type"]; got != "xai" {
		t.Fatalf("expected type xai, got %#v", got)
	}
	if got := entry["provider"]; got != "xai" {
		t.Fatalf("expected provider xai, got %#v", got)
	}
	if got := entry["account_type"]; got != "oauth" {
		t.Fatalf("expected account_type oauth, got %#v", got)
	}
	if got := entry["cooldown_model"]; got != "grok-4" {
		t.Fatalf("expected cooldown_model grok-4, got %#v", got)
	}
	if _, ok := entry["cooldown_until"].(string); !ok {
		t.Fatalf("expected cooldown_until string, got %#v", entry["cooldown_until"])
	}
}

func TestListAuthFilesFromDisk_IncludesWebsockets(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "codex-user@example.com-pro.json")
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex","email":"user@example.com","websockets":false}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := firstAuthFileEntry(t, h)
	if got := entry["websockets"]; got != false {
		t.Fatalf("expected websockets false, got %#v", got)
	}
}

func firstAuthFileEntry(t *testing.T, h *Handler) map[string]any {
	t.Helper()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode list payload: %v", errUnmarshal)
	}
	filesRaw, ok := payload["files"].([]any)
	if !ok {
		t.Fatalf("expected files array, payload: %#v", payload)
	}
	if len(filesRaw) != 1 {
		t.Fatalf("expected 1 auth entry, got %d", len(filesRaw))
	}
	fileEntry, ok := filesRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected file entry object, got %#v", filesRaw[0])
	}
	return fileEntry
}
