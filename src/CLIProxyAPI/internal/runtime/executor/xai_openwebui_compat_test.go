package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIClientResponseFormatFallsBackFromProviderID(t *testing.T) {
	got := xaiClientResponseFormat(cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FromString("xai"),
	})
	if got != sdktranslator.FormatOpenAI {
		t.Fatalf("response format = %q, want openai", got)
	}

	got = xaiClientResponseFormat(cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FromString("xai"),
	})
	if got != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("response format = %q, want openai-response", got)
	}

	got = xaiClientResponseFormat(cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatCodex,
	})
	if got != sdktranslator.FormatCodex {
		t.Fatalf("response format = %q, want codex", got)
	}
}

func TestXAIExecutorExecuteChatCompletionsWhenResponseFormatIsProviderID(t *testing.T) {
	// Reproduce OpenWebUI path: /v1/chat/completions with handlers setting ResponseFormat=xai.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !gjson.GetBytes(body, "stream").Bool() {
			t.Fatalf("upstream stream=false, want true; body=%s", string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"sequence_number\":1,\"type\":\"response.completed\",\"response\":{\"id\":\"resp_owui\",\"object\":\"response\",\"created_at\":1785158335,\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"id\":\"msg_1\",\"role\":\"assistant\",\"type\":\"message\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello openwebui\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"stream":false,"model":"grok-4.5","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FromString("xai"),
		Stream:         false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello openwebui" {
		t.Fatalf("content = %q, want hello openwebui; payload=%s", got, string(resp.Payload))
	}
	if typ := gjson.GetBytes(resp.Payload, "type").String(); strings.HasPrefix(typ, "response.") {
		t.Fatalf("payload still responses event type=%q; payload=%s", typ, string(resp.Payload))
	}
	if ct := resp.Headers.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestConvertXAIResponsesPayloadToOpenAIChatFromCompletedEvent(t *testing.T) {
	raw := []byte(`{"sequence_number":278,"type":"response.completed","response":{"id":"fb8","model":"grok-4.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"我是 Grok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	out := convertXAIResponsesPayloadToOpenAIChat(context.Background(), "grok-4.5", nil, nil, raw)
	if got := gjson.GetBytes(out, "object").String(); got != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion; out=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); !strings.Contains(got, "Grok") {
		t.Fatalf("content = %q, want Grok; out=%s", got, string(out))
	}
}
