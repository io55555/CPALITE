package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/openai_compat_state"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/packetcapture"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

const (
	openAICompatImageHandlerType            = "openai-image"
	openAICompatImagesGenerationsPath       = "/images/generations"
	openAICompatImagesEditsPath             = "/images/edits"
	openAICompatDefaultImageEndpoint        = openAICompatImagesGenerationsPath
	openAICompatMultipartMemory       int64 = 32 << 20
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

var openAICompatProxyFailureBackoff sync.Map

const packetFilterDisabledAPIKeyKey = "PACKET_FILTER_DISABLED_API_KEY"

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.DeferFailureUntilClientResponse()
	defer reporter.TrackFailure(ctx, &err)
	if raw := helps.BuildDownstreamRawRequest(ctx, opts.OriginalRequest); strings.TrimSpace(raw) != "" {
		helps.RecordUsageRawRequest(ctx, raw)
		reporter.SetRawRequest(formatOpenAICompatUsageRequests(raw, ""))
	}

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}
	if strings.TrimSpace(apiKey) == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider api key"}
		return
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	reporter.SetTranslatedReasoningEffort(translated, to.String())
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	rawRequest := openai_compat_state.BuildRawRequest(httpReq, translated)
	rawRequest, err = e.applyUpstreamRequestPacketFilters(ctx, auth, apiKey, req.Model, rawRequest, &translated, httpReq)
	if err != nil {
		return resp, err
	}
	clientRawRequest := helps.BuildDownstreamRawRequest(ctx, opts.OriginalRequest)
	usageRawRequest := formatOpenAICompatUsageRequests(clientRawRequest, rawRequest)
	helps.RecordUsageRawRequest(ctx, usageRawRequest)
	reporter.SetRawRequest(usageRawRequest)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		rawTransportFailure := formatOpenAICompatTransportFailure(err)
		rulerDetail := e.markProxyFailure(auth, apiKey, req.Model, err, rawRequest)
		if strings.TrimSpace(rulerDetail) == "" {
			rulerDetail = formatOpenAICompatNoStatusRulerDetail("未收到供应商HTTP响应，未执行status-rulers")
		}
		usageRawResponse := formatOpenAICompatUsageResponses(rawTransportFailure, rulerDetail, formatOpenAICompatClientResponse(http.StatusBadGateway, http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}, []byte(rawTransportFailure)))
		helps.RecordUsageRawResponse(ctx, usageRawResponse)
		reporter.SetRawResponse(usageRawResponse)
		logOpenAICompatAttemptTrace(ctx, e.cfg, e.Identifier(), auth, apiKey, rawRequest, rawTransportFailure, rulerDetail)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		rawResponse := openai_compat_state.BuildRawResponse(httpResp, b)
		rawResponse, err = e.applyUpstreamResponsePacketFilters(ctx, auth, apiKey, req.Model, rawResponse, &b)
		if err != nil {
			return resp, err
		}
		rulerDetail, rulerMatched, terminal := e.applyKeyStatusRulers(ctx, auth, apiKey, req.Model, httpResp.StatusCode, b, rawRequest, rawResponse)
		if strings.TrimSpace(rulerDetail) == "" {
			rulerDetail = formatOpenAICompatNoStatusRulerDetail("未匹配status-rulers规则")
		}
		clientStatus, clientMessage := httpResp.StatusCode, string(b)
		clientBody := b
		if terminal.matched {
			clientStatus, clientMessage = terminal.status, terminal.message
			clientBody = formatOpenAICompatClientErrorBody(clientStatus, clientMessage)
		}
		clientRawResponse := formatOpenAICompatClientResponse(clientStatus, httpResp.Header, clientBody)
		originalClientBody := append([]byte(nil), clientBody...)
		clientRawResponse, clientStatus, clientBody, err = e.applyClientResponsePacketFilters(ctx, auth, apiKey, req.Model, clientStatus, httpResp.Header, clientBody)
		clientFilterTerminal := err != nil
		if clientFilterTerminal || !bytes.Equal(clientBody, originalClientBody) {
			clientMessage = string(clientBody)
		}
		usageRawResponse := formatOpenAICompatUsageResponses(rawResponse, rulerDetail, clientRawResponse)
		helps.RecordUsageRawResponse(ctx, usageRawResponse)
		reporter.SetRawResponse(usageRawResponse)
		logOpenAICompatAttemptTrace(ctx, e.cfg, e.Identifier(), auth, apiKey, rawRequest, rawResponse, rulerDetail)
		err = statusErr{code: clientStatus, msg: clientMessage, authFault: rulerMatched && !terminal.matched, stopRetry: terminal.matched || clientFilterTerminal}
		reporter.PublishFailure(ctx, err)
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	rawResponse := openai_compat_state.BuildRawResponse(httpResp, body)
	rawResponse, err = e.applyUpstreamResponsePacketFilters(ctx, auth, apiKey, req.Model, rawResponse, &body)
	if err != nil {
		return resp, err
	}
	rulerDetail := formatOpenAICompatNoStatusRulerDetail("上游响应成功，未触发status-rulers")
	helps.RecordUsageRawResponse(ctx, formatOpenAICompatUsageResponses(rawResponse, rulerDetail, ""))
	reporter.SetRawResponse(formatOpenAICompatUsageResponses(rawResponse, rulerDetail, ""))
	logOpenAICompatAttemptTrace(ctx, e.cfg, e.Identifier(), auth, apiKey, rawRequest, rawResponse, rulerDetail)
	e.resetProxyFailureBackoff(auth, apiKey, req.Model)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return resp, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), false)
	if errPrepare != nil {
		err = errPrepare
		return resp, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		err = errRead
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImagesStream(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.DeferFailureUntilClientResponse()
	defer reporter.TrackFailure(ctx, &err)
	if raw := helps.BuildDownstreamRawRequest(ctx, opts.OriginalRequest); strings.TrimSpace(raw) != "" {
		helps.RecordUsageRawRequest(ctx, raw)
		reporter.SetRawRequest(formatOpenAICompatUsageRequests(raw, ""))
	}

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider api key"}
		return nil, err
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	rawRequest := openai_compat_state.BuildRawRequest(httpReq, translated)
	rawRequest, err = e.applyUpstreamRequestPacketFilters(ctx, auth, apiKey, req.Model, rawRequest, &translated, httpReq)
	if err != nil {
		return nil, err
	}
	clientRawRequest := helps.BuildDownstreamRawRequest(ctx, opts.OriginalRequest)
	usageRawRequest := formatOpenAICompatUsageRequests(clientRawRequest, rawRequest)
	helps.RecordUsageRawRequest(ctx, usageRawRequest)
	reporter.SetRawRequest(usageRawRequest)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		rawTransportFailure := formatOpenAICompatTransportFailure(err)
		rulerDetail := e.markProxyFailure(auth, apiKey, req.Model, err, rawRequest)
		if strings.TrimSpace(rulerDetail) == "" {
			rulerDetail = formatOpenAICompatNoStatusRulerDetail("未收到供应商HTTP响应，未执行status-rulers")
		}
		usageRawResponse := formatOpenAICompatUsageResponses(rawTransportFailure, rulerDetail, formatOpenAICompatClientResponse(http.StatusBadGateway, http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}, []byte(rawTransportFailure)))
		helps.RecordUsageRawResponse(ctx, usageRawResponse)
		reporter.SetRawResponse(usageRawResponse)
		logOpenAICompatAttemptTrace(ctx, e.cfg, e.Identifier(), auth, apiKey, rawRequest, rawTransportFailure, rulerDetail)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		rawResponse := openai_compat_state.BuildRawResponse(httpResp, b)
		rawResponse, err = e.applyUpstreamResponsePacketFilters(ctx, auth, apiKey, req.Model, rawResponse, &b)
		if err != nil {
			return nil, err
		}
		rulerDetail, rulerMatched, terminal := e.applyKeyStatusRulers(ctx, auth, apiKey, req.Model, httpResp.StatusCode, b, rawRequest, rawResponse)
		if strings.TrimSpace(rulerDetail) == "" {
			rulerDetail = formatOpenAICompatNoStatusRulerDetail("未匹配status-rulers规则")
		}
		clientStatus, clientMessage := httpResp.StatusCode, string(b)
		clientBody := b
		if terminal.matched {
			clientStatus, clientMessage = terminal.status, terminal.message
			clientBody = formatOpenAICompatClientErrorBody(clientStatus, clientMessage)
		}
		clientRawResponse := formatOpenAICompatClientResponse(clientStatus, http.Header{"Content-Type": []string{"application/json"}}, clientBody)
		originalClientBody := append([]byte(nil), clientBody...)
		clientRawResponse, clientStatus, clientBody, err = e.applyClientResponsePacketFilters(ctx, auth, apiKey, req.Model, clientStatus, http.Header{"Content-Type": []string{"application/json"}}, clientBody)
		clientFilterTerminal := err != nil
		if clientFilterTerminal || !bytes.Equal(clientBody, originalClientBody) {
			clientMessage = string(clientBody)
		}
		usageRawResponse := formatOpenAICompatUsageResponses(rawResponse, rulerDetail, clientRawResponse)
		helps.RecordUsageRawResponse(ctx, usageRawResponse)
		reporter.SetRawResponse(usageRawResponse)
		logOpenAICompatAttemptTrace(ctx, e.cfg, e.Identifier(), auth, apiKey, rawRequest, rawResponse, rulerDetail)
		err = statusErr{code: clientStatus, msg: clientMessage, authFault: rulerMatched && !terminal.matched, stopRetry: terminal.matched || clientFilterTerminal}
		reporter.PublishFailure(ctx, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				continue
			}

			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("event:")) ||
					bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
					continue
				}
				if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
					streamErr := statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				continue
			}

			// OpenAI-compatible streams must use SSE data lines.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(trimmedLine), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else {
			// In case the upstream close the stream without a terminal [DONE] marker.
			// Feed a synthetic done marker through the translator so pending
			// response.completed events are still emitted exactly once.
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		// Ensure we record the request if no usage chunk was ever seen
		e.resetProxyFailureBackoff(auth, apiKey, req.Model)
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) executeImagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), true)
	if errPrepare != nil {
		err = errPrepare
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
			reporter.EnsurePublished(ctx)
		}()
		buffer := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				chunk := bytes.Clone(buffer[:n])
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, errRead)
					reporter.PublishFailure(ctx, errRead)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func openAICompatImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return ""
	}
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, "/images/edits") {
		return openAICompatImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return openAICompatImagesGenerationsPath
	}
	return openAICompatDefaultImageEndpoint
}

func prepareOpenAICompatImagesPayload(payload []byte, model string, contentType string, stream bool) ([]byte, string, error) {
	model = strings.TrimSpace(model)
	contentType = strings.TrimSpace(contentType)
	if json.Valid(payload) {
		if model != "" {
			payload, _ = sjson.SetBytes(payload, "model", model)
		}
		if stream {
			payload, _ = sjson.SetBytes(payload, "stream", true)
		} else {
			payload, _ = sjson.DeleteBytes(payload, "stream")
		}
		return payload, "application/json", nil
	}

	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return payload, contentType, nil
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	return rewriteOpenAICompatImagesMultipartPayload(payload, model, boundary, stream)
}

func cloneOpenAICompatMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func rewriteOpenAICompatImagesMultipartPayload(payload []byte, model string, boundary string, stream bool) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	form, errRead := reader.ReadForm(openAICompatMultipartMemory)
	if errRead != nil {
		return nil, "", fmt.Errorf("read multipart form failed: %w", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("openai compat executor: remove multipart form files error: %v", errRemove)
		}
	}()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if model != "" {
		if errWrite := writer.WriteField("model", model); errWrite != nil {
			return nil, "", fmt.Errorf("write model field failed: %w", errWrite)
		}
	}
	if stream {
		if errWrite := writer.WriteField("stream", "true"); errWrite != nil {
			return nil, "", fmt.Errorf("write stream field failed: %w", errWrite)
		}
	}
	for key, values := range form.Value {
		if key == "model" || key == "stream" {
			continue
		}
		for _, value := range values {
			if errWrite := writer.WriteField(key, value); errWrite != nil {
				return nil, "", fmt.Errorf("write form field %s failed: %w", key, errWrite)
			}
		}
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			if fileHeader == nil {
				continue
			}
			header := cloneOpenAICompatMIMEHeader(fileHeader.Header)
			header.Set("Content-Disposition", multipart.FileContentDisposition(key, fileHeader.Filename))
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				return nil, "", fmt.Errorf("create file field %s failed: %w", key, errCreate)
			}
			src, errOpen := fileHeader.Open()
			if errOpen != nil {
				return nil, "", fmt.Errorf("open upload file failed: %w", errOpen)
			}
			_, errCopy := io.Copy(part, src)
			if errClose := src.Close(); errClose != nil {
				log.Errorf("openai compat executor: close upload file error: %v", errClose)
				if errCopy == nil {
					errCopy = errClose
				}
			}
			if errCopy != nil {
				return nil, "", fmt.Errorf("copy upload file failed: %w", errCopy)
			}
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", fmt.Errorf("close multipart writer failed: %w", errClose)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

type openAICompatTerminalRuler struct {
	matched bool
	status  int
	message string
}

func (e *OpenAICompatExecutor) applyKeyStatusRulers(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model string, status int, body []byte, rawRequest, rawResponse string) (string, bool, openAICompatTerminalRuler) {
	if packetFilterDisabledAPIKey(ctx) {
		detail := "结果: 已由抓包/过滤规则禁用API Key，跳过重复执行status-rulers"
		helps.RecordUsageStatusRulers(ctx, detail)
		return detail, true, openAICompatTerminalRuler{}
	}
	service := openai_compat_state.DefaultService()
	compat := e.resolveCompatConfig(auth)
	if service == nil || compat == nil || strings.TrimSpace(apiKey) == "" {
		return "", false, openAICompatTerminalRuler{}
	}
	if result := service.ApplyRulersForModelResult(*compat, apiKey, model, status, body, rawRequest, rawResponse); result.Matched {
		detail := formatOpenAICompatStatusRulerDetail(compat.Name, apiKey, model, status, result.State)
		helps.RecordUsageStatusRulers(ctx, detail)
		terminal := openAICompatTerminalRuler{}
		if result.Terminal {
			terminal.matched = true
			terminal.status = result.ClientStatus
			terminal.message = result.ClientMessage
			if terminal.status <= 0 {
				terminal.status = status
			}
			if terminal.message == "" {
				terminal.message = strings.TrimSpace(string(body))
			}
		}
		return detail, true, terminal
	} else {
		service.MarkErrorForModel(compat.Name, apiKey, model, string(body), rawRequest, rawResponse)
	}
	return "", false, openAICompatTerminalRuler{}
}

func (e *OpenAICompatExecutor) providerName(auth *cliproxyauth.Auth) string {
	if compat := e.resolveCompatConfig(auth); compat != nil {
		return strings.TrimSpace(compat.Name)
	}
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			return v
		}
	}
	return strings.TrimSpace(e.provider)
}

func (e *OpenAICompatExecutor) applyUpstreamRequestPacketFilters(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model, rawPacket string, body *[]byte, req *http.Request) (string, error) {
	meta := e.packetFilterMeta(ctx, auth, apiKey, model)
	filtered, blockErr, _ := packetcapture.ApplyRules(ctx, meta, "upstream_request", rawPacket)
	if blockErr != nil {
		return rawPacket, blockErr
	}
	if req != nil {
		applyFilteredPacketHeaders(req, filtered)
	}
	if body != nil {
		if filteredBody := packetcapture.PacketBody(filtered); strings.TrimSpace(filteredBody) != "" && filteredBody != string(*body) {
			*body = []byte(filteredBody)
			if req != nil {
				req.Body = io.NopCloser(bytes.NewReader(*body))
				req.ContentLength = int64(len(*body))
				req.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(*body)), nil
				}
			}
		}
	}
	return filtered, nil
}

func applyFilteredPacketHeaders(req *http.Request, packet string) {
	headerBlock, _, _ := strings.Cut(packet, "\n\n")
	if strings.TrimSpace(headerBlock) == strings.TrimSpace(packet) {
		headerBlock, _, _ = strings.Cut(packet, "\r\n\r\n")
	}
	lines := strings.Split(headerBlock, "\n")
	next := http.Header{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		next.Add(key, strings.TrimSpace(value))
	}
	if len(next) > 0 {
		req.Header = next
	}
}

func (e *OpenAICompatExecutor) applyUpstreamResponsePacketFilters(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model, rawPacket string, body *[]byte) (string, error) {
	meta := e.packetFilterMeta(ctx, auth, apiKey, model)
	filtered, blockErr, triggers := packetcapture.ApplyRules(ctx, meta, "upstream_response", rawPacket)
	e.applyUpstreamResponsePacketActions(ctx, auth, apiKey, model, filtered, triggers)
	if blockErr != nil {
		return rawPacket, blockErr
	}
	if body != nil {
		if filteredBody := packetcapture.PacketBody(filtered); filteredBody != "" && filteredBody != string(*body) {
			*body = []byte(filteredBody)
		}
	}
	return filtered, nil
}

func (e *OpenAICompatExecutor) applyClientResponsePacketFilters(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model string, status int, headers http.Header, body []byte) (string, int, []byte, error) {
	rawPacket := formatOpenAICompatClientResponse(status, headers, body)
	meta := e.packetFilterMeta(ctx, auth, apiKey, model)
	filtered, blockErr, _ := packetcapture.ApplyRules(ctx, meta, "client_response", rawPacket)
	if blockErr != nil {
		blockStatus := http.StatusForbidden
		if se, ok := any(blockErr).(interface{ StatusCode() int }); ok && se.StatusCode() > 0 {
			blockStatus = se.StatusCode()
		}
		blockBody := formatOpenAICompatClientErrorBody(blockStatus, blockErr.Error())
		return formatOpenAICompatClientResponse(blockStatus, http.Header{"Content-Type": []string{"application/json"}}, blockBody), blockStatus, blockBody, blockErr
	}
	if filteredBody := packetcapture.PacketBody(filtered); filteredBody != "" && filteredBody != string(body) {
		body = []byte(filteredBody)
	}
	if parsedStatus := statusFromPacketLine(filtered); parsedStatus > 0 {
		status = parsedStatus
	}
	return filtered, status, body, nil
}

func (e *OpenAICompatExecutor) applyUpstreamResponsePacketActions(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model, filteredPacket string, triggers []packetcapture.TriggerRecord) {
	if len(triggers) == 0 || strings.TrimSpace(apiKey) == "" {
		return
	}
	service := openai_compat_state.DefaultService()
	if service == nil {
		return
	}
	provider := e.providerName(auth)
	if provider == "" {
		return
	}
	for _, trigger := range triggers {
		action := strings.TrimSpace(trigger.Action)
		target := strings.TrimSpace(trigger.Target)
		if target != "api_key" && target != "auth" {
			continue
		}
		message := "packet filter matched: " + strings.TrimSpace(trigger.RuleName)
		switch action {
		case "disable":
			st := service.MarkDisabledForModel(provider, apiKey, model, message, "", filteredPacket)
			if auth != nil {
				service.ApplyToAuth(auth)
			}
			if ginCtx, _ := ctx.Value("gin").(*gin.Context); ginCtx != nil {
				ginCtx.Set(packetFilterDisabledAPIKeyKey, true)
			}
			log.Infof("openai compat api key disabled by packet filter: provider=%s model=%s api_key=%s status=%s detail=%s", provider, model, util.HideAPIKey(apiKey), st.Status, trigger.Detail)
			return
		case "cooldown":
			seconds := packetFilterCooldownSeconds(trigger)
			if seconds <= 0 {
				seconds = 300
			}
			st := service.MarkFrozenForModel(provider, apiKey, model, message, "", filteredPacket, time.Duration(seconds)*time.Second)
			if auth != nil {
				service.ApplyToAuth(auth)
			}
			log.Infof("openai compat api key cooled down by packet filter: provider=%s model=%s api_key=%s status=%s seconds=%d detail=%s", provider, model, util.HideAPIKey(apiKey), st.Status, seconds, trigger.Detail)
			return
		}
	}
}

func packetFilterDisabledAPIKey(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	if ginCtx == nil {
		return false
	}
	value, exists := ginCtx.Get(packetFilterDisabledAPIKeyKey)
	disabled, _ := value.(bool)
	return exists && disabled
}

func packetFilterCooldownSeconds(trigger packetcapture.TriggerRecord) int {
	if trigger.CooldownSeconds > 0 {
		return trigger.CooldownSeconds
	}
	return 0
}

func (e *OpenAICompatExecutor) packetFilterMeta(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model string) packetcapture.Record {
	meta := packetcapture.Record{
		ID:            logging.GetRequestID(ctx),
		RequestID:     logging.GetRequestID(ctx),
		Provider:      e.providerName(auth),
		ProviderGroup: "openai-compatibility",
		Source:        e.providerName(auth),
		Model:         strings.TrimSpace(model),
		APIKey:        util.HideAPIKey(apiKey),
	}
	if auth != nil {
		meta.AuthType, meta.AuthLabel = auth.AccountInfo()
		meta.AuthIndex = auth.EnsureIndex()
	}
	return meta
}

func (e *OpenAICompatExecutor) markProxyFailure(auth *cliproxyauth.Auth, apiKey, model string, err error, rawRequest string) string {
	if err == nil || !isProxyFailureMessage(err.Error()) {
		return ""
	}
	providerName := e.providerName(auth)
	if providerName == "" || strings.TrimSpace(apiKey) == "" {
		return ""
	}
	cooldown := e.nextProxyFailureCooldown(providerName, apiKey, model)
	rawResponse := formatOpenAICompatTransportFailure(err)
	if service := openai_compat_state.DefaultService(); service != nil {
		st := service.MarkFrozenForModel(providerName, apiKey, model, err.Error(), rawRequest, rawResponse, cooldown)
		return formatOpenAICompatStatusRulerDetail(providerName, apiKey, model, 0, st)
	}
	return ""
}

func (e *OpenAICompatExecutor) nextProxyFailureCooldown(provider, apiKey, model string) time.Duration {
	base := 300 * time.Second
	maxCooldown := 600 * time.Second
	if e.cfg != nil {
		if e.cfg.ProxyFailureCooldownSeconds > 0 {
			base = time.Duration(e.cfg.ProxyFailureCooldownSeconds) * time.Second
		}
		if e.cfg.ProxyFailureMaxCooldownSeconds > 0 {
			maxCooldown = time.Duration(e.cfg.ProxyFailureMaxCooldownSeconds) * time.Second
		}
	}
	if maxCooldown < base {
		maxCooldown = base
	}
	key := openAICompatProxyBackoffKey(provider, apiKey, model)
	count := 1
	if raw, ok := openAICompatProxyFailureBackoff.Load(key); ok {
		if previous, okInt := raw.(int); okInt && previous > 0 {
			count = previous + 1
		}
	}
	openAICompatProxyFailureBackoff.Store(key, count)
	cooldown := time.Duration(count) * base
	if cooldown > maxCooldown {
		return maxCooldown
	}
	return cooldown
}

func (e *OpenAICompatExecutor) resetProxyFailureBackoff(auth *cliproxyauth.Auth, apiKey, model string) {
	provider := e.providerName(auth)
	if provider == "" || strings.TrimSpace(apiKey) == "" {
		return
	}
	openAICompatProxyFailureBackoff.Delete(openAICompatProxyBackoffKey(provider, apiKey, model))
}

func openAICompatProxyBackoffKey(provider, apiKey, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + strings.TrimSpace(apiKey) + "\x00" + strings.TrimSpace(model)
}

func formatOpenAICompatTransportFailure(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join([]string{
		"# 未收到上游 HTTP 响应，连接在传输阶段失败",
		"Failure Type: proxy_or_network_error",
		"Failure Message: " + err.Error(),
	}, "\n")
}

func formatOpenAICompatUsageRequests(clientRaw, upstreamRaw string) string {
	return joinOpenAICompatUsageSections([]string{
		"=== 客户端发给CPA的完整数据包 ===\n" + strings.TrimSpace(clientRaw),
		"=== CPA发给供应商的完整数据包 ===\n" + strings.TrimSpace(upstreamRaw),
	})
}

func formatOpenAICompatUsageResponses(upstreamRaw, rulerDetail, clientRaw string) string {
	return joinOpenAICompatUsageSections([]string{
		"=== 供应商返回CPA的完整数据包 ===\n" + strings.TrimSpace(upstreamRaw),
		"=== 触发status-rulers ===\n" + strings.TrimSpace(rulerDetail),
		"=== CPA发送给客户端的完整数据包 ===\n" + strings.TrimSpace(clientRaw),
	})
}

func joinOpenAICompatUsageSections(sections []string) string {
	var kept []string
	for _, section := range sections {
		if strings.TrimSpace(section) == "" || strings.HasSuffix(strings.TrimSpace(section), "===") {
			continue
		}
		kept = append(kept, strings.TrimSpace(section))
	}
	return strings.Join(kept, "\n\n")
}

func formatOpenAICompatClientResponse(status int, headers http.Header, body []byte) string {
	if status <= 0 {
		status = http.StatusBadGateway
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", status, http.StatusText(status))
	if headers == nil {
		headers = http.Header{"Content-Type": []string{"application/json"}}
	}
	_ = headers.Write(&b)
	b.WriteByte('\n')
	b.Write(body)
	return b.String()
}

func statusFromPacketLine(packet string) int {
	line, _, _ := strings.Cut(strings.TrimSpace(packet), "\n")
	parts := strings.Fields(line)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return 0
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return status
}

func formatOpenAICompatClientErrorBody(status int, message string) []byte {
	trimmed := strings.TrimSpace(message)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		return []byte(trimmed)
	}
	if trimmed == "" {
		trimmed = http.StatusText(status)
	}
	errType := "invalid_request_error"
	code := ""
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
		code = "model_not_found"
	default:
		if status >= http.StatusInternalServerError {
			errType = "server_error"
			code = "internal_server_error"
		}
	}
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": trimmed,
			"type":    errType,
			"code":    code,
		},
	})
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":"internal_server_error"}}`, trimmed))
	}
	return payload
}

func formatOpenAICompatStatusRulerDetail(provider, apiKey, model string, status int, st openai_compat_state.State) string {
	action := "标记异常"
	switch st.Status {
	case openai_compat_state.StatusDisabled:
		action = "禁用"
	case openai_compat_state.StatusFrozen:
		action = "冷却"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "规则: %s\n", strings.TrimSpace(st.StatusMessage))
	fmt.Fprintf(&b, "动作: %s\n", action)
	fmt.Fprintf(&b, "上游状态码: %d\n", status)
	fmt.Fprintf(&b, "影响范围: provider=%s model=%s api_key=%s\n", strings.TrimSpace(provider), strings.TrimSpace(model), util.HideAPIKey(apiKey))
	if st.Status == openai_compat_state.StatusFrozen && !st.FrozenUntil.IsZero() {
		fmt.Fprintf(&b, "冷却至: %s\n", st.FrozenUntil.Format(time.RFC3339))
	}
	return b.String()
}

func formatOpenAICompatNoStatusRulerDetail(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "未匹配status-rulers规则"
	}
	return "结果: " + message
}

func formatOpenAICompatAuthForLog(auth *cliproxyauth.Auth, apiKey string) string {
	if auth == nil {
		return util.HideAPIKey(apiKey)
	}
	accountType, accountInfo := auth.AccountInfo()
	if accountInfo == "" {
		accountInfo = apiKey
	}
	if accountType == "" {
		accountType = "apikey"
	}
	return fmt.Sprintf("%s=%s auth=%s", accountType, accountInfo, auth.ID)
}

func logOpenAICompatAttemptTrace(ctx context.Context, cfg *config.Config, provider string, auth *cliproxyauth.Auth, apiKey, rawRequest, rawResponse, rulerDetail string) {
	if cfg != nil && !cfg.PacketCapture.CLIDetailedLogEnabled() {
		return
	}
	authText := formatOpenAICompatAuthForLog(auth, apiKey)
	helps.LogWithRequestID(ctx).Infof(
		"\n================ OpenAI兼容请求链路 ================\n"+
			"[2/6][%s][%s] CPA选择apikey或凭证账号\n"+
			"[3/6][%s][%s] CPA发送请求给供应商\n%s\n"+
			"---------------- 供应商响应 ----------------\n"+
			"[4/6][%s][%s] 供应商返回CPA\n%s\n"+
			"---------------- status-rulers ----------------\n"+
			"[5/6][%s][%s] %s\n"+
			"====================================================",
		provider, authText,
		provider, authText, strings.TrimSpace(rawRequest),
		provider, authText, strings.TrimSpace(rawResponse),
		provider, authText, strings.TrimSpace(rulerDetail),
	)
}

func isProxyFailureMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	for _, signal := range []string{
		"tls handshake timeout",
		"i/o timeout",
		"deadline exceeded",
		"no such host",
		"connection refused",
		"connection reset",
		"connectex",
		"no connection could be made",
		"network is unreachable",
	} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	hasProxySignal := strings.Contains(lower, "socks connect") ||
		strings.Contains(lower, "proxyconnect") ||
		strings.Contains(lower, "proxy connection") ||
		strings.Contains(lower, "proxy error") ||
		strings.Contains(lower, "proxy failed")
	if !hasProxySignal {
		return false
	}
	for _, signal := range []string{
		"dial tcp",
		"connectex",
		"connection refused",
		"actively refused",
		"no connection could be made",
		"connection reset",
		"i/o timeout",
		"tls handshake timeout",
		"no such host",
	} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
	authFault  bool
	stopRetry  bool
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }
func (e statusErr) AuthFault() bool            { return e.authFault }
func (e statusErr) StopRetry() bool            { return e.stopRetry }
