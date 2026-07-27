package openai

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteOpenAIChatSSEDataStripsExistingPrefix(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOpenAIChatSSEData(rec, []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`))
	body := rec.Body.String()
	if strings.Contains(body, "data: data:") {
		t.Fatalf("double SSE prefix: %q", body)
	}
	if !strings.HasPrefix(body, "data: {") {
		t.Fatalf("unexpected body: %q", body)
	}
	if !strings.Contains(body, `"content":"hi"`) {
		t.Fatalf("missing payload: %q", body)
	}
}

func TestWriteOpenAIChatSSEDataRawJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOpenAIChatSSEData(rec, []byte(`{"choices":[{"delta":{"content":"hi"}}]}`))
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), "data: {") {
		t.Fatalf("unexpected body: %q", body)
	}
	if strings.Count(body, "data:") != 1 {
		t.Fatalf("expected single data prefix, got %q", body)
	}
}
