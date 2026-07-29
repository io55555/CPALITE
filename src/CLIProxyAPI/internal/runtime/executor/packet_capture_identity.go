package executor

import (
	"path/filepath"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func packetCaptureAuthIdentity(auth *cliproxyauth.Auth) (string, string) {
	if auth == nil {
		return "", ""
	}
	if auth.AuthKind() == cliproxyauth.AuthKindAPIKey {
		kind, value := auth.AccountInfo()
		return kind, strings.TrimSpace(value)
	}
	// 认证文件优先完整文件名，避免把 email/Label 当成“文件名”展示。
	for _, candidate := range []string{
		strings.TrimSpace(auth.FileName),
		strings.TrimSpace(auth.ID),
	} {
		if candidate == "" {
			continue
		}
		base := filepath.Base(candidate)
		if base != "" {
			return "auth_file", base
		}
	}
	if label := strings.TrimSpace(auth.Label); label != "" && looksLikeAuthFileName(label) {
		return "auth_file", label
	}
	if index := strings.TrimSpace(auth.Index); index != "" && looksLikeAuthFileName(index) {
		return "auth_file", index
	}
	// 最后回退：仍返回可用标识，前端会把邮箱归到“账号”。
	if label := strings.TrimSpace(auth.Label); label != "" {
		return "auth_file", label
	}
	if _, email := auth.AccountInfo(); strings.TrimSpace(email) != "" {
		return "auth_file", strings.TrimSpace(email)
	}
	if index := strings.TrimSpace(auth.Index); index != "" {
		return "auth_file", index
	}
	return "auth_file", strings.TrimSpace(auth.ID)
}

func looksLikeAuthFileName(value string) bool {
	base := filepath.Base(strings.TrimSpace(value))
	if base == "" {
		return false
	}
	lower := strings.ToLower(base)
	for _, ext := range []string{".json", ".txt", ".yaml", ".yml"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
