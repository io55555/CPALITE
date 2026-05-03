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
		Latency:     1500 * time.Millisecond,
		FirstByteLatency: 300 * time.Millisecond,
		ThinkingEffort: "high",
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
	if got := model.Details[0].FirstByteLatencyMs; got != 300 {
		t.Fatalf("first_byte_latency_ms = %d, want 300", got)
	}
	if got := model.Details[0].GenerationMs; got != 1200 {
		t.Fatalf("generation_ms = %d, want 1200", got)
	}
	if got := model.Details[0].ThinkingEffort; got != "high" {
		t.Fatalf("thinking_effort = %q, want high", got)
	}
}
