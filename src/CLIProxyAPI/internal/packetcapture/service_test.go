package packetcapture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestCaptureFromUsageRecordWaitsForClientResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := InitDefaultInLogDir(t.TempDir()); err != nil {
		t.Fatalf("InitDefaultInLogDir: %v", err)
	}
	defer CloseDefault()
	if err := DefaultStore().SetEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := context.WithValue(logging.WithRequestID(context.Background(), "req-1"), "gin", c)

	CaptureFromUsageRecord(ctx, coreusage.Record{
		Provider:    "groq",
		Model:       "llama-3.1-8b-instant",
		Source:      "groq",
		RequestedAt: time.Now(),
		RawRequest:  "=== 客户端发给CPA的完整数据包 ===\nPOST /v1/chat/completions HTTP/1.1\n\n{}\n\n=== CPA发给供应商的完整数据包 ===\nPOST /openai/v1/chat/completions HTTP/1.1\n\n{}",
		RawResponse: "=== 供应商返回CPA的完整数据包 ===\nHTTP/2.0 200 OK\n\n{}",
	})

	if got, err := DefaultStore().Query(context.Background(), QueryOptions{}); err != nil || len(got) != 0 {
		t.Fatalf("records before client response = %d, err=%v", len(got), err)
	}

	c.Set("USAGE_CLIENT_RESPONSE", "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"ok\":true}")
	FlushPendingRecords(c)

	got, err := DefaultStore().Query(context.Background(), QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records len = %d, want 1", len(got))
	}
	detail, ok, err := DefaultStore().Get(context.Background(), got[0].ID)
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if detail.Packets.ClientResponse == "" {
		t.Fatalf("client response packet missing: %+v", detail.Packets)
	}
}

func TestCaptureFromUsageRecordStoresUnmarkedRawPackets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := InitDefaultInLogDir(t.TempDir()); err != nil {
		t.Fatalf("InitDefaultInLogDir: %v", err)
	}
	defer CloseDefault()
	if err := DefaultStore().SetEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	CaptureFromUsageRecord(context.Background(), coreusage.Record{
		Provider:    "compat",
		Model:       "model-a",
		Source:      "compat",
		RequestedAt: time.Now(),
		RawRequest:  "POST /v1/chat/completions HTTP/1.1\n\n{}",
		RawResponse: "HTTP/1.1 200 OK\n\n{\"ok\":true}",
	})

	got, err := DefaultStore().Query(context.Background(), QueryOptions{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("records len = %d, want 1", len(got))
	}
	detail, ok, err := DefaultStore().Get(context.Background(), got[0].ID)
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if detail.Packets.ClientRequest == "" || detail.Packets.UpstreamResponse == "" {
		t.Fatalf("unmarked packets missing: %+v", detail.Packets)
	}
}

func TestRulesWildcardRandomAndOriginalReplacement(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	_, err = store.UpsertRule(context.Background(), Rule{
		Name:        "ua",
		Enabled:     true,
		Packet:      "upstream_request",
		Part:        "header",
		Header:      "User-Agent",
		Operator:    "wildcard",
		Value:       "cli-*",
		Action:      "replace",
		Replacement: "{{random_curl_ua}}",
	})
	if err != nil {
		t.Fatalf("UpsertRule ua: %v", err)
	}
	packet := "POST / HTTP/1.1\nUser-Agent: cli-proxy-openai-compat\n\n{}"
	filtered, errBlock, _ := ApplyRules(context.Background(), Record{Provider: "groq"}, "upstream_request", packet)
	if errBlock != nil {
		t.Fatalf("ApplyRules block: %v", errBlock)
	}
	if got := headerValue(filtered, "User-Agent"); got == "" || got == "cli-proxy-openai-compat" {
		t.Fatalf("User-Agent not replaced: %q", got)
	}

	_, err = store.UpsertRule(context.Background(), Rule{
		Name:        "append",
		Enabled:     true,
		Priority:    1,
		Packet:      "upstream_request",
		Part:        "body_json",
		JSONPath:    "messages.0.content",
		Operator:    "contains",
		Value:       "hello",
		Action:      "replace",
		Replacement: "{original} world",
	})
	if err != nil {
		t.Fatalf("UpsertRule append: %v", err)
	}
	filtered, errBlock, _ = ApplyRules(context.Background(), Record{Provider: "groq"}, "upstream_request", "POST / HTTP/1.1\n\n{\"messages\":[{\"content\":\"hello\"}]}")
	if errBlock != nil {
		t.Fatalf("ApplyRules append block: %v", errBlock)
	}
	if body := PacketBody(filtered); body != "{\"messages\":[{\"content\":\"hello world\"}]}" {
		t.Fatalf("body = %s", body)
	}
}

func TestWildcardMatchIsCaseInsensitiveAndAnchored(t *testing.T) {
	if !wildcardMatch("HTTP/2 400\n\nORGANIZATION_RESTRICTED", "http/* 400*organization_restricted*") {
		t.Fatal("expected wildcard match")
	}
	if wildcardMatch("prefix HTTP/2 400 organization_restricted", "HTTP/* 400*organization_restricted*") {
		t.Fatal("expected anchored wildcard mismatch")
	}
}

func TestApplyRulesMatchesHTTPStatusWithoutScanningWholePacket(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	_, err = store.UpsertRule(context.Background(), Rule{
		Name:          "status 429",
		Enabled:       true,
		RecordHistory: true,
		Packet:        "upstream_response",
		Part:          "status",
		Operator:      "num_eq",
		Value:         "body should not matter",
		// 数值运算符用 ValueNumber 匹配响应码。
		ValueNumber:     429,
		Action:          "cooldown",
		Target:          "api_key",
		CooldownSeconds: 300,
	})
	if err != nil {
		t.Fatalf("UpsertRule: %v", err)
	}

	packet := "HTTP/2 429 Too Many Requests\nContent-Type: application/json\n\n{\"ok\":false}"
	_, blockErr, triggers := ApplyRules(context.Background(), Record{Provider: "groq"}, "upstream_response", packet)
	if blockErr != nil {
		t.Fatalf("ApplyRules block: %v", blockErr)
	}
	if len(triggers) != 1 {
		t.Fatalf("triggers len = %d, want 1", len(triggers))
	}
	if !strings.Contains(triggers[0].Detail, "part=status") {
		t.Fatalf("trigger detail = %q, want status part", triggers[0].Detail)
	}
}

func TestApplyRulesEqualsStatusUsesValueNumber(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	previousProvider := defaultRulesProvider
	SetDefaultRulesProvider(func(context.Context) ([]Rule, error) {
		return []Rule{{
			Name:     "legacy equals status",
			Enabled:  true,
			Packet:   "upstream_response",
			Part:     "status",
			Operator: "equals",
			Value:    "unauthorized",
			// 兼容旧表单写法：状态码字段应优先按 value_number 精确匹配。
			ValueNumber: 403,
			Action:      "disable",
			Target:      "api_key",
		}}, nil
	})
	defer SetDefaultRulesProvider(previousProvider)

	packet := "HTTP/1.1 403 Forbidden\nContent-Type: application/json\n\n{}"
	_, blockErr, triggers := ApplyRules(context.Background(), Record{Provider: "gemini"}, "upstream_response", packet)
	if blockErr != nil {
		t.Fatalf("ApplyRules block: %v", blockErr)
	}
	if len(triggers) != 1 || triggers[0].Action != "disable" || triggers[0].Target != "api_key" {
		t.Fatalf("triggers = %+v, want disable api_key", triggers)
	}
}

func TestTriggerRecordsIncludeAccountAndLookupByRequestID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.InsertTrigger(ctx, TriggerRecord{
		RuleID:   "rule-1",
		RuleName: "disable key",
		RecordID: "req-1",
		Action:   "disable",
		Target:   "api_key",
		Account:  "AIza...EHkY",
	}); err != nil {
		t.Fatalf("InsertTrigger: %v", err)
	}
	triggers, err := store.ListTriggers(ctx, 10)
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(triggers) != 1 || triggers[0].Account != "AIza...EHkY" {
		t.Fatalf("triggers = %+v, want account", triggers)
	}

	if err := store.Insert(ctx, Record{RequestID: "req-1", Provider: "gemini", Packets: PacketSet{UpstreamResponse: "HTTP/1.1 403 Forbidden\n\n{}"}}); err != nil {
		t.Fatalf("Insert record: %v", err)
	}
	record, ok, err := store.GetByRequestID(ctx, "req-1")
	if err != nil {
		t.Fatalf("GetByRequestID: %v", err)
	}
	if !ok || record.Provider != "gemini" {
		t.Fatalf("record = %+v ok=%v, want gemini record", record, ok)
	}
}

func TestApplyRulesMatchesStatusAndBodyConditions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	rule := Rule{
		Name:          "status and body",
		Enabled:       true,
		RecordHistory: true,
		Packet:        "upstream_response",
		MatchLogic:    "all",
		Conditions: []Condition{
			{Packet: "upstream_response", Part: "status", Operator: "num_eq", ValueNumber: 401},
			{Packet: "upstream_response", Part: "body", Operator: "contains", Value: "invalid_api_key"},
		},
		Actions: []Action{
			{Type: "disable", Packet: "upstream_response", Target: "api_key"},
		},
	}
	previousProvider := defaultRulesProvider
	SetDefaultRulesProvider(func(context.Context) ([]Rule, error) { return []Rule{rule}, nil })
	defer SetDefaultRulesProvider(previousProvider)

	missPacket := "HTTP/2 401 Unauthorized\nContent-Type: application/json\n\n{\"error\":{\"code\":\"other\"}}"
	_, blockErr, triggers := ApplyRules(context.Background(), Record{Provider: "groq"}, "upstream_response", missPacket)
	if blockErr != nil || len(triggers) != 0 {
		t.Fatalf("unexpected match for body miss: block=%v triggers=%d", blockErr, len(triggers))
	}

	hitPacket := "HTTP/2 401 Unauthorized\nContent-Type: application/json\n\n{\"error\":{\"code\":\"invalid_api_key\"}}"
	_, blockErr, triggers = ApplyRules(context.Background(), Record{Provider: "groq"}, "upstream_response", hitPacket)
	if blockErr != nil {
		t.Fatalf("ApplyRules block: %v", blockErr)
	}
	if len(triggers) != 1 || triggers[0].Action != "disable" || triggers[0].Target != "api_key" {
		t.Fatalf("triggers = %+v, want disable api_key", triggers)
	}
}

func TestApplyRulesReturnClean500(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	_, err = store.UpsertRule(context.Background(), Rule{
		Name:     "clean 500",
		Enabled:  true,
		Packet:   "client_response",
		Part:     "body_json",
		JSONPath: "error.message",
		Operator: "contains",
		Value:    "api.groq.com/openai",
		Action:   "return_clean_500",
		Target:   "response",
	})
	if err != nil {
		t.Fatalf("UpsertRule: %v", err)
	}
	packet := "HTTP/1.1 500 Internal Server Error\nContent-Type: application/json\n\n" +
		`{"error":{"message":"Post \"https://api.groq.com/openai/v1/chat/completions\": EOF","type":"server_error","code":"internal_server_error"}}`
	_, blockErr, _ := ApplyRules(context.Background(), Record{Provider: "groq"}, "client_response", packet)
	if blockErr == nil {
		t.Fatal("expected clean return error")
	}
	status, ok := blockErr.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != 500 {
		t.Fatalf("status = %v, want 500", blockErr)
	}
	if strings.Contains(blockErr.Error(), "api.groq.com") || !strings.Contains(blockErr.Error(), "Internal Server Error") {
		t.Fatalf("unexpected body: %s", blockErr.Error())
	}
}

func TestApplyRulesModifyStatus(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	_, err = store.UpsertRule(context.Background(), Rule{
		Name:        "403 to 404",
		Enabled:     true,
		Packet:      "client_response",
		Part:        "status",
		Operator:    "num_eq",
		ValueNumber: 403,
		Action:      "modify_status",
		Replacement: "404",
	})
	if err != nil {
		t.Fatalf("UpsertRule: %v", err)
	}

	filtered, blockErr, _ := ApplyRules(context.Background(), Record{Provider: "ollama_cloud"}, "client_response", "HTTP/1.1 403 Forbidden\nContent-Type: application/json\n\n{}")
	if blockErr != nil {
		t.Fatalf("ApplyRules block: %v", blockErr)
	}
	if !strings.HasPrefix(filtered, "HTTP/1.1 404 Not Found") {
		t.Fatalf("filtered = %q, want 404 status line", filtered)
	}
}

func TestApplyRulesReturnClean404ModelNotSupport(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "packet_capture.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	defer func() {
		defaultMu.Lock()
		defaultService = previous
		defaultMu.Unlock()
	}()

	rule := Rule{
		Name:       "ollama subscription",
		Enabled:    true,
		Provider:   "ollama_cloud",
		Packet:     "client_response",
		MatchLogic: "all",
		Conditions: []Condition{
			{Packet: "client_response", Part: "status", Operator: "num_eq", ValueNumber: 403},
			{Packet: "client_response", Part: "body_json", JSONPath: "error", Operator: "contains", Value: "model requires a subscription"},
		},
		Actions: []Action{
			{Type: "return_clean_404_model_not_support", Packet: "client_response", Target: "response"},
		},
	}
	previousProvider := defaultRulesProvider
	SetDefaultRulesProvider(func(context.Context) ([]Rule, error) { return []Rule{rule}, nil })
	defer SetDefaultRulesProvider(previousProvider)

	packet := "HTTP/1.1 403 Forbidden\nContent-Type: application/json\n\n" + `{"error":"this model requires a subscription"}`
	_, blockErr, _ := ApplyRules(context.Background(), Record{Provider: "ollama_cloud"}, "client_response", packet)
	if blockErr == nil {
		t.Fatal("expected clean return error")
	}
	status, ok := blockErr.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %v, want 404", blockErr)
	}
	if blockErr.Error() != `{"error":"model not support"}` {
		t.Fatalf("body = %s", blockErr.Error())
	}
}
