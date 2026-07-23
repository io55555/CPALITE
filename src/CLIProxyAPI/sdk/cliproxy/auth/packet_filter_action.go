package auth

import (
	"context"
	"strings"
	"sync"
)

// packetFilterApplier 由 API Server 注入，让 executor 在规则命中时直接改 Manager 状态
// （不依赖 gin context / MarkResult 时序）。
var (
	packetFilterApplierMu sync.RWMutex
	packetFilterApplier   func(ctx context.Context, authID, authIndex, provider, model, action, target string, seconds int, ruleName string, identities ...string) bool
)

// SetPacketFilterApplier registers the runtime applier used by executors.
func SetPacketFilterApplier(fn func(ctx context.Context, authID, authIndex, provider, model, action, target string, seconds int, ruleName string, identities ...string) bool) {
	packetFilterApplierMu.Lock()
	packetFilterApplier = fn
	packetFilterApplierMu.Unlock()
}

// ApplyPacketFilterActionNow applies a packet-filter action immediately via the registered manager.
func ApplyPacketFilterActionNow(ctx context.Context, authID, authIndex, provider, model, action, target string, seconds int, ruleName string, identities ...string) bool {
	packetFilterApplierMu.RLock()
	fn := packetFilterApplier
	packetFilterApplierMu.RUnlock()
	if fn == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return fn(ctx, authID, authIndex, provider, model, action, target, seconds, ruleName, identities...)
}

type packetFilterActionState struct {
	action  string
	target  string
	authID  string
	rule    string
	seconds int
}

type packetFilterActionStateContextKey struct{}

func contextWithPacketFilterActionState(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, packetFilterActionStateContextKey{}, &packetFilterActionState{})
}

// PublishPacketFilterAction stores an executor packet-filter action so MarkResult
// can apply it to the selected auth even when no gin context is available.
func PublishPacketFilterAction(ctx context.Context, action, target string, seconds int, ruleName, authID string) {
	action = strings.TrimSpace(action)
	target = strings.TrimSpace(target)
	if action == "" || target == "" {
		return
	}
	if state, _ := ctx.Value(packetFilterActionStateContextKey{}).(*packetFilterActionState); state != nil {
		state.action = action
		state.target = target
		state.seconds = seconds
		state.rule = strings.TrimSpace(ruleName)
		state.authID = strings.TrimSpace(authID)
	}
	if ginCtx, _ := ctx.Value("gin").(interface{ Set(string, any) }); ginCtx != nil {
		ginCtx.Set(packetFilterActionContextKey, action)
		ginCtx.Set(packetFilterTargetContextKey, target)
		ginCtx.Set(packetFilterCooldownSecondsContextKey, seconds)
		ginCtx.Set(packetFilterRuleContextKey, strings.TrimSpace(ruleName))
		ginCtx.Set(packetFilterAuthIDContextKey, strings.TrimSpace(authID))
	}
}

func packetFilterActionStateFromContext(ctx context.Context) (action, target, authID string, seconds int, ruleName string) {
	state, _ := ctx.Value(packetFilterActionStateContextKey{}).(*packetFilterActionState)
	if state == nil {
		return "", "", "", 0, ""
	}
	return strings.TrimSpace(state.action), strings.TrimSpace(state.target), strings.TrimSpace(state.authID), state.seconds, strings.TrimSpace(state.rule)
}
