package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestFileTokenStoreListIgnoresLogsAndNonAuthJSON(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	logsDir := filepath.Join(baseDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("create logs dir: %v", err)
	}
	files := map[string]string{
		filepath.Join(baseDir, "codex.json"):                `{"type":"codex","email":"u@example.com"}`,
		filepath.Join(baseDir, "settings.json"):             `{"prices":{"gpt":1}}`,
		filepath.Join(logsDir, "usage_model_prices.json"):   `{"gpt-5":{"input":1}}`,
		filepath.Join(logsDir, "accidental-auth-like.json"): `{"type":"codex","email":"log@example.com"}`,
	}
	for path, raw := range files {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)

	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("len(auths)=%d, want 1: %#v", len(auths), auths)
	}
	if auths[0].ID != "codex.json" || auths[0].Provider != "codex" {
		t.Fatalf("auth=%#v, want codex.json/codex", auths[0])
	}
}

func TestExtractAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
		expected string
	}{
		{
			"antigravity top-level access_token",
			map[string]any{"access_token": "tok-abc"},
			"tok-abc",
		},
		{
			"gemini nested token.access_token",
			map[string]any{
				"token": map[string]any{"access_token": "tok-nested"},
			},
			"tok-nested",
		},
		{
			"top-level takes precedence over nested",
			map[string]any{
				"access_token": "tok-top",
				"token":        map[string]any{"access_token": "tok-nested"},
			},
			"tok-top",
		},
		{
			"empty metadata",
			map[string]any{},
			"",
		},
		{
			"whitespace-only access_token",
			map[string]any{"access_token": "   "},
			"",
		},
		{
			"wrong type access_token",
			map[string]any{"access_token": 12345},
			"",
		},
		{
			"token is not a map",
			map[string]any{"token": "not-a-map"},
			"",
		},
		{
			"nested whitespace-only",
			map[string]any{
				"token": map[string]any{"access_token": "  "},
			},
			"",
		},
		{
			"fallback to nested when top-level empty",
			map[string]any{
				"access_token": "",
				"token":        map[string]any{"access_token": "tok-fallback"},
			},
			"tok-fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAccessToken(tt.metadata)
			if got != tt.expected {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFileTokenStoreListExpandsPluginMultiAuths(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "geminicli.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"gemini-cli","headers":{"X-Test":"value"}}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(ctx context.Context, req pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		if req.Provider != "gemini-cli" || req.Path != path || req.FileName != "geminicli.json" {
			t.Fatalf("ParseAuths request = %#v, want file context", req)
		}
		return []*cliproxyauth.Auth{
			{
				ID:       "geminicli.json",
				Provider: "gemini-cli",
				Metadata: map[string]any{
					"type": "gemini-cli",
					"headers": map[string]any{
						"X-Test": "value",
					},
				},
			},
			nil,
			{
				ID:       "geminicli-project-a.json",
				Provider: "gemini-cli",
				Metadata: map[string]any{
					"type":       "gemini-cli",
					"project_id": "project-a",
					"headers": map[string]any{
						"X-Test": "value",
					},
				},
			},
		}, true, nil
	}))
	t.Cleanup(func() {
		RegisterPluginAuthParser(nil)
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(auths) != 2 {
		t.Fatalf("List() len = %d, want two plugin auths", len(auths))
	}
	if firstIndex, secondIndex := auths[0].EnsureIndex(), auths[1].EnsureIndex(); firstIndex == "" || firstIndex == secondIndex {
		t.Fatalf("auth indexes = %q/%q, want distinct non-empty indexes", firstIndex, secondIndex)
	}
	for _, auth := range auths {
		if !cliproxyauth.IsPluginVirtualAuth(auth) {
			t.Fatalf("auth attributes = %#v, want plugin virtual marker", auth.Attributes)
		}
		if auth.Attributes[cliproxyauth.AttributeVirtualSource] != path {
			t.Fatalf("virtual_source = %q, want %q", auth.Attributes[cliproxyauth.AttributeVirtualSource], path)
		}
		if auth.Attributes["path"] != path || auth.Attributes["source"] != path {
			t.Fatalf("auth attributes = %#v, want source path", auth.Attributes)
		}
		if gotHeader := auth.Attributes["header:X-Test"]; gotHeader != "value" {
			t.Fatalf("header:X-Test = %q, want value", gotHeader)
		}
	}
	if gotProject := auths[1].Metadata["project_id"]; gotProject != "project-a" {
		t.Fatalf("project_id = %#v, want project-a", gotProject)
	}
}

func TestFileTokenStoreListKeepsGeminiOAuthAuth(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "user@example.com-project-a.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"gemini","email":"user@example.com","project_id":"project-a"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	RegisterPluginAuthParser(nil)
	t.Cleanup(func() {
		RegisterPluginAuthParser(nil)
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(auths) != 1 {
		t.Fatalf("List() len = %d, want one Gemini OAuth auth", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "gemini" {
		t.Fatalf("Provider = %q, want gemini", auth.Provider)
	}
	if gotProject := auth.Metadata["project_id"]; gotProject != "project-a" {
		t.Fatalf("project_id = %#v, want project-a", gotProject)
	}
	if gotEmail := auth.Attributes["email"]; gotEmail != "user@example.com" {
		t.Fatalf("email attribute = %q, want user@example.com", gotEmail)
	}
	if gotKind, gotAccount := auth.AccountInfo(); gotKind != "oauth" || gotAccount != "user@example.com" {
		t.Fatalf("AccountInfo() = %q/%q, want oauth/user@example.com", gotKind, gotAccount)
	}
}

func TestFileTokenStoreListPluginHandledEmptySuppressesBuiltin(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","access_token":"token"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		return nil, true, nil
	}))
	t.Cleanup(func() {
		RegisterPluginAuthParser(nil)
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(auths) != 0 {
		t.Fatalf("List() len = %d, want plugin-handled empty result", len(auths))
	}
}

type fileStoreMultiAuthParserFunc func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error)

func (f fileStoreMultiAuthParserFunc) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*cliproxyauth.Auth, bool, error) {
	return nil, false, nil
}

func (f fileStoreMultiAuthParserFunc) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
	return f(ctx, req)
}
