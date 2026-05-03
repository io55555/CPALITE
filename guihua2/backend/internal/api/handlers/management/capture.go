package management

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/capture"
)

func (h *Handler) GetCaptureSettings(c *gin.Context) {
	store := capture.DefaultStore()
	if store == nil {
		c.JSON(http.StatusOK, gin.H{"settings": capture.Settings{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": store.Settings()})
}

func (h *Handler) PutCaptureSettings(c *gin.Context) {
	store := capture.DefaultStore()
	if store == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "capture store unavailable"})
		return
	}
	var body capture.Settings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	settings, err := store.UpdateSettings(c.Request.Context(), body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

func (h *Handler) ListCaptures(c *gin.Context) {
	store := capture.DefaultStore()
	if store == nil {
		c.JSON(http.StatusOK, gin.H{"items": []capture.Record{}})
		return
	}
	failedOnly := c.Query("failed_only") == "1" || c.Query("failed_only") == "true"
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	items, err := store.List(c.Request.Context(), capture.ListFilter{
		Query:      c.Query("q"),
		FailedOnly: failedOnly,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) GetCapture(c *gin.Context) {
	store := capture.DefaultStore()
	if store == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "capture not found"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	item, err := store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if item == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "capture not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": item})
}

func (h *Handler) ClearCaptures(c *gin.Context) {
	store := capture.DefaultStore()
	if store == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if err := store.Clear(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) ExportCaptures(c *gin.Context) {
	store := capture.DefaultStore()
	if store == nil {
		c.String(http.StatusOK, "")
		return
	}
	failedOnly := c.Query("failed_only") == "1" || c.Query("failed_only") == "true"
	raw, err := store.Export(c.Request.Context(), capture.ListFilter{
		Query:      c.Query("q"),
		FailedOnly: failedOnly,
		Limit:      200,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="captures.txt"`)
	c.String(http.StatusOK, raw)
}
