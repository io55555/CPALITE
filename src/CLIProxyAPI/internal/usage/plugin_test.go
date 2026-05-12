package usage

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestLoggerPluginPersistsRecord(t *testing.T) {
	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	ctx := internallogging.WithEndpoint(context.Background(), "POST /v1/messages")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 200)

	plugin.HandleUsage(ctx, coreusage.Record{
		APIKey:      " api-key ",
		Provider:    " claude ",
		Model:       " claude-sonnet-4-6 ",
		Source:      " user@example.com ",
		AuthIndex:   " 0 ",
		AuthType:    " oauth ",
		RequestedAt: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		Latency:     1800 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     300,
			OutputTokens:    500,
			ReasoningTokens: 60,
			CachedTokens:    100,
		},
	})

	usage, err := store.Query(context.Background(), QueryRange{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	details := usage["api-key"]["claude-sonnet-4-6"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	got := details[0]
	if got.ID == "" {
		t.Fatalf("ID is empty")
	}
	if got.GenerationMs != 1800 {
		t.Fatalf("GenerationMs = %d, want 1800", got.GenerationMs)
	}
}

func TestLoggerPluginDropsRawPacketsForSuccessfulRecords(t *testing.T) {
	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	ctx := internallogging.WithEndpoint(context.Background(), "POST /v1/messages")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 200)

	plugin.HandleUsage(ctx, coreusage.Record{
		Model:       "claude-sonnet-4-6",
		RequestedAt: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		RawRequest:  "POST /v1/messages HTTP/2\n\n{}",
		RawResponse: "HTTP/2 200\n\n{}",
		Detail:      coreusage.Detail{TotalTokens: 1},
	})

	usage, err := store.Query(context.Background(), QueryRange{IncludeRaw: true})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	details := usage["POST /v1/messages"]["claude-sonnet-4-6"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].RawRequest != "" || details[0].RawResponse != "" {
		t.Fatalf("raw fields = (%q, %q), want empty for success", details[0].RawRequest, details[0].RawResponse)
	}
}

func TestLoggerPluginPersistsRecordWhenLegacyStatisticsDisabled(t *testing.T) {
	previous := StatisticsEnabled()
	SetStatisticsEnabled(false)
	defer SetStatisticsEnabled(previous)

	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	ctx := internallogging.WithEndpoint(context.Background(), "POST /v1/messages")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 200)

	plugin.HandleUsage(ctx, coreusage.Record{
		APIKey:      "api-key",
		Model:       "claude-sonnet-4-6",
		RequestedAt: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		Latency:     time.Second,
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 1,
		},
	})

	usage, err := store.Query(context.Background(), QueryRange{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(usage) == 0 {
		t.Fatalf("usage len = 0, want record persisted")
	}
}

func TestLoggerPluginBackfillsFailurePacketsFromGinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	ginCtx := &gin.Context{}
	ginCtx.Set("USAGE_RAW_REQUEST", "POST /v1/chat/completions HTTP/2\nHost: ip.99.tf\n\n{\"model\":\"llama\"}")
	ginCtx.Set("API_RESPONSE", []byte("HTTP/1.1 503 Service Unavailable\nContent-Type: application/json\n\n{\"error\":{\"message\":\"no auth available\"}}"))

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 503)

	plugin.HandleUsage(ctx, coreusage.Record{
		Provider:    "groq",
		Model:       "llama-3.1-8b-instant",
		RequestedAt: time.Date(2026, 5, 10, 9, 42, 16, 0, time.UTC),
		Failed:      true,
		Fail: coreusage.Failure{
			StatusCode: 503,
		},
	})

	usage, err := store.Query(context.Background(), QueryRange{IncludeRaw: true})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	details := usage["POST /v1/chat/completions"]["llama-3.1-8b-instant"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	got := details[0]
	if !strings.Contains(got.RawRequest, "POST /v1/chat/completions HTTP/2") {
		t.Fatalf("raw request = %q", got.RawRequest)
	}
	if !strings.Contains(got.RawResponse, "no auth available") {
		t.Fatalf("raw response = %q", got.RawResponse)
	}
	if got.FailureMessage != "no auth available" {
		t.Fatalf("failure message = %q, want no auth available", got.FailureMessage)
	}
}

func TestLoggerPluginBuildsFallbackRawRequestFromGinRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	req, err := http.NewRequest(http.MethodPost, "http://123.ccc/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.ProtoMajor = 2
	req.ProtoMinor = 0
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer 123")
	ginCtx := &gin.Context{Request: req}

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = internallogging.WithEndpoint(ctx, "POST /v1/chat/completions")
	ctx = internallogging.WithResponseStatusHolder(ctx)
	internallogging.SetResponseStatus(ctx, 504)

	plugin.HandleUsage(ctx, coreusage.Record{
		Provider:    "groq",
		Model:       "llama-3.1-8b-instant",
		RequestedAt: time.Date(2026, 5, 10, 9, 42, 16, 0, time.UTC),
		Failed:      true,
		Fail: coreusage.Failure{
			StatusCode: 504,
			Body:       "context deadline exceeded",
		},
	})

	usage, err := store.Query(context.Background(), QueryRange{IncludeRaw: true})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	details := usage["POST /v1/chat/completions"]["llama-3.1-8b-instant"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	raw := details[0].RawRequest
	if !strings.Contains(raw, "POST /v1/chat/completions HTTP/2") {
		t.Fatalf("raw request missing request line: %q", raw)
	}
	if !strings.Contains(raw, "Host: 123.ccc") {
		t.Fatalf("raw request missing host: %q", raw)
	}
	if !strings.Contains(raw, "Authorization: Bearer 123") {
		t.Fatalf("raw request missing authorization: %q", raw)
	}
}

func TestLoggerPluginTruncatesRawPacketsBeforeQueueing(t *testing.T) {
	store := newPluginTestSQLiteStore(t)
	recorder := NewRecorder(store)
	plugin := &LoggerPlugin{recorder: recorder}

	oversized := strings.Repeat("x", usageRawPacketMaxBytes+1024)
	ctx := internallogging.WithEndpoint(context.Background(), "POST /v1/chat/completions")
	plugin.HandleUsage(ctx, coreusage.Record{
		Model:       "llama",
		RequestedAt: time.Date(2026, 5, 10, 9, 42, 16, 0, time.UTC),
		Failed:      true,
		RawRequest:  oversized,
		RawResponse: oversized,
		Fail: coreusage.Failure{
			StatusCode: 502,
			Body:       oversized,
		},
	})

	usage, err := store.Query(context.Background(), QueryRange{IncludeRaw: true})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	details := usage["POST /v1/chat/completions"]["llama"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if len(details[0].RawRequest) != usageRawPacketMaxBytes {
		t.Fatalf("raw request len = %d, want %d", len(details[0].RawRequest), usageRawPacketMaxBytes)
	}
	if len(details[0].RawResponse) != usageRawPacketMaxBytes {
		t.Fatalf("raw response len = %d, want %d", len(details[0].RawResponse), usageRawPacketMaxBytes)
	}
	if len(details[0].FailureMessage) != usageRawPacketMaxBytes {
		t.Fatalf("failure message len = %d, want %d", len(details[0].FailureMessage), usageRawPacketMaxBytes)
	}
}

func TestReplaceDefaultStoreClosesPreviousStore(t *testing.T) {
	isolateDefaultRecorder(t)

	oldStore := &fakeStore{}
	newStore := &fakeStore{}

	if previous := defaultRecorder.SetStore(oldStore); previous != nil {
		t.Fatalf("SetStore() previous = %T, want nil", previous)
	}
	replaceDefaultStore(newStore)

	if oldStore.closeCalls != 1 {
		t.Fatalf("oldStore closeCalls = %d, want 1", oldStore.closeCalls)
	}
	if newStore.closeCalls != 0 {
		t.Fatalf("newStore closeCalls = %d, want 0", newStore.closeCalls)
	}
	if got := DefaultStore(); got != newStore {
		t.Fatalf("DefaultStore() = %T, want newStore", got)
	}
}

func TestCloseDefaultStoreClosesAndClearsActiveStore(t *testing.T) {
	isolateDefaultRecorder(t)

	store := &fakeStore{}
	defaultRecorder.SetStore(store)

	if err := CloseDefaultStore(); err != nil {
		t.Fatalf("CloseDefaultStore() error = %v", err)
	}
	if store.closeCalls != 1 {
		t.Fatalf("closeCalls = %d, want 1", store.closeCalls)
	}
	if got := DefaultStore(); got != nil {
		t.Fatalf("DefaultStore() = %T, want nil", got)
	}
}

func TestSetDefaultStoreForTestRestoresPreviousStoreWithoutClosing(t *testing.T) {
	isolateDefaultRecorder(t)

	previousStore := &fakeStore{}
	testStore := &fakeStore{}
	defaultRecorder.SetStore(previousStore)

	restore := SetDefaultStoreForTest(testStore)
	if got := DefaultStore(); got != testStore {
		t.Fatalf("DefaultStore() = %T, want testStore", got)
	}

	restore()
	if got := DefaultStore(); got != previousStore {
		t.Fatalf("DefaultStore() = %T, want previousStore", got)
	}
	if previousStore.closeCalls != 0 {
		t.Fatalf("previousStore closeCalls = %d, want 0", previousStore.closeCalls)
	}
	if testStore.closeCalls != 0 {
		t.Fatalf("testStore closeCalls = %d, want 0", testStore.closeCalls)
	}
}

type fakeStore struct {
	closeCalls int
}

func (s *fakeStore) Insert(ctx context.Context, record Record) error { return nil }

func (s *fakeStore) Query(ctx context.Context, rng QueryRange) (APIUsage, error) { return nil, nil }

func (s *fakeStore) Delete(ctx context.Context, ids []string) (DeleteResult, error) {
	return DeleteResult{}, nil
}

func (s *fakeStore) Close() error {
	s.closeCalls++
	return nil
}

func isolateDefaultRecorder(t *testing.T) {
	t.Helper()
	original := defaultRecorder
	defaultRecorder = NewRecorder(nil)
	t.Cleanup(func() {
		if defaultRecorder != nil {
			_ = CloseDefaultStore()
		}
		defaultRecorder = original
	})
}

func newPluginTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
