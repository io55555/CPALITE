package management

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/packetcapture"
)

type packetDeleteRequest struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

type packetRulesExport struct {
	ExportedAt time.Time            `json:"exported_at"`
	Rules      []packetcapture.Rule `json:"rules"`
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
	c.JSON(http.StatusOK, h.packetFilterRules())
}

func (h *Handler) PutPacketFilterRule(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	var rule packetcapture.Rule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	incomingCreatedAtZero := rule.CreatedAt.IsZero()
	saved := normalizePacketRule(rule)
	h.mu.Lock()
	rules := configRulesToPacketRules(h.cfg.PacketCapture.FilterRules)
	replaced := false
	for i := range rules {
		if strings.TrimSpace(rules[i].ID) == saved.ID {
			if incomingCreatedAtZero {
				saved.CreatedAt = rules[i].CreatedAt
			}
			rules[i] = saved
			replaced = true
			break
		}
	}
	if !replaced {
		rules = append(rules, saved)
	}
	sortPacketRules(rules)
	h.cfg.PacketCapture.FilterRules = packetRulesToConfigRules(rules)
	if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); err != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save rule"})
		return
	}
	h.mu.Unlock()
	c.JSON(http.StatusOK, saved)
}

func (h *Handler) DeletePacketFilterRule(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	h.mu.Lock()
	rules := configRulesToPacketRules(h.cfg.PacketCapture.FilterRules)
	filtered := rules[:0]
	for _, rule := range rules {
		if strings.TrimSpace(rule.ID) != id {
			filtered = append(filtered, rule)
		}
	}
	h.cfg.PacketCapture.FilterRules = packetRulesToConfigRules(filtered)
	if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); err != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete rule"})
		return
	}
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) ExportPacketFilterRules(c *gin.Context) {
	data, err := json.MarshalIndent(packetRulesExport{
		ExportedAt: time.Now().UTC(),
		Rules:      h.packetFilterRules(),
	}, "", "  ")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to export rules"})
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="packet-filter-rules.json"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func (h *Handler) ImportPacketFilterRules(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	data, err := readPacketRulesImportBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid import file"})
		return
	}
	var payload packetRulesExport
	if err := json.Unmarshal(data, &payload); err != nil || len(payload.Rules) == 0 {
		var rules []packetcapture.Rule
		if errRules := json.Unmarshal(data, &rules); errRules != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid import file"})
			return
		}
		payload.Rules = rules
	}
	rules := make([]packetcapture.Rule, 0, len(payload.Rules))
	for _, rule := range payload.Rules {
		rules = append(rules, normalizePacketRule(rule))
	}
	sortPacketRules(rules)
	h.mu.Lock()
	h.cfg.PacketCapture.FilterRules = packetRulesToConfigRules(rules)
	if err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg); err != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to import rules"})
		return
	}
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"imported": len(rules)})
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

func (h *Handler) packetFilterRules() []packetcapture.Rule {
	if h == nil || h.cfg == nil {
		return []packetcapture.Rule{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rules := configRulesToPacketRules(h.cfg.PacketCapture.FilterRules)
	sortPacketRules(rules)
	return rules
}

func readPacketRulesImportBody(c *gin.Context) ([]byte, error) {
	if file, err := c.FormFile("file"); err == nil && file != nil {
		f, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, 2*1024*1024))
	}
	return io.ReadAll(io.LimitReader(c.Request.Body, 2*1024*1024))
}

func normalizePacketRule(rule packetcapture.Rule) packetcapture.Rule {
	now := time.Now().UTC()
	if strings.TrimSpace(rule.ID) == "" {
		rule.ID = uuid.NewString()
	}
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	rule.UpdatedAt = now
	if strings.TrimSpace(rule.Name) == "" {
		rule.Name = "未命名规则"
	}
	if strings.TrimSpace(rule.Packet) == "" {
		rule.Packet = "client_request"
	}
	if strings.TrimSpace(rule.Part) == "" {
		rule.Part = "body"
	}
	if strings.TrimSpace(rule.Operator) == "" {
		rule.Operator = "contains"
	}
	if strings.TrimSpace(rule.Action) == "" {
		rule.Action = "record"
	}
	return rule
}

func sortPacketRules(rules []packetcapture.Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].UpdatedAt.After(rules[j].UpdatedAt)
	})
}

func configRulesToPacketRules(in []config.PacketFilterRule) []packetcapture.Rule {
	out := make([]packetcapture.Rule, 0, len(in))
	for _, rule := range in {
		out = append(out, packetcapture.Rule{
			ID:              rule.ID,
			Name:            rule.Name,
			Enabled:         rule.Enabled,
			RecordHistory:   rule.RecordHistory,
			Priority:        rule.Priority,
			Provider:        rule.Provider,
			ProviderKeyword: rule.ProviderKeyword,
			Model:           rule.Model,
			ModelKeyword:    rule.ModelKeyword,
			Packet:          rule.Packet,
			Part:            rule.Part,
			JSONPath:        rule.JSONPath,
			Header:          rule.Header,
			Operator:        rule.Operator,
			Value:           rule.Value,
			ValueNumber:     rule.ValueNumber,
			Action:          rule.Action,
			Replacement:     rule.Replacement,
			ReplaceLimit:    rule.ReplaceLimit,
			CooldownSeconds: rule.CooldownSeconds,
			Target:          rule.Target,
			Notes:           rule.Notes,
			CreatedAt:       rule.CreatedAt,
			UpdatedAt:       rule.UpdatedAt,
		})
	}
	return out
}

func packetRulesToConfigRules(in []packetcapture.Rule) []config.PacketFilterRule {
	out := make([]config.PacketFilterRule, 0, len(in))
	for _, rule := range in {
		out = append(out, config.PacketFilterRule{
			ID:              rule.ID,
			Name:            rule.Name,
			Enabled:         rule.Enabled,
			RecordHistory:   rule.RecordHistory,
			Priority:        rule.Priority,
			Provider:        rule.Provider,
			ProviderKeyword: rule.ProviderKeyword,
			Model:           rule.Model,
			ModelKeyword:    rule.ModelKeyword,
			Packet:          rule.Packet,
			Part:            rule.Part,
			JSONPath:        rule.JSONPath,
			Header:          rule.Header,
			Operator:        rule.Operator,
			Value:           rule.Value,
			ValueNumber:     rule.ValueNumber,
			Action:          rule.Action,
			Replacement:     rule.Replacement,
			ReplaceLimit:    rule.ReplaceLimit,
			CooldownSeconds: rule.CooldownSeconds,
			Target:          rule.Target,
			Notes:           rule.Notes,
			CreatedAt:       rule.CreatedAt,
			UpdatedAt:       rule.UpdatedAt,
		})
	}
	return out
}
