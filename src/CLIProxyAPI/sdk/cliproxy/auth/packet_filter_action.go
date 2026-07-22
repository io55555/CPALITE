package auth

import (
	"context"
	"strings"
)

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
