package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRangeReadsFromSQLiteStore(t *testing.T) {
	t.Parallel()

	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	restore := SetDefaultStoreForTest(store)
	defer restore()

	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	if err := store.Insert(context.Background(), PersistedRecord{
		Timestamp:          now,
		APIKey:             "provider-a",
		Model:              "model-a",
		Source:             "user-a",
		RequestID:          "req-1",
		LatencyMs:          1800,
		FirstByteLatencyMs: 200,
		GenerationMs:       1600,
		ThinkingEffort:     "medium",
		Tokens: TokenStats{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	}); err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	snapshot, err := SnapshotRange(context.Background(), QueryRange{})
	if err != nil {
		t.Fatalf("SnapshotRange() error = %v", err)
	}
	model := snapshot.APIs["provider-a"].Models["model-a"]
	if len(model.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(model.Details))
	}
	if got := model.Details[0].ThinkingEffort; got != "medium" {
		t.Fatalf("thinking_effort = %q, want medium", got)
	}
	if got := snapshot.TotalTokens; got != 30 {
		t.Fatalf("total_tokens = %d, want 30", got)
	}
}
