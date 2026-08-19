package authindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	if err := os.WriteFile(path, []byte(`{"type":"xai","email":"xai@example.com","access_token":"secret-token"}`), 0o600); err != nil {
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
