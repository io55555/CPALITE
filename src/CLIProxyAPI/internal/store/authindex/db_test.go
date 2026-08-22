package authindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestStoreUpsertQueryAndDelete(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "codex-user.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","email":"user@example.com","priority":7}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := openTestStore(t, authDir)
	defer func() { _ = store.Close() }()

	if err := store.UpsertFile(context.Background(), path); err != nil {
		t.Fatalf("upsert file: %v", err)
	}
	result, err := store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 10, Provider: "codex"})
	if err != nil {
		t.Fatalf("query index: %v", err)
	}
	if result.Total != 1 || len(result.Entries) != 1 {
		t.Fatalf("expected one indexed entry, total=%d len=%d", result.Total, len(result.Entries))
	}
	got := result.Entries[0]
	if got.ID != "codex-user.json" || got.Email != "user@example.com" || got.Priority != 7 {
		t.Fatalf("unexpected indexed entry: %#v", got)
	}

	if err = store.Delete(context.Background(), "codex-user.json"); err != nil {
		t.Fatalf("delete index: %v", err)
	}
	result, err = store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query after delete: %v", err)
	}
	if result.Total != 0 || len(result.Entries) != 0 {
		t.Fatalf("expected empty index after delete, total=%d len=%d", result.Total, len(result.Entries))
	}
}

func TestStoreSyncDirRemovesStaleRows(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "xai-user.json")
	if err := os.WriteFile(path, []byte(`{"type":"xai","email":"xai@example.com"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := openTestStore(t, authDir)
	defer func() { _ = store.Close() }()

	if err := store.SyncDir(context.Background()); err != nil {
		t.Fatalf("sync dir: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove auth file: %v", err)
	}
	if err := store.SyncDir(context.Background()); err != nil {
		t.Fatalf("sync dir after remove: %v", err)
	}
	result, err := store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query after stale removal: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected stale row removed, total=%d", result.Total)
	}
}

func TestStoreListLightweightReturnsSQLiteStubsWithoutPayload(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "codex-user.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","email":"user@example.com","access_token":"secret","priority":3}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := openTestStore(t, authDir)
	defer func() { _ = store.Close() }()

	if err := store.SyncDir(context.Background()); err != nil {
		t.Fatalf("sync dir: %v", err)
	}
	auths, err := store.ListLightweight(context.Background())
	if err != nil {
		t.Fatalf("list lightweight: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("lightweight len = %d, want 1", len(auths))
	}
	got := auths[0]
	if !coreauth.IsSQLiteAuthStub(got) {
		t.Fatalf("auth is not sqlite stub: %#v", got.Attributes)
	}
	if got.Metadata != nil {
		t.Fatalf("lightweight metadata = %#v, want nil", got.Metadata)
	}
	if got.Provider != "codex" || got.FileName != "codex-user.json" || got.Label != "user@example.com" {
		t.Fatalf("unexpected lightweight auth: %#v", got)
	}
}

func TestStoreHydrateAuthRestoresFullPayload(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "xai-user.json")
	if err := os.WriteFile(path, []byte(`{"type":"xai","email":"xai@example.com","sub":"user-1","access_token":"secret-token","refresh_token":"refresh-token","token_endpoint":"https://auth.x.ai/token","base_url":"https://api.x.ai/v1","using_api":true,"proxy_url":"http://127.0.0.1:8080","headers":{"X-Test":"ok"},"attributes":{"priority":"9"}}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := openTestStore(t, authDir)
	defer func() { _ = store.Close() }()

	if err := store.SyncDir(context.Background()); err != nil {
		t.Fatalf("sync dir: %v", err)
	}
	auth, err := store.HydrateAuth(context.Background(), "xai-user.json")
	if err != nil {
		t.Fatalf("hydrate auth: %v", err)
	}
	if coreauth.IsSQLiteAuthStub(auth) {
		t.Fatalf("hydrated auth still marked as sqlite stub")
	}
	if got, _ := auth.Metadata["access_token"].(string); got != "secret-token" {
		t.Fatalf("hydrated access_token = %q, want secret-token", got)
	}
	if auth.Provider != "xai" || auth.FileName != "xai-user.json" {
		t.Fatalf("unexpected hydrated auth: %#v", auth)
	}
	if got := auth.ProxyURL; got != "http://127.0.0.1:8080" {
		t.Fatalf("proxy url = %q", got)
	}
	if got := auth.Attributes["base_url"]; got != "https://api.x.ai/v1" {
		t.Fatalf("base_url attr = %q", got)
	}
	if got := auth.Attributes["using_api"]; got != "true" {
		t.Fatalf("using_api attr = %q", got)
	}
	if got := auth.Attributes["sub"]; got != "user-1" {
		t.Fatalf("sub attr = %q", got)
	}
	if got := auth.Attributes["header:X-Test"]; got != "ok" {
		t.Fatalf("custom header attr = %q", got)
	}
	if got := auth.Attributes["priority"]; got != "9" {
		t.Fatalf("priority attr = %q", got)
	}
}

func TestStoreIndexesSynthesizedProvidersForCodexAndXAI(t *testing.T) {
	authDir := t.TempDir()
	files := map[string]string{
		"codex-user.json": `{"type":"codex","email":"codex@example.com","access_token":"codex-token"}`,
		"xai-user.json":   `{"type":"xai","email":"xai@example.com","access_token":"xai-token"}`,
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(authDir, name), []byte(raw), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	store := openTestStore(t, authDir)
	defer func() { _ = store.Close() }()

	if err := store.SyncDir(context.Background()); err != nil {
		t.Fatalf("sync dir: %v", err)
	}
	for provider := range map[string]struct{}{"codex": {}, "xai": {}} {
		result, err := store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 10, Provider: provider})
		if err != nil {
			t.Fatalf("query %s: %v", provider, err)
		}
		if result.Total != 1 || len(result.Entries) != 1 {
			t.Fatalf("%s total=%d len=%d, want one", provider, result.Total, len(result.Entries))
		}
		if got := result.Entries[0].Provider; got != provider {
			t.Fatalf("%s provider = %q", provider, got)
		}
	}
}

func TestStoreUpsertFileReplacesAllRowsForPluginMultiAuthFile(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "plugin.json")
	if err := os.WriteFile(path, []byte(`{"type":"plugin","email":"source@example.com"}`), 0o600); err != nil {
		t.Fatalf("write plugin file: %v", err)
	}

	store := openTestStore(t, authDir)
	store.SetSynthesisContext(&config.Config{}, multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		return []*coreauth.Auth{
			{ID: "plugin-a", Provider: "codex", Metadata: map[string]any{"email": "a@example.com"}},
			{ID: "plugin-b", Provider: "xai", Metadata: map[string]any{"email": "b@example.com"}},
		}, true, nil
	}))
	defer func() { _ = store.Close() }()

	if err := store.UpsertFile(context.Background(), path); err != nil {
		t.Fatalf("upsert multi auth file: %v", err)
	}
	result, err := store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query multi auth index: %v", err)
	}
	if result.Total != 2 || len(result.Entries) != 2 {
		t.Fatalf("multi auth total=%d len=%d, want two", result.Total, len(result.Entries))
	}

	store.SetSynthesisContext(&config.Config{}, multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		return []*coreauth.Auth{{ID: "plugin-a", Provider: "codex", Metadata: map[string]any{"email": "a@example.com"}}}, true, nil
	}))
	if err := store.UpsertFile(context.Background(), path); err != nil {
		t.Fatalf("upsert reduced auth file: %v", err)
	}
	result, err = store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query reduced auth index: %v", err)
	}
	if result.Total != 1 || result.Entries[0].ID != "plugin-a" {
		t.Fatalf("reduced rows = %#v, want only plugin-a", result.Entries)
	}
}

func TestStoreQuerySummaryIgnoresPagination(t *testing.T) {
	authDir := t.TempDir()
	files := map[string]string{
		"codex-active.json":   `{"type":"codex","email":"active@example.com"}`,
		"codex-disabled.json": `{"type":"codex","email":"disabled@example.com","disabled":true}`,
		"xai-active.json":     `{"type":"xai","email":"xai@example.com"}`,
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(authDir, name), []byte(raw), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	store := openTestStore(t, authDir)
	defer func() { _ = store.Close() }()

	if err := store.SyncDir(context.Background()); err != nil {
		t.Fatalf("sync dir: %v", err)
	}
	result, err := store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("query index: %v", err)
	}
	if len(result.Entries) != 1 || result.Total != 3 {
		t.Fatalf("page len=%d total=%d, want page one of three", len(result.Entries), result.Total)
	}
	if result.Summary.Total != 3 || result.Summary.Active != 2 || result.Summary.Disabled != 1 || result.Summary.Unavailable != 0 {
		t.Fatalf("unexpected summary: %#v", result.Summary)
	}
	if result.Summary.ByProvider["codex"] != 2 || result.Summary.ByProvider["xai"] != 1 {
		t.Fatalf("unexpected provider summary: %#v", result.Summary.ByProvider)
	}
}

func TestStoreDeleteFileIndexOnlyRemovesPluginMultiAuthRows(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "plugin.json")
	if err := os.WriteFile(path, []byte(`{"type":"plugin","email":"source@example.com"}`), 0o600); err != nil {
		t.Fatalf("write plugin file: %v", err)
	}

	store := openTestStore(t, authDir)
	store.SetSynthesisContext(&config.Config{}, multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		return []*coreauth.Auth{
			{ID: "plugin-a", Provider: "codex", Metadata: map[string]any{"email": "a@example.com"}},
			{ID: "plugin-b", Provider: "xai", Metadata: map[string]any{"email": "b@example.com"}},
		}, true, nil
	}))
	defer func() { _ = store.Close() }()

	if err := store.UpsertFile(context.Background(), path); err != nil {
		t.Fatalf("upsert multi auth file: %v", err)
	}
	if err := store.DeleteFileIndexOnly(context.Background(), path); err != nil {
		t.Fatalf("delete file index: %v", err)
	}
	result, err := store.Query(context.Background(), QueryOptions{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("query after delete file index: %v", err)
	}
	if result.Total != 0 || len(result.Entries) != 0 {
		t.Fatalf("rows after delete = %#v total=%d, want empty", result.Entries, result.Total)
	}
}

type multiAuthParserFunc func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error)

func (f multiAuthParserFunc) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return nil, false, nil
}

func (f multiAuthParserFunc) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	return f(ctx, req)
}

func openTestStore(t *testing.T, authDir string) *Store {
	t.Helper()
	cfg := config.DefaultAuthIndexCacheConfig()
	cfg.Enabled = true
	cfg.DBPath = filepath.Join(t.TempDir(), "index.db")
	store, err := Open(context.Background(), authDir, cfg)
	if err != nil {
		t.Fatalf("open auth index: %v", err)
	}
	return store
}
