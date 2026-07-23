package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type packetFilterTestGin struct {
	values map[string]any
}

func (g packetFilterTestGin) Get(key string) (any, bool) {
	value, ok := g.values[key]
	return value, ok
}

type packetFilterCarrierExecutor struct{}

func (packetFilterCarrierExecutor) Identifier() string { return "codex" }

func (packetFilterCarrierExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	PublishPacketFilterAction(ctx, "cooldown", "api_key", 86400, "codex 429 cooldown", auth.ID)
	return cliproxyexecutor.Response{}, &Error{Code: requestScopedErrorCode, Message: "request scoped"}
}

func (packetFilterCarrierExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{Code: "not_implemented", Message: "not implemented"}
}

func (packetFilterCarrierExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (packetFilterCarrierExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "not implemented"}
}

func (packetFilterCarrierExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "not implemented"}
}

func TestManager_Update_PreservesModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "test-model"
	backoffLevel := 7

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v"},
		ModelStates: map[string]*ModelState{
			model: {
				Quota: QuotaState{BackoffLevel: backoffLevel},
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v2"},
	}); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected ModelStates to be preserved")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}

func TestApplyPacketFilterActionStateDisablesMatchingAuth(t *testing.T) {
	ginCtx := packetFilterTestGin{values: map[string]any{
		packetFilterActionContextKey: "disable",
		packetFilterTargetContextKey: "api_key",
		packetFilterAuthIDContextKey: "auth-1",
		packetFilterRuleContextKey:   "gemini permission denied",
	}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	auth := &Auth{ID: "auth-1", Provider: "gemini"}

	applyPacketFilterActionState(ctx, auth, "auth-1", "gemini-3-pro-preview", time.Now())

	if !auth.Disabled || auth.Status != StatusDisabled {
		t.Fatalf("auth state = disabled:%v status:%s, want disabled", auth.Disabled, auth.Status)
	}
	state := auth.ModelStates["gemini-3-pro-preview"]
	if state == nil || state.Status != StatusDisabled {
		t.Fatalf("model state = %+v, want disabled", state)
	}
}

func TestApplyPacketFilterActionStateIgnoresOtherAuth(t *testing.T) {
	ginCtx := packetFilterTestGin{values: map[string]any{
		packetFilterActionContextKey: "disable",
		packetFilterTargetContextKey: "api_key",
		packetFilterAuthIDContextKey: "auth-1",
	}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	auth := &Auth{ID: "auth-2", Provider: "gemini"}

	applyPacketFilterActionState(ctx, auth, "auth-2", "gemini-3-pro-preview", time.Now())

	if auth.Disabled || auth.Status == StatusDisabled {
		t.Fatalf("auth state = disabled:%v status:%s, want unchanged", auth.Disabled, auth.Status)
	}
}

func TestApplyPacketFilterActionStateMatchesAuthIndex(t *testing.T) {
	ctx := contextWithPacketFilterActionState(context.Background())
	PublishPacketFilterAction(ctx, "cooldown", "api_key", 86400, "xai 429 cooldown", "xai-index-1")
	auth := &Auth{ID: "auth-1", Index: "xai-index-1", Provider: "xai"}

	applyPacketFilterActionState(ctx, auth, "auth-1", "grok-4", time.Now())

	if !auth.Unavailable || time.Until(auth.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected auth 24h cooldown by index, got unavailable=%v next=%v", auth.Unavailable, auth.NextRetryAfter)
	}
	state := auth.ModelStates["grok-4"]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected model 24h cooldown by index, got %+v", state)
	}
}

func TestApplyPacketFilterActionStateMatchesAuthAccount(t *testing.T) {
	ctx := contextWithPacketFilterActionState(context.Background())
	PublishPacketFilterAction(ctx, "cooldown", "api_key", 86400, "xai 429 cooldown", "js.js.7178.3@googlemail.com")
	auth := &Auth{
		ID:       "xai-20260721-235814-js.js.7178.3@googlemail.com.json",
		Index:    "b4863f65e3b5ac47",
		FileName: "xai-20260721-235814-js.js.7178.3@googlemail.com.json",
		Label:    "js.js.7178.3@googlemail.com",
		Provider: "xai",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindOAuth,
		},
		Metadata: map[string]any{
			"email": "js.js.7178.3@googlemail.com",
		},
	}

	applyPacketFilterActionState(ctx, auth, auth.ID, "grok-4.5-build-free", time.Now())

	if !auth.Unavailable || time.Until(auth.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected auth 24h cooldown by account, got unavailable=%v next=%v", auth.Unavailable, auth.NextRetryAfter)
	}
	state := auth.ModelStates["grok-4.5-build-free"]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected model 24h cooldown by account, got %+v", state)
	}
}

func TestManagerMarkResult_AppliesPacketFilterCooldownForRequestScopedError(t *testing.T) {
	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ginCtx := packetFilterTestGin{values: map[string]any{
		packetFilterActionContextKey:          "cooldown",
		packetFilterTargetContextKey:          "api_key",
		packetFilterAuthIDContextKey:          "auth-1",
		packetFilterRuleContextKey:            "codex 429 cooldown",
		packetFilterCooldownSecondsContextKey: 180,
	}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	m.MarkResult(ctx, Result{
		AuthID:   "auth-1",
		Provider: "codex",
		Model:    "gpt-5-codex",
		Success:  false,
		Error:    &Error{Code: requestScopedErrorCode, Message: "request scoped"},
	})

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if !updated.Unavailable || !updated.NextRetryAfter.After(time.Now()) {
		t.Fatalf("expected auth cooldown, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	state := updated.ModelStates["gpt-5-codex"]
	if state == nil || !state.Unavailable || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("expected model cooldown, got %+v", state)
	}
}

func TestManagerMarkResult_RequestScoped429UsesProviderCooldownConfig(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		CodexQuotaCooldownBaseSeconds: 86400,
		CodexQuotaCooldownMaxSeconds:  604800,
		XAIQuotaCooldownBaseSeconds:   86400,
		XAIQuotaCooldownMaxSeconds:    86400,
	})
	for _, auth := range []*Auth{
		{ID: "codex-auth", Provider: "codex", Status: StatusActive},
		{ID: "xai-auth", Provider: "xai", Status: StatusActive},
	} {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}

	for _, tc := range []struct {
		authID string
		model  string
	}{
		{authID: "codex-auth", model: "gpt-5-codex"},
		{authID: "xai-auth", model: "grok-4"},
	} {
		m.MarkResult(context.Background(), Result{
			AuthID:   tc.authID,
			Provider: tc.authID[:len(tc.authID)-5],
			Model:    tc.model,
			Success:  false,
			Error:    &Error{Code: requestScopedErrorCode, Message: "429 wrapped as request scoped", HTTPStatus: http.StatusTooManyRequests},
		})
		updated, ok := m.GetByID(tc.authID)
		if !ok || updated == nil {
			t.Fatalf("expected auth %s to be present", tc.authID)
		}
		if !updated.Unavailable || updated.NextRetryAfter.Before(now.Add(23*time.Hour)) {
			t.Fatalf("%s cooldown = unavailable:%v next:%v, want about 24h", tc.authID, updated.Unavailable, updated.NextRetryAfter)
		}
		state := updated.ModelStates[tc.model]
		if state == nil || !state.Unavailable || state.NextRetryAfter.Before(now.Add(23*time.Hour)) {
			t.Fatalf("%s model state = %+v, want about 24h cooldown", tc.authID, state)
		}
	}
}

func TestManagerMarkResult_RetryAfterAppliesWhenStatusMissing(t *testing.T) {
	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "xai-auth",
		Provider: "xai",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	retryAfter := 24 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID:     "xai-auth",
		Provider:   "xai",
		Model:      "grok-4.5-build-free",
		Success:    false,
		RetryAfter: &retryAfter,
		Error:      &Error{Code: requestScopedErrorCode, Message: "429 wrapped without status"},
	})

	updated, ok := m.GetByID("xai-auth")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if !updated.Unavailable || time.Until(updated.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected auth 24h cooldown from retryAfter, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	state := updated.ModelStates["grok-4.5-build-free"]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected model 24h cooldown from retryAfter, got %+v", state)
	}
}

func TestManagerMarkResult_AppliesPacketFilterCooldownFromContextCarrier(t *testing.T) {
	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ctx := contextWithPacketFilterActionState(context.Background())
	PublishPacketFilterAction(ctx, "cooldown", "api_key", 86400, "codex 429 cooldown", "auth-1")
	m.MarkResult(ctx, Result{
		AuthID:   "auth-1",
		Provider: "codex",
		Model:    "gpt-5-codex",
		Success:  false,
		Error:    &Error{Code: requestScopedErrorCode, Message: "request scoped"},
	})

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if !updated.Unavailable || time.Until(updated.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected auth 24h cooldown, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	state := updated.ModelStates["gpt-5-codex"]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected model 24h cooldown, got %+v", state)
	}
}

func TestManagerExecute_AppliesPacketFilterCooldownWithoutGinContext(t *testing.T) {
	registerSchedulerModels(t, "codex", "gpt-5-codex", "auth-1")
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(packetFilterCarrierExecutor{})
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected execution error")
	}

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if !updated.Unavailable || time.Until(updated.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected auth 24h cooldown, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	state := updated.ModelStates["gpt-5-codex"]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected model 24h cooldown, got %+v", state)
	}
}

func TestManagerApplyPacketFilterAction_ResolvesAuthByIndex(t *testing.T) {
	m := NewManager(nil, nil, nil)
	registered, err := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Index:    "xai-auth-file-1",
		Provider: "xai",
		Status:   StatusActive,
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}
	if registered.Index != "xai-auth-file-1" {
		t.Fatalf("registered index = %q, want xai-auth-file-1", registered.Index)
	}

	ok := m.ApplyPacketFilterAction(context.Background(), "", "xai-auth-file-1", "xai", "grok-4", "cooldown", "api_key", 86400, "xai 429 cooldown")
	if !ok {
		t.Fatal("expected packet filter action to apply")
	}

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if !updated.Unavailable || time.Until(updated.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected auth 24h cooldown, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	state := updated.ModelStates["grok-4"]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected model 24h cooldown, got %+v", state)
	}
}

func TestManagerApplyPacketFilterAction_ResolvesAuthByAccountIdentity(t *testing.T) {
	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "xai-20260722-002254-oxjit-fonh2u@icloud.com.json",
		Index:    "110968f2f64ea783",
		FileName: "xai-20260722-002254-oxjit-fonh2u@icloud.com.json",
		Label:    "oxjit+fonh2u@icloud.com",
		Provider: "xai",
		Status:   StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"email": "oxjit+fonh2u@icloud.com",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ok := m.ApplyPacketFilterAction(context.Background(), "", "", "xai", "grok-4.5-build-free", "cooldown", "api_key", 86400, "xai 429 cooldown", "oxjit+fonh2u@icloud.com")
	if !ok {
		t.Fatal("expected packet filter action to resolve by account email")
	}

	updated, ok := m.GetByID("xai-20260722-002254-oxjit-fonh2u@icloud.com.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if !updated.Unavailable || time.Until(updated.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected auth 24h cooldown, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
	state := updated.ModelStates["grok-4.5-build-free"]
	if state == nil || !state.Unavailable || time.Until(state.NextRetryAfter) < 23*time.Hour {
		t.Fatalf("expected model 24h cooldown, got %+v", state)
	}
}

func TestManager_Update_DisabledExistingDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with existing ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 5},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — should NOT inherit stale states.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-disabled")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled auth NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveToDisabledDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register an active auth with ModelStates (simulates existing live auth).
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 9},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// File watcher deletes config → synthesizes Disabled=true auth → Update.
	// Even though existing is active, incoming auth is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-a2d")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected active→disabled transition NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_DisabledToActiveDoesNotInheritStaleModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with stale ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 4},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Re-enable: incoming auth is active, existing is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-d2a")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled→active transition NOT to inherit stale ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveInheritsModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "active-model"
	backoffLevel := 3

	// Register an active auth with ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Quota: QuotaState{BackoffLevel: backoffLevel},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — both sides active → SHOULD inherit.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-active")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected active auth to inherit ModelStates")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}
