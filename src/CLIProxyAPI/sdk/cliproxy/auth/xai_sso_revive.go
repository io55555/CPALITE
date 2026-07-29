package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	log "github.com/sirupsen/logrus"
)

const (
	packetFilterActionXAISSORevive = "xai_sso_revive"
	xaiSSOReviveStatusMessage      = "xai sso revive"
)

// exchangeSSOCookieFn is overridable in tests.
var exchangeSSOCookieFn = xaiauth.ExchangeSSOCookie

var xaiSSOReviveInflight sync.Map // authID -> struct{}

// triggerDetailUpdater 由 packetcapture 侧注入，避免 auth->packetcapture 循环依赖。
var triggerDetailUpdater func(ctx context.Context, authID, action, detail string)

// SetPacketFilterTriggerDetailUpdater registers async action result writer for trigger history.
func SetPacketFilterTriggerDetailUpdater(fn func(ctx context.Context, authID, action, detail string)) {
	triggerDetailUpdater = fn
}

func writeTriggerDetail(ctx context.Context, authID, action, detail string) {
	if triggerDetailUpdater == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	triggerDetailUpdater(ctx, authID, action, detail)
}

// IsXAISSOReviveAction reports whether the packet-filter action is the xAI SSO revive action.
func IsXAISSOReviveAction(action string) bool {
	return strings.EqualFold(strings.TrimSpace(action), packetFilterActionXAISSORevive)
}

// QueueXAISSORevive starts a non-blocking SSO cookie -> token revive for one auth file.
// If metadata has no sso/sso_cookie field, revive is skipped (auth stays as-is, typically disabled).
func (m *Manager) QueueXAISSORevive(ctx context.Context, authID, authIndex, provider, ruleName string, identities ...string) bool {
	if m == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolvedAuthID := m.resolvePacketFilterAuthID(authID, authIndex, provider, identities...)
	if resolvedAuthID == "" {
		log.Warnf("xai sso revive deferred: unresolved auth id=%q index=%q provider=%q rule=%q identities=%v", authID, authIndex, provider, ruleName, identities)
		return false
	}
	if _, loaded := xaiSSOReviveInflight.LoadOrStore(resolvedAuthID, struct{}{}); loaded {
		log.Infof("xai sso revive already in flight: auth=%s rule=%s", resolvedAuthID, strings.TrimSpace(ruleName))
		return true
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		defer xaiSSOReviveInflight.Delete(resolvedAuthID)
		result, err := m.runXAISSORevive(bg, resolvedAuthID, ruleName)
		if err != nil {
			log.Warnf("xai sso revive failed: auth=%s rule=%s err=%v", resolvedAuthID, strings.TrimSpace(ruleName), err)
			writeTriggerDetail(bg, resolvedAuthID, packetFilterActionXAISSORevive, "复活失败: "+err.Error())
			return
		}
		if strings.TrimSpace(result) == "" {
			result = "复活成功"
		}
		writeTriggerDetail(bg, resolvedAuthID, packetFilterActionXAISSORevive, result)
	}()
	return true
}

func (m *Manager) runXAISSORevive(ctx context.Context, authID, ruleName string) (string, error) {
	if m == nil || strings.TrimSpace(authID) == "" {
		return "", fmt.Errorf("nil manager or empty auth id")
	}
	auth, ok := m.GetByID(authID)
	if !ok || auth == nil {
		return "", fmt.Errorf("auth not found: %s", authID)
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "xai") &&
		!strings.Contains(strings.ToLower(strings.TrimSpace(auth.Provider)), "xai") {
		// Still allow if metadata type is xai.
		typeName := ""
		if auth.Metadata != nil {
			typeName, _ = auth.Metadata["type"].(string)
		}
		if !strings.EqualFold(strings.TrimSpace(typeName), "xai") {
			return "", fmt.Errorf("not an xai auth: provider=%s", auth.Provider)
		}
	}

	ssoCookie := xaiauth.ExtractSSOCookie(auth.Metadata)
	if ssoCookie == "" {
		log.Infof("xai sso revive skipped (no sso cookie in auth file): auth=%s file=%s rule=%s",
			authID, strings.TrimSpace(auth.FileName), strings.TrimSpace(ruleName))
		return "复活跳过: 认证文件无SSO cookie", nil
	}

	authProxy := strings.TrimSpace(auth.ProxyURL)
	if authProxy == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["proxy_url"].(string); ok {
			authProxy = strings.TrimSpace(v)
		}
		if authProxy == "" {
			if v, ok := auth.Metadata["proxy-url"].(string); ok {
				authProxy = strings.TrimSpace(v)
			}
		}
	}
	cpaProxy := ""
	if cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config); cfg != nil {
		cpaProxy = strings.TrimSpace(cfg.ProxyURL)
	}
	proxyURL := xaiauth.ResolveReviveProxyURL(authProxy, cpaProxy)

	log.Infof("xai sso revive start: auth=%s file=%s proxy=%s rule=%s",
		authID, strings.TrimSpace(auth.FileName), redactProxyForLog(proxyURL), strings.TrimSpace(ruleName))

	token, err := exchangeSSOCookieFn(ctx, ssoCookie, proxyURL, 6)
	if err != nil {
		return "", err
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return "", fmt.Errorf("empty access_token from sso exchange")
	}

	now := time.Now()
	var snapshot *Auth
	m.mu.Lock()
	current := m.auths[authID]
	if current == nil {
		m.mu.Unlock()
		return "", fmt.Errorf("auth disappeared during revive: %s", authID)
	}
	if current.Metadata == nil {
		current.Metadata = make(map[string]any)
	} else {
		// shallow copy to avoid concurrent map writes while others read
		copied := make(map[string]any, len(current.Metadata)+8)
		for k, v := range current.Metadata {
			copied[k] = v
		}
		current.Metadata = copied
	}
	current.Metadata = xaiauth.ApplyTokenDataToMetadata(current.Metadata, token, ssoCookie)
	current.Disabled = false
	current.Unavailable = false
	current.Status = StatusActive
	msg := xaiSSOReviveStatusMessage + " success"
	if strings.TrimSpace(ruleName) != "" {
		msg += ": " + strings.TrimSpace(ruleName)
	}
	current.StatusMessage = msg
	current.NextRetryAfter = time.Time{}
	current.Quota = QuotaState{}
	current.LastError = nil
	current.LastRefreshedAt = now
	current.UpdatedAt = now
	for _, state := range current.ModelStates {
		if state == nil {
			continue
		}
		state.Unavailable = false
		state.Status = StatusActive
		state.StatusMessage = ""
		state.NextRetryAfter = time.Time{}
		state.Quota = QuotaState{}
		state.LastError = nil
		state.UpdatedAt = now
	}
	updateAggregatedAvailability(current, now)
	snapshot = current.Clone()
	m.mu.Unlock()

	m.queuePersist(ctx, snapshot)
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if m.hook != nil {
		m.hook.OnAuthUpdated(ctx, snapshot)
	}
	log.Infof("xai sso revive success: auth=%s file=%s email=%s rule=%s",
		authID, strings.TrimSpace(snapshot.FileName), strings.TrimSpace(fmt.Sprint(snapshot.Metadata["email"])), strings.TrimSpace(ruleName))
	return "复活成功", nil
}

func redactProxyForLog(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "direct"
	}
	if strings.EqualFold(raw, "direct") {
		return "direct"
	}
	// avoid dumping credentials
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			return raw[:i+3] + "***@" + rest[at+1:]
		}
	}
	return raw
}
