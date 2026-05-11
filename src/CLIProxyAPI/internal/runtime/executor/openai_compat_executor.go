package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
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
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, requestPath)
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
		logOpenAICompatAttemptTrace(ctx, e.Identifier(), auth, apiKey, rawRequest, rawTransportFailure, rulerDetail)
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
		rulerDetail, rulerMatched := e.applyKeyStatusRulers(ctx, auth, apiKey, req.Model, httpResp.StatusCode, b, rawRequest, rawResponse)
		if strings.TrimSpace(rulerDetail) == "" {
			rulerDetail = formatOpenAICompatNoStatusRulerDetail("未匹配status-rulers规则")
		}
		clientRawResponse := formatOpenAICompatClientResponse(httpResp.StatusCode, httpResp.Header, b)
		usageRawResponse := formatOpenAICompatUsageResponses(rawResponse, rulerDetail, clientRawResponse)
		helps.RecordUsageRawResponse(ctx, usageRawResponse)
		reporter.SetRawResponse(usageRawResponse)
		logOpenAICompatAttemptTrace(ctx, e.Identifier(), auth, apiKey, rawRequest, rawResponse, rulerDetail)
		err = statusErr{code: httpResp.StatusCode, msg: string(b), authFault: rulerMatched}
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
	logOpenAICompatAttemptTrace(ctx, e.Identifier(), auth, apiKey, rawRequest, rawResponse, rulerDetail)
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

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
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
	translated = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel, requestPath)

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
		logOpenAICompatAttemptTrace(ctx, e.Identifier(), auth, apiKey, rawRequest, rawTransportFailure, rulerDetail)
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
		rulerDetail, rulerMatched := e.applyKeyStatusRulers(ctx, auth, apiKey, req.Model, httpResp.StatusCode, b, rawRequest, rawResponse)
		if strings.TrimSpace(rulerDetail) == "" {
			rulerDetail = formatOpenAICompatNoStatusRulerDetail("未匹配status-rulers规则")
		}
		clientRawResponse := formatOpenAICompatClientResponse(httpResp.StatusCode, http.Header{"Content-Type": []string{"application/json"}}, b)
		usageRawResponse := formatOpenAICompatUsageResponses(rawResponse, rulerDetail, clientRawResponse)
		helps.RecordUsageRawResponse(ctx, usageRawResponse)
		reporter.SetRawResponse(usageRawResponse)
		logOpenAICompatAttemptTrace(ctx, e.Identifier(), auth, apiKey, rawRequest, rawResponse, rulerDetail)
		err = statusErr{code: httpResp.StatusCode, msg: string(b), authFault: rulerMatched}
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

func (e *OpenAICompatExecutor) applyKeyStatusRulers(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model string, status int, body []byte, rawRequest, rawResponse string) (string, bool) {
	if packetFilterDisabledAPIKey(ctx) {
		detail := "结果: 已由抓包/过滤规则禁用API Key，跳过重复执行status-rulers"
		helps.RecordUsageStatusRulers(ctx, detail)
		return detail, true
	}
	service := openai_compat_state.DefaultService()
	compat := e.resolveCompatConfig(auth)
	if service == nil || compat == nil || strings.TrimSpace(apiKey) == "" {
		return "", false
	}
	if st, matched := service.ApplyRulersForModel(*compat, apiKey, model, status, body, rawRequest, rawResponse); matched {
		detail := formatOpenAICompatStatusRulerDetail(compat.Name, apiKey, model, status, st)
		helps.RecordUsageStatusRulers(ctx, detail)
		return detail, true
	} else {
		service.MarkErrorForModel(compat.Name, apiKey, model, string(body), rawRequest, rawResponse)
	}
	return "", false
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
		if strings.TrimSpace(trigger.Action) != "disable" || strings.TrimSpace(trigger.Target) != "api_key" {
			continue
		}
		message := "packet filter matched: " + strings.TrimSpace(trigger.RuleName)
		st := service.MarkDisabledForModel(provider, apiKey, model, message, "", filteredPacket)
		if auth != nil {
			service.ApplyToAuth(auth)
		}
		if ginCtx, _ := ctx.Value("gin").(*gin.Context); ginCtx != nil {
			ginCtx.Set(packetFilterDisabledAPIKeyKey, true)
		}
		log.Infof("openai compat api key disabled by packet filter: provider=%s model=%s api_key=%s status=%s detail=%s", provider, model, util.HideAPIKey(apiKey), st.Status, trigger.Detail)
		return
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

func (e *OpenAICompatExecutor) packetFilterMeta(ctx context.Context, auth *cliproxyauth.Auth, apiKey, model string) packetcapture.Record {
	meta := packetcapture.Record{
		ID:        logging.GetRequestID(ctx),
		RequestID: logging.GetRequestID(ctx),
		Provider:  e.providerName(auth),
		Source:    e.providerName(auth),
		Model:     strings.TrimSpace(model),
		APIKey:    util.HideAPIKey(apiKey),
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

func logOpenAICompatAttemptTrace(ctx context.Context, provider string, auth *cliproxyauth.Auth, apiKey, rawRequest, rawResponse, rulerDetail string) {
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
