package openai_compat_state

import (
	"path/filepath"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyToAuthReenablesRuntimeAuth(t *testing.T) {
	service, err := New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()

	auth := &cliproxyauth.Auth{
		ID:             "auth-1",
		Provider:       "groq",
		Disabled:       true,
		Unavailable:    true,
		Status:         cliproxyauth.StatusDisabled,
		StatusMessage:  "disabled by rule",
		NextRetryAfter: time.Now().Add(time.Hour),
		Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(time.Hour), BackoffLevel: 1},
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "key-1",
		},
		ModelStates: map[string]*cliproxyauth.ModelState{
			"llama-3.1-8b-instant": {
				Status:         cliproxyauth.StatusError,
				StatusMessage:  "disabled by rule",
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(time.Hour),
				Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(time.Hour), BackoffLevel: 1},
			},
		},
	}

	service.SetEnabled("groq", "key-1", true)
	service.ApplyToAuth(auth)

	if auth.Disabled || auth.Unavailable || auth.Status != cliproxyauth.StatusActive || !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth not reenabled: disabled=%v unavailable=%v status=%s next=%v", auth.Disabled, auth.Unavailable, auth.Status, auth.NextRetryAfter)
	}
	state := auth.ModelStates["llama-3.1-8b-instant"]
	if state == nil {
		t.Fatal("model state missing")
	}
	if state.Unavailable || state.Status != cliproxyauth.StatusActive || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded {
		t.Fatalf("model state not cleared: unavailable=%v status=%s next=%v quota=%+v", state.Unavailable, state.Status, state.NextRetryAfter, state.Quota)
	}
}

func TestApplyToAuthDoesNotClearModelCooldownOnPlainErrorState(t *testing.T) {
	service, err := New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()
	next := time.Now().Add(10 * time.Minute)
	auth := &cliproxyauth.Auth{
		ID:         "auth-1",
		Provider:   "pool",
		Attributes: map[string]string{"compat_name": "pool", "api_key": "key-1"},
		ModelStates: map[string]*cliproxyauth.ModelState{
			"gpt-5": {
				Status:         cliproxyauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				LastError:      &cliproxyauth.Error{HTTPStatus: 401, Message: "unauthorized"},
			},
		},
	}
	service.MarkErrorForModel("pool", "key-1", "gpt-5", "unauthorized", "", "")

	service.ApplyToAuth(auth)

	state := auth.ModelStates["gpt-5"]
	if state == nil || !state.Unavailable || !state.NextRetryAfter.Equal(next) {
		t.Fatalf("model cooldown was cleared: %#v", state)
	}
	if auth.Disabled {
		t.Fatalf("auth.Disabled = true, want false")
	}
}

func TestApplyToAuthClearsStaleRuntimeStateWithoutPersistedState(t *testing.T) {
	service, err := New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()

	auth := &cliproxyauth.Auth{
		ID:             "auth-1",
		Provider:       "groq",
		Unavailable:    true,
		Status:         cliproxyauth.StatusError,
		StatusMessage:  "stale cooldown",
		NextRetryAfter: time.Now().Add(time.Hour),
		Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(time.Hour), BackoffLevel: 1},
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "key-1",
		},
		ModelStates: map[string]*cliproxyauth.ModelState{
			"llama-3.1-8b-instant": {
				Status:         cliproxyauth.StatusError,
				StatusMessage:  "stale cooldown",
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(time.Hour),
				Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(time.Hour), BackoffLevel: 1},
			},
		},
	}

	service.ApplyToAuth(auth)

	if auth.Unavailable || auth.Status != cliproxyauth.StatusActive || !auth.NextRetryAfter.IsZero() || auth.Quota.Exceeded {
		t.Fatalf("auth stale state not cleared: unavailable=%v status=%s next=%v quota=%+v", auth.Unavailable, auth.Status, auth.NextRetryAfter, auth.Quota)
	}
	state := auth.ModelStates["llama-3.1-8b-instant"]
	if state == nil || state.Unavailable || state.Status != cliproxyauth.StatusActive || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded {
		t.Fatalf("model stale state not cleared: state=%+v", state)
	}
}

func TestApplyToAuthIgnoresNonOpenAICompatAuth(t *testing.T) {
	service, err := New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()

	until := time.Now().Add(24 * time.Hour)
	auth := &cliproxyauth.Auth{
		ID:             "xai-20260722-003202-h.tt.p.s.a.p.p@googlemail.com.json",
		Provider:       "xai",
		Unavailable:    true,
		Status:         cliproxyauth.StatusError,
		StatusMessage:  "packet filter matched: [运营商到CPA]xai响应码429冷却24h",
		NextRetryAfter: until,
		Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: until},
		Attributes: map[string]string{
			"path":   ".cli-proxy-api/xai-20260722-003202-h.tt.p.s.a.p.p@googlemail.com.json",
			"source": "file",
		},
		ModelStates: map[string]*cliproxyauth.ModelState{
			"grok-4.5-build-free": {
				Status:         cliproxyauth.StatusError,
				StatusMessage:  "packet filter matched: [运营商到CPA]xai响应码429冷却24h",
				Unavailable:    true,
				NextRetryAfter: until,
				Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: until},
			},
		},
	}

	service.ApplyToAuth(auth)

	if !auth.Unavailable || auth.Status != cliproxyauth.StatusError || !auth.NextRetryAfter.Equal(until) {
		t.Fatalf("xai cooldown wiped by ApplyToAuth: unavailable=%v status=%s next=%v msg=%q", auth.Unavailable, auth.Status, auth.NextRetryAfter, auth.StatusMessage)
	}
	state := auth.ModelStates["grok-4.5-build-free"]
	if state == nil || !state.Unavailable || !state.NextRetryAfter.Equal(until) {
		t.Fatalf("xai model cooldown wiped: %+v", state)
	}
}
