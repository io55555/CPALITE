package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const insertTimeout = 5 * time.Second
const usageRawPacketMaxBytes = 256 * 1024

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

type Recorder struct {
	mu    sync.RWMutex
	store Store
}

func NewRecorder(store Store) *Recorder {
	return &Recorder{store: store}
}

func (r *Recorder) SetStore(store Store) Store {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.store
	r.store = store
	return previous
}

func (r *Recorder) Store() Store {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store
}

var defaultRecorder = NewRecorder(nil)

type LoggerPlugin struct {
	recorder *Recorder
}

func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{recorder: defaultRecorder} }

type pendingRecord struct {
	ctx    context.Context
	record coreusage.Record
}

func InitDefaultStore(path string) error {
	store, err := NewSQLiteStore(path)
	if err != nil {
		return err
	}
	replaceDefaultStore(store)
	return nil
}

func InitDefaultStoreInLogDir(logDir string) error {
	return InitDefaultStore(filepath.Join(logDir, "usage.db"))
}

func DefaultStore() Store { return defaultRecorder.Store() }

func SetDefaultStoreForTest(store Store) func() {
	previous := defaultRecorder.SetStore(store)
	return func() {
		defaultRecorder.SetStore(previous)
	}
}

func CloseDefaultStore() error {
	previous := defaultRecorder.SetStore(nil)
	if previous == nil {
		return nil
	}
	return previous.Close()
}

func replaceDefaultStore(store Store) {
	previous := defaultRecorder.SetStore(store)
	if previous == nil {
		return
	}
	if err := previous.Close(); err != nil {
		log.Warnf("usage: close previous store failed: %v", err)
	}
}

func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

func StatisticsEnabled() bool { return statisticsEnabled.Load() }

func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.recorder == nil {
		return
	}
	normalized := normalizeRecord(ctx, record)

	store := p.recorder.Store()
	if store == nil {
		return
	}
	insertCtx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()
	if err := store.Insert(insertCtx, normalized); err != nil {
		log.Warnf("usage: insert failed: %v", err)
	}
}

func QueuePendingRecord(ctx context.Context, record coreusage.Record) bool {
	if ctx == nil {
		return false
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil {
		return false
	}
	pending := pendingRecordsFromGin(ginCtx)
	pending = append(pending, pendingRecord{ctx: ctx, record: record})
	ginCtx.Set("USAGE_PENDING_RECORDS", pending)
	return true
}

func FlushPendingRecords(c *gin.Context) {
	if c == nil {
		return
	}
	pending := pendingRecordsFromGin(c)
	if len(pending) == 0 {
		return
	}
	c.Set("USAGE_PENDING_RECORDS", []pendingRecord(nil))
	for _, item := range pending {
		if item.ctx == nil {
			item.ctx = context.Background()
		}
		coreusage.PublishRecord(item.ctx, item.record)
	}
}

func pendingRecordsFromGin(c *gin.Context) []pendingRecord {
	if c == nil {
		return nil
	}
	value, exists := c.Get("USAGE_PENDING_RECORDS")
	if !exists || value == nil {
		return nil
	}
	pending, _ := value.([]pendingRecord)
	return pending
}

func normalizeRecord(ctx context.Context, record coreusage.Record) Record {
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	timestamp = timestamp.UTC()

	latencyMs := durationMs(record.Latency)
	detail := record.Detail
	rawRequest := firstNonEmpty(record.RawRequest, contextString(ctx, "USAGE_RAW_REQUEST"), contextString(ctx, "API_REQUEST"))
	rawResponse := firstNonEmpty(record.RawResponse, contextString(ctx, "USAGE_RAW_RESPONSE"), contextString(ctx, "API_RESPONSE"))
	rawResponse = mergeClientResponsePacket(rawResponse, contextString(ctx, "USAGE_CLIENT_RESPONSE"))
	if strings.TrimSpace(rawRequest) == "" {
		rawRequest = buildFallbackRawRequest(ctx, record)
	}
	if strings.TrimSpace(rawResponse) == "" && strings.TrimSpace(record.Fail.Body) != "" {
		if record.Fail.StatusCode > 0 {
			rawResponse = fmt.Sprintf("HTTP/1.1 %d %s\n\n%s", record.Fail.StatusCode, http.StatusText(record.Fail.StatusCode), strings.TrimSpace(record.Fail.Body))
		} else {
			rawResponse = strings.TrimSpace(record.Fail.Body)
		}
	}
	failureMessage := firstNonEmpty(record.Fail.Body, failureMessageFromRawResponse(rawResponse), httpStatusFailureMessage(record.Fail.StatusCode))
	rawRequest = truncateUsageRawPacket(rawRequest)
	rawResponse = truncateUsageRawPacket(rawResponse)
	failureMessage = truncateUsageRawPacket(failureMessage)

	return Record{
		ID:                 uuid.NewString(),
		Timestamp:          timestamp,
		APIKey:             strings.TrimSpace(record.APIKey),
		Provider:           strings.TrimSpace(record.Provider),
		Model:              normalizeModel(record.Model),
		Source:             strings.TrimSpace(record.Source),
		AuthIndex:          strings.TrimSpace(record.AuthIndex),
		AuthType:           strings.TrimSpace(record.AuthType),
		RawRequest:         rawRequest,
		RawResponse:        rawResponse,
		FailureStatusCode:  record.Fail.StatusCode,
		FailureMessage:     failureMessage,
		Endpoint:           internallogging.GetEndpoint(ctx),
		RequestID:          internallogging.GetRequestID(ctx),
		LatencyMs:          latencyMs,
		FirstByteLatencyMs: 0,
		GenerationMs:       latencyMs,
		Tokens: TokenStats{
			InputTokens:     detail.InputTokens,
			OutputTokens:    detail.OutputTokens,
			ReasoningTokens: detail.ReasoningTokens,
			CachedTokens:    detail.CachedTokens,
			TotalTokens:     normalizeCoreTotal(detail),
		},
		Failed: resolveFailed(ctx, record),
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

func buildFallbackRawRequest(ctx context.Context, record coreusage.Record) string {
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil || ginCtx.Request == nil {
		return ""
	}
	req := ginCtx.Request
	path := "/"
	if req.URL != nil && req.URL.RequestURI() != "" {
		path = req.URL.RequestURI()
	}
	protoMajor, protoMinor := req.ProtoMajor, req.ProtoMinor
	if protoMajor == 0 {
		protoMajor = 1
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s %s %s\n", req.Method, path, formatHTTPProto(protoMajor, protoMinor))
	body := strings.TrimSpace(record.RawRequest)
	writeDownstreamHeaders(&builder, req, len(body))
	builder.WriteByte('\n')
	if body != "" {
		builder.WriteString(body)
	}
	return builder.String()
}

func writeDownstreamHeaders(builder *strings.Builder, req *http.Request, bodyLen int) {
	if builder == nil || req == nil {
		return
	}
	if req.Host != "" {
		fmt.Fprintf(builder, "Host: %s\n", req.Host)
	}
	hasContentLength := false
	for key := range req.Header {
		if strings.EqualFold(key, "Content-Length") {
			hasContentLength = true
			break
		}
	}
	_ = req.Header.Write(builder)
	if !hasContentLength {
		contentLength := req.ContentLength
		if contentLength <= 0 && bodyLen > 0 {
			contentLength = int64(bodyLen)
		}
		if contentLength > 0 {
			fmt.Fprintf(builder, "Content-Length: %d\n", contentLength)
		}
	}
}

func formatHTTPProto(major, minor int) string {
	if major <= 0 {
		return "HTTP/1.1"
	}
	if major == 2 && minor == 0 {
		return "HTTP/2"
	}
	return fmt.Sprintf("HTTP/%d.%d", major, minor)
}

func failureMessageFromRawResponse(rawResponse string) string {
	body := strings.TrimSpace(rawResponse)
	if body == "" {
		return ""
	}
	if idx := strings.Index(body, "\n\n"); idx >= 0 {
		body = strings.TrimSpace(body[idx+2:])
	}
	if idx := strings.Index(body, "\r\n\r\n"); idx >= 0 {
		body = strings.TrimSpace(body[idx+4:])
	}
	if body == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err == nil {
		if msg := stringFromNestedError(parsed); msg != "" {
			return msg
		}
	}
	for _, prefix := range []string{"Failure Message:", "Error:"} {
		if idx := strings.Index(body, prefix); idx >= 0 {
			return strings.TrimSpace(body[idx+len(prefix):])
		}
	}
	return body
}

func stringFromNestedError(parsed map[string]any) string {
	if parsed == nil {
		return ""
	}
	if errObj, ok := parsed["error"].(map[string]any); ok {
		for _, key := range []string{"message", "code", "type"} {
			if value, ok := errObj[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	for _, key := range []string{"message", "error_message"} {
		if value, ok := parsed[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func httpStatusFailureMessage(status int) string {
	if status <= 0 {
		return ""
	}
	text := http.StatusText(status)
	if text == "" {
		return fmt.Sprintf("HTTP status %d", status)
	}
	return text
}

func truncateUsageRawPacket(value string) string {
	if len(value) <= usageRawPacketMaxBytes {
		return value
	}
	return value[:usageRawPacketMaxBytes]
}

func resolveFailed(ctx context.Context, record coreusage.Record) bool {
	if record.Failed {
		return true
	}
	return internallogging.GetResponseStatus(ctx) >= 400
}

func durationMs(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return duration.Milliseconds()
}

func normalizeCoreTotal(detail coreusage.Detail) int64 {
	if detail.TotalTokens != 0 {
		return detail.TotalTokens
	}
	total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	if total != 0 {
		return total
	}
	return detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens + detail.CachedTokens
}
