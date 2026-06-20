package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

func TestGetUsageQueuePopsRequestedRecords(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload []json.RawMessage
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if len(payload) != 2 {
			t.Fatalf("response records = %d, want 2", len(payload))
		}
		requireRecordID(t, payload[0], 1)
		requireRecordID(t, payload[1], 2)

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":3}` {
			t.Fatalf("remaining queue = %q, want third item only", remaining)
		}
	})
}

func TestGetUsageQueueInvalidCountDoesNotPop(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=0", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":1}` {
			t.Fatalf("remaining queue = %q, want original item", remaining)
		}
	})
}

func TestBuildUsagePayloadWrapsDetailsForSsfunFrontend(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	payload := buildUsagePayload(usage.APIUsage{
		"POST /v1/chat/completions": {
			"gpt-test": {
				{
					ID:        "older",
					Timestamp: now,
					Tokens:    usage.TokenStats{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				},
				{
					ID:        "newer",
					Timestamp: now.Add(time.Second),
					Tokens:    usage.TokenStats{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
					Failed:    true,
				},
			},
		},
	}, 0, 50)

	if payload["latest_id"] != 2 {
		t.Fatalf("latest_id = %v, want 2", payload["latest_id"])
	}
	if payload["total_requests"] != 2 || payload["success_count"] != 1 || payload["failure_count"] != 1 {
		t.Fatalf("unexpected summary: %#v", payload)
	}
	apis, ok := payload["apis"].(gin.H)
	if !ok || len(apis) != 1 {
		t.Fatalf("apis = %#v, want one wrapped api entry", payload["apis"])
	}
}

func TestBuildUsagePayloadSupportsAfterIDIncrementalSlice(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	payload := buildUsagePayload(usage.APIUsage{
		"POST /v1/messages": {
			"claude-test": {
				{ID: "1", Timestamp: now, Tokens: usage.TokenStats{TotalTokens: 1}},
				{ID: "2", Timestamp: now.Add(time.Second), Tokens: usage.TokenStats{TotalTokens: 2}},
			},
		},
	}, 1, 10)

	if payload["latest_id"] != 2 {
		t.Fatalf("latest_id = %v, want 2", payload["latest_id"])
	}
	if payload["total_requests"] != 1 || payload["total_tokens"] != int64(2) {
		t.Fatalf("incremental summary = %#v, want only the second record", payload)
	}
}

func withManagementUsageQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	defer func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	}()

	fn()
}

func requireRecordID(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()

	var payload struct {
		ID int `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal record: %v", errUnmarshal)
	}
	if payload.ID != want {
		t.Fatalf("record id = %d, want %d", payload.ID, want)
	}
}
