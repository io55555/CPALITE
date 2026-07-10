// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the request logging middleware that captures comprehensive
// request and response data when enabled through configuration.
package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const maxErrorOnlyCapturedRequestBodyBytes int64 = 1 << 20 // 1 MiB

// RequestLoggingMiddleware creates a Gin middleware that logs HTTP requests and responses.
// It captures detailed information about the request and response, including headers and body,
// and uses the provided RequestLogger to record this data. When full request logging is disabled,
// body capture is limited to small known-size payloads to avoid large per-request memory spikes.
func RequestLoggingMiddleware(logger logging.RequestLogger, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if logger == nil {
			c.Next()
			return
		}

		if shouldSkipMethodForRequestLogging(c.Request) {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if !shouldLogRequest(path) {
			c.Next()
			return
		}

		loggerEnabled := logger.IsEnabled()

		// Capture request information
		requestInfo, err := captureRequestInfo(c, shouldCaptureRequestBody(loggerEnabled, c.Request))
		if err != nil {
			// Log error but continue processing
			// In a real implementation, you might want to use a proper logger here
			c.Next()
			return
		}
		if rawRequest := buildCapturedRawRequest(c.Request, requestInfo); strings.TrimSpace(rawRequest) != "" {
			c.Set("USAGE_RAW_REQUEST", rawRequest)
		}

		// Create response writer wrapper
		wrapper := NewResponseWriterWrapper(c.Writer, logger, requestInfo)
		if !loggerEnabled {
			wrapper.logOnErrorOnly = true
		}
		c.Writer = wrapper
		attachRequestLogSources(c, logger, loggerEnabled)

		// Process the request
		c.Next()

		if packet, recorded := setUsageClientResponseFromWrapper(c, wrapper); recorded {
			logCPASentClientResponseFromMiddleware(c, packet, cfg)
		}
		internalusage.FlushPendingRecords(c)

		// Finalize logging after request processing
		if err = wrapper.Finalize(c); err != nil {
			// Log error but don't interrupt the response
			// In a real implementation, you might want to use a proper logger here
		}
	}
}

type fileBodySourceFactory interface {
	NewFileBodySource(prefix string) (*logging.FileBodySource, error)
}

func attachRequestLogSources(c *gin.Context, logger logging.RequestLogger, loggerEnabled bool) {
	if c == nil || !loggerEnabled {
		return
	}
	factory, ok := logger.(fileBodySourceFactory)
	if !ok || factory == nil {
		return
	}
	if source, errSource := factory.NewFileBodySource("api-request"); errSource == nil {
		c.Set(logging.APIRequestSourceContextKey, source)
	}
	if source, errSource := factory.NewFileBodySource("api-response"); errSource == nil {
		c.Set(logging.APIResponseSourceContextKey, source)
	}
	if !isResponsesWebsocketUpgrade(c.Request) {
		return
	}
	if source, errSource := factory.NewFileBodySource("websocket-timeline"); errSource == nil {
		c.Set(logging.WebsocketTimelineSourceContextKey, source)
	}
	if source, errSource := factory.NewFileBodySource("api-websocket-timeline"); errSource == nil {
		c.Set(logging.APIWebsocketTimelineSourceContextKey, source)
	}
}

func setUsageClientResponseFromWrapper(c *gin.Context, wrapper *ResponseWriterWrapper) (string, bool) {
	if c == nil || wrapper == nil {
		return "", false
	}
	if existing, exists := c.Get("USAGE_CLIENT_RESPONSE"); exists {
		if text, ok := existing.(string); ok && strings.TrimSpace(text) != "" {
			return text, false
		}
	}
	status := wrapper.statusCode
	if status == 0 {
		status = c.Writer.Status()
	}
	if status <= 0 {
		status = http.StatusOK
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", status, http.StatusText(status))
	for key, values := range wrapper.cloneHeaders() {
		for _, value := range values {
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(value)
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	if wrapper.body != nil && wrapper.body.Len() > 0 {
		b.Write(wrapper.body.Bytes())
	}
	packet := b.String()
	c.Set("USAGE_CLIENT_RESPONSE", packet)
	return packet, true
}

func logCPASentClientResponseFromMiddleware(c *gin.Context, packet string, cfg *config.Config) {
	if c == nil || strings.TrimSpace(packet) == "" || (cfg != nil && !cfg.PacketCapture.CLIDetailedLogEnabled()) {
		return
	}
	provider := strings.TrimSpace(c.GetString("cpa.request_provider"))
	model := strings.TrimSpace(c.GetString("cpa.request_model"))
	if provider == "" && model == "" {
		return
	}
	if provider == "" {
		provider = "-"
	}
	if model == "" {
		model = "-"
	}
	account := "-"
	if c.Request != nil {
		account = strings.TrimSpace(c.Request.Header.Get("Authorization"))
		if account == "" {
			account = "-"
		}
	}
	entry := log.NewEntry(log.StandardLogger())
	if c.Request != nil {
		if requestID := logging.GetRequestID(c.Request.Context()); requestID != "" {
			entry = log.WithField("request_id", requestID)
		} else if requestID := logging.GetGinRequestID(c); requestID != "" {
			entry = log.WithField("request_id", requestID)
		}
	}
	entry.Infof(
		"\n================ CPA发送客户端响应 ================\n"+
			"[6/6][%s][%s] CPA发送给客户端\n"+
			"请求模型: %s\n"+
			"---------------- CPA发送给客户端 ----------------\n"+
			"%s\n"+
			"==================================================",
		provider, account, model, packet,
	)
}

func buildCapturedRawRequest(req *http.Request, info *RequestInfo) string {
	if req == nil || info == nil {
		return ""
	}
	path := info.URL
	if strings.TrimSpace(path) == "" && req.URL != nil {
		path = req.URL.RequestURI()
	}
	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	protoMajor, protoMinor := req.ProtoMajor, req.ProtoMinor
	if protoMajor == 0 {
		protoMajor = 1
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s %s %s\n", info.Method, path, formatHTTPProto(protoMajor, protoMinor))
	if req.Host != "" {
		builder.WriteString("Host: ")
		builder.WriteString(req.Host)
		builder.WriteByte('\n')
	}
	for key, values := range info.Headers {
		if strings.EqualFold(key, "Host") {
			continue
		}
		for _, value := range values {
			builder.WriteString(key)
			builder.WriteString(": ")
			builder.WriteString(value)
			builder.WriteByte('\n')
		}
	}
	if req.ContentLength >= 0 {
		hasContentLength := false
		for key := range info.Headers {
			if strings.EqualFold(key, "Content-Length") {
				hasContentLength = true
				break
			}
		}
		if !hasContentLength {
			builder.WriteString("Content-Length: ")
			builder.WriteString(fmt.Sprintf("%d", req.ContentLength))
			builder.WriteByte('\n')
		}
	}
	builder.WriteByte('\n')
	builder.Write(info.Body)
	return builder.String()
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

func shouldSkipMethodForRequestLogging(req *http.Request) bool {
	if req == nil {
		return true
	}
	if req.Method != http.MethodGet {
		return false
	}
	return !isResponsesWebsocketUpgrade(req)
}

func isResponsesWebsocketUpgrade(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if req.URL.Path != "/v1/responses" && req.URL.Path != "/backend-api/codex/responses" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket")
}

func shouldCaptureRequestBody(loggerEnabled bool, req *http.Request) bool {
	if loggerEnabled {
		return true
	}
	if req == nil || req.Body == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return false
	}
	if req.ContentLength <= 0 {
		return false
	}
	return req.ContentLength <= maxErrorOnlyCapturedRequestBodyBytes
}

// captureRequestInfo extracts relevant information from the incoming HTTP request.
// It captures the URL, method, headers, and body. The request body is read and then
// restored so that it can be processed by subsequent handlers.
func captureRequestInfo(c *gin.Context, captureBody bool) (*RequestInfo, error) {
	// Capture URL with sensitive query parameters masked
	maskedQuery := util.MaskSensitiveQuery(c.Request.URL.RawQuery)
	url := c.Request.URL.Path
	if maskedQuery != "" {
		url += "?" + maskedQuery
	}

	// Capture method
	method := c.Request.Method

	// Capture headers
	headers := make(map[string][]string)
	for key, values := range c.Request.Header {
		headers[key] = values
	}

	// Capture request body
	var body []byte
	if captureBody && c.Request.Body != nil {
		// Read the body
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}

		// Restore the body for the actual request processing
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		body = decodeCapturedRequestBodyForLog(bodyBytes, c.Request.Header.Get("Content-Encoding"))
	}

	return &RequestInfo{
		URL:       url,
		Method:    method,
		Headers:   headers,
		Body:      body,
		RequestID: logging.GetGinRequestID(c),
		Timestamp: time.Now(),
	}, nil
}

func decodeCapturedRequestBodyForLog(raw []byte, encoding string) []byte {
	if len(raw) == 0 {
		return raw
	}
	decoded, err := decodeCapturedRequestBody(raw, encoding)
	if err != nil {
		return raw
	}
	return decoded
}

func decodeCapturedRequestBody(raw []byte, encoding string) ([]byte, error) {
	encoding = strings.TrimSpace(encoding)
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, err := decodeCapturedZstdRequestBody(body)
			if err != nil {
				return nil, err
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeCapturedZstdRequestBody(raw []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	decoded, err := io.ReadAll(decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	return decoded, nil
}

// shouldLogRequest determines whether the request should be logged.
// It skips management endpoints to avoid leaking secrets but allows
// all other routes, including module-provided ones, to honor request-log.
func shouldLogRequest(path string) bool {
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/management") {
		return false
	}

	return true
}
