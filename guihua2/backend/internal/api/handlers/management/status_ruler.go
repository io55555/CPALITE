package management

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/statusruler"
)

func (h *Handler) ListStatusRules(c *gin.Context) {
	store := statusruler.DefaultStore()
	if store == nil {
		c.JSON(http.StatusOK, gin.H{"items": []statusruler.Rule{}})
		return
	}
	items, err := store.ListRules(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) PutStatusRule(c *gin.Context) {
	store := statusruler.DefaultStore()
	if store == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "status-ruler store unavailable"})
		return
	}
	var body statusruler.Rule
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	item, err := store.UpsertRule(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": item})
}

func (h *Handler) DeleteStatusRule(c *gin.Context) {
	store := statusruler.DefaultStore()
	if store == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := store.DeleteRule(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) ListStatusRuleHits(c *gin.Context) {
	store := statusruler.DefaultStore()
	if store == nil {
		c.JSON(http.StatusOK, gin.H{"items": []statusruler.Hit{}})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	items, err := store.ListHits(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
