package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func (h *Handler) PatchOpenAICompatRuntimeState(c *gin.Context) {
	var body struct {
		AuthIndex *string `json:"auth-index"`
		Disabled  *bool   `json:"disabled"`
		Action    string  `json:"action"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.AuthIndex == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	auth := h.findAuthByIndex(*body.AuthIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	if !isOpenAICompatAuth(auth) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is not openai-compatibility"})
		return
	}

	now := time.Now()
	if body.Disabled != nil {
		if *body.Disabled {
			auth.Disabled = true
			auth.Unavailable = true
			auth.Status = coreauth.StatusDisabled
			auth.StatusMessage = "manual_disable"
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.Disabled = false
			auth.Unavailable = false
			auth.Status = coreauth.StatusActive
			auth.StatusMessage = ""
			auth.NextRetryAfter = time.Time{}
			auth.LastError = nil
		}
	} else if action := strings.ToLower(strings.TrimSpace(body.Action)); action != "" {
		switch {
		case action == "enable":
			auth.Disabled = false
			auth.Unavailable = false
			auth.Status = coreauth.StatusActive
			auth.StatusMessage = ""
			auth.NextRetryAfter = time.Time{}
			auth.LastError = nil
		case action == "disable":
			auth.Disabled = true
			auth.Unavailable = true
			auth.Status = coreauth.StatusDisabled
			auth.StatusMessage = "manual_disable"
			auth.NextRetryAfter = time.Time{}
		case action == "freeze":
			auth.Disabled = false
			auth.Unavailable = true
			auth.Status = coreauth.StatusError
			auth.StatusMessage = "manual_freeze"
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported action"})
			return
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing disabled or action"})
		return
	}

	auth.UpdatedAt = now
	if _, err := h.authManager.Update(c.Request.Context(), auth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := h.persistOpenAICompatRuntimeState(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) findAuthByIndex(raw string) *coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	want := strings.TrimSpace(raw)
	if want == "" {
		return nil
	}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if idx := strings.TrimSpace(auth.EnsureIndex()); idx == want {
			return auth
		}
	}
	return nil
}

func isOpenAICompatAuth(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return true
	}
	return auth.Attributes != nil && strings.TrimSpace(auth.Attributes["compat_name"]) != ""
}
