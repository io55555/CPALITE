package auth

import (
	"strings"
	"sync"
	"time"
)

type pendingPacketCooldown struct {
	action    string
	model     string
	ruleName  string
	seconds   int
	provider  string
	authID    string
	authIndex string
	idents    []string
	until     time.Time
}

var pendingPacketCooldowns sync.Map // key string -> pendingPacketCooldown

func pendingPacketKey(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = normalizePacketFilterIdentity(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "|")
}

func registerPendingPacketCooldown(authID, authIndex, provider, model, action string, seconds int, ruleName string, identities ...string) {
	action = strings.TrimSpace(action)
	if action != "cooldown" && action != "disable" {
		return
	}
	if seconds <= 0 {
		seconds = 300
	}
	item := pendingPacketCooldown{
		action:    action,
		model:     strings.TrimSpace(model),
		ruleName:  strings.TrimSpace(ruleName),
		seconds:   seconds,
		provider:  strings.TrimSpace(provider),
		authID:    strings.TrimSpace(authID),
		authIndex: strings.TrimSpace(authIndex),
		idents:    append([]string{}, identities...),
		until:     time.Now().Add(time.Duration(seconds) * time.Second),
	}
	keys := []string{
		pendingPacketKey("id", item.authID),
		pendingPacketKey("index", item.authIndex),
	}
	for _, identity := range item.idents {
		// id/ident 双写，兼容按文件名/邮箱/账号解析
		keys = append(keys, pendingPacketKey("id", identity), pendingPacketKey("ident", identity))
	}
	for _, key := range keys {
		if key == "" || strings.HasSuffix(key, "|") {
			continue
		}
		pendingPacketCooldowns.Store(key, item)
	}
}

func clearPendingPacketCooldownForAuth(auth *Auth) {
	if auth == nil {
		return
	}
	keys := []string{
		pendingPacketKey("id", auth.ID),
		pendingPacketKey("id", auth.FileName),
		pendingPacketKey("index", auth.Index),
	}
	if _, account := auth.AccountInfo(); account != "" {
		keys = append(keys, pendingPacketKey("ident", account))
	}
	for _, key := range keys {
		pendingPacketCooldowns.Delete(key)
	}
}

func consumePendingPacketCooldown(auth *Auth) (pendingPacketCooldown, bool) {
	if auth == nil {
		return pendingPacketCooldown{}, false
	}
	candidates := []string{
		pendingPacketKey("id", auth.ID),
		pendingPacketKey("id", auth.FileName),
		pendingPacketKey("id", auth.Label),
		pendingPacketKey("index", auth.Index),
		pendingPacketKey("ident", auth.ID),
		pendingPacketKey("ident", auth.FileName),
		pendingPacketKey("ident", auth.Label),
	}
	if _, account := auth.AccountInfo(); account != "" {
		candidates = append(candidates, pendingPacketKey("id", account), pendingPacketKey("ident", account))
	}
	now := time.Now()
	for _, key := range candidates {
		if key == "" {
			continue
		}
		value, ok := pendingPacketCooldowns.Load(key)
		if !ok {
			continue
		}
		item, ok := value.(pendingPacketCooldown)
		if !ok {
			pendingPacketCooldowns.Delete(key)
			continue
		}
		if !item.until.After(now) && item.action == "cooldown" {
			pendingPacketCooldowns.Delete(key)
			continue
		}
		// Keep disable pending until applied.
		clearPendingPacketCooldownForAuth(auth)
		return item, true
	}
	return pendingPacketCooldown{}, false
}
