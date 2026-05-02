package usage

import (
	"context"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordStoresRequestID(t *testing.T) {
	stats := NewRequestStatistics()
	ctx := internallogging.WithRequestID(context.Background(), "req-test-001")

	stats.Record(ctx, coreusage.Record{
		APIKey:      "provider-a",
		Model:       "model-a",
		RequestedAt: time.Unix(1700000000, 0).UTC(),
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 2,
			TotalTokens:  3,
		},
	})

	snapshot := stats.Snapshot()
	model := snapshot.APIs["provider-a"].Models["model-a"]
	if len(model.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(model.Details))
	}
	if got := model.Details[0].RequestID; got != "req-test-001" {
		t.Fatalf("request_id = %q, want req-test-001", got)
	}
}
