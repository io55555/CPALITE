package packetcapture

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func nowUTC() time.Time { return time.Now().UTC() }

func normalizePacketName(value string) string {
	switch strings.TrimSpace(value) {
	case "client_request", "客户端发给CPA的完整数据包":
		return "client_request"
	case "upstream_request", "CPA发给供应商的完整数据包":
		return "upstream_request"
	case "upstream_response", "供应商返回CPA的完整数据包":
		return "upstream_response"
	case "client_response", "CPA发送给客户端的完整数据包":
		return "client_response"
	default:
		return strings.TrimSpace(value)
	}
}

func ruleMatchesMeta(rule Rule, meta Record) bool {
	if rule.Provider != "" {
		switch normalizeProviderAlias(rule.Provider) {
		case "", normalizeProviderAlias("所有渠道"):
		case normalizeProviderAlias("所有openai兼容渠道"):
			if normalizeProviderAlias(meta.ProviderGroup) != normalizeProviderAlias("openai-compatibility") &&
				normalizeProviderAlias(meta.ProviderGroup) != normalizeProviderAlias("openai") {
				return false
			}
		default:
			if normalizeProviderAlias(rule.Provider) != normalizeProviderAlias(meta.Provider) &&
				normalizeProviderAlias(rule.Provider) != normalizeProviderAlias(meta.Source) {
				return false
			}
		}
	}
	if rule.ProviderKeyword != "" {
		text := strings.ToLower(meta.Provider + "\n" + meta.Source)
		if !strings.Contains(text, strings.ToLower(rule.ProviderKeyword)) {
			return false
		}
	}
	if rule.Model != "" && !strings.EqualFold(rule.Model, meta.Model) {
		return false
	}
	if rule.ModelKeyword != "" && !strings.Contains(strings.ToLower(meta.Model), strings.ToLower(rule.ModelKeyword)) {
		return false
	}
	return true
}

func normalizeProviderAlias(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func applyRuleToPacket(rule Rule, packet string) (string, bool, string) {
	if !ruleConditionsMatch(rule, packet) {
		return packet, false, ""
	}
	next := packet
	for _, action := range ruleActions(rule) {
		if action.Packet != "" && normalizePacketName(action.Packet) != normalizePacketName(rule.Packet) {
			continue
		}
		actionRule := ruleForAction(rule, action)
		switch strings.TrimSpace(actionRule.Action) {
		case "replace", "redact", "append", "modify_status":
			next = replacePart(next, actionRule)
		case "delete":
			actionRule.Replacement = ""
			next = replacePart(next, actionRule)
		}
	}
	return next, true, ruleMatchDetail(rule)
}

func ruleConditionsMatch(rule Rule, packet string) bool {
	conditions := ruleConditions(rule)
	if len(conditions) == 0 {
		return true
	}
	anyMode := strings.EqualFold(strings.TrimSpace(rule.MatchLogic), "any")
	matched := 0
	for _, condition := range conditions {
		condRule := ruleForCondition(rule, condition)
		if condition.Packet != "" && normalizePacketName(condition.Packet) != normalizePacketName(rule.Packet) {
			if anyMode {
				continue
			}
			return false
		}
		if matchValue(selectPart(packet, condRule), condRule) {
			matched++
			if anyMode {
				return true
			}
			continue
		}
		if !anyMode {
			return false
		}
	}
	return matched > 0
}

func ruleConditions(rule Rule) []Condition {
	if len(rule.Conditions) > 0 {
		return rule.Conditions
	}
	return []Condition{{
		Packet:      rule.Packet,
		Part:        rule.Part,
		JSONPath:    rule.JSONPath,
		Header:      rule.Header,
		Operator:    rule.Operator,
		Value:       rule.Value,
		ValueNumber: rule.ValueNumber,
	}}
}

func ruleActions(rule Rule) []Action {
	if len(rule.Actions) > 0 {
		return rule.Actions
	}
	return []Action{{
		Type:            rule.Action,
		Packet:          rule.Packet,
		Part:            rule.Part,
		JSONPath:        rule.JSONPath,
		Header:          rule.Header,
		Value:           rule.Value,
		Replacement:     rule.Replacement,
		ReplaceLimit:    rule.ReplaceLimit,
		Target:          rule.Target,
		CooldownSeconds: rule.CooldownSeconds,
	}}
}

func ruleForCondition(rule Rule, condition Condition) Rule {
	next := rule
	if condition.Packet != "" {
		next.Packet = condition.Packet
	}
	next.Part = firstNonEmptyString(condition.Part, rule.Part)
	next.JSONPath = firstNonEmptyString(condition.JSONPath, rule.JSONPath)
	next.Header = firstNonEmptyString(condition.Header, rule.Header)
	next.Operator = firstNonEmptyString(condition.Operator, rule.Operator)
	next.Value = condition.Value
	next.ValueNumber = condition.ValueNumber
	return next
}

func ruleForAction(rule Rule, action Action) Rule {
	next := rule
	if action.Packet != "" {
		next.Packet = action.Packet
	}
	next.Action = firstNonEmptyString(action.Type, rule.Action)
	next.Part = firstNonEmptyString(action.Part, rule.Part)
	next.JSONPath = firstNonEmptyString(action.JSONPath, rule.JSONPath)
	next.Header = firstNonEmptyString(action.Header, rule.Header)
	if strings.TrimSpace(action.Value) != "" {
		next.Value = action.Value
	}
	next.Replacement = action.Replacement
	next.ReplaceLimit = action.ReplaceLimit
	next.Target = firstNonEmptyString(action.Target, rule.Target)
	next.CooldownSeconds = action.CooldownSeconds
	return next
}

func ruleMatchDetail(rule Rule) string {
	if len(rule.Conditions) == 0 {
		return fmt.Sprintf("packet=%s part=%s operator=%s value=%s", rule.Packet, rule.Part, rule.Operator, rule.Value)
	}
	parts := make([]string, 0, len(rule.Conditions))
	for _, condition := range rule.Conditions {
		parts = append(parts, fmt.Sprintf("packet=%s part=%s operator=%s value=%s", firstNonEmptyString(condition.Packet, rule.Packet), firstNonEmptyString(condition.Part, rule.Part), firstNonEmptyString(condition.Operator, rule.Operator), condition.Value))
	}
	return "conditions(" + strings.Join(parts, "; ") + ")"
}

func selectPart(packet string, rule Rule) string {
	part := strings.TrimSpace(rule.Part)
	switch part {
	case "path", "request_path":
		return requestPathFromPacket(packet)
	case "status", "status_code", "http_status":
		if code := statusFromPacket(packet, 0); code > 0 {
			return strconv.Itoa(code)
		}
		return ""
	case "headers", "header":
		if rule.Header != "" {
			return headerValue(packet, rule.Header)
		}
		return packetHeaders(packet)
	case "body_json":
		body := packetBody(packet)
		if rule.JSONPath != "" {
			return gjson.Get(body, rule.JSONPath).String()
		}
		return body
	case "body", "":
		return packetBody(packet)
	default:
		return packet
	}
}

func matchValue(actual string, rule Rule) bool {
	op := strings.TrimSpace(rule.Operator)
	expected := rule.Value
	switch op {
	case "equals":
		if rule.ValueNumber != 0 {
			value, err := strconv.ParseFloat(strings.TrimSpace(actual), 64)
			if err == nil && !math.IsNaN(value) && value == rule.ValueNumber {
				return true
			}
		}
		return actual == expected
	case "not_equals":
		return actual != expected
	case "starts_with":
		return strings.HasPrefix(actual, expected)
	case "ends_with":
		return strings.HasSuffix(actual, expected)
	case "not_contains":
		return !strings.Contains(actual, expected)
	case "exists":
		return strings.TrimSpace(actual) != ""
	case "not_exists":
		return strings.TrimSpace(actual) == ""
	case "wildcard":
		return wildcardMatch(actual, expected)
	case "not_wildcard":
		return !wildcardMatch(actual, expected)
	case "num_eq", "num_gt", "num_gte", "num_lt", "num_lte":
		value, err := strconv.ParseFloat(strings.TrimSpace(actual), 64)
		if err != nil || math.IsNaN(value) {
			return false
		}
		switch op {
		case "num_eq":
			return value == rule.ValueNumber
		case "num_gt":
			return value > rule.ValueNumber
		case "num_gte":
			return value >= rule.ValueNumber
		case "num_lt":
			return value < rule.ValueNumber
		case "num_lte":
			return value <= rule.ValueNumber
		}
	default:
		return strings.Contains(actual, expected)
	}
	return false
}

func wildcardMatch(actual, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	text := []rune(strings.ToLower(actual))
	glob := []rune(strings.ToLower(pattern))
	ti, pi, star, match := 0, 0, -1, 0
	for ti < len(text) {
		if pi < len(glob) && (glob[pi] == '?' || glob[pi] == text[ti]) {
			ti++
			pi++
			continue
		}
		if pi < len(glob) && glob[pi] == '*' {
			star = pi
			match = ti
			pi++
			continue
		}
		if star >= 0 {
			pi = star + 1
			match++
			ti = match
			continue
		}
		return false
	}
	for pi < len(glob) && glob[pi] == '*' {
		pi++
	}
	return pi == len(glob)
}

func replacePart(packet string, rule Rule) string {
	part := strings.TrimSpace(rule.Part)
	switch part {
	case "status", "status_code", "http_status":
		return replaceStatusLine(packet, expandReplacement(firstNonEmptyString(rule.Replacement, rule.Value), strconv.Itoa(statusFromPacket(packet, 0))))
	case "path", "request_path":
		return replaceRequestPath(packet, rule.Value, expandReplacement(rule.Replacement, rule.Value))
	case "header", "headers":
		if rule.Header == "" {
			return replaceLimited(packet, rule.Value, expandReplacement(rule.Replacement, rule.Value), rule.ReplaceLimit)
		}
		original := headerValue(packet, rule.Header)
		return replaceHeader(packet, rule.Header, expandReplacement(rule.Replacement, original))
	case "body_json":
		if rule.JSONPath == "" {
			return replaceBody(packet, replaceLimited(packetBody(packet), rule.Value, expandReplacement(rule.Replacement, rule.Value), rule.ReplaceLimit))
		}
		body := packetBody(packet)
		original := gjson.Get(body, rule.JSONPath).String()
		replacement := expandReplacement(rule.Replacement, original)
		if strings.TrimSpace(rule.Action) == "append" {
			replacement = original + replacement
		}
		var updated string
		var err error
		if json.Valid([]byte(replacement)) {
			updated, err = sjson.SetRaw(body, rule.JSONPath, replacement)
		} else {
			updated, err = sjson.Set(body, rule.JSONPath, replacement)
		}
		if err == nil {
			return replaceBody(packet, updated)
		}
		return packet
	case "body", "":
		return replaceBody(packet, replaceLimited(packetBody(packet), rule.Value, expandReplacement(rule.Replacement, rule.Value), rule.ReplaceLimit))
	default:
		return replaceLimited(packet, rule.Value, expandReplacement(rule.Replacement, rule.Value), rule.ReplaceLimit)
	}
}

func requestPathFromPacket(packet string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(packet), "\n")
	parts := strings.Fields(line)
	if len(parts) >= 2 && !strings.HasPrefix(parts[0], "HTTP/") {
		return parts[1]
	}
	return ""
}

func replaceRequestPath(packet, old, replacement string) string {
	line, rest, ok := strings.Cut(strings.TrimSpace(packet), "\n")
	parts := strings.Fields(line)
	if len(parts) < 3 || strings.HasPrefix(parts[0], "HTTP/") {
		return packet
	}
	if strings.TrimSpace(old) == "" || parts[1] == old {
		parts[1] = replacement
	} else {
		parts[1] = strings.ReplaceAll(parts[1], old, replacement)
	}
	nextLine := strings.Join(parts, " ")
	if ok {
		return nextLine + "\n" + rest
	}
	return nextLine
}

func expandReplacement(replacement, original string) string {
	out := strings.ReplaceAll(replacement, "{original}", original)
	out = strings.ReplaceAll(out, "{{original}}", original)
	for marker, values := range map[string][]string{
		"{{random_codex_ua}}": {
			"codex-cli/0.41.0 (Windows_NT 10.0.26100; x64)",
			"codex-cli/0.42.1 (Windows_NT 10.0.26100; x64)",
			"codex-cli/0.43.0 (Windows_NT 10.0.26100; x64)",
		},
		"{{random_claude_code_ua}}": {
			"claude-code/1.0.98 (Windows_NT 10.0.26100; x64)",
			"claude-code/1.0.102 (Windows_NT 10.0.26100; x64)",
			"claude-code/1.0.106 (Windows_NT 10.0.26100; x64)",
		},
		"{{random_curl_ua}}": {
			"curl/8.7.1",
			"curl/8.10.1",
			"curl/8.17.0",
		},
	} {
		if strings.Contains(out, marker) {
			out = strings.ReplaceAll(out, marker, values[rand.Intn(len(values))])
		}
	}
	return out
}

func replaceLimited(text, old, replacement string, limit int) string {
	if old == "" {
		return text
	}
	if limit <= 0 {
		return strings.ReplaceAll(text, old, replacement)
	}
	return strings.Replace(text, old, replacement, limit)
}

func packetHeaders(packet string) string {
	head, _, ok := strings.Cut(packet, "\r\n\r\n")
	if ok {
		return head
	}
	head, _, _ = strings.Cut(packet, "\n\n")
	return head
}

func packetBody(packet string) string {
	_, body, ok := strings.Cut(packet, "\r\n\r\n")
	if ok {
		return body
	}
	_, body, ok = strings.Cut(packet, "\n\n")
	if ok {
		return body
	}
	return ""
}

func PacketBody(packet string) string {
	return packetBody(packet)
}

func CleanReturnStatus(action string) int {
	switch strings.TrimSpace(action) {
	case "return_clean_400":
		return http.StatusBadRequest
	case "return_clean_401":
		return http.StatusUnauthorized
	case "return_clean_404", "return_clean_404_model_not_support":
		return http.StatusNotFound
	case "return_clean_429":
		return http.StatusTooManyRequests
	case "return_clean_500":
		return http.StatusInternalServerError
	default:
		return 0
	}
}

func CleanReturnBody(status int) string {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	errType := "invalid_request_error"
	code := ""
	switch status {
	case http.StatusUnauthorized:
		errType = "authentication_error"
		code = "invalid_api_key"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
		code = "rate_limit_exceeded"
	default:
		if status >= http.StatusInternalServerError {
			errType = "server_error"
			code = "internal_server_error"
		}
	}
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": http.StatusText(status),
			"type":    errType,
			"code":    code,
		},
	})
	if err != nil {
		return fmt.Sprintf(`{"error":{"message":%q,"type":"server_error","code":"internal_server_error"}}`, http.StatusText(status))
	}
	return string(body)
}

func CleanReturnBodyForAction(action string, status int) string {
	if strings.TrimSpace(action) == "return_clean_404_model_not_support" {
		return `{"error":"model not support"}`
	}
	return CleanReturnBody(status)
}

func replaceStatusLine(packet, replacement string) string {
	replacement = strings.TrimSpace(replacement)
	status, err := strconv.Atoi(replacement)
	if err != nil || status < 100 || status > 999 {
		return packet
	}
	line, rest, ok := strings.Cut(strings.TrimSpace(packet), "\n")
	parts := strings.Fields(line)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return packet
	}
	parts[1] = strconv.Itoa(status)
	text := http.StatusText(status)
	if text == "" {
		text = replacement
	}
	if len(parts) >= 3 {
		parts = append(parts[:2], text)
	} else {
		parts = append(parts, text)
	}
	nextLine := strings.Join(parts, " ")
	if ok {
		return nextLine + "\n" + rest
	}
	return nextLine
}

func replaceBody(packet, body string) string {
	head, _, ok := strings.Cut(packet, "\r\n\r\n")
	if ok {
		return head + "\r\n\r\n" + body
	}
	head, _, ok = strings.Cut(packet, "\n\n")
	if ok {
		return head + "\n\n" + body
	}
	return body
}

func headerValue(packet, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, line := range strings.Split(packetHeaders(packet), "\n") {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if ok && strings.ToLower(strings.TrimSpace(key)) == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func replaceHeader(packet, name, value string) string {
	headers := packetHeaders(packet)
	body := packetBody(packet)
	nameLower := strings.ToLower(strings.TrimSpace(name))
	lines := strings.Split(headers, "\n")
	found := false
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if ok && strings.ToLower(strings.TrimSpace(key)) == nameLower {
			found = true
			if value == "" {
				lines = append(lines[:i], lines[i+1:]...)
			} else {
				lines[i] = key + ": " + value
			}
			break
		}
	}
	if !found && value != "" {
		lines = append(lines, name+": "+value)
	}
	return strings.Join(lines, "\n") + "\n\n" + body
}

func extractNamedSection(content, title string) string {
	marker := "=== " + title + " ==="
	start := strings.Index(content, marker)
	if start < 0 {
		return ""
	}
	from := start + len(marker)
	next := strings.Index(content[from:], "=== ")
	if next >= 0 {
		return strings.TrimSpace(content[from : from+next])
	}
	return strings.TrimSpace(content[from:])
}

func statusFromPacket(packet string, fallback int) int {
	if fallback > 0 {
		return fallback
	}
	line, _, _ := strings.Cut(strings.TrimSpace(packet), "\n")
	parts := strings.Fields(line)
	if len(parts) >= 2 && strings.HasPrefix(parts[0], "HTTP/") {
		if code, err := strconv.Atoi(parts[1]); err == nil {
			return code
		}
	}
	return 0
}

func buildSummary(record Record) string {
	status := record.UpstreamStatusCode
	if status <= 0 && record.Failed {
		status = http.StatusBadGateway
	}
	return fmt.Sprintf("model=%s provider=%s source=%s status=%d bytes=%d", record.Model, record.Provider, record.Source, status, record.TotalBytes)
}
