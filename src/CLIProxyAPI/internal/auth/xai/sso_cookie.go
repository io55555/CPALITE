package xai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// SSO device-flow scopes match grok-manager convert path.
const ssoDeviceScopes = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"

const (
	ssoAccountsURL     = "https://accounts.x.ai/"
	ssoDefaultRetries  = 6
	ssoHTTPTimeout     = 45 * time.Second
	ssoMaxPollSeconds  = 120
)

// ExtractSSOCookie reads SSO cookie from auth-file style metadata.
// Supported keys: sso / sso_cookie / ssoCookie / SSOcookie / SSO.
// Empty means caller must skip revive.
func ExtractSSOCookie(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	for _, key := range []string{"sso", "sso_cookie", "ssoCookie", "SSOcookie", "SSO"} {
		if raw, ok := meta[key]; ok {
			if value := normalizeSSOCookieValue(raw); value != "" {
				return value
			}
		}
	}
	return ""
}

func normalizeSSOCookieValue(raw any) string {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return ""
		}
		if strings.HasPrefix(strings.ToLower(s), "sso=") {
			s = strings.TrimSpace(s[4:])
		}
		return s
	default:
		return ""
	}
}

// ResolveReviveProxyURL prioritizes auth-file proxy, then CPA proxy, then direct.
// "direct"/"none"/... force direct and skip CPA proxy.
func ResolveReviveProxyURL(authProxy, cpaProxy string) string {
	if p := normalizeReviveProxySetting(authProxy); p != "" {
		return p
	}
	if p := normalizeReviveProxySetting(cpaProxy); p != "" {
		return p
	}
	return "direct"
}

func normalizeReviveProxySetting(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	switch strings.ToLower(raw) {
	case "direct", "none", "off", "false", "0", "null", "nil", "-":
		return "direct"
	default:
		return raw
	}
}

func newSSOReviveHTTPClient(proxyURL string) *http.Client {
	proxyURL = ResolveReviveProxyURL(proxyURL, "")
	client := &http.Client{Timeout: ssoHTTPTimeout}
	transport, mode, err := proxyutil.BuildHTTPTransport(proxyURL)
	if err == nil && transport != nil {
		client.Transport = transport
		return client
	}
	if mode == proxyutil.ModeDirect || strings.EqualFold(proxyURL, "direct") {
		client.Transport = proxyutil.NewDirectTransport()
	}
	return client
}

// ExchangeSSOCookie exchanges an xAI SSO cookie for OAuth tokens.
// Implementation mirrors src/grok-manager SSO device-flow convert path.
func ExchangeSSOCookie(ctx context.Context, ssoCookie, proxyURL string, maxRetries int) (*TokenData, error) {
	ssoCookie = normalizeSSOCookieValue(ssoCookie)
	if ssoCookie == "" {
		return nil, fmt.Errorf("xai sso revive: empty sso cookie")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if maxRetries < 1 {
		maxRetries = ssoDefaultRetries
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	for _, host := range []string{ssoAccountsURL, "https://auth.x.ai/", "https://x.ai/"} {
		u, _ := url.Parse(host)
		jar.SetCookies(u, []*http.Cookie{
			{Name: "sso", Value: ssoCookie, Path: "/", Domain: "x.ai", Secure: true},
			{Name: "sso", Value: ssoCookie, Path: "/"},
		})
	}

	client := newSSOReviveHTTPClient(proxyURL)
	client.Jar = jar
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 12 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ssoAccountsURL, nil)
	if err != nil {
		return nil, err
	}
	setSSOBrowserHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accounts.x.ai: %w", err)
	}
	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	low := strings.ToLower(finalURL)
	if strings.Contains(low, "sign-in") || strings.Contains(low, "sign-up") || strings.Contains(low, "login") {
		return nil, fmt.Errorf("sso invalid (redirected to login)")
	}

	var userCode, deviceCode, verifyComplete string
	interval := 5
	expiresIn := 1800

	freshDevice := func() error {
		dc, errDevice := requestSSODeviceCode(ctx, client)
		if errDevice != nil {
			return errDevice
		}
		userCode, _ = dc["user_code"].(string)
		deviceCode, _ = dc["device_code"].(string)
		verifyComplete, _ = dc["verification_uri_complete"].(string)
		if userCode == "" || deviceCode == "" || verifyComplete == "" {
			return fmt.Errorf("device/code response incomplete")
		}
		if v, ok := dc["interval"].(float64); ok && v > 0 {
			interval = int(v)
		}
		if v, ok := dc["expires_in"].(float64); ok && v > 0 {
			expiresIn = int(v)
		}
		vreq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, verifyComplete, nil)
		if errReq != nil {
			return errReq
		}
		setSSOBrowserHeaders(vreq)
		vresp, errDo := client.Do(vreq)
		if errDo != nil {
			return fmt.Errorf("verification_uri: %w", errDo)
		}
		_, _ = io.Copy(io.Discard, vresp.Body)
		_ = vresp.Body.Close()
		return nil
	}

	if err = freshDevice(); err != nil {
		return nil, err
	}

	verifyOK := false
	approveOK := false
	rateHits := 0
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		form := url.Values{"user_code": {userCode}}
		vreq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth2/device/verify", strings.NewReader(form.Encode()))
		if errReq != nil {
			return nil, errReq
		}
		setSSOBrowserHeaders(vreq)
		vreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		vresp, errDo := client.Do(vreq)
		if errDo != nil {
			sleepSSOBackoff(ctx, attempt)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(vresp.Body, 4096))
		_ = vresp.Body.Close()
		vURL := ""
		if vresp.Request != nil && vresp.Request.URL != nil {
			vURL = vresp.Request.URL.String()
		}
		if isSSORateLimited(vURL, string(body)) {
			rateHits++
			sleepSSOBackoff(ctx, attempt)
			if err = freshDevice(); err != nil {
				return nil, err
			}
			continue
		}
		if vresp.StatusCode >= 400 && !strings.Contains(strings.ToLower(vURL+" "+string(body)), "consent") {
			return nil, fmt.Errorf("verify failed: status=%d url=%s", vresp.StatusCode, vURL)
		}
		verifyOK = true

		aform := url.Values{
			"user_code":      {userCode},
			"action":         {"allow"},
			"principal_type": {"User"},
			"principal_id":   {""},
		}
		areq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth2/device/approve", strings.NewReader(aform.Encode()))
		if errReq != nil {
			return nil, errReq
		}
		setSSOBrowserHeaders(areq)
		areq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		aresp, errDo := client.Do(areq)
		if errDo != nil {
			sleepSSOBackoff(ctx, attempt)
			continue
		}
		abody, _ := io.ReadAll(io.LimitReader(aresp.Body, 4096))
		_ = aresp.Body.Close()
		aURL := ""
		if aresp.Request != nil && aresp.Request.URL != nil {
			aURL = aresp.Request.URL.String()
		}
		if isSSORateLimited(aURL, string(abody)) {
			rateHits++
			verifyOK = false
			sleepSSOBackoff(ctx, attempt)
			if err = freshDevice(); err != nil {
				return nil, err
			}
			continue
		}
		if aresp.StatusCode >= 400 && !strings.Contains(strings.ToLower(aURL), "done") {
			return nil, fmt.Errorf("approve failed: status=%d url=%s body=%s", aresp.StatusCode, aURL, trimSSOErr(string(abody)))
		}
		approveOK = true
		break
	}
	if !verifyOK {
		if rateHits > 0 {
			return nil, fmt.Errorf("verify rate-limited retries exhausted")
		}
		return nil, fmt.Errorf("verify failed")
	}
	if !approveOK {
		if rateHits > 0 {
			return nil, fmt.Errorf("approve rate-limited retries exhausted")
		}
		return nil, fmt.Errorf("approve failed")
	}

	tok, err := pollSSODeviceToken(ctx, client, deviceCode, interval, expiresIn)
	if err != nil {
		return nil, err
	}
	if email, errInfo := fetchSSOUserinfoEmail(ctx, client, tok.AccessToken); errInfo == nil && email != "" {
		tok.Email = email
	}
	if tok.Subject == "" {
		_, sub := parseJWTIdentity(firstNonEmpty(tok.IDToken, tok.AccessToken))
		tok.Subject = sub
	}
	return tok, nil
}

func requestSSODeviceCode(ctx context.Context, client *http.Client) (map[string]any, error) {
	form := url.Values{
		"client_id": {ClientID},
		"scope":     {ssoDeviceScopes},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth2/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	setSSOBrowserHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("device/code HTTP %d: %s", resp.StatusCode, trimSSOErr(string(raw)))
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func pollSSODeviceToken(ctx context.Context, client *http.Client, deviceCode string, interval, expiresIn int) (*TokenData, error) {
	if interval < 2 {
		interval = 5
	}
	maxSeconds := expiresIn
	if maxSeconds <= 0 || maxSeconds > ssoMaxPollSeconds {
		maxSeconds = ssoMaxPollSeconds
	}
	deadline := time.Now().Add(time.Duration(maxSeconds) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		form := url.Values{
			"grant_type":  {DeviceCodeGrantType},
			"client_id":   {ClientID},
			"device_code": {deviceCode},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, Issuer+"/oauth2/token", strings.NewReader(form.Encode()))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var payload struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				IDToken      string `json:"id_token"`
				TokenType    string `json:"token_type"`
				ExpiresIn    int    `json:"expires_in"`
			}
			if err = json.Unmarshal(raw, &payload); err != nil {
				return nil, err
			}
			if strings.TrimSpace(payload.AccessToken) == "" {
				return nil, fmt.Errorf("token response missing access_token")
			}
			email, subject := parseJWTIdentity(payload.IDToken)
			if email == "" || subject == "" {
				e2, s2 := parseJWTIdentity(payload.AccessToken)
				email = firstNonEmpty(email, e2)
				subject = firstNonEmpty(subject, s2)
			}
			return buildTokenData(payload.AccessToken, payload.RefreshToken, payload.IDToken, payload.TokenType, payload.ExpiresIn, email, subject), nil
		}
		var errObj map[string]any
		_ = json.Unmarshal(raw, &errObj)
		errCode, _ := errObj["error"].(string)
		switch errCode {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5
			continue
		default:
			return nil, fmt.Errorf("token: %s %s", errCode, trimSSOErr(string(raw)))
		}
	}
	return nil, fmt.Errorf("poll token timeout")
}

func fetchSSOUserinfoEmail(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("empty token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Issuer+"/oauth2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("userinfo HTTP %d", resp.StatusCode)
	}
	var info map[string]any
	if err = json.Unmarshal(raw, &info); err != nil {
		return "", err
	}
	email, _ := info["email"].(string)
	return strings.TrimSpace(email), nil
}

func setSSOBrowserHeaders(req *http.Request) {
	if req == nil {
		return
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
}

func isSSORateLimited(u, body string) bool {
	blob := strings.ToLower(u + "\n" + body)
	return strings.Contains(blob, "rate_limited") ||
		strings.Contains(blob, "rate-limited") ||
		strings.Contains(blob, "too_many_requests") ||
		strings.Contains(blob, "ratelimit") ||
		strings.Contains(blob, "\"status\":429") ||
		strings.Contains(blob, " 429 ")
}

func sleepSSOBackoff(ctx context.Context, attempt int) {
	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 4 {
		shift = 4
	}
	d := time.Duration(10*(1<<shift)) * time.Second
	if d > 90*time.Second {
		d = 90 * time.Second
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func trimSSOErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 240 {
		return s[:240]
	}
	return s
}

// ApplyTokenDataToMetadata writes refreshed OAuth fields while preserving sso cookie.
func ApplyTokenDataToMetadata(meta map[string]any, token *TokenData, ssoCookie string) map[string]any {
	if meta == nil {
		meta = make(map[string]any)
	}
	if token == nil {
		return meta
	}
	meta["type"] = "xai"
	meta["auth_kind"] = "oauth"
	meta["access_token"] = token.AccessToken
	if token.RefreshToken != "" {
		meta["refresh_token"] = token.RefreshToken
	}
	if token.IDToken != "" {
		meta["id_token"] = token.IDToken
	}
	if token.TokenType != "" {
		meta["token_type"] = token.TokenType
	} else {
		meta["token_type"] = "Bearer"
	}
	if token.ExpiresIn > 0 {
		meta["expires_in"] = token.ExpiresIn
	}
	if token.Expire != "" {
		meta["expired"] = token.Expire
	}
	meta["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	if token.Email != "" {
		meta["email"] = token.Email
	}
	if token.Subject != "" {
		meta["sub"] = token.Subject
	}
	if _, ok := meta["base_url"]; !ok || strings.TrimSpace(fmt.Sprint(meta["base_url"])) == "" {
		meta["base_url"] = CLIChatProxyBaseURL
	}
	if _, ok := meta["token_endpoint"]; !ok || strings.TrimSpace(fmt.Sprint(meta["token_endpoint"])) == "" {
		meta["token_endpoint"] = Issuer + "/oauth2/token"
	}
	if sso := normalizeSSOCookieValue(ssoCookie); sso != "" {
		meta["sso"] = sso
	}
	meta["disabled"] = false
	return meta
}

// DecodeJWTExp helper kept for tests/diagnostics.
func DecodeJWTClaimString(token, claim string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	seg += strings.Repeat("=", (4-len(seg)%4)%4)
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		return ""
	}
	if v, ok := claims[claim].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

