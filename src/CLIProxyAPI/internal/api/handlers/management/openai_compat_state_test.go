package management

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/openai_compat_state"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestMergeOpenAICompatRuntimeKeyStates_IncludesModelCooldown(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	next := time.Now().Add(5 * time.Minute)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "groq-key",
		Provider: "mixed",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "key-1",
		},
		ModelStates: map[string]*coreauth.ModelState{
			"llama-3.1-8b-instant": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				StatusMessage:  "quota exhausted",
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	states := (&Handler{authManager: manager}).mergeOpenAICompatRuntimeKeyStates(nil)
	if len(states) != 1 {
		t.Fatalf("states len = %d, want 1", len(states))
	}
	if states[0].Status != openai_compat_state.StatusFrozen {
		t.Fatalf("status = %q, want frozen", states[0].Status)
	}
	if states[0].FrozenUntil.IsZero() || states[0].FrozenUntil.Sub(next) > time.Second {
		t.Fatalf("frozen_until = %v, want about %v", states[0].FrozenUntil, next)
	}
}

func TestMergeOpenAICompatRuntimeKeyStates_RuntimeOverridesPersistedActive(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "groq-key",
		Provider: "mixed",
		Status:   coreauth.StatusDisabled,
		Disabled: true,
		Attributes: map[string]string{
			"compat_name":  "groq",
			"provider_key": "groq",
			"api_key":      "key-1",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	states := (&Handler{authManager: manager}).mergeOpenAICompatRuntimeKeyStates([]openai_compat_state.State{{
		ProviderName: "groq",
		APIKey:       "key-1",
		Enabled:      true,
		Status:       openai_compat_state.StatusActive,
	}})
	if len(states) != 1 {
		t.Fatalf("states len = %d, want 1", len(states))
	}
	if states[0].Enabled || states[0].Status != openai_compat_state.StatusDisabled {
		t.Fatalf("state = enabled:%v status:%q, want disabled", states[0].Enabled, states[0].Status)
	}
}
