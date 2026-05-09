package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

func logAuthStateTransition(ctx context.Context, before, after *Auth) {
	if after == nil {
		return
	}
	entry := logEntryWithRequestID(ctx).WithFields(log.Fields{
		"auth_id":  after.ID,
		"provider": after.Provider,
		"account":  authLogAccount(after),
	})
	if before == nil {
		if after.Disabled || after.Status == StatusDisabled || authCooldownSeconds(after, time.Now()) > 0 {
			entry.Infof("凭证状态初始化: %s", describeAuthRuntimeState(after, time.Now()))
		}
		return
	}
	now := time.Now()
	beforeState := describeAuthRuntimeState(before, now)
	afterState := describeAuthRuntimeState(after, now)
	if before.Disabled != after.Disabled ||
		before.Status != after.Status ||
		before.Unavailable != after.Unavailable ||
		cooldownBucket(before.NextRetryAfter, now) != cooldownBucket(after.NextRetryAfter, now) ||
		before.Quota.Exceeded != after.Quota.Exceeded ||
		cooldownBucket(before.Quota.NextRecoverAt, now) != cooldownBucket(after.Quota.NextRecoverAt, now) {
		entry.Infof("凭证状态变更: %s -> %s", beforeState, afterState)
	}
	logModelStateTransitions(entry, before, after, now)
}

func logModelStateTransitions(entry *log.Entry, before, after *Auth, now time.Time) {
	if after == nil || len(after.ModelStates) == 0 {
		return
	}
	for model, afterState := range after.ModelStates {
		if afterState == nil {
			continue
		}
		var beforeState *ModelState
		if before != nil && before.ModelStates != nil {
			beforeState = before.ModelStates[model]
		}
		if !modelStateChangedForLog(beforeState, afterState, now) {
			continue
		}
		entry.WithField("model", model).Infof(
			"模型凭证状态变更: %s -> %s",
			describeModelRuntimeState(beforeState, now),
			describeModelRuntimeState(afterState, now),
		)
	}
}

func modelStateChangedForLog(before, after *ModelState, now time.Time) bool {
	if after == nil {
		return false
	}
	if before == nil {
		return after.Status == StatusDisabled || after.Unavailable || modelCooldownSeconds(after, now) > 0
	}
	return before.Status != after.Status ||
		before.Unavailable != after.Unavailable ||
		cooldownBucket(before.NextRetryAfter, now) != cooldownBucket(after.NextRetryAfter, now) ||
		before.Quota.Exceeded != after.Quota.Exceeded ||
		cooldownBucket(before.Quota.NextRecoverAt, now) != cooldownBucket(after.Quota.NextRecoverAt, now)
}

func describeAuthRuntimeState(auth *Auth, now time.Time) string {
	if auth == nil {
		return "未知"
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return "停用"
	}
	if seconds := authCooldownSeconds(auth, now); seconds > 0 {
		return fmt.Sprintf("冷却%d秒", seconds)
	}
	if auth.Unavailable {
		return "不可用"
	}
	return "启用"
}

func describeModelRuntimeState(state *ModelState, now time.Time) string {
	if state == nil {
		return "启用"
	}
	if state.Status == StatusDisabled {
		return "停用"
	}
	if seconds := modelCooldownSeconds(state, now); seconds > 0 {
		return fmt.Sprintf("冷却%d秒", seconds)
	}
	if state.Unavailable {
		return "不可用"
	}
	return "启用"
}

func authCooldownSeconds(auth *Auth, now time.Time) int64 {
	if auth == nil {
		return 0
	}
	if auth.Quota.Exceeded && auth.Quota.NextRecoverAt.After(now) {
		return int64(time.Until(auth.Quota.NextRecoverAt).Round(time.Second) / time.Second)
	}
	if auth.NextRetryAfter.After(now) {
		return int64(time.Until(auth.NextRetryAfter).Round(time.Second) / time.Second)
	}
	return 0
}

func modelCooldownSeconds(state *ModelState, now time.Time) int64 {
	if state == nil {
		return 0
	}
	if state.Quota.Exceeded && state.Quota.NextRecoverAt.After(now) {
		return int64(time.Until(state.Quota.NextRecoverAt).Round(time.Second) / time.Second)
	}
	if state.NextRetryAfter.After(now) {
		return int64(time.Until(state.NextRetryAfter).Round(time.Second) / time.Second)
	}
	return 0
}

func cooldownBucket(t, now time.Time) int64 {
	if !t.After(now) {
		return 0
	}
	return int64(t.Sub(now).Round(time.Second) / time.Second)
}

func authLogAccount(auth *Auth) string {
	if auth == nil {
		return ""
	}
	kind, value := auth.AccountInfo()
	kind = strings.TrimSpace(kind)
	value = strings.TrimSpace(value)
	if value == "" {
		return kind
	}
	if strings.EqualFold(kind, "api_key") {
		return "api_key:" + value
	}
	return kind + ":" + value
}
