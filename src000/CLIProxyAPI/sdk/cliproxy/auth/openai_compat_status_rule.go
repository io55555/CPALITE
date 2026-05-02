package auth

import (
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

type openAICompatRuleAction struct {
	disable bool
	freeze  time.Duration
	label   string
}

func matchOpenAICompatStatusRule(cfg *internalconfig.Config, auth *Auth, statusCode int, body string) (openAICompatRuleAction, bool) {
	if cfg == nil || auth == nil {
		return openAICompatRuleAction{}, false
	}
	entry := resolveOpenAICompatConfig(cfg, authCompatProviderKey(auth), authCompatName(auth), auth.Provider)
	if entry == nil || len(entry.StatusRules) == 0 {
		return openAICompatRuleAction{}, false
	}

	bodyTrimmed := strings.TrimSpace(body)
	bodyBytes := []byte(bodyTrimmed)
	for i := range entry.StatusRules {
		rule := entry.StatusRules[i]
		if rule.Status > 0 && rule.Status != statusCode {
			continue
		}
		if !matchOpenAICompatRuleBody(rule, bodyTrimmed, bodyBytes) {
			continue
		}
		action := parseOpenAICompatRuleAction(rule.Action)
		if !action.disable && action.freeze <= 0 {
			continue
		}
		action.label = strings.TrimSpace(rule.Name)
		if action.label == "" {
			action.label = strings.TrimSpace(rule.Action)
		}
		return action, true
	}
	return openAICompatRuleAction{}, false
}

func matchOpenAICompatRuleBody(rule internalconfig.OpenAICompatStatusRule, body string, bodyBytes []byte) bool {
	if v := strings.TrimSpace(rule.BodyEquals); v != "" && body != v {
		return false
	}
	if v := strings.TrimSpace(rule.BodyContains); v != "" && !strings.Contains(body, v) {
		return false
	}

	jsonPath := strings.TrimSpace(rule.JSONPath)
	jsonEquals := strings.TrimSpace(rule.JSONEquals)
	jsonContains := strings.TrimSpace(rule.JSONContains)
	if v := strings.TrimSpace(rule.JSONCode); v != "" {
		if jsonPath == "" {
			jsonPath = "error.code"
		}
		if jsonEquals == "" {
			jsonEquals = v
		}
	}
	if jsonPath == "" {
		return true
	}

	result := gjson.GetBytes(bodyBytes, jsonPath)
	if !result.Exists() {
		return false
	}
	value := strings.TrimSpace(result.String())
	if jsonEquals != "" && value != jsonEquals {
		return false
	}
	if jsonContains != "" && !strings.Contains(value, jsonContains) {
		return false
	}
	return true
}

func parseOpenAICompatRuleAction(raw string) openAICompatRuleAction {
	action := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case action == "disable":
		return openAICompatRuleAction{disable: true}
	case action == "freeze":
		return openAICompatRuleAction{freeze: 30 * time.Minute}
	case strings.HasPrefix(action, "freeze-"):
		if dur := parseFreezeDuration(strings.TrimPrefix(action, "freeze-")); dur > 0 {
			return openAICompatRuleAction{freeze: dur}
		}
	}
	return openAICompatRuleAction{}
}

func parseFreezeDuration(raw string) time.Duration {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return 0
	}
	if dur, err := time.ParseDuration(trimmed); err == nil && dur > 0 {
		return dur
	}
	switch {
	case strings.HasSuffix(trimmed, "d"):
		if dur, err := time.ParseDuration(strings.TrimSuffix(trimmed, "d") + "h"); err == nil && dur > 0 {
			return dur * 24
		}
	}
	return 0
}

func authCompatProviderKey(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["provider_key"])
}

func authCompatName(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["compat_name"])
}
