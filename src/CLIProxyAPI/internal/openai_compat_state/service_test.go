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
