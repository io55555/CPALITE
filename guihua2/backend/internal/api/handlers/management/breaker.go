package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/breaker"
)

func (h *Handler) GetBreakerState(c *gin.Context) {
	manager := breaker.DefaultManager()
	if manager == nil {
		c.JSON(http.StatusOK, gin.H{"items": []breaker.State{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": manager.Snapshot()})
}

func (h *Handler) ResetBreakerState(c *gin.Context) {
	manager := breaker.DefaultManager()
	if manager == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	var body struct {
		Scope string `json:"scope"`
		Key   string `json:"key"`
	}
	_ = c.ShouldBindJSON(&body)
	manager.Reset(body.Scope, body.Key)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
