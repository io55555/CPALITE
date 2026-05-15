package packetcapture

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type Service struct {
	store *Store
}

type pendingCaptureRecord struct {
	ctx    context.Context
	record coreusage.Record
	done   chan struct{}
	once   sync.Once
}

const pendingCaptureKey = "PACKET_CAPTURE_PENDING_RECORDS"

var (
	defaultMu            sync.RWMutex
	defaultService       *Service
	defaultRulesProvider func(context.Context) ([]Rule, error)
)

func InitDefaultInLogDir(logDir string) error {
	store, err := Open(filepath.Join(logDir, "packet_capture.db"))
	if err != nil {
		return err
	}
	defaultMu.Lock()
	previous := defaultService
	defaultService = &Service{store: store}
	defaultMu.Unlock()
	if previous != nil && previous.store != nil {
		_ = previous.store.Close()
	}
	return nil
}

func Default() *Service {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultService
}

func DefaultStore() *Store {
	if service := Default(); service != nil {
		return service.store
	}
	return nil
}

func SetDefaultRulesProvider(provider func(context.Context) ([]Rule, error)) {
	defaultMu.Lock()
	defaultRulesProvider = provider
	defaultMu.Unlock()
}

func CloseDefault() error {
	defaultMu.Lock()
	previous := defaultService
	defaultService = nil
	defaultRulesProvider = nil
	defaultMu.Unlock()
	if previous == nil || previous.store == nil {
		return nil
	}
	return previous.store.Close()
}

func Enabled(ctx context.Context) bool {
	store := DefaultStore()
	return store != nil && store.Enabled(ctx)
}

func CaptureFromUsageRecord(ctx context.Context, record coreusage.Record) {
	captureFromUsageRecord(ctx, record, true)
}

func CaptureFromGin(c *gin.Context, clientRequest string) {
	store := DefaultStore()
	if c == nil || store == nil || !store.Enabled(c.Request.Context()) {
		return
	}
	clientResponse := ginValueString(c, "USAGE_CLIENT_RESPONSE")
	usageRawRequest := ginValueString(c, "USAGE_RAW_REQUEST")
	usageRawResponse := ginValueString(c, "USAGE_RAW_RESPONSE")
	packets := PacketsFromUsageRaw(usageRawRequest, usageRawResponse)
	if strings.TrimSpace(packets.ClientRequest) == "" {
		packets.ClientRequest = strings.TrimSpace(clientRequest)
	}
	if strings.TrimSpace(packets.UpstreamRequest) == "" {
		packets.UpstreamRequest = packetFromAPIRequest(ginValueString(c, "API_REQUEST"))
	}
	if strings.TrimSpace(packets.UpstreamResponse) == "" {
		packets.UpstreamResponse = packetFromAPIResponse(ginValueString(c, "API_RESPONSE"))
	}
	if strings.TrimSpace(packets.ClientResponse) == "" {
		packets.ClientResponse = strings.TrimSpace(clientResponse)
	}
	if packetBytes(packets) == 0 {
		return
	}
	status := statusFromPacket(packets.UpstreamResponse, 0)
	if status <= 0 {
		status = responseStatusFromClientPacket(packets.ClientResponse)
	}
	rec := Record{
		ID:                 uuid.NewString(),
		Timestamp:          nowUTC(),
		RequestID:          internallogging.GetRequestID(c.Request.Context()),
		Provider:           firstNonEmptyString(strings.TrimSpace(c.GetString("cpa.request_provider")), providerFromAPIRequest(ginValueString(c, "API_REQUEST"))),
		Source:             firstNonEmptyString(strings.TrimSpace(c.GetString("cpa.request_provider")), providerFromAPIRequest(ginValueString(c, "API_REQUEST"))),
		Model:              firstNonEmptyString(strings.TrimSpace(c.GetString("cpa.request_model")), modelFromPacket(packets.ClientRequest), modelFromPacket(packets.UpstreamRequest)),
		Endpoint:           internallogging.GetEndpoint(c.Request.Context()),
		UpstreamStatusCode: status,
		Failed:             status >= 400,
		Packets:            packets,
	}
	if c.Request != nil {
		rec.ClientUA = strings.TrimSpace(c.Request.UserAgent())
		if token := strings.TrimSpace(c.Request.Header.Get("Authorization")); token != "" {
			rec.UserToken = token
		}
	}
	rec.TotalBytes = packetBytes(rec.Packets)
	rec.Summary = buildSummary(rec)
	_ = store.Insert(context.Background(), rec)
}

func captureFromUsageRecord(ctx context.Context, record coreusage.Record, allowQueue bool) {
	store := DefaultStore()
	if store == nil {
		return
	}
	if allowQueue && !store.Enabled(ctx) {
		return
	}
	record.RawResponse = mergeClientResponsePacket(record.RawResponse, contextString(ctx, "USAGE_CLIENT_RESPONSE"))
	if allowQueue && shouldQueueUntilClientResponse(ctx, record) && queuePendingRecord(ctx, record) {
		return
	}
	packets := PacketsFromUsageRaw(record.RawRequest, record.RawResponse)
	if packetBytes(packets) == 0 {
		return
	}
	rec := Record{
		ID:                 uuid.NewString(),
		Timestamp:          record.RequestedAt,
		RequestID:          internallogging.GetRequestID(ctx),
		Provider:           strings.TrimSpace(record.Provider),
		Source:             strings.TrimSpace(record.Source),
		Model:              strings.TrimSpace(record.Model),
		UserToken:          strings.TrimSpace(record.APIKey),
		AuthType:           strings.TrimSpace(record.AuthType),
		AuthIndex:          strings.TrimSpace(record.AuthIndex),
		APIKey:             strings.TrimSpace(record.AuthID),
		Endpoint:           internallogging.GetEndpoint(ctx),
		UpstreamStatusCode: statusFromPacket(packets.UpstreamResponse, record.Fail.StatusCode),
		Failed:             record.Failed,
		Packets:            packets,
	}
	if ginCtx, _ := ctx.Value("gin").(*gin.Context); ginCtx != nil && ginCtx.Request != nil {
		rec.ClientUA = strings.TrimSpace(ginCtx.Request.UserAgent())
		if token := strings.TrimSpace(ginCtx.Request.Header.Get("Authorization")); token != "" {
			rec.UserToken = token
		}
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = nowUTC()
	}
	rec.TotalBytes = packetBytes(rec.Packets)
	rec.Summary = buildSummary(rec)
	_ = store.Insert(context.Background(), rec)
}

func ginValueString(c *gin.Context, key string) string {
	if c == nil || strings.TrimSpace(key) == "" {
		return ""
	}
	value, exists := c.Get(key)
	if !exists || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func packetFromAPIRequest(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "POST ") || strings.HasPrefix(text, "GET ") || strings.HasPrefix(text, "PUT ") || strings.HasPrefix(text, "PATCH ") || strings.HasPrefix(text, "DELETE ") {
		return text
	}
	method := extractLineValue(text, "HTTP Method:")
	if method == "" {
		method = "POST"
	}
	rawURL := extractLineValue(text, "Upstream URL:")
	path := "/"
	if idx := strings.Index(rawURL, "://"); idx >= 0 {
		rest := rawURL[idx+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			path = rest[slash:]
		}
	} else if strings.HasPrefix(rawURL, "/") {
		path = rawURL
	}
	headers := strings.TrimSpace(sectionBetween(text, "Headers:", "Body:"))
	body := strings.TrimSpace(afterMarker(text, "Body:"))
	if body == "<empty>" {
		body = ""
	}
	return strings.TrimSpace(method + " " + path + " HTTP/1.1\n" + headers + "\n\n" + body)
}

func packetFromAPIResponse(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "HTTP/") {
		return text
	}
	status := extractLineValue(text, "Status:")
	if _, err := strconv.Atoi(status); err != nil {
		status = "0"
	}
	headers := strings.TrimSpace(sectionBetween(text, "Headers:", "Body:"))
	body := strings.TrimSpace(afterMarker(text, "Body:"))
	if body == "<empty>" {
		body = ""
	}
	if status == "0" {
		errText := extractLineValue(text, "Error:")
		if errText != "" {
			body = firstNonEmptyString(body, errText)
		}
		return strings.TrimSpace("HTTP/1.1 502 Bad Gateway\n" + headers + "\n\n" + body)
	}
	return strings.TrimSpace("HTTP/1.1 " + status + " " + http.StatusText(atoi(status)) + "\n" + headers + "\n\n" + body)
}

func extractLineValue(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func sectionBetween(text, start, end string) string {
	from := strings.Index(text, start)
	if from < 0 {
		return ""
	}
	from += len(start)
	to := strings.Index(text[from:], end)
	if to < 0 {
		return text[from:]
	}
	return text[from : from+to]
}

func afterMarker(text, marker string) string {
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	return text[idx+len(marker):]
}

func providerFromAPIRequest(text string) string {
	auth := extractLineValue(text, "Auth:")
	for _, part := range strings.Fields(auth) {
		if strings.HasPrefix(part, "provider=") {
			return strings.TrimSpace(strings.TrimPrefix(part, "provider="))
		}
	}
	return ""
}

func modelFromPacket(packet string) string {
	body := packetBody(packet)
	if strings.TrimSpace(body) == "" {
		return ""
	}
	if idx := strings.Index(body, `"model"`); idx >= 0 {
		part := body[idx+len(`"model"`):]
		if colon := strings.Index(part, ":"); colon >= 0 {
			part = strings.TrimSpace(part[colon+1:])
			if strings.HasPrefix(part, `"`) {
				part = strings.TrimPrefix(part, `"`)
				if end := strings.Index(part, `"`); end >= 0 {
					return part[:end]
				}
			}
		}
	}
	return ""
}

func responseStatusFromClientPacket(packet string) int {
	return statusFromPacket(packet, 0)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func atoi(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func FlushPendingRecords(c *gin.Context) {
	if c == nil {
		return
	}
	pending := pendingRecordsFromGin(c)
	if len(pending) == 0 {
		return
	}
	c.Set(pendingCaptureKey, []*pendingCaptureRecord(nil))
	for _, item := range pending {
		item.capture()
	}
}

func (p *pendingCaptureRecord) capture() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		close(p.done)
		ctx := p.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		captureFromUsageRecord(ctx, p.record, false)
	})
}

func shouldQueueUntilClientResponse(ctx context.Context, record coreusage.Record) bool {
	if strings.TrimSpace(contextString(ctx, "USAGE_CLIENT_RESPONSE")) != "" {
		return false
	}
	packets := PacketsFromUsageRaw(record.RawRequest, record.RawResponse)
	if packetBytes(packets) == 0 || strings.TrimSpace(packets.ClientResponse) != "" {
		return false
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return ginCtx != nil
}

func queuePendingRecord(ctx context.Context, record coreusage.Record) bool {
	if ctx == nil {
		return false
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil {
		return false
	}
	item := &pendingCaptureRecord{ctx: ctx, record: record, done: make(chan struct{})}
	pending := pendingRecordsFromGin(ginCtx)
	pending = append(pending, item)
	ginCtx.Set(pendingCaptureKey, pending)
	go item.captureAfterClientResponseOrTimeout()
	return true
}

func (p *pendingCaptureRecord) captureAfterClientResponseOrTimeout() {
	if p == nil {
		return
	}
	timer := time.NewTimer(2 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			if strings.TrimSpace(contextString(p.ctx, "USAGE_CLIENT_RESPONSE")) != "" {
				p.capture()
				return
			}
		case <-timer.C:
			p.capture()
			return
		}
	}
}

func pendingRecordsFromGin(c *gin.Context) []*pendingCaptureRecord {
	if c == nil {
		return nil
	}
	value, exists := c.Get(pendingCaptureKey)
	if !exists || value == nil {
		return nil
	}
	pending, _ := value.([]*pendingCaptureRecord)
	return pending
}

func PacketsFromUsageRaw(rawRequest, rawResponse string) PacketSet {
	packets := PacketSet{
		ClientRequest:    extractNamedSection(rawRequest, "客户端发给CPA的完整数据包"),
		UpstreamRequest:  extractNamedSection(rawRequest, "CPA发给供应商的完整数据包"),
		UpstreamResponse: extractNamedSection(rawResponse, "供应商返回CPA的完整数据包"),
		ClientResponse:   extractNamedSection(rawResponse, "CPA发送给客户端的完整数据包"),
	}
	if packetBytes(packets) > 0 {
		return packets
	}
	rawRequest = strings.TrimSpace(rawRequest)
	rawResponse = strings.TrimSpace(rawResponse)
	if rawRequest != "" {
		packets.ClientRequest = rawRequest
	}
	if rawResponse != "" {
		packets.ClientResponse = rawResponse
		packets.UpstreamResponse = rawResponse
	}
	return packets
}

func ApplyRules(ctx context.Context, meta Record, packetName string, packet string) (string, error, []TriggerRecord) {
	store := DefaultStore()
	if store == nil {
		return packet, nil, nil
	}
	rules, err := enabledRules(context.Background(), store)
	if err != nil || len(rules) == 0 {
		return packet, nil, nil
	}
	current := packet
	var triggers []TriggerRecord
	for _, rule := range rules {
		if !ruleMatchesMeta(rule, meta) || normalizePacketName(rule.Packet) != normalizePacketName(packetName) {
			continue
		}
		next, matched, detail := applyRuleToPacket(rule, current)
		if !matched {
			continue
		}
		for _, action := range ruleActions(rule) {
			if action.Packet != "" && normalizePacketName(action.Packet) != normalizePacketName(rule.Packet) {
				continue
			}
			actionRule := ruleForAction(rule, action)
			trigger := TriggerRecord{
				ID:              uuid.NewString(),
				RuleID:          rule.ID,
				RuleName:        rule.Name,
				RecordID:        meta.ID,
				Timestamp:       nowUTC(),
				Action:          actionRule.Action,
				Target:          actionRule.Target,
				Detail:          detail,
				CooldownSeconds: actionRule.CooldownSeconds,
			}
			triggers = append(triggers, trigger)
			if rule.RecordHistory {
				_ = store.InsertTrigger(context.Background(), trigger)
			}
			switch strings.TrimSpace(actionRule.Action) {
			case "block":
				return current, &httpError{status: http.StatusForbidden, message: "请求被抓包/过滤规则拦截: " + rule.Name}, triggers
			case "return_clean_400", "return_clean_401", "return_clean_404", "return_clean_404_model_not_support", "return_clean_429", "return_clean_500":
				status := CleanReturnStatus(actionRule.Action)
				return current, &httpError{status: status, message: CleanReturnBodyForAction(actionRule.Action, status)}, triggers
			}
		}
		if next != current {
			current = next
		}
	}
	return current, nil, triggers
}

func enabledRules(ctx context.Context, store *Store) ([]Rule, error) {
	defaultMu.RLock()
	provider := defaultRulesProvider
	defaultMu.RUnlock()
	if provider != nil {
		rules, err := provider(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]Rule, 0, len(rules))
		for _, rule := range rules {
			if rule.Enabled {
				out = append(out, rule)
			}
		}
		sortRules(out)
		return out, nil
	}
	return store.EnabledRules(ctx)
}

func sortRules(rules []Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].UpdatedAt.After(rules[j].UpdatedAt)
	})
}

func contextString(ctx context.Context, key string) string {
	if ctx == nil || strings.TrimSpace(key) == "" {
		return ""
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil {
		return ""
	}
	value, exists := ginCtx.Get(key)
	if !exists || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}

func mergeClientResponsePacket(rawResponse, clientResponse string) string {
	clientResponse = strings.TrimSpace(clientResponse)
	if clientResponse == "" {
		return rawResponse
	}
	const marker = "=== CPA发送给客户端的完整数据包 ==="
	if strings.Contains(rawResponse, marker) {
		head, _, _ := strings.Cut(rawResponse, marker)
		return strings.TrimRight(head, "\r\n ") + "\n\n" + marker + "\n" + clientResponse
	}
	if strings.TrimSpace(rawResponse) == "" {
		return marker + "\n" + clientResponse
	}
	return strings.TrimRight(rawResponse, "\r\n ") + "\n\n" + marker + "\n" + clientResponse
}

type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string     { return e.message }
func (e *httpError) StatusCode() int   { return e.status }
func (e *httpError) Unwrap() error     { return nil }
func (e *httpError) HTTPStatus() int   { return e.status }
func (e *httpError) ResponseText() any { return e.message }
