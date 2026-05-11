package management

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/packetcapture"
)

type packetDeleteRequest struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

func (h *Handler) packetStore() *packetcapture.Store {
	store := packetcapture.DefaultStore()
	if store != nil {
		return store
	}
	if h == nil || strings.TrimSpace(h.logDir) == "" {
		return nil
	}
	if err := packetcapture.InitDefaultInLogDir(h.logDir); err != nil {
		return nil
	}
	return packetcapture.DefaultStore()
}

func (h *Handler) GetPacketCaptureState(c *gin.Context) {
	store := h.packetStore()
	enabled := false
	if store != nil {
		enabled = store.Enabled(c.Request.Context())
	}
	c.JSON(http.StatusOK, gin.H{"enabled": enabled})
}

func (h *Handler) PutPacketCaptureState(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "packet capture store unavailable"})
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if err := store.SetEnabled(c.Request.Context(), body.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update packet capture state"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": body.Enabled})
}

func (h *Handler) ListPacketCaptures(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusOK, []packetcapture.RecordSummary{})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))
	items, err := store.Query(c.Request.Context(), packetcapture.QueryOptions{
		Limit:     limit,
		Model:     c.Query("model"),
		Source:    c.Query("source"),
		Result:    c.Query("result"),
		Provider:  c.Query("provider"),
		RequestID: c.Query("request_id"),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query packet captures"})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (h *Handler) GetPacketCapture(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "packet capture store unavailable"})
		return
	}
	record, ok, err := store.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load packet capture"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "packet capture not found"})
		return
	}
	c.JSON(http.StatusOK, record)
}

func (h *Handler) DeletePacketCaptures(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "packet capture store unavailable"})
		return
	}
	var body packetDeleteRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	var result packetcapture.DeleteResult
	var err error
	if body.All {
		result, err = store.DeleteAll(c.Request.Context())
	} else {
		result, err = store.Delete(c.Request.Context(), body.IDs)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete packet captures"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) ListPacketFilterRules(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusOK, []packetcapture.Rule{})
		return
	}
	rules, err := store.ListRules(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query rules"})
		return
	}
	c.JSON(http.StatusOK, rules)
}

func (h *Handler) PutPacketFilterRule(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "packet capture store unavailable"})
		return
	}
	var rule packetcapture.Rule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	saved, err := store.UpsertRule(ctx, rule)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save rule"})
		return
	}
	c.JSON(http.StatusOK, saved)
}

func (h *Handler) DeletePacketFilterRule(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "packet capture store unavailable"})
		return
	}
	if err := store.DeleteRule(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete rule"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) ListPacketFilterTriggers(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusOK, []packetcapture.TriggerRecord{})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	items, err := store.ListTriggers(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query trigger records"})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (h *Handler) DeletePacketFilterTriggers(c *gin.Context) {
	store := h.packetStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "packet capture store unavailable"})
		return
	}
	var body packetDeleteRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	var result packetcapture.DeleteResult
	var err error
	if body.All {
		result, err = store.DeleteAllTriggers(c.Request.Context())
	} else {
		result, err = store.DeleteTriggers(c.Request.Context(), body.IDs)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete trigger records"})
		return
	}
	c.JSON(http.StatusOK, result)
}
