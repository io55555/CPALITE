package executor

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	codexchat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func xaiOpenWebUICompatEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.XAIOpenWebUICompat
}

func xaiGrokBuildHeaderDefaultsEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.XAIGrokBuildHeaderDefaults
}

const defaultXAIGrokBuildUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// xaiClientResponseFormat resolves the client-facing schema.
// handlers often set ResponseFormat to the provider id ("xai") when no exit protocol
// is specified; that is not a translator schema and must fall back to SourceFormat.
func xaiClientResponseFormat(opts cliproxyexecutor.Options) sdktranslator.Format {
	if opts.ResponseFormat != "" && opts.ResponseFormat != sdktranslator.FromString("xai") {
		return opts.ResponseFormat
	}
	if opts.SourceFormat != "" {
		return opts.SourceFormat
	}
	return cliproxyexecutor.ResponseFormatOrSource(opts)
}

func xaiForceChatCompletionsFormat(cfg *config.Config, responseFormat sdktranslator.Format) sdktranslator.Format {
	if !xaiOpenWebUICompatEnabled(cfg) {
		return responseFormat
	}
	// OpenWebUI Chat Completions path expects openai object format, not responses SSE.
	if responseFormat == sdktranslator.FormatOpenAI || responseFormat == sdktranslator.FormatOpenAIResponse || responseFormat == "" {
		return sdktranslator.FormatOpenAI
	}
	return responseFormat
}

func xaiLooksLikeResponsesEvent(payload []byte) bool {
	raw := bytes.TrimSpace(payload)
	if len(raw) == 0 {
		return false
	}
	typ := gjson.GetBytes(raw, "type").String()
	if strings.HasPrefix(typ, "response.") {
		return true
	}
	if gjson.GetBytes(raw, "object").String() == "response" {
		return true
	}
	return gjson.GetBytes(raw, "response.output").Exists() && !gjson.GetBytes(raw, "choices").Exists()
}

// xaiEnsureOpenAIChatPayload guarantees Chat Completions JSON for openai clients.
// TranslateNonStream should already convert; this is a residual safety net for
// OpenWebUI and similar clients that only understand choices[].message.content.
func xaiEnsureOpenAIChatPayload(ctx context.Context, responseFormat sdktranslator.Format, modelName string, originalPayload, requestBody, payload []byte) []byte {
	if responseFormat != sdktranslator.FormatOpenAI {
		return payload
	}
	if !xaiLooksLikeResponsesEvent(payload) {
		if gjson.GetBytes(payload, "object").String() == "chat.completion" || gjson.GetBytes(payload, "choices").Exists() {
			return payload
		}
	}
	return convertXAIResponsesPayloadToOpenAIChat(ctx, modelName, originalPayload, requestBody, payload)
}

func convertXAIResponsesPayloadToOpenAIChat(ctx context.Context, modelName string, originalPayload, requestBody, payload []byte) []byte {
	raw := bytes.TrimSpace(payload)
	if len(raw) == 0 {
		return payload
	}
	if gjson.GetBytes(raw, "object").String() == "chat.completion" || (gjson.GetBytes(raw, "choices").Exists() && !strings.Contains(gjson.GetBytes(raw, "type").String(), "response.")) {
		return raw
	}
	var param any
	// Non-stream helper expects raw JSON event payload (not necessarily data: prefix).
	if out := codexchat.ConvertCodexResponseToOpenAINonStream(ctx, modelName, originalPayload, requestBody, raw, &param); len(out) > 0 {
		return out
	}
	dataLine := append([]byte("data: "), raw...)
	chunks := codexchat.ConvertCodexResponseToOpenAI(ctx, modelName, originalPayload, requestBody, dataLine, &param)
	if len(chunks) == 0 {
		return payload
	}
	for i := len(chunks) - 1; i >= 0; i-- {
		c := bytes.TrimSpace(chunks[i])
		c = bytes.TrimPrefix(c, []byte("data:"))
		c = bytes.TrimSpace(c)
		if gjson.GetBytes(c, "object").String() == "chat.completion" || gjson.GetBytes(c, "choices").Exists() {
			return c
		}
	}
	var content strings.Builder
	id := ""
	model := modelName
	for _, chunk := range chunks {
		c := bytes.TrimSpace(chunk)
		c = bytes.TrimPrefix(c, []byte("data:"))
		c = bytes.TrimSpace(c)
		if id == "" {
			id = gjson.GetBytes(c, "id").String()
		}
		if m := gjson.GetBytes(c, "model").String(); m != "" {
			model = m
		}
		content.WriteString(gjson.GetBytes(c, "choices.0.delta.content").String())
	}
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "choices.0.message.content", content.String())
	return out
}

func convertXAIStreamLineToOpenAIChat(ctx context.Context, modelName string, originalPayload, requestBody, line []byte, param *any) [][]byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil
	}
	return codexchat.ConvertCodexResponseToOpenAI(ctx, modelName, originalPayload, requestBody, trimmed, param)
}

// applyXAIGrokBuildHeaderDefaults sets fixed Grok Build style headers on auth-file requests.
func applyXAIGrokBuildHeaderDefaults(cfg *config.Config, req *http.Request) {
	if !xaiGrokBuildHeaderDefaultsEnabled(cfg) || req == nil {
		return
	}
	// 强制固定拟真 UA / 版本，避免被默认 Header 覆盖
	userAgent := strings.TrimSpace(cfg.XAIGrokBuildHeaderDefaultsUserAgent)
	if userAgent == "" {
		userAgent = defaultXAIGrokBuildUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set(xaiClientVersionHeader, xaiClientVersionValue)
	req.Header.Set("x-grok-client-version", xaiClientVersionValue)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	}
	if req.Header.Get("Origin") == "" {
		req.Header.Set("Origin", "https://grok.com")
	}
	if req.Header.Get("Referer") == "" {
		req.Header.Set("Referer", "https://grok.com/")
	}
}
