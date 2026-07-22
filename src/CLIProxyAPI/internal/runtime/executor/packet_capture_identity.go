package executor

import (
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
	value := strings.TrimSpace(auth.FileName)
	if value == "" {
		value = strings.TrimSpace(auth.Label)
	}
	if value == "" {
		_, value = auth.AccountInfo()
	}
	if value == "" {
		value = strings.TrimSpace(auth.Index)
	}
	if value == "" {
		value = strings.TrimSpace(auth.ID)
	}
	return "auth_file", value
}
