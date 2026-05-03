package management

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestParseUsageQueryRange(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "/v0/management/usage?start=2026-05-01T00:00:00.123Z&end=2026-05-02T03:04:05Z", nil)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req

	got, err := parseUsageQueryRange(ctx)
	if err != nil {
		t.Fatalf("parseUsageQueryRange() error = %v", err)
	}
	if got.Start == nil || got.End == nil {
		t.Fatalf("expected both start and end to be set, got %#v", got)
	}
	if got.Start.Format(time.RFC3339Nano) != "2026-05-01T00:00:00.123Z" {
		t.Fatalf("start = %s", got.Start.Format(time.RFC3339Nano))
	}
	if got.End.Format(time.RFC3339Nano) != "2026-05-02T03:04:05Z" {
		t.Fatalf("end = %s", got.End.Format(time.RFC3339Nano))
	}
}

func TestParseUsageQueryRangeRejectsInvalidTime(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "/v0/management/usage?start=not-a-time", nil)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req

	if _, err := parseUsageQueryRange(ctx); err == nil {
		t.Fatal("expected error for invalid start time")
	}
}
