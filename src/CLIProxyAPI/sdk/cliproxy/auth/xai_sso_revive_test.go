package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestExtractAndQueueXAISSORevive_SkipWithoutSSO(t *testing.T) {
	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "xai-1",
		Provider: "xai",
		Metadata: map[string]any{"type": "xai", "access_token": "old"},
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	var called atomic.Int32
	old := exchangeSSOCookieFn
	exchangeSSOCookieFn = func(context.Context, string, string, int) (*xaiauth.TokenData, error) {
		called.Add(1)
		return &xaiauth.TokenData{AccessToken: "new"}, nil
	}
	t.Cleanup(func() { exchangeSSOCookieFn = old })

	if !m.QueueXAISSORevive(context.Background(), "xai-1", "", "xai", "test-rule") {
		t.Fatal("expected queue true")
	}
	// wait briefly for goroutine
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if called.Load() == 0 {
			auth, _ := m.GetByID("xai-1")
			if auth != nil && auth.Disabled {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if called.Load() != 0 {
		t.Fatalf("exchange should not be called without sso, called=%d", called.Load())
	}
	auth, ok := m.GetByID("xai-1")
	if !ok || auth == nil || !auth.Disabled {
		t.Fatalf("auth should remain disabled, got %#v", auth)
	}
}

func TestQueueXAISSORevive_SuccessReenables(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{SDKConfig: internalconfig.SDKConfig{ProxyURL: "http://cpa-proxy:8080"}})
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "xai-2",
		Provider: "xai",
		ProxyURL: "socks5://auth-proxy:1080",
		Metadata: map[string]any{"type": "xai", "access_token": "old", "sso": "cookie-1"},
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	var gotProxy string
	old := exchangeSSOCookieFn
	exchangeSSOCookieFn = func(_ context.Context, sso, proxy string, _ int) (*xaiauth.TokenData, error) {
		if sso != "cookie-1" {
			t.Fatalf("sso=%q", sso)
		}
		gotProxy = proxy
		return &xaiauth.TokenData{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			Email:        "u@x.ai",
			Subject:      "sub",
			ExpiresIn:    3600,
		}, nil
	}
	t.Cleanup(func() { exchangeSSOCookieFn = old })

	if !m.ApplyPacketFilterAction(context.Background(), "xai-2", "", "xai", "", packetFilterActionXAISSORevive, "auth", 0, "rule-xai") {
		t.Fatal("ApplyPacketFilterAction should accept xai_sso_revive")
	}
	deadline := time.Now().Add(3 * time.Second)
	var auth *Auth
	for time.Now().Before(deadline) {
		a, ok := m.GetByID("xai-2")
		if ok && a != nil && !a.Disabled && a.Status == StatusActive {
			auth = a
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if auth == nil {
		t.Fatal("auth not re-enabled in time")
	}
	if gotProxy != "socks5://auth-proxy:1080" {
		t.Fatalf("proxy priority want auth proxy, got %q", gotProxy)
	}
	if auth.Metadata["access_token"] != "new-access" {
		t.Fatalf("access_token=%v", auth.Metadata["access_token"])
	}
	if auth.Metadata["sso"] != "cookie-1" {
		t.Fatalf("sso not preserved: %v", auth.Metadata["sso"])
	}
}

func TestResolveReviveProxyFallbackToCPA(t *testing.T) {
	if got := xaiauth.ResolveReviveProxyURL("", "http://cpa:9"); got != "http://cpa:9" {
		t.Fatalf("got %q", got)
	}
}
