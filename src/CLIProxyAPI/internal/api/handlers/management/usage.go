package management

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

const (
	usageQueryTimeout  = 10 * time.Second
	usageQueryMaxRows  = 50000
	usageQueryCacheTTL = 2 * time.Second
	usageSSEPoll       = 5 * time.Second
	usageSSEMaxIdle    = 10 * time.Minute
)

var usageQueryCache = newUsageQueryCache()

type usageQueryCacheEntry struct {
	result    usage.APIUsage
	expiresAt time.Time
	ready     chan struct{}
	loading   bool
	err       error
}

type usageQueryCacheStore struct {
	mu      sync.Mutex
	entries map[string]*usageQueryCacheEntry
}

func newUsageQueryCache() *usageQueryCacheStore {
	return &usageQueryCacheStore{entries: make(map[string]*usageQueryCacheEntry)}
}

type deleteUsageRequest struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

type modelPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
	Cache      float64 `json:"cache"`
}

type modelPricesRequest struct {
	Prices map[string]modelPrice `json:"prices"`
}

type usageSettings struct {
	RetentionDays int `json:"retentionDays"`
	WebDAV        struct {
		Enabled         bool   `json:"enabled"`
		IntervalMinutes int    `json:"intervalMinutes"`
		RetentionDays   int    `json:"retentionDays"`
		URL             string `json:"url"`
		Username        string `json:"username"`
		Password        string `json:"password"`
	} `json:"webdav"`
}

type usageImportResult struct {
	Added       int `json:"added"`
	Skipped     int `json:"skipped"`
	Total       int `json:"total"`
	Failed      int `json:"failed"`
	Unsupported int `json:"unsupported"`
}

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetFwindyUsage keeps Fwindy's /usage frontend API.
func (h *Handler) GetFwindyUsage(c *gin.Context) {
	h.getUsageSnapshot(c, 0, usageQueryMaxRows)
}

// DeleteFwindyUsage keeps Fwindy's /usage deletion API.
func (h *Handler) DeleteFwindyUsage(c *gin.Context) {
	h.DeleteUsageRecords(c)
}

// GetUsageStatistics 返回已持久化的请求统计。
func (h *Handler) GetUsageStatistics(c *gin.Context) {
	rng, ok := parseUsageRange(c)
	if !ok {
		return
	}

	store := h.currentUsageStore()
	if store == nil {
		store = h.ensureUsageStoreForMonitoring()
	}
	if store == nil {
		c.JSON(http.StatusOK, usage.APIUsage{})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), usageQueryTimeout)
	defer cancel()
	result, err := usageQueryCache.query(ctx, store, rng)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query usage"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) GetUsageEvents(c *gin.Context) {
	afterID := parseNonNegativeQueryInt(c.Query("after_id"), 0)
	limit := parsePositiveQueryInt(c.Query("limit"), 5000, usageQueryMaxRows)
	h.getUsageSnapshot(c, afterID, limit)
}

func (h *Handler) StreamUsage(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	afterID := parseNonNegativeQueryInt(c.Query("after_id"), 0)
	if payload, ok := h.buildUsageSnapshotForRequest(c, afterID, usageQueryMaxRows); ok {
		writeUsageSSE(c, payload)
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(usageSSEPoll)
	defer ticker.Stop()
	idle := time.NewTimer(usageSSEMaxIdle)
	defer idle.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-idle.C:
			return
		case <-ticker.C:
			payload, ok := h.buildUsageSnapshotForRequest(c, afterID, usageQueryMaxRows)
			if !ok {
				return
			}
			if latest := usagePayloadLatestID(payload); latest > afterID {
				writeUsageSSE(c, payload)
				afterID = latest
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(usageSSEMaxIdle)
			} else {
				if _, err := c.Writer.Write([]byte(": keepalive\n\n")); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) getUsageSnapshot(c *gin.Context, afterID int, limit int) {
	payload, ok := h.buildUsageSnapshotForRequest(c, afterID, limit)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (h *Handler) buildUsageSnapshotForRequest(c *gin.Context, afterID int, limit int) (gin.H, bool) {
	rng, ok := parseUsageRange(c)
	if !ok {
		return nil, false
	}
	rng.Limit = usageQueryMaxRows
	store := h.currentUsageStore()
	if store == nil {
		store = h.ensureUsageStoreForMonitoring()
	}
	if store == nil {
		return buildUsagePayload(usage.APIUsage{}, afterID, limit), true
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), usageQueryTimeout)
	defer cancel()
	result, err := usageQueryCache.query(ctx, store, rng)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query usage"})
		return nil, false
	}
	return buildUsagePayload(result, afterID, limit), true
}

func buildUsagePayload(records usage.APIUsage, afterID int, limit int) gin.H {
	all := flattenUsageDetails(records)
	totalCount := len(all)
	if afterID < 0 {
		afterID = 0
	}
	if afterID > totalCount {
		afterID = totalCount
	}
	if limit <= 0 || limit > usageQueryMaxRows {
		limit = usageQueryMaxRows
	}
	end := afterID + limit
	if end > totalCount {
		end = totalCount
	}
	return buildUsagePayloadFromDetails(all[afterID:end], totalCount)
}

type usageDetailWithKeys struct {
	Endpoint string
	Model    string
	Detail   usage.RequestDetail
}

func flattenUsageDetails(records usage.APIUsage) []usageDetailWithKeys {
	details := make([]usageDetailWithKeys, 0)
	for endpoint, models := range records {
		for model, entries := range models {
			for _, detail := range entries {
				details = append(details, usageDetailWithKeys{
					Endpoint: endpoint,
					Model:    model,
					Detail:   detail,
				})
			}
		}
	}
	sort.SliceStable(details, func(i, j int) bool {
		left, right := details[i].Detail, details[j].Detail
		if !left.Timestamp.Equal(right.Timestamp) {
			return left.Timestamp.Before(right.Timestamp)
		}
		return left.ID < right.ID
	})
	return details
}

func buildUsagePayloadFromDetails(details []usageDetailWithKeys, latestID int) gin.H {
	apis := gin.H{}
	totalRequests, successCount, failureCount, totalTokens := 0, 0, 0, int64(0)
	for _, item := range details {
		endpoint := strings.TrimSpace(item.Endpoint)
		if endpoint == "" {
			endpoint = "unknown"
		}
		model := strings.TrimSpace(item.Model)
		if model == "" {
			model = "unknown"
		}
		apiEntry, _ := apis[endpoint].(gin.H)
		if apiEntry == nil {
			apiEntry = gin.H{
				"total_requests": 0,
				"success_count":  0,
				"failure_count":  0,
				"total_tokens":   int64(0),
				"models":         gin.H{},
			}
			apis[endpoint] = apiEntry
		}
		models, _ := apiEntry["models"].(gin.H)
		modelEntry, _ := models[model].(gin.H)
		if modelEntry == nil {
			modelEntry = gin.H{
				"total_requests": 0,
				"success_count":  0,
				"failure_count":  0,
				"total_tokens":   int64(0),
				"details":        []usage.RequestDetail{},
			}
			models[model] = modelEntry
		}

		tokens := item.Detail.Tokens.TotalTokens
		if tokens == 0 {
			tokens = item.Detail.Tokens.InputTokens + item.Detail.Tokens.OutputTokens + item.Detail.Tokens.ReasoningTokens + item.Detail.Tokens.CachedTokens
		}
		failed := item.Detail.Failed
		totalRequests++
		if failed {
			failureCount++
		} else {
			successCount++
		}
		totalTokens += tokens
		incrementUsageCounters(apiEntry, failed, tokens)
		incrementUsageCounters(modelEntry, failed, tokens)
		modelEntry["details"] = append(modelEntry["details"].([]usage.RequestDetail), item.Detail)
	}
	return gin.H{
		"total_requests": totalRequests,
		"success_count":  successCount,
		"failure_count":  failureCount,
		"total_tokens":   totalTokens,
		"latest_id":      latestID,
		"apis":           apis,
	}
}

func incrementUsageCounters(entry gin.H, failed bool, tokens int64) {
	entry["total_requests"] = entry["total_requests"].(int) + 1
	if failed {
		entry["failure_count"] = entry["failure_count"].(int) + 1
	} else {
		entry["success_count"] = entry["success_count"].(int) + 1
	}
	entry["total_tokens"] = entry["total_tokens"].(int64) + tokens
}

func usagePayloadLatestID(payload gin.H) int {
	value, _ := payload["latest_id"].(int)
	return value
}

func writeUsageSSE(c *gin.Context, payload gin.H) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = c.Writer.Write([]byte("event: usage\n"))
	_, _ = c.Writer.Write([]byte("data: "))
	_, _ = c.Writer.Write(data)
	_, _ = c.Writer.Write([]byte("\n\n"))
}

func (h *Handler) GetUsageModelPrices(c *gin.Context) {
	prices, err := h.loadUsageModelPrices()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model prices"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

func (h *Handler) PutUsageModelPrices(c *gin.Context) {
	var body modelPricesRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	prices := normalizeModelPrices(body.Prices)
	if err := h.saveUsageModelPrices(prices); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save model prices"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

func (h *Handler) GetUsageSettings(c *gin.Context) {
	settings, err := h.loadUsageSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load usage settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

func (h *Handler) PutUsageSettings(c *gin.Context) {
	var body struct {
		Settings usageSettings `json:"settings"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	settings := normalizeUsageSettings(body.Settings)
	if err := h.saveUsageSettings(settings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save usage settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

func (h *Handler) ExportUsage(c *gin.Context) {
	payload, ok := h.buildUsageSnapshotForRequest(c, 0, usageQueryMaxRows)
	if !ok {
		return
	}
	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="usage-events.jsonl"`)
	encoder := json.NewEncoder(c.Writer)
	for _, item := range flattenPayloadDetails(payload) {
		_ = encoder.Encode(item)
	}
}

func (h *Handler) ImportUsage(c *gin.Context) {
	store := h.currentUsageStore()
	if store == nil {
		store = h.ensureUsageStoreForMonitoring()
	}
	if store == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage store unavailable"})
		return
	}

	var result usageImportResult
	scanner := bufio.NewScanner(c.Request.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		result.Total++
		record, ok := decodeUsageImportLine([]byte(line))
		if !ok {
			result.Unsupported++
			result.Failed++
			continue
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), usageQueryTimeout)
		err := store.Insert(ctx, record)
		cancel()
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "constraint") {
				result.Skipped++
				continue
			}
			result.Failed++
			continue
		}
		result.Added++
	}
	if err := scanner.Err(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read usage import"})
		return
	}
	usageQueryCache = newUsageQueryCache()
	c.JSON(http.StatusOK, result)
}

func decodeUsageImportLine(line []byte) (usage.Record, bool) {
	var item usageDetailWithKeys
	if err := json.Unmarshal(line, &item); err != nil {
		var detail usage.RequestDetail
		if err := json.Unmarshal(line, &detail); err != nil {
			return usage.Record{}, false
		}
		item.Detail = detail
		item.Endpoint = detail.Endpoint
		item.Model = "unknown"
	}
	detail := item.Detail
	if strings.TrimSpace(detail.ID) == "" {
		return usage.Record{}, false
	}
	timestamp := detail.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	endpoint := strings.TrimSpace(detail.Endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(item.Endpoint)
	}
	provider := strings.TrimSpace(detail.Provider)
	return usage.Record{
		ID:                 strings.TrimSpace(detail.ID),
		Timestamp:          timestamp.UTC(),
		Provider:           provider,
		Model:              strings.TrimSpace(item.Model),
		Source:             strings.TrimSpace(detail.Source),
		AuthIndex:          strings.TrimSpace(detail.AuthIndex),
		AuthType:           strings.TrimSpace(detail.AuthType),
		Endpoint:           endpoint,
		RequestID:          strings.TrimSpace(detail.RequestID),
		LatencyMs:          detail.LatencyMs,
		FirstByteLatencyMs: detail.FirstByteLatencyMs,
		GenerationMs:       detail.GenerationMs,
		ThinkingEffort:     strings.TrimSpace(detail.ThinkingEffort),
		ClientUA:           strings.TrimSpace(detail.ClientUA),
		UpstreamUA:         strings.TrimSpace(detail.UpstreamUA),
		RawRequest:         detail.RawRequest,
		RawResponse:        detail.RawResponse,
		FailureStatusCode:  detail.FailureStatusCode,
		FailureMessage:     strings.TrimSpace(detail.FailureMessage),
		Tokens:             detail.Tokens,
		Failed:             detail.Failed,
	}, true
}

func flattenPayloadDetails(payload gin.H) []usageDetailWithKeys {
	apis, _ := payload["apis"].(gin.H)
	details := make([]usageDetailWithKeys, 0)
	for endpoint, apiRaw := range apis {
		apiEntry, _ := apiRaw.(gin.H)
		models, _ := apiEntry["models"].(gin.H)
		for model, modelRaw := range models {
			modelEntry, _ := modelRaw.(gin.H)
			switch values := modelEntry["details"].(type) {
			case []usage.RequestDetail:
				for _, detail := range values {
					details = append(details, usageDetailWithKeys{Endpoint: endpoint, Model: model, Detail: detail})
				}
			case []any:
				for _, raw := range values {
					if detail, ok := raw.(usage.RequestDetail); ok {
						details = append(details, usageDetailWithKeys{Endpoint: endpoint, Model: model, Detail: detail})
					}
				}
			}
		}
	}
	return details
}

func (h *Handler) usageDataPath(name string) string {
	dir := strings.TrimSpace(h.logDir)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, name)
}

func (h *Handler) loadUsageModelPrices() (map[string]modelPrice, error) {
	path := h.usageDataPath("usage_model_prices.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]modelPrice{}, nil
		}
		return nil, err
	}
	var body modelPricesRequest
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	return normalizeModelPrices(body.Prices), nil
}

func (h *Handler) saveUsageModelPrices(prices map[string]modelPrice) error {
	path := h.usageDataPath("usage_model_prices.json")
	return writeUsageJSONFile(path, modelPricesRequest{Prices: normalizeModelPrices(prices)})
}

func normalizeModelPrices(input map[string]modelPrice) map[string]modelPrice {
	result := make(map[string]modelPrice, len(input))
	for model, price := range input {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if price.Prompt < 0 || price.Completion < 0 || price.Cache < 0 {
			continue
		}
		result[model] = price
	}
	return result
}

func (h *Handler) loadUsageSettings() (usageSettings, error) {
	path := h.usageDataPath("usage_settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return normalizeUsageSettings(usageSettings{}), nil
		}
		return usageSettings{}, err
	}
	var body struct {
		Settings usageSettings `json:"settings"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return usageSettings{}, err
	}
	return normalizeUsageSettings(body.Settings), nil
}

func (h *Handler) saveUsageSettings(settings usageSettings) error {
	path := h.usageDataPath("usage_settings.json")
	return writeUsageJSONFile(path, gin.H{"settings": normalizeUsageSettings(settings)})
}

func normalizeUsageSettings(settings usageSettings) usageSettings {
	if settings.RetentionDays < 0 {
		settings.RetentionDays = 0
	}
	if settings.WebDAV.IntervalMinutes <= 0 {
		settings.WebDAV.IntervalMinutes = 1440
	}
	if settings.WebDAV.RetentionDays < 0 {
		settings.WebDAV.RetentionDays = 0
	}
	settings.WebDAV.URL = strings.TrimSpace(settings.WebDAV.URL)
	settings.WebDAV.Username = strings.TrimSpace(settings.WebDAV.Username)
	return settings
}

func writeUsageJSONFile(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func (s *usageQueryCacheStore) query(ctx context.Context, store usage.Store, rng usage.QueryRange) (usage.APIUsage, error) {
	if s == nil || store == nil {
		return usage.APIUsage{}, nil
	}
	key := usageQueryCacheKey(rng)
	now := time.Now()
	s.mu.Lock()
	if entry := s.entries[key]; entry != nil {
		if !entry.loading && now.Before(entry.expiresAt) {
			result, err := entry.result, entry.err
			s.mu.Unlock()
			return result, err
		}
		if entry.loading {
			ready := entry.ready
			s.mu.Unlock()
			select {
			case <-ready:
				s.mu.Lock()
				latest := s.entries[key]
				if latest == nil {
					s.mu.Unlock()
					return usage.APIUsage{}, nil
				}
				result, err := latest.result, latest.err
				s.mu.Unlock()
				return result, err
			case <-ctx.Done():
				return usage.APIUsage{}, ctx.Err()
			}
		}
	}
	entry := &usageQueryCacheEntry{ready: make(chan struct{}), loading: true}
	s.entries[key] = entry
	s.mu.Unlock()

	result, err := store.Query(ctx, rng)

	s.mu.Lock()
	entry.result = result
	entry.err = err
	entry.expiresAt = time.Now().Add(usageQueryCacheTTL)
	entry.loading = false
	close(entry.ready)
	for cacheKey, cached := range s.entries {
		if cached == nil || cached.loading || time.Now().Before(cached.expiresAt) {
			continue
		}
		delete(s.entries, cacheKey)
	}
	s.mu.Unlock()
	return result, err
}

func usageQueryCacheKey(rng usage.QueryRange) string {
	start, end := "", ""
	if rng.Start != nil && !rng.Start.IsZero() {
		start = rng.Start.UTC().Format(time.RFC3339Nano)
	}
	if rng.End != nil && !rng.End.IsZero() {
		end = rng.End.UTC().Format(time.RFC3339Nano)
	}
	return start + "|" + end + "|" + strconv.Itoa(rng.Limit) + "|" + strconv.FormatBool(rng.IncludeRaw)
}

func (h *Handler) ensureUsageStoreForMonitoring() usage.Store {
	if h == nil || strings.TrimSpace(h.logDir) == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.usageStore != nil {
		return h.usageStore
	}
	if err := usage.InitDefaultStoreInLogDir(h.logDir); err != nil {
		return nil
	}
	h.usageStore = usage.DefaultStore()
	return h.usageStore
}

// DeleteUsageRecords 按记录 ID 删除已持久化的统计记录。
func (h *Handler) DeleteUsageRecords(c *gin.Context) {
	store := h.currentUsageStore()
	if store == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage store unavailable"})
		return
	}

	var body deleteUsageRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	if body.All {
		if clearStore, ok := store.(interface {
			DeleteAll(context.Context) (usage.DeleteResult, error)
		}); ok {
			result, err := clearStore.DeleteAll(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete usage records"})
				return
			}
			usageQueryCache = newUsageQueryCache()
			c.JSON(http.StatusOK, result)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "usage store does not support delete all"})
		return
	}

	ids := make([]string, 0, len(body.IDs))
	seen := make(map[string]struct{}, len(body.IDs))
	for _, id := range body.IDs {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		ids = append(ids, trimmed)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids required"})
		return
	}

	result, err := store.Delete(c.Request.Context(), ids)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete usage records"})
		return
	}
	usageQueryCache = newUsageQueryCache()
	c.JSON(http.StatusOK, result)
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageRange(c *gin.Context) (usage.QueryRange, bool) {
	var rng usage.QueryRange

	if rawStart := strings.TrimSpace(c.Query("start")); rawStart != "" {
		start, err := time.Parse(time.RFC3339, rawStart)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start"})
			return rng, false
		}
		start = start.UTC()
		rng.Start = &start
	}

	if rawEnd := strings.TrimSpace(c.Query("end")); rawEnd != "" {
		end, err := time.Parse(time.RFC3339, rawEnd)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end"})
			return rng, false
		}
		end = end.UTC()
		rng.End = &end
	}
	rng.Limit = usageQueryMaxRows
	rng.IncludeRaw = parseUsageIncludeRaw(c.Query("include_raw"))

	return rng, true
}

func parseUsageIncludeRaw(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "all":
		return true
	default:
		return false
	}
}

func parseNonNegativeQueryInt(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func parsePositiveQueryInt(value string, fallback int, maxValue int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	if maxValue > 0 && parsed > maxValue {
		return maxValue
	}
	return parsed
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}
