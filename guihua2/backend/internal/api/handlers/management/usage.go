package management

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

type usageExportPayload struct {
	Version    int                      `json:"version"`
	ExportedAt time.Time                `json:"exported_at"`
	Usage      usage.StatisticsSnapshot `json:"usage"`
}

type usageImportPayload struct {
	Version int                      `json:"version"`
	Usage   usage.StatisticsSnapshot `json:"usage"`
}

// GetUsageStatistics returns the in-memory request statistics snapshot.
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	queryRange, err := parseUsageQueryRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	snapshot, err := usage.SnapshotRange(c.Request.Context(), queryRange)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load usage statistics"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":           snapshot,
		"failed_requests": snapshot.FailureCount,
	})
}

func parseUsageQueryRange(c *gin.Context) (usage.QueryRange, error) {
	var out usage.QueryRange
	if c == nil {
		return out, nil
	}
	parse := func(name string) (*time.Time, error) {
		raw := c.Query(name)
		if raw == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, err
		}
		utc := parsed.UTC()
		return &utc, nil
	}
	start, err := parse("start")
	if err != nil {
		return out, err
	}
	end, err := parse("end")
	if err != nil {
		return out, err
	}
	out.Start = start
	out.End = end
	return out, nil
}

// ExportUsageStatistics returns a complete usage snapshot for backup/migration.
func (h *Handler) ExportUsageStatistics(c *gin.Context) {
	snapshot, err := usage.SnapshotRange(c.Request.Context(), usage.QueryRange{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to export usage statistics"})
		return
	}
	c.JSON(http.StatusOK, usageExportPayload{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	})
}

// ImportUsageStatistics merges a previously exported usage snapshot into memory.
func (h *Handler) ImportUsageStatistics(c *gin.Context) {
	if h == nil || (h.usageStats == nil && usage.DefaultStore() == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage statistics unavailable"})
		return
	}

	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload usageImportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	if payload.Version != 0 && payload.Version != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version"})
		return
	}

	result, err := usage.ImportSnapshot(c.Request.Context(), payload.Usage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to import usage statistics"})
		return
	}
	snapshot, err := usage.SnapshotRange(c.Request.Context(), usage.QueryRange{})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to refresh usage statistics"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"added":           result.Added,
		"skipped":         result.Skipped,
		"total_requests":  snapshot.TotalRequests,
		"failed_requests": snapshot.FailureCount,
	})
}
