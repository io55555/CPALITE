package management

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/openai_compat_state"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type patchOpenAICompatKeyStateRequest struct {
	ProviderName string `json:"provider_name"`
	APIKey       string `json:"api_key"`
	Enabled      *bool  `json:"enabled"`
}

type testOpenAICompatKeyRequest struct {
	ProviderName string `json:"provider_name"`
	APIKey       string `json:"api_key"`
	ProxyURL     string `json:"proxy_url"`
}

// GetOpenAICompatKeyStates 返回 OpenAI 兼容 key 的运行态。
func (h *Handler) GetOpenAICompatKeyStates(c *gin.Context) {
	service := h.currentOpenAICompatKeyState()
	if service == nil {
		c.JSON(http.StatusOK, []openai_compat_state.State{})
		return
	}
	c.JSON(http.StatusOK, service.List())
}

// PatchOpenAICompatKeyState 启用或停用单个 key。
func (h *Handler) PatchOpenAICompatKeyState(c *gin.Context) {
	service := h.currentOpenAICompatKeyState()
	if service == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "openai compat key state unavailable"})
		return
	}
	var body patchOpenAICompatKeyStateRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	st := service.SetEnabled(body.ProviderName, body.APIKey, *body.Enabled)
	c.JSON(http.StatusOK, st)
}

// GetOpenAICompatKeyStateDetail 返回单个 key 的错误和原始包详情。
func (h *Handler) GetOpenAICompatKeyStateDetail(c *gin.Context) {
	service := h.currentOpenAICompatKeyState()
	if service == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	st, ok := service.Get(c.Query("provider_name"), c.Query("api_key"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, st)
}

// TestOpenAICompatKey 在服务端测试单个 key，并使用 key 级代理 -> 全局代理 -> 直连。
func (h *Handler) TestOpenAICompatKey(c *gin.Context) {
	var body testOpenAICompatKeyRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	result := h.testOpenAICompatKey(c.Request.Context(), body)
	c.JSON(http.StatusOK, result)
}

// TestAllOpenAICompatKeys 限流批量测试所有 key。
func (h *Handler) TestAllOpenAICompatKeys(c *gin.Context) {
	cfg := h.cfg
	if cfg == nil {
		c.JSON(http.StatusOK, []gin.H{})
		return
	}
	type item struct {
		ProviderName string `json:"provider_name"`
		APIKey       string `json:"api_key"`
		ProxyURL     string `json:"proxy_url"`
	}
	var keys []item
	for i := range cfg.OpenAICompatibility {
		provider := cfg.OpenAICompatibility[i]
		for j := range provider.APIKeyEntries {
			key := provider.APIKeyEntries[j]
			keys = append(keys, item{ProviderName: provider.Name, APIKey: key.APIKey, ProxyURL: key.ProxyURL})
		}
	}
	results := make([]gin.H, len(keys))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range keys {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = h.testOpenAICompatKey(c.Request.Context(), testOpenAICompatKeyRequest(keys[i]))
		}()
	}
	wg.Wait()
	c.JSON(http.StatusOK, results)
}

func (h *Handler) testOpenAICompatKey(ctx context.Context, body testOpenAICompatKeyRequest) gin.H {
	provider := h.findOpenAICompatProvider(body.ProviderName)
	if provider == nil {
		return gin.H{"ok": false, "error": "provider not found", "provider_name": body.ProviderName}
	}
	apiKey := strings.TrimSpace(body.APIKey)
	if apiKey == "" {
		return gin.H{"ok": false, "error": "api_key required", "provider_name": body.ProviderName}
	}
	reqBody := []byte(`{"model":"` + firstOpenAICompatModel(*provider) + `","messages":[{"role":"user","content":"ping"}],"stream":false,"max_tokens":1}`)
	url := strings.TrimSuffix(provider.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return gin.H{"ok": false, "error": err.Error(), "provider_name": provider.Name, "api_key": apiKey}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "cli-proxy-openai-compat-test")
	for k, v := range provider.Headers {
		req.Header.Set(k, v)
	}
	rawRequest := openai_compat_state.BuildRawRequest(req, reqBody)
	authProxy := strings.TrimSpace(body.ProxyURL)
	client := helps.NewProxyAwareHTTPClient(ctx, h.cfg, proxyAuth(authProxy), 0)
	ctxTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctxTimeout))
	if err != nil {
		if service := h.currentOpenAICompatKeyState(); service != nil {
			service.MarkError(provider.Name, apiKey, err.Error(), rawRequest, "")
		}
		return gin.H{"ok": false, "error": err.Error(), "provider_name": provider.Name, "api_key": apiKey}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	rawResponse := openai_compat_state.BuildRawResponse(resp, b)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if service := h.currentOpenAICompatKeyState(); service != nil {
			service.ApplyRulers(*provider, apiKey, resp.StatusCode, b, rawRequest, rawResponse)
		}
		return gin.H{"ok": false, "status": resp.StatusCode, "error": string(b), "provider_name": provider.Name, "api_key": apiKey}
	}
	if service := h.currentOpenAICompatKeyState(); service != nil {
		service.SetEnabled(provider.Name, apiKey, true)
	}
	return gin.H{"ok": true, "status": resp.StatusCode, "provider_name": provider.Name, "api_key": apiKey}
}

func (h *Handler) findOpenAICompatProvider(name string) *config.OpenAICompatibility {
	cfg := h.cfg
	if cfg == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	for i := range cfg.OpenAICompatibility {
		if strings.EqualFold(cfg.OpenAICompatibility[i].Name, name) {
			return &cfg.OpenAICompatibility[i]
		}
	}
	return nil
}

func firstOpenAICompatModel(provider config.OpenAICompatibility) string {
	for _, m := range provider.Models {
		if strings.TrimSpace(m.Name) != "" {
			return strings.TrimSpace(m.Name)
		}
		if strings.TrimSpace(m.Alias) != "" {
			return strings.TrimSpace(m.Alias)
		}
	}
	return "gpt-3.5-turbo"
}

func proxyAuth(proxyURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ProxyURL: strings.TrimSpace(proxyURL)}
}
