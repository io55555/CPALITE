package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
