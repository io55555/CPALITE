// Package handlers provides core API handler functionality for the CLI Proxy API server.
// It includes common types, client management, load balancing, and error handling
// shared across all API endpoint handlers (OpenAI, Claude, Gemini).
package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/packetcapture"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/context"
)

// ErrorResponse represents a standard error response format for the API.
// It contains a single ErrorDetail field.
type ErrorResponse struct {
	// Error contains detailed information about the error that occurred.
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides specific information about an error that occurred.
// It includes a human-readable message, an error type, and an optional error code.
type ErrorDetail struct {
	// Message is a human-readable message providing more details about the error.
	Message string `json:"message"`

	// Type is the category of error that occurred (e.g., "invalid_request_error").
	Type string `json:"type"`

	// Code is a short code identifying the error, if applicable.
	Code string `json:"code,omitempty"`
}

const idempotencyKeyMetadataKey = "idempotency_key"

const (
	defaultStreamingKeepAliveSeconds  = 0
	defaultStreamingBootstrapRetries  = 0
	maxStreamInterceptorHistoryChunks = 64
	maxStreamInterceptorHistoryBytes  = 1 << 20
)

type pinnedAuthContextKey struct{}
type selectedAuthCallbackContextKey struct{}
type executionSessionContextKey struct{}
type disallowFreeAuthContextKey struct{}

type PluginModelRouterHost interface {
	RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool)
}

type PluginExecutorHost interface {
	ExecutePluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	ExecutePluginExecutorStream(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
	CountPluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
}

type PluginExecutorFormatHost interface {
	PluginExecutorRequestToFormat(string, coreexecutor.Request, coreexecutor.Options) sdktranslator.Format
}

type pluginRequestAfterAuthInterceptor interface {
	InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse
}

type pluginRequestBeforeAuthInterceptor interface {
	InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse
}

type pluginRequestBeforeAuthInterceptorExcept interface {
	InterceptRequestBeforeAuthExcept(context.Context, pluginapi.RequestInterceptRequest, string) pluginapi.RequestInterceptResponse
}

type pluginRequestAfterAuthInterceptorExcept interface {
	InterceptRequestAfterAuthExcept(context.Context, pluginapi.RequestInterceptRequest, string) pluginapi.RequestInterceptResponse
}

type pluginResponseInterceptor interface {
	InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse
}

type pluginResponseInterceptorExcept interface {
	InterceptResponseExcept(context.Context, pluginapi.ResponseInterceptRequest, string) pluginapi.ResponseInterceptResponse
}

type pluginStreamChunkInterceptor interface {
	InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse
}

type pluginStreamChunkInterceptorExcept interface {
	InterceptStreamChunkExcept(context.Context, pluginapi.StreamChunkInterceptRequest, string) pluginapi.StreamChunkInterceptResponse
}

type pluginStreamInterceptorCapability interface {
	HasStreamInterceptors() bool
}

// WithPinnedAuthID returns a child context that requests execution on a specific auth ID.
func WithPinnedAuthID(ctx context.Context, authID string) context.Context {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, pinnedAuthContextKey{}, authID)
}

// WithSelectedAuthIDCallback returns a child context that receives the selected auth ID.
func WithSelectedAuthIDCallback(ctx context.Context, callback func(string)) context.Context {
	if callback == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectedAuthCallbackContextKey{}, callback)
}

// WithExecutionSessionID returns a child context tagged with a long-lived execution session ID.
func WithExecutionSessionID(ctx context.Context, sessionID string) context.Context {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionSessionContextKey{}, sessionID)
}

// WithDisallowFreeAuth returns a child context that requests skipping known free-tier credentials.
func WithDisallowFreeAuth(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disallowFreeAuthContextKey{}, true)
}

// BuildErrorResponseBody builds an OpenAI-compatible JSON error response body.
// If errText is already valid JSON, it is returned as-is to preserve upstream error payloads.
func BuildErrorResponseBody(status int, errText string) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(errText) == "" {
		errText = http.StatusText(status)
	}

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return []byte(trimmed)
	}

	errType := "invalid_request_error"
	var code string
	switch status {
	case http.StatusUnauthorized:
		errType = "authentication_error"
		code = "invalid_api_key"
	case http.StatusForbidden:
		errType = "permission_error"
		code = "insufficient_quota"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
		code = "rate_limit_exceeded"
	case http.StatusNotFound:
		errType = "invalid_request_error"
		code = "model_not_found"
	default:
		if status >= http.StatusInternalServerError {
			errType = "server_error"
			code = "internal_server_error"
		}
	}

	payload, err := json.Marshal(ErrorResponse{
		Error: ErrorDetail{
			Message: errText,
			Type:    errType,
			Code:    code,
		},
	})
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":"internal_server_error"}}`, errText))
	}
	return payload
}

// StreamingKeepAliveInterval returns the SSE keep-alive interval for this server.
// Returning 0 disables keep-alives (default when unset).
func StreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := defaultStreamingKeepAliveSeconds
	if cfg != nil {
		seconds = cfg.Streaming.KeepAliveSeconds
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// NonStreamingKeepAliveInterval returns the keep-alive interval for non-streaming responses.
// Returning 0 disables keep-alives (default when unset).
func NonStreamingKeepAliveInterval(cfg *config.SDKConfig) time.Duration {
	seconds := 0
	if cfg != nil {
		seconds = cfg.NonStreamKeepAliveInterval
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// StreamingBootstrapRetries returns how many times a streaming request may be retried before any bytes are sent.
func StreamingBootstrapRetries(cfg *config.SDKConfig) int {
	retries := defaultStreamingBootstrapRetries
	if cfg != nil {
		retries = cfg.Streaming.BootstrapRetries
	}
	if retries < 0 {
		retries = 0
	}
	return retries
}

// PassthroughHeadersEnabled returns whether upstream response headers should be forwarded to clients.
// Default is false.
func PassthroughHeadersEnabled(cfg *config.SDKConfig) bool {
	return cfg != nil && cfg.PassthroughHeaders
}

func requestExecutionMetadata(ctx context.Context) map[string]any {
	// Idempotency-Key is an optional client-supplied header used to correlate retries.
	// Only include it if the client explicitly provides it.
	key := ""
	requestPath := ""
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			key = strings.TrimSpace(ginCtx.GetHeader("Idempotency-Key"))
			requestPath = strings.TrimSpace(ginCtx.FullPath())
			if requestPath == "" && ginCtx.Request.URL != nil {
				requestPath = strings.TrimSpace(ginCtx.Request.URL.Path)
			}
		}
	}

	meta := make(map[string]any)
	if key != "" {
		meta[idempotencyKeyMetadataKey] = key
	}
	if requestPath != "" {
		meta[coreexecutor.RequestPathMetadataKey] = requestPath
	}
	if pinnedAuthID := pinnedAuthIDFromContext(ctx); pinnedAuthID != "" {
		meta[coreexecutor.PinnedAuthMetadataKey] = pinnedAuthID
	}
	if selectedCallback := selectedAuthIDCallbackFromContext(ctx); selectedCallback != nil {
		meta[coreexecutor.SelectedAuthCallbackMetadataKey] = selectedCallback
	}
	if executionSessionID := executionSessionIDFromContext(ctx); executionSessionID != "" {
		meta[coreexecutor.ExecutionSessionMetadataKey] = executionSessionID
	}
	if disallowFreeAuthFromContext(ctx) {
		meta[coreexecutor.DisallowFreeAuthMetadataKey] = true
	}
	return meta
}

func setReasoningEffortMetadata(meta map[string]any, handlerType, model string, rawJSON []byte) {
	if meta == nil {
		return
	}
	effort := thinking.ExtractReasoningEffort(rawJSON, handlerType, model)
	if effort == "" {
		return
	}
	meta[coreexecutor.ReasoningEffortMetadataKey] = effort
}

func setServiceTierMetadata(meta map[string]any, rawJSON []byte) {
	if meta == nil {
		return
	}
	serviceTier := coreusage.AutoServiceTier
	node := gjson.GetBytes(rawJSON, "service_tier")
	if node.Exists() {
		value := strings.TrimSpace(node.String())
		if value != "" {
			serviceTier = value
		}
	}
	meta[coreexecutor.ServiceTierMetadataKey] = serviceTier
}

func setGenerateMetadata(meta map[string]any, rawJSON []byte) {
	if meta == nil {
		return
	}
	generate := true
	node := gjson.GetBytes(rawJSON, "generate")
	if node.Exists() && node.IsBool() && !node.Bool() {
		generate = false
	}
	meta[coreexecutor.GenerateMetadataKey] = generate
}

// headersFromContext extracts the original HTTP request headers from the gin context
// embedded in the provided context. This allows session affinity selectors to read
// client headers like X-Amp-Thread-Id.
func headersFromContext(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.Request.Header.Clone()
	}
	return nil
}

func pinnedAuthIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(pinnedAuthContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func selectedAuthIDCallbackFromContext(ctx context.Context) func(string) {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(selectedAuthCallbackContextKey{})
	if callback, ok := raw.(func(string)); ok && callback != nil {
		return callback
	}
	return nil
}

func executionSessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(executionSessionContextKey{})
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func disallowFreeAuthFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw, ok := ctx.Value(disallowFreeAuthContextKey{}).(bool)
	return ok && raw
}

// BaseAPIHandler contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service and manages
// load balancing, client selection, and configuration.
type BaseAPIHandler struct {
	// AuthManager manages auth lifecycle and execution in the new architecture.
	AuthManager *coreauth.Manager

	// Cfg holds the current application configuration.
	Cfg *config.SDKConfig

	PluginHost      any
	ModelRouterHost PluginModelRouterHost
}

func (h *BaseAPIHandler) SetPluginHost(host any) {
	if h == nil {
		return
	}
	if isNilInterface(host) {
		h.PluginHost = nil
		h.ModelRouterHost = nil
		return
	}
	h.PluginHost = host
	if router, ok := host.(PluginModelRouterHost); ok {
		h.ModelRouterHost = router
	}
}

func (h *BaseAPIHandler) SetModelRouterHost(host PluginModelRouterHost) {
	if h == nil {
		return
	}
	if isNilInterface(host) {
		h.ModelRouterHost = nil
		return
	}
	h.ModelRouterHost = host
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// NewBaseAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and configuration as input.
//
// Parameters:
//   - cliClients: A slice of AI service clients
//   - cfg: The application configuration
//
// Returns:
//   - *BaseAPIHandler: A new API handlers instance
func NewBaseAPIHandlers(cfg *config.SDKConfig, authManager *coreauth.Manager) *BaseAPIHandler {
	return &BaseAPIHandler{
		Cfg:         cfg,
		AuthManager: authManager,
	}
}

// UpdateClients updates the handlers' client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (h *BaseAPIHandler) UpdateClients(cfg *config.SDKConfig) { h.Cfg = cfg }

// GetAlt extracts the 'alt' parameter from the request query string.
// It checks both 'alt' and '$alt' parameters and returns the appropriate value.
//
// Parameters:
//   - c: The Gin context containing the HTTP request
//
// Returns:
//   - string: The alt parameter value, or empty string if it's "sse"
func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	var alt string
	var hasAlt bool
	alt, hasAlt = c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

// GetContextWithCancel creates a new context with cancellation capabilities.
// It embeds the Gin context and the API handler into the new context for later use.
// The returned cancel function also handles logging the API response if request logging is enabled.
//
// Parameters:
//   - handler: The API handler associated with the request.
//   - c: The Gin context of the current request.
//   - ctx: The parent context (caller values/deadlines are preserved; request context adds cancellation and request ID).
//
// Returns:
//   - context.Context: The new context with cancellation and embedded values.
//   - APIHandlerCancelFunc: A function to cancel the context and log the response.
func (h *BaseAPIHandler) GetContextWithCancel(handler interfaces.APIHandler, c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	var requestCtx context.Context
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}

	if requestCtx != nil && logging.GetRequestID(parentCtx) == "" {
		if requestID := logging.GetRequestID(requestCtx); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		} else if requestID = logging.GetGinRequestID(c); requestID != "" {
			parentCtx = logging.WithRequestID(parentCtx, requestID)
		}
	}
	newCtx, cancel := context.WithCancel(parentCtx)

	endpoint := ""
	if c != nil && c.Request != nil {
		path := strings.TrimSpace(c.FullPath())
		if path == "" && c.Request.URL != nil {
			path = strings.TrimSpace(c.Request.URL.Path)
		}
		if path != "" {
			method := strings.TrimSpace(c.Request.Method)
			if method != "" {
				endpoint = method + " " + path
			} else {
				endpoint = path
			}
		}
	}
	if endpoint != "" {
		newCtx = logging.WithEndpoint(newCtx, endpoint)
	}
	newCtx = logging.WithResponseStatusHolder(newCtx)
	newCtx = logging.WithResponseHeadersHolder(newCtx)

	cancelCtx := newCtx
	if requestCtx != nil && requestCtx != parentCtx {
		go func() {
			select {
			case <-requestCtx.Done():
				cancel()
			case <-cancelCtx.Done():
			}
		}()
	}
	newCtx = context.WithValue(newCtx, "gin", c)
	newCtx = context.WithValue(newCtx, "handler", handler)
	return newCtx, func(params ...interface{}) {
		if c != nil {
			logging.SetResponseStatus(cancelCtx, c.Writer.Status())
		}
		if h.Cfg.RequestLog && len(params) == 1 {
			if existing, exists := c.Get("API_RESPONSE"); exists {
				if existingBytes, ok := existing.([]byte); ok && len(bytes.TrimSpace(existingBytes)) > 0 {
					switch params[0].(type) {
					case error, string:
						cancel()
						return
					}
				}
			}

			var payload []byte
			switch data := params[0].(type) {
			case []byte:
				payload = data
			case error:
				if data != nil {
					payload = []byte(data.Error())
				}
			case string:
				payload = []byte(data)
			}
			if len(payload) > 0 {
				if existing, exists := c.Get("API_RESPONSE"); exists {
					if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
						trimmedPayload := bytes.TrimSpace(payload)
						if len(trimmedPayload) > 0 && bytes.Contains(existingBytes, trimmedPayload) {
							cancel()
							return
						}
					}
				}
				appendAPIResponse(c, payload)
			}
		}

		cancel()
	}
}

// StartNonStreamingKeepAlive emits blank lines every 5 seconds while waiting for a non-streaming response.
// It returns a stop function that must be called before writing the final response.
func (h *BaseAPIHandler) StartNonStreamingKeepAlive(c *gin.Context, ctx context.Context) func() {
	if h == nil || c == nil {
		return func() {}
	}
	interval := NonStreamingKeepAliveInterval(h.Cfg)
	if interval <= 0 {
		return func() {}
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	stopChan := make(chan struct{})
	var stopOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
		wg.Wait()
	}
}

// appendAPIResponse preserves any previously captured API response and appends new data.
func appendAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}

	// Capture timestamp on first API response
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); !exists {
		c.Set("API_RESPONSE_TIMESTAMP", time.Now())
	}

	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}

	c.Set("API_RESPONSE", bytes.Clone(data))
}

// ExecuteWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, false)
}

// ExecuteImageWithAuthManager executes an OpenAI-compatible image endpoint request.
func (h *BaseAPIHandler) ExecuteImageWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, true)
}

func (h *BaseAPIHandler) executeWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManagerWithOptions(ctx, handlerType, modelName, rawJSON, alt, allowImageModel, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeWithAuthManagerWithOptions(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	routeDecision := h.applyModelRouter(ctx, handlerType, modelName, rawJSON, false, execOptions)
	if routeDecision.ExecutorPluginID != "" {
		return h.executeWithPluginExecutor(ctx, handlerType, modelName, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, modelName, allowImageModel, routeDecision)
	if errMsg != nil {
		publishRequestValidationFailureUsage(ctx, handlerType, modelName, rawJSON, errMsg)
		return nil, nil, errMsg
	}
	filteredRawJSON, filterErr := applyClientRequestPacketFilters(ctx, strings.Join(providers, ","), normalizedModel, rawJSON)
	if filterErr != nil {
		publishRequestValidationFailureUsage(ctx, strings.Join(providers, ","), normalizedModel, rawJSON, filterErr)
		return nil, nil, filterErr
	}
	rawJSON = filteredRawJSON
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, handlerType, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	headers := modelExecutionHeaders(ctx, execOptions.Headers)
	rawJSON, headers = h.applyPluginBeforeAuthInterceptor(ctx, handlerType, normalizedModel, modelName, rawJSON, headers, false, reqMeta, execOptions)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
		Format:  sdktranslator.FromString(firstProvider(providers)),
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
		ResponseFormat:  modelExecutionResponseFormat(execOptions.ResponseFormat, firstProvider(providers)),
		Headers:         headers,
		Query:           modelExecutionQuery(ctx, execOptions.Query),
	}
	opts.Metadata = reqMeta
	req, opts = h.applyPluginAfterAuthInterceptor(ctx, handlerType, modelName, req, opts, execOptions)
	logCPAReceivedClientRequest(ctx, strings.Join(providers, ","), normalizedModel, rawJSON)
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		if shouldPublishAuthSelectionFailure(err) {
			publishAuthSelectionFailureUsage(ctx, strings.Join(providers, ","), normalizedModel, rawJSON, status, err)
		}
		return nil, nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	payload, filterMsg := applyClientResponsePacketFilters(ctx, strings.Join(providers, ","), normalizedModel, resp.Payload)
	if filterMsg != nil {
		return nil, nil, filterMsg
	}
	responseHeaders := FilterUpstreamHeaders(resp.Headers)
	rawResponseHeaders := cloneHeader(responseHeaders)
	payload, responseHeaders = h.applyPluginResponseInterceptor(ctx, handlerType, normalizedModel, modelName, req, opts, responseHeaders, payload, execOptions)
	if !PassthroughHeadersEnabled(h.Cfg) && !execOptions.InternalSource {
		responseHeaders = pluginOnlyHeaders(responseHeaders, rawResponseHeaders)
		if len(responseHeaders) == 0 {
			return payload, nil, nil
		}
	}
	return payload, responseHeaders, nil
}

// ExecuteCountWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeCountWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	routeDecision := h.applyModelRouter(ctx, handlerType, modelName, rawJSON, false, execOptions)
	if routeDecision.ExecutorPluginID != "" {
		return h.countWithPluginExecutor(ctx, handlerType, modelName, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, modelName, false, routeDecision)
	if errMsg != nil {
		publishRequestValidationFailureUsage(ctx, handlerType, modelName, rawJSON, errMsg)
		return nil, nil, errMsg
	}
	filteredRawJSON, filterErr := applyClientRequestPacketFilters(ctx, strings.Join(providers, ","), normalizedModel, rawJSON)
	if filterErr != nil {
		publishRequestValidationFailureUsage(ctx, strings.Join(providers, ","), normalizedModel, rawJSON, filterErr)
		return nil, nil, filterErr
	}
	rawJSON = filteredRawJSON
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, handlerType, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
		Headers:         modelExecutionHeaders(ctx, execOptions.Headers),
		Query:           modelExecutionQuery(ctx, execOptions.Query),
	}
	opts.Metadata = reqMeta
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		if shouldPublishAuthSelectionFailure(err) {
			publishAuthSelectionFailureUsage(ctx, strings.Join(providers, ","), normalizedModel, rawJSON, status, err)
		}
		return nil, nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	if !PassthroughHeadersEnabled(h.Cfg) {
		payload, filterMsg := applyClientResponsePacketFilters(ctx, strings.Join(providers, ","), normalizedModel, resp.Payload)
		if filterMsg != nil {
			return nil, nil, filterMsg
		}
		return payload, nil, nil
	}
	payload, filterMsg := applyClientResponsePacketFilters(ctx, strings.Join(providers, ","), normalizedModel, resp.Payload)
	if filterMsg != nil {
		return nil, nil, filterMsg
	}
	return payload, FilterUpstreamHeaders(resp.Headers), nil
}

func publishAuthSelectionFailureUsage(ctx context.Context, provider, model string, rawJSON []byte, status int, err error) {
	rawRequest := truncateUsagePacket(buildDownstreamRawRequest(ctx, rawJSON))
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	rawResponse := truncateUsagePacket(fmt.Sprintf("HTTP/1.1 %d %s\nContent-Type: application/json\n\n%s", status, http.StatusText(status), string(BuildErrorResponseBody(status, errText))))
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:    provider,
		Model:       model,
		Source:      "auth-selection",
		RequestedAt: time.Now(),
		Failed:      true,
		RawRequest:  rawRequest,
		RawResponse: rawResponse,
		Fail: coreusage.Failure{
			StatusCode: status,
			Body:       errText,
		},
	})
}

func applyClientRequestPacketFilters(ctx context.Context, provider, model string, rawJSON []byte) ([]byte, *interfaces.ErrorMessage) {
	rawPacket := buildDownstreamRawRequest(ctx, rawJSON)
	meta := packetFilterMeta(ctx, provider, model)
	filtered, errBlock, _ := packetcapture.ApplyRules(ctx, meta, "client_request", rawPacket)
	if errBlock != nil {
		status := http.StatusForbidden
		if se, ok := any(errBlock).(interface{ StatusCode() int }); ok && se.StatusCode() > 0 {
			status = se.StatusCode()
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: errBlock}
	}
	body := packetcapture.PacketBody(filtered)
	if strings.TrimSpace(body) == "" {
		return rawJSON, nil
	}
	return []byte(body), nil
}

func applyClientResponsePacketFilters(ctx context.Context, provider, model string, payload []byte) ([]byte, *interfaces.ErrorMessage) {
	packet := fmt.Sprintf("HTTP/1.1 200 OK\nContent-Type: application/json\n\n%s", string(payload))
	meta := packetFilterMeta(ctx, provider, model)
	filtered, errBlock, _ := packetcapture.ApplyRules(ctx, meta, "client_response", packet)
	if errBlock != nil {
		status := http.StatusForbidden
		if se, ok := any(errBlock).(interface{ StatusCode() int }); ok && se.StatusCode() > 0 {
			status = se.StatusCode()
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: errBlock}
	}
	body := packetcapture.PacketBody(filtered)
	if strings.TrimSpace(body) == "" {
		return payload, nil
	}
	return []byte(body), nil
}

func packetFilterMeta(ctx context.Context, provider, model string) packetcapture.Record {
	meta := packetcapture.Record{
		ID:        logging.GetRequestID(ctx),
		Provider:  strings.TrimSpace(provider),
		Source:    strings.TrimSpace(provider),
		Model:     strings.TrimSpace(model),
		RequestID: logging.GetRequestID(ctx),
		Endpoint:  logging.GetEndpoint(ctx),
	}
	if ginCtx, _ := ctx.Value("gin").(*gin.Context); ginCtx != nil && ginCtx.Request != nil {
		meta.ClientUA = strings.TrimSpace(ginCtx.Request.UserAgent())
		meta.UserToken = strings.TrimSpace(ginCtx.Request.Header.Get("Authorization"))
	}
	return meta
}

func publishRequestValidationFailureUsage(ctx context.Context, provider, model string, rawJSON []byte, msg *interfaces.ErrorMessage) {
	if msg == nil {
		return
	}
	status := msg.StatusCode
	if status <= 0 {
		status = http.StatusBadGateway
	}
	errText := http.StatusText(status)
	if msg.Error != nil {
		if trimmed := strings.TrimSpace(msg.Error.Error()); trimmed != "" {
			errText = trimmed
		}
	}
	rawRequest := truncateUsagePacket(buildDownstreamRawRequest(ctx, rawJSON))
	rawResponse := truncateUsagePacket(fmt.Sprintf("HTTP/1.1 %d %s\nContent-Type: application/json\n\n%s", status, http.StatusText(status), string(BuildErrorResponseBody(status, errText))))
	coreusage.PublishRecord(ctx, coreusage.Record{
		Provider:    strings.TrimSpace(provider),
		Model:       strings.TrimSpace(model),
		Source:      "request-validation",
		RequestedAt: time.Now(),
		Failed:      true,
		RawRequest:  rawRequest,
		RawResponse: rawResponse,
		Fail: coreusage.Failure{
			StatusCode: status,
			Body:       errText,
		},
	})
}

func buildDownstreamRawRequest(ctx context.Context, body []byte) string {
	if ctx == nil {
		return string(body)
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return string(body)
	}
	req := ginCtx.Request
	path := "/"
	if req.URL != nil && req.URL.RequestURI() != "" {
		path = req.URL.RequestURI()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\n", req.Method, path, formatHTTPProto(req.ProtoMajor, req.ProtoMinor))
	if req.Host != "" {
		fmt.Fprintf(&b, "Host: %s\n", req.Host)
	}
	hasContentLength := false
	for key := range req.Header {
		if strings.EqualFold(key, "Content-Length") {
			hasContentLength = true
			break
		}
	}
	_ = req.Header.Write(&b)
	if !hasContentLength {
		contentLength := req.ContentLength
		if contentLength <= 0 && len(body) > 0 {
			contentLength = int64(len(body))
		}
		if contentLength > 0 {
			fmt.Fprintf(&b, "Content-Length: %d\n", contentLength)
		}
	}
	b.WriteByte('\n')
	b.Write(body)
	return b.String()
}

func logCPAReceivedClientRequest(ctx context.Context, provider, model string, body []byte) {
	entry := logEntryWithRequestID(ctx)
	raw := buildDownstreamRawRequest(ctx, body)
	bearer := ""
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			bearer = strings.TrimSpace(ginCtx.Request.Header.Get("Authorization"))
			ginCtx.Set("cpa.request_model", model)
			ginCtx.Set("cpa.request_provider", provider)
		}
	}
	account := bearer
	if account == "" {
		account = "-"
	}
	entry.Infof(
		"\n================ CPA收到客户端请求 ================\n"+
			"[1/6][%s][%s] CPA收到curl请求\n"+
			"客户端Bearerkey: %s\n"+
			"请求模型: %s\n"+
			"---------------- 客户端发给CPA ----------------\n"+
			"%s\n"+
			"==================================================",
		provider, account, bearer, model, raw,
	)
}

func logEntryWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

func logCPASentClientResponse(c *gin.Context, status int, body []byte) {
	if c == nil {
		return
	}
	provider := "-"
	model := "-"
	account := "-"
	if rawModel := strings.TrimSpace(c.GetString("cpa.request_model")); rawModel != "" {
		model = rawModel
	}
	if rawProvider := strings.TrimSpace(c.GetString("cpa.request_provider")); rawProvider != "" {
		provider = rawProvider
	}
	if c.Request != nil {
		account = strings.TrimSpace(c.Request.Header.Get("Authorization"))
		if account == "" {
			account = "-"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", status, http.StatusText(status))
	_ = c.Writer.Header().Write(&b)
	b.WriteByte('\n')
	b.Write(body)
	c.Set("USAGE_CLIENT_RESPONSE", b.String())
	packetcapture.FlushPendingRecords(c)
	internalusage.FlushPendingRecords(c)
	var ctx context.Context
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	logEntryWithRequestID(ctx).Infof(
		"\n================ CPA发送客户端响应 ================\n"+
			"[6/6][%s][%s] CPA发送给客户端\n"+
			"请求模型: %s\n"+
			"---------------- CPA发送给客户端 ----------------\n"+
			"%s\n"+
			"==================================================",
		provider, account, model, b.String(),
	)
}

func truncateUsagePacket(value string) string {
	const max = 256 * 1024
	if len(value) <= max {
		return value
	}
	return value[:max]
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

// ExecuteStreamWithAuthManager executes a streaming request via the core auth manager.
// This path is the only supported execution route.
// The returned http.Header carries upstream response headers captured before streaming begins.
func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, false)
}

// ExecuteImageStreamWithAuthManager executes a streaming OpenAI-compatible image endpoint request.
func (h *BaseAPIHandler) ExecuteImageStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, true)
}

func (h *BaseAPIHandler) executeStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManagerWithOptions(ctx, handlerType, modelName, rawJSON, alt, allowImageModel, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeStreamWithAuthManagerWithOptions(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	routeDecision := h.applyModelRouter(ctx, handlerType, modelName, rawJSON, true, execOptions)
	if routeDecision.ExecutorPluginID != "" {
		return h.streamWithPluginExecutor(ctx, handlerType, modelName, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, modelName, allowImageModel, routeDecision)
	if errMsg != nil {
		publishRequestValidationFailureUsage(ctx, handlerType, modelName, rawJSON, errMsg)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, nil, errChan
	}
	filteredRawJSON, filterErr := applyClientRequestPacketFilters(ctx, strings.Join(providers, ","), normalizedModel, rawJSON)
	if filterErr != nil {
		publishRequestValidationFailureUsage(ctx, strings.Join(providers, ","), normalizedModel, rawJSON, filterErr)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- filterErr
		close(errChan)
		return nil, nil, errChan
	}
	rawJSON = filteredRawJSON
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, handlerType, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	headers := modelExecutionHeaders(ctx, execOptions.Headers)
	rawJSON, headers = h.applyPluginBeforeAuthInterceptor(ctx, handlerType, normalizedModel, modelName, rawJSON, headers, true, reqMeta, execOptions)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
		Format:  sdktranslator.FromString(firstProvider(providers)),
	}
	opts := coreexecutor.Options{
		Stream:          true,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
		ResponseFormat:  modelExecutionResponseFormat(execOptions.ResponseFormat, firstProvider(providers)),
		Headers:         headers,
		Query:           modelExecutionQuery(ctx, execOptions.Query),
	}
	opts.Metadata = reqMeta
	req, opts = h.applyPluginAfterAuthInterceptor(ctx, handlerType, modelName, req, opts, execOptions)
	logCPAReceivedClientRequest(ctx, strings.Join(providers, ","), normalizedModel, rawJSON)
	streamResult, err := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		if shouldPublishAuthSelectionFailure(err) {
			publishAuthSelectionFailureUsage(ctx, strings.Join(providers, ","), normalizedModel, rawJSON, status, err)
		}
		errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
		close(errChan)
		return nil, nil, errChan
	}
	passthroughHeadersEnabled := PassthroughHeadersEnabled(h.Cfg) || execOptions.InternalSource
	// Capture upstream headers from the initial connection synchronously before the goroutine starts.
	// Keep a mutable map so bootstrap retries can replace it before first payload is sent.
	upstreamHeaders := make(http.Header)
	if passthroughHeadersEnabled {
		upstreamHeaders = cloneHeader(streamResult.Headers)
		if upstreamHeaders == nil {
			upstreamHeaders = make(http.Header)
		}
	}
	chunks := streamResult.Chunks
	streamInterceptorsEnabled := h.pluginStreamInterceptorsEnabled()
	if !passthroughHeadersEnabled && !streamInterceptorsEnabled {
		upstreamHeaders = nil
	}
	var pendingChunk *coreexecutor.StreamChunk
	skipStreamHeaderInit := false
	maxBootstrapRetries := StreamingBootstrapRetries(h.Cfg)
	bootstrapRetriesStart := 0
	select {
	case chunk, ok := <-chunks:
		if ok {
			pendingChunk = &chunk
			skipStreamHeaderInit = chunk.Err != nil
		} else {
			chunks = nil
		}
	case <-time.After(5 * time.Millisecond):
	}
	if pendingChunk != nil && pendingChunk.Err != nil && maxBootstrapRetries > 0 {
		status := statusFromError(pendingChunk.Err)
		eligible := status == 0 || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired ||
			status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
		if eligible {
			if retryResult, retryErr := h.AuthManager.ExecuteStream(ctx, providers, req, opts); retryErr == nil {
				bootstrapRetriesStart = 1
				pendingChunk = nil
				chunks = retryResult.Chunks
				if passthroughHeadersEnabled {
					upstreamHeaders = cloneHeader(retryResult.Headers)
					if upstreamHeaders == nil {
						upstreamHeaders = make(http.Header)
					}
				}
				skipStreamHeaderInit = false
			}
		}
	}
	if !skipStreamHeaderInit {
		upstreamHeaders = h.initializePluginStreamInterceptor(ctx, handlerType, normalizedModel, modelName, req, opts, upstreamHeaders, execOptions)
	}
	returnHeaders := upstreamHeaders
	if !streamInterceptorsEnabled {
		returnHeaders = cloneHeader(upstreamHeaders)
	}
	interceptorHeaders := cloneHeader(upstreamHeaders)
	if interceptorHeaders == nil {
		interceptorHeaders = make(http.Header)
	}
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		sentPayload := false
		bootstrapRetries := bootstrapRetriesStart
		streamHistory := make([][]byte, 0)
		chunkIndex := 0

		sendErr := func(msg *interfaces.ErrorMessage) bool {
			if ctx == nil {
				errChan <- msg
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case errChan <- msg:
				return true
			}
		}

		sendData := func(chunk []byte) bool {
			if ctx == nil {
				dataChan <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case dataChan <- chunk:
				return true
			}
		}

		bootstrapEligible := func(err error) bool {
			status := statusFromError(err)
			if status == 0 {
				return true
			}
			switch status {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired,
				http.StatusRequestTimeout, http.StatusTooManyRequests:
				return true
			default:
				return status >= http.StatusInternalServerError
			}
		}

	outer:
		for {
			for {
				var chunk coreexecutor.StreamChunk
				var ok bool
				if pendingChunk != nil {
					chunk = *pendingChunk
					pendingChunk = nil
					ok = true
				} else if chunks == nil {
					return
				} else if ctx != nil {
					select {
					case <-ctx.Done():
						return
					case chunk, ok = <-chunks:
					}
				} else {
					chunk, ok = <-chunks
				}
				if !ok {
					return
				}
				if chunk.Err != nil {
					streamErr := chunk.Err
					// Safe bootstrap recovery: if the upstream fails before any payload bytes are sent,
					// retry a few times (to allow auth rotation / transient recovery) and then attempt model fallback.
					if !sentPayload {
						if bootstrapRetries < maxBootstrapRetries && bootstrapEligible(streamErr) {
							bootstrapRetries++
							retryResult, retryErr := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
							if retryErr == nil {
								if passthroughHeadersEnabled {
									if upstreamHeaders == nil {
										upstreamHeaders = make(http.Header)
									}
									replaceHeader(upstreamHeaders, retryResult.Headers)
									replaceHeader(interceptorHeaders, upstreamHeaders)
								}
								chunks = retryResult.Chunks
								continue outer
							}
							streamErr = enrichBootstrapRetrySelectionError(retryErr, providers, normalizedModel)
						}
					}

					status := http.StatusInternalServerError
					if se, ok := streamErr.(interface{ StatusCode() int }); ok && se != nil {
						if code := se.StatusCode(); code > 0 {
							status = code
						}
					}
					var addon http.Header
					if he, ok := streamErr.(interface{ Headers() http.Header }); ok && he != nil {
						if hdr := he.Headers(); hdr != nil {
							addon = hdr.Clone()
						}
					}
					_ = sendErr(&interfaces.ErrorMessage{StatusCode: status, Error: streamErr, Addon: addon})
					return
				}
				if len(chunk.Payload) > 0 {
					if handlerType == "openai-response" {
						if err := validateSSEDataJSON(chunk.Payload); err != nil {
							_ = sendErr(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
							return
						}
					}
					streamPayload, streamHeaders, dropChunk := h.applyPluginStreamChunkInterceptor(ctx, handlerType, normalizedModel, modelName, req, opts, interceptorHeaders, chunk.Payload, streamHistory, chunkIndex, execOptions)
					replaceHeader(interceptorHeaders, streamHeaders)
					if !sentPayload && upstreamHeaders != nil {
						replaceHeader(upstreamHeaders, streamHeaders)
					}
					chunkIndex++
					if dropChunk {
						continue
					}
					streamHistory = appendStreamInterceptorHistory(streamHistory, streamPayload)
					sentPayload = true
					if okSendData := sendData(cloneBytes(streamPayload)); !okSendData {
						return
					}
				}
			}
		}
	}()
	return dataChan, returnHeaders, errChan
}

func shouldPublishAuthSelectionFailure(err error) bool {
	if err == nil {
		return false
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		return authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable" || authErr.Code == "model_cooldown"
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "auth_not_found") ||
		strings.Contains(lower, "auth_unavailable") ||
		strings.Contains(lower, "model_cooldown")
}

func validateSSEDataJSON(chunk []byte) error {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[5:])
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if json.Valid(data) {
			continue
		}
		const max = 512
		preview := data
		if len(preview) > max {
			preview = preview[:max]
		}
		return fmt.Errorf("invalid SSE data JSON (len=%d): %q", len(data), preview)
	}
	return nil
}

func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
		if code := se.StatusCode(); code > 0 {
			return code
		}
	}
	return 0
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	return h.getRequestDetailsWithOptions(modelName, false)
}

func (h *BaseAPIHandler) getRequestDetailsWithOptions(modelName string, allowImageModel bool) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	resolvedModelName := modelName
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
			if initialSuffix.HasSuffix {
				resolvedModelName = fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
			} else {
				resolvedModelName = resolvedBase
			}
		}
	} else {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedModelName = util.ResolveAutoModel(modelName)
		}
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	if errMsg := h.validateImageOnlyModel(baseModel, allowImageModel); errMsg != nil {
		return nil, "", errMsg
	}

	if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return []string{"home"}, resolvedModelName, nil
	}

	providers = util.GetProviderName(baseModel)
	// Fallback: if baseModel has no provider but differs from resolvedModelName,
	// try using the full model name. This handles edge cases where custom models
	// may be registered with their full suffixed name (e.g., "my-model(8192)").
	// Evaluated in Story 11.8: This fallback is intentionally preserved to support
	// custom model registrations that include thinking suffixes.
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}
	if len(providers) == 0 && allowImageModel {
		providers = defaultImageModelProviders(baseModel)
	}

	if len(providers) == 0 {
		return nil, "", &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("unknown provider for model %s", modelName)}
	}

	// The thinking suffix is preserved in the model name itself, so no
	// metadata-based configuration passing is needed.
	return providers, resolvedModelName, nil
}

func (h *BaseAPIHandler) validateImageOnlyModel(modelName string, allowImageModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isOpenAIImageOnlyModel(baseModel) && !allowImageModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusServiceUnavailable,
			Error:      fmt.Errorf("model %s is only supported on /v1/images/generations and /v1/images/edits", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isOpenAIImageOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "gpt-image-1.5", "gpt-image-2", "grok-imagine-image", "grok-imagine-image-quality":
		return true
	default:
		return false
	}
}

func defaultImageModelProviders(model string) []string {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "gpt-image-1.5", "gpt-image-2":
		return []string{"codex"}
	case "grok-imagine-image", "grok-imagine-image-quality":
		return []string{"xai"}
	default:
		return nil
	}
}

func routeModelBaseName(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func cloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

type modelRouteDecision struct {
	ExecutorPluginID string
	Provider         string
	Model            string
}

func (h *BaseAPIHandler) modelRouterHost() PluginModelRouterHost {
	if h == nil {
		return nil
	}
	if !isNilInterface(h.ModelRouterHost) {
		return h.ModelRouterHost
	}
	if router, ok := h.PluginHost.(PluginModelRouterHost); ok && !isNilInterface(router) {
		return router
	}
	return nil
}

func (h *BaseAPIHandler) pluginExecutorHost() PluginExecutorHost {
	if h == nil {
		return nil
	}
	if host, ok := h.ModelRouterHost.(PluginExecutorHost); ok && !isNilInterface(host) {
		return host
	}
	if host, ok := h.PluginHost.(PluginExecutorHost); ok && !isNilInterface(host) {
		return host
	}
	return nil
}

func modelRoutersEnabled(host PluginModelRouterHost, skipPluginID string) bool {
	if host == nil {
		return false
	}
	if checker, ok := host.(interface{ HasModelRoutersExcept(string) bool }); ok {
		return checker.HasModelRoutersExcept(skipPluginID)
	}
	if skipPluginID != "" {
		return false
	}
	if checker, ok := host.(interface{ HasModelRouters() bool }); ok {
		return checker.HasModelRouters()
	}
	return false
}

func routeModel(ctx context.Context, host PluginModelRouterHost, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if host == nil || !modelRoutersEnabled(host, skipPluginID) {
		return pluginapi.ModelRouteResponse{}, false
	}
	if skipPluginID != "" {
		if router, ok := host.(interface {
			RouteModelExcept(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
		}); ok {
			return router.RouteModelExcept(ctx, req, skipPluginID)
		}
		return pluginapi.ModelRouteResponse{}, false
	}
	return host.RouteModel(ctx, req)
}

func (h *BaseAPIHandler) applyModelRouter(ctx context.Context, handlerType, modelName string, rawJSON []byte, stream bool, execOptions modelExecutionOptions) modelRouteDecision {
	var decision modelRouteDecision
	resp, ok := routeModel(ctx, h.modelRouterHost(), pluginapi.ModelRouteRequest{
		SourceFormat:   handlerType,
		RequestedModel: modelName,
		Stream:         stream,
		Headers:        modelExecutionHeaders(ctx, execOptions.Headers),
		Query:          modelExecutionQuery(ctx, execOptions.Query),
		Body:           cloneBytes(rawJSON),
		Metadata:       requestExecutionMetadata(ctx),
	}, execOptions.SkipRouterPluginID)
	if !ok || !resp.Handled {
		return decision
	}
	switch resp.TargetKind {
	case pluginapi.ModelRouteTargetSelf, pluginapi.ModelRouteTargetExecutor:
		decision.ExecutorPluginID = strings.TrimSpace(resp.Target)
	case pluginapi.ModelRouteTargetProvider:
		decision.Provider = strings.TrimSpace(resp.Target)
		decision.Model = strings.TrimSpace(resp.TargetModel)
	}
	return decision
}

func (h *BaseAPIHandler) providersForExecution(modelName, originalRequestedModel string, allowImageModel bool, decision modelRouteDecision) ([]string, string, *interfaces.ErrorMessage) {
	if strings.TrimSpace(decision.Provider) == "" {
		return h.getRequestDetailsWithOptions(modelName, allowImageModel)
	}
	normalizedModel := strings.TrimSpace(decision.Model)
	if normalizedModel == "" {
		normalizedModel = strings.TrimSpace(originalRequestedModel)
	}
	parsed := thinking.ParseSuffix(normalizedModel)
	baseModel := strings.TrimSpace(parsed.ModelName)
	if errMsg := h.validateImageOnlyModel(baseModel, allowImageModel); errMsg != nil {
		return nil, "", errMsg
	}
	return []string{strings.ToLower(strings.TrimSpace(decision.Provider))}, normalizedModel, nil
}

func (h *BaseAPIHandler) executeWithPluginExecutor(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	host := h.pluginExecutorHost()
	if host == nil {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
	}
	req, opts := h.pluginExecutorRequest(ctx, handlerType, modelName, rawJSON, alt, false, execOptions)
	req.Format = h.pluginExecutorRequestFormat(executorPluginID, req, opts, req.Format)
	req, opts = h.applyPluginAfterAuthInterceptor(ctx, handlerType, modelName, req, opts, execOptions)
	resp, errExecute := host.ExecutePluginExecutor(ctx, executorPluginID, req, opts)
	if errExecute != nil {
		return nil, nil, executionErrorMessage(errExecute)
	}
	payload, filterMsg := applyClientResponsePacketFilters(ctx, executorPluginID, modelName, resp.Payload)
	if filterMsg != nil {
		return nil, nil, filterMsg
	}
	return payload, FilterUpstreamHeaders(resp.Headers), nil
}

func (h *BaseAPIHandler) countWithPluginExecutor(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	host := h.pluginExecutorHost()
	if host == nil {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
	}
	req, opts := h.pluginExecutorRequest(ctx, handlerType, modelName, rawJSON, alt, false, execOptions)
	req.Format = h.pluginExecutorRequestFormat(executorPluginID, req, opts, req.Format)
	req, opts = h.applyPluginAfterAuthInterceptor(ctx, handlerType, modelName, req, opts, execOptions)
	resp, errCount := host.CountPluginExecutor(ctx, executorPluginID, req, opts)
	if errCount != nil {
		return nil, nil, executionErrorMessage(errCount)
	}
	return resp.Payload, FilterUpstreamHeaders(resp.Headers), nil
}

func (h *BaseAPIHandler) streamWithPluginExecutor(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	errChan := make(chan *interfaces.ErrorMessage, 1)
	host := h.pluginExecutorHost()
	if host == nil {
		errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
		close(errChan)
		return nil, nil, errChan
	}
	req, opts := h.pluginExecutorRequest(ctx, handlerType, modelName, rawJSON, alt, true, execOptions)
	req.Format = h.pluginExecutorRequestFormat(executorPluginID, req, opts, req.Format)
	req, opts = h.applyPluginAfterAuthInterceptor(ctx, handlerType, modelName, req, opts, execOptions)
	result, errStream := host.ExecutePluginExecutorStream(ctx, executorPluginID, req, opts)
	if errStream != nil {
		errChan <- executionErrorMessage(errStream)
		close(errChan)
		return nil, nil, errChan
	}
	dataChan := make(chan []byte)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		if result == nil || result.Chunks == nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-result.Chunks:
				if !ok {
					return
				}
				if chunk.Err != nil {
					errChan <- executionErrorMessage(chunk.Err)
					return
				}
				payload, filterMsg := applyClientResponsePacketFilters(ctx, executorPluginID, modelName, chunk.Payload)
				if filterMsg != nil {
					errChan <- filterMsg
					return
				}
				dataChan <- payload
			}
		}
	}()
	return dataChan, FilterUpstreamHeaders(result.Headers), errChan
}

func (h *BaseAPIHandler) pluginExecutorRequest(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, stream bool, execOptions modelExecutionOptions) (coreexecutor.Request, coreexecutor.Options) {
	filteredRawJSON, _ := applyClientRequestPacketFilters(ctx, "", modelName, rawJSON)
	meta := requestExecutionMetadata(ctx)
	meta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(meta, execOptions.InternalSource)
	req := coreexecutor.Request{
		Model:   modelName,
		Payload: cloneBytes(filteredRawJSON),
		Format:  sdktranslator.FromString(modelExecutionTargetFormat("", handlerType)),
	}
	opts := coreexecutor.Options{
		Stream:          stream,
		Alt:             alt,
		OriginalRequest: cloneBytes(filteredRawJSON),
		SourceFormat:    sdktranslator.FromString(handlerType),
		ResponseFormat:  modelExecutionResponseFormat(execOptions.ResponseFormat, handlerType),
		Headers:         modelExecutionHeaders(ctx, execOptions.Headers),
		Query:           modelExecutionQuery(ctx, execOptions.Query),
		Metadata:        meta,
	}
	return req, opts
}

func (h *BaseAPIHandler) pluginExecutorRequestFormat(pluginID string, req coreexecutor.Request, opts coreexecutor.Options, fallback sdktranslator.Format) sdktranslator.Format {
	if h == nil {
		return fallback
	}
	if host, ok := h.PluginHost.(PluginExecutorFormatHost); ok && host != nil {
		if format := host.PluginExecutorRequestToFormat(pluginID, req, opts); format != "" {
			return format
		}
	}
	if host, ok := h.ModelRouterHost.(PluginExecutorFormatHost); ok && host != nil {
		if format := host.PluginExecutorRequestToFormat(pluginID, req, opts); format != "" {
			return format
		}
	}
	return fallback
}

func (h *BaseAPIHandler) applyPluginBeforeAuthInterceptor(ctx context.Context, handlerType, modelName, requestedModel string, rawJSON []byte, headers http.Header, stream bool, meta map[string]any, execOptions modelExecutionOptions) ([]byte, http.Header) {
	if h == nil || h.PluginHost == nil {
		return rawJSON, headers
	}
	req := pluginapi.RequestInterceptRequest{
		SourceFormat:   handlerType,
		Model:          modelName,
		RequestedModel: requestedModel,
		Stream:         stream,
		Headers:        cloneHeader(headers),
		Body:           cloneBytes(rawJSON),
		Metadata:       meta,
	}
	var resp pluginapi.RequestInterceptResponse
	if execOptions.SkipInterceptorPluginID != "" {
		interceptor, ok := h.PluginHost.(pluginRequestBeforeAuthInterceptorExcept)
		if !ok || interceptor == nil {
			return rawJSON, headers
		}
		resp = interceptor.InterceptRequestBeforeAuthExcept(ctx, req, execOptions.SkipInterceptorPluginID)
	} else {
		interceptor, ok := h.PluginHost.(pluginRequestBeforeAuthInterceptor)
		if !ok || interceptor == nil {
			return rawJSON, headers
		}
		resp = interceptor.InterceptRequestBeforeAuth(ctx, req)
	}
	headers = mergePluginHeaders(headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		rawJSON = cloneBytes(resp.Body)
	}
	return rawJSON, headers
}

func (h *BaseAPIHandler) applyPluginAfterAuthInterceptor(ctx context.Context, handlerType, modelName string, req coreexecutor.Request, opts coreexecutor.Options, execOptions modelExecutionOptions) (coreexecutor.Request, coreexecutor.Options) {
	if h == nil || h.PluginHost == nil {
		return req, opts
	}
	interceptReq := pluginapi.RequestInterceptRequest{
		SourceFormat:   handlerType,
		ToFormat:       string(req.Format),
		Model:          req.Model,
		RequestedModel: modelName,
		Stream:         opts.Stream,
		Headers:        cloneHeader(opts.Headers),
		Body:           cloneBytes(req.Payload),
		Metadata:       opts.Metadata,
	}
	var resp pluginapi.RequestInterceptResponse
	if execOptions.SkipInterceptorPluginID != "" {
		interceptor, ok := h.PluginHost.(pluginRequestAfterAuthInterceptorExcept)
		if !ok || interceptor == nil {
			return req, opts
		}
		resp = interceptor.InterceptRequestAfterAuthExcept(ctx, interceptReq, execOptions.SkipInterceptorPluginID)
	} else {
		interceptor, ok := h.PluginHost.(pluginRequestAfterAuthInterceptor)
		if !ok || interceptor == nil {
			return req, opts
		}
		resp = interceptor.InterceptRequestAfterAuth(ctx, interceptReq)
	}
	opts.Headers = mergePluginHeaders(opts.Headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		req.Payload = cloneBytes(resp.Body)
		opts.OriginalRequest = cloneBytes(resp.Body)
	}
	return req, opts
}

func (h *BaseAPIHandler) applyPluginResponseInterceptor(ctx context.Context, handlerType, modelName, requestedModel string, req coreexecutor.Request, opts coreexecutor.Options, headers http.Header, payload []byte, execOptions modelExecutionOptions) ([]byte, http.Header) {
	if h == nil || h.PluginHost == nil {
		return payload, headers
	}
	interceptReq := pluginapi.ResponseInterceptRequest{
		SourceFormat:    handlerType,
		Model:           modelName,
		RequestedModel:  requestedModel,
		Stream:          false,
		RequestHeaders:  cloneHeader(opts.Headers),
		ResponseHeaders: cloneHeader(headers),
		OriginalRequest: cloneBytes(opts.OriginalRequest),
		RequestBody:     cloneBytes(req.Payload),
		Body:            cloneBytes(payload),
		StatusCode:      http.StatusOK,
		Metadata:        opts.Metadata,
	}
	var resp pluginapi.ResponseInterceptResponse
	if execOptions.SkipInterceptorPluginID != "" {
		interceptor, ok := h.PluginHost.(pluginResponseInterceptorExcept)
		if !ok || interceptor == nil {
			return payload, headers
		}
		resp = interceptor.InterceptResponseExcept(ctx, interceptReq, execOptions.SkipInterceptorPluginID)
	} else {
		interceptor, ok := h.PluginHost.(pluginResponseInterceptor)
		if !ok || interceptor == nil {
			return payload, headers
		}
		resp = interceptor.InterceptResponse(ctx, interceptReq)
	}
	headers = mergePluginHeaders(headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		payload = cloneBytes(resp.Body)
	}
	return payload, headers
}

func (h *BaseAPIHandler) initializePluginStreamInterceptor(ctx context.Context, handlerType, modelName, requestedModel string, req coreexecutor.Request, opts coreexecutor.Options, headers http.Header, execOptions modelExecutionOptions) http.Header {
	if !h.pluginStreamInterceptorsEnabled() {
		return headers
	}
	_, updatedHeaders, _ := h.applyPluginStreamChunkInterceptor(ctx, handlerType, modelName, requestedModel, req, opts, headers, nil, nil, pluginapi.StreamChunkHeaderInitIndex, execOptions)
	return updatedHeaders
}

func (h *BaseAPIHandler) applyPluginStreamChunkInterceptor(ctx context.Context, handlerType, modelName, requestedModel string, req coreexecutor.Request, opts coreexecutor.Options, headers http.Header, payload []byte, history [][]byte, chunkIndex int, execOptions modelExecutionOptions) ([]byte, http.Header, bool) {
	if !h.pluginStreamInterceptorsEnabled() {
		return payload, headers, false
	}
	interceptReq := pluginapi.StreamChunkInterceptRequest{
		SourceFormat:    handlerType,
		Model:           modelName,
		RequestedModel:  requestedModel,
		RequestHeaders:  cloneHeader(opts.Headers),
		ResponseHeaders: cloneHeader(headers),
		OriginalRequest: cloneBytes(opts.OriginalRequest),
		RequestBody:     cloneBytes(req.Payload),
		Body:            cloneBytes(payload),
		HistoryChunks:   cloneByteSlices(history),
		ChunkIndex:      chunkIndex,
		Metadata:        opts.Metadata,
	}
	var resp pluginapi.StreamChunkInterceptResponse
	if execOptions.SkipInterceptorPluginID != "" {
		interceptor, ok := h.PluginHost.(pluginStreamChunkInterceptorExcept)
		if !ok || interceptor == nil {
			return payload, headers, false
		}
		resp = interceptor.InterceptStreamChunkExcept(ctx, interceptReq, execOptions.SkipInterceptorPluginID)
	} else {
		interceptor, ok := h.PluginHost.(pluginStreamChunkInterceptor)
		if !ok || interceptor == nil {
			return payload, headers, false
		}
		resp = interceptor.InterceptStreamChunk(ctx, interceptReq)
	}
	headers = mergePluginHeaders(headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		payload = cloneBytes(resp.Body)
	}
	return payload, headers, resp.DropChunk
}

func (h *BaseAPIHandler) pluginStreamInterceptorsEnabled() bool {
	if h == nil || h.PluginHost == nil {
		return false
	}
	if capability, ok := h.PluginHost.(pluginStreamInterceptorCapability); ok && capability != nil {
		return capability.HasStreamInterceptors()
	}
	if _, ok := h.PluginHost.(pluginStreamChunkInterceptor); ok {
		return true
	}
	if _, ok := h.PluginHost.(pluginStreamChunkInterceptorExcept); ok {
		return true
	}
	return false
}

func mergePluginHeaders(current http.Header, updates http.Header, clear []string) http.Header {
	merged := cloneHeader(current)
	if merged == nil {
		merged = make(http.Header)
	}
	if updates != nil {
		merged = cloneHeader(updates)
		if merged == nil {
			merged = make(http.Header)
		}
	}
	for _, key := range clear {
		merged.Del(key)
	}
	return merged
}

func cloneByteSlices(src [][]byte) [][]byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([][]byte, len(src))
	for i, item := range src {
		dst[i] = cloneBytes(item)
	}
	return dst
}

func pluginOnlyHeaders(headers, raw http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	out := make(http.Header)
	for key, values := range headers {
		if headerValuesEqual(values, raw.Values(key)) {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func headerValuesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstProvider(providers []string) string {
	if len(providers) == 0 {
		return ""
	}
	return providers[0]
}

func modelExecutionTargetFormat(provider, fallback string) string {
	provider = strings.TrimSpace(provider)
	if provider != "" {
		return provider
	}
	return fallback
}

func modelExecutionResponseFormat(responseFormat, fallbackProvider string) sdktranslator.Format {
	responseFormat = strings.TrimSpace(responseFormat)
	if responseFormat != "" {
		return sdktranslator.FromString(responseFormat)
	}
	return sdktranslator.FromString(modelExecutionTargetFormat(fallbackProvider, ""))
}

func appendStreamInterceptorHistory(history [][]byte, chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return history
	}
	history = append(history, cloneBytes(chunk))
	for len(history) > maxStreamInterceptorHistoryChunks || byteSlicesSize(history) > maxStreamInterceptorHistoryBytes {
		history[0] = nil
		history = history[1:]
	}
	if len(history) == 0 {
		return nil
	}
	return history
}

func byteSlicesSize(items [][]byte) int {
	total := 0
	for _, item := range items {
		total += len(item)
	}
	return total
}

func executionErrorMessage(err error) *interfaces.ErrorMessage {
	if err == nil {
		return nil
	}
	statusCode := http.StatusBadGateway
	if statusErr, ok := err.(interface{ StatusCode() int }); ok {
		if code := statusErr.StatusCode(); code > 0 {
			statusCode = code
		}
	}
	addon := http.Header(nil)
	if headerErr, ok := err.(interface{ Headers() http.Header }); ok && headerErr != nil {
		addon = FilterUpstreamHeaders(headerErr.Headers())
	}
	return &interfaces.ErrorMessage{
		StatusCode: statusCode,
		Error:      err,
		Addon:      addon,
	}
}

func replaceHeader(dst http.Header, src http.Header) {
	for key := range dst {
		delete(dst, key)
	}
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
}

func enrichAuthSelectionError(err error, providers []string, model string) error {
	if err == nil {
		return nil
	}

	var authErr *coreauth.Error
	if !errors.As(err, &authErr) || authErr == nil {
		return err
	}

	code := strings.TrimSpace(authErr.Code)
	if code != "auth_not_found" && code != "auth_unavailable" {
		return err
	}

	providerText := strings.Join(providers, ",")
	if providerText == "" {
		providerText = "unknown"
	}
	modelText := strings.TrimSpace(model)
	if modelText == "" {
		modelText = "unknown"
	}

	baseMessage := strings.TrimSpace(authErr.Message)
	if baseMessage == "" {
		baseMessage = "no auth available"
	}
	detail := fmt.Sprintf("%s (providers=%s, model=%s)", baseMessage, providerText, modelText)

	// Clarify the most common alias confusion between Anthropic route names and internal provider keys.
	if strings.Contains(","+providerText+",", ",claude,") {
		detail += "; check Claude auth/key session and cooldown state via /v0/management/auth-files"
	}

	status := authErr.HTTPStatus
	if status <= 0 {
		status = http.StatusServiceUnavailable
	}

	return &coreauth.Error{
		Code:       authErr.Code,
		Message:    detail,
		Retryable:  authErr.Retryable,
		HTTPStatus: status,
	}
}

func enrichBootstrapRetrySelectionError(err error, providers []string, model string) error {
	if err == nil {
		return nil
	}
	if isModelCooldownError(err) {
		providerText := strings.Join(providers, ",")
		if providerText == "" {
			providerText = "unknown"
		}
		modelText := strings.TrimSpace(model)
		if modelText == "" {
			modelText = "unknown"
		}
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = "no auth available"
		}
		return &coreauth.Error{
			Code:       "auth_unavailable",
			Message:    fmt.Sprintf("%s (providers=%s, model=%s)", message, providerText, modelText),
			Retryable:  true,
			HTTPStatus: http.StatusServiceUnavailable,
		}
	}
	return enrichAuthSelectionError(err, providers, model)
}

func isModelCooldownError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *coreauth.Error
	if errors.As(err, &authErr) && authErr != nil {
		return strings.TrimSpace(authErr.Code) == "model_cooldown"
	}
	return strings.Contains(strings.ToLower(err.Error()), `"code":"model_cooldown"`) ||
		strings.Contains(strings.ToLower(err.Error()), "model_cooldown")
}

// WriteErrorResponse writes an error message to the response writer using the HTTP status embedded in the message.
func (h *BaseAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.Addon != nil && PassthroughHeadersEnabled(h.Cfg) {
		for key, values := range msg.Addon {
			if len(values) == 0 {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}

	errText := http.StatusText(status)
	if msg != nil && msg.Error != nil {
		if v := strings.TrimSpace(msg.Error.Error()); v != "" {
			errText = v
		}
	}

	body := BuildErrorResponseBody(status, errText)
	// Append first to preserve upstream response logs, then drop duplicate payloads if already recorded.
	var previous []byte
	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			previous = existingBytes
		}
	}
	appendAPIResponse(c, body)
	trimmedErrText := strings.TrimSpace(errText)
	trimmedBody := bytes.TrimSpace(body)
	if len(previous) > 0 {
		if (trimmedErrText != "" && bytes.Contains(previous, []byte(trimmedErrText))) ||
			(len(trimmedBody) > 0 && bytes.Contains(previous, trimmedBody)) {
			c.Set("API_RESPONSE", previous)
		}
	}

	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	logCPASentClientResponse(c, status, body)
	c.Status(status)
	_, _ = c.Writer.Write(body)
}

func (h *BaseAPIHandler) LoggingAPIResponseError(ctx context.Context, err *interfaces.ErrorMessage) {
	if h.Cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			if apiResponseErrors, isExist := ginContext.Get("API_RESPONSE_ERROR"); isExist {
				if slicesAPIResponseError, isOk := apiResponseErrors.([]*interfaces.ErrorMessage); isOk {
					slicesAPIResponseError = append(slicesAPIResponseError, err)
					ginContext.Set("API_RESPONSE_ERROR", slicesAPIResponseError)
				}
			} else {
				// Create new response data entry
				ginContext.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{err})
			}
		}
	}
}

// APIHandlerCancelFunc is a function type for canceling an API handler's context.
// It can optionally accept parameters, which are used for logging the response.
type APIHandlerCancelFunc func(params ...interface{})
