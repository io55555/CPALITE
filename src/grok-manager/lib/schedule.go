package grokmanager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const scheduleFileName = "schedule.json"

type scheduleConfig struct {
	Enabled          bool   `json:"enabled"`
	IntervalMin      int    `json:"interval_min"` // minutes, min 15
	AutoRefresh401   bool   `json:"auto_refresh_401"`
	RecheckAfter401  bool   `json:"recheck_after_401"` // after refresh, scan again
	Workers          int    `json:"workers"`
	TimeoutSec       int    `json:"timeout_sec"`
	Model            string `json:"model"`
	NamePrefix       string `json:"name_prefix,omitempty"`
	DeleteStatuses   []int  `json:"delete_statuses,omitempty"`
}

type schedulePublic struct {
	Enabled         bool   `json:"enabled"`
	IntervalMin     int    `json:"interval_min"`
	AutoRefresh401  bool   `json:"auto_refresh_401"`
	RecheckAfter401 bool   `json:"recheck_after_401"`
	Workers         int    `json:"workers"`
	TimeoutSec      int    `json:"timeout_sec"`
	Model           string `json:"model,omitempty"`
	NamePrefix      string `json:"name_prefix,omitempty"`
	LastRunAt       string `json:"last_run_at,omitempty"`
	NextRunAt       string `json:"next_run_at,omitempty"`
	LastMessage     string `json:"last_message,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	ConfigPath      string `json:"config_path,omitempty"`
	LoopRunning     bool   `json:"loop_running"`
	Pipeline        string `json:"pipeline,omitempty"`
}

type scheduleState struct {
	mu          sync.Mutex
	cfg         scheduleConfig
	lastRun     time.Time
	nextRun     time.Time
	lastMessage string
	lastError   string
	path        string
	loopStarted bool
	pipeline    string
}

var sched = &scheduleState{
	cfg: scheduleConfig{
		Enabled:         false,
		IntervalMin:     360,
		AutoRefresh401:  true,
		RecheckAfter401: true,
		Workers:         16,
		TimeoutSec:      20,
		Model:           "grok-4.5",
		DeleteStatuses:  []int{401, 402, 403},
	},
}

func defaultScheduleConfig() scheduleConfig {
	return scheduleConfig{
		Enabled:         false,
		IntervalMin:     360,
		AutoRefresh401:  true,
		RecheckAfter401: true,
		Workers:         16,
		TimeoutSec:      20,
		Model:           "grok-4.5",
		DeleteStatuses:  []int{401, 402, 403},
	}
}

func normalizeSchedule(c scheduleConfig) scheduleConfig {
	if c.IntervalMin < 15 {
		c.IntervalMin = 15
	}
	if c.IntervalMin > 10080 {
		c.IntervalMin = 10080
	}
	if c.Workers < 1 {
		c.Workers = 16
	}
	if c.Workers > 128 {
		c.Workers = 128
	}
	if c.TimeoutSec < 3 {
		c.TimeoutSec = 20
	}
	if c.Model == "" {
		c.Model = "grok-4.5"
	}
	if len(c.DeleteStatuses) == 0 {
		c.DeleteStatuses = []int{401, 402, 403}
	}
	return c
}

func loadScheduleOnStart() {
	for _, p := range pluginDataCandidates(scheduleFileName) {
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) == 0 {
			continue
		}
		var c scheduleConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			continue
		}
		sched.mu.Lock()
		sched.cfg = normalizeSchedule(c)
		sched.path = p
		if sched.cfg.Enabled {
			sched.nextRun = time.Now().UTC().Add(time.Duration(sched.cfg.IntervalMin) * time.Minute)
			sched.lastMessage = "schedule loaded from disk"
		}
		sched.mu.Unlock()
		return
	}
	sched.mu.Lock()
	if sched.cfg.IntervalMin == 0 {
		sched.cfg = defaultScheduleConfig()
	}
	sched.mu.Unlock()
}

func saveSchedule() {
	sched.mu.Lock()
	c := sched.cfg
	path := sched.path
	sched.mu.Unlock()
	if path == "" {
		path = resolvePluginDataPath(scheduleFileName, &sched.path)
	}
	c = normalizeSchedule(c)
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.WriteFile(path, raw, 0o644)
		_ = os.Remove(tmp)
	}
	sched.mu.Lock()
	sched.path = path
	sched.mu.Unlock()
}

func scheduleSnapshot() schedulePublic {
	sched.mu.Lock()
	defer sched.mu.Unlock()
	c := normalizeSchedule(sched.cfg)
	out := schedulePublic{
		Enabled:         c.Enabled,
		IntervalMin:     c.IntervalMin,
		AutoRefresh401:  c.AutoRefresh401,
		RecheckAfter401: c.RecheckAfter401,
		Workers:         c.Workers,
		TimeoutSec:      c.TimeoutSec,
		Model:           c.Model,
		NamePrefix:      c.NamePrefix,
		LastMessage:     sched.lastMessage,
		LastError:       sched.lastError,
		ConfigPath:      sched.path,
		LoopRunning:     sched.loopStarted,
		Pipeline:        firstNonEmpty(sched.pipeline, "scan → refresh401 → recheck"),
	}
	if !sched.lastRun.IsZero() {
		out.LastRunAt = sched.lastRun.UTC().Format(time.RFC3339)
	}
	if c.Enabled {
		if !sched.nextRun.IsZero() {
			out.NextRunAt = sched.nextRun.UTC().Format(time.RFC3339)
		} else {
			out.NextRunAt = time.Now().UTC().Add(time.Duration(c.IntervalMin) * time.Minute).Format(time.RFC3339)
		}
	}
	return out
}

func handleSetSchedule(body []byte) ([]byte, error) {
	var req scheduleConfig
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", err.Error())
		}
	}
	req = normalizeSchedule(req)
	sched.mu.Lock()
	sched.cfg = req
	sched.lastError = ""
	if req.Enabled {
		sched.nextRun = time.Now().UTC().Add(time.Duration(req.IntervalMin) * time.Minute)
		sched.lastMessage = "schedule enabled"
	} else {
		sched.nextRun = time.Time{}
		sched.lastMessage = "schedule disabled"
	}
	sched.mu.Unlock()
	saveSchedule()
	startScheduleLoop()
	return jsonManagementEnvelope(http.StatusOK, scheduleSnapshot())
}

func startScheduleLoop() {
	sched.mu.Lock()
	if sched.loopStarted {
		sched.mu.Unlock()
		return
	}
	sched.loopStarted = true
	sched.mu.Unlock()
	go scheduleLoop()
}

func scheduleLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		sched.mu.Lock()
		cfg := sched.cfg
		next := sched.nextRun
		enabled := cfg.Enabled
		sched.mu.Unlock()
		if !enabled {
			continue
		}
		if next.IsZero() {
			sched.mu.Lock()
			sched.nextRun = time.Now().UTC().Add(time.Duration(cfg.IntervalMin) * time.Minute)
			sched.mu.Unlock()
			continue
		}
		if time.Now().UTC().Before(next) {
			continue
		}
		runScheduledPipeline(cfg)
	}
}

func setPipelineMsg(msg string) {
	sched.mu.Lock()
	sched.pipeline = msg
	sched.lastMessage = msg
	sched.mu.Unlock()
}

// runScheduledPipeline: scan → (optional) wait SSO 401 refresh → (optional) recheck scan
func runScheduledPipeline(cfg scheduleConfig) {
	job.mu.Lock()
	busy := job.running
	job.mu.Unlock()
	ssoJob.mu.Lock()
	ssoBusy := ssoJob.running
	ssoJob.mu.Unlock()
	if busy || ssoBusy {
		sched.mu.Lock()
		sched.lastMessage = "skipped: scan or SSO job busy"
		sched.nextRun = time.Now().UTC().Add(5 * time.Minute)
		sched.mu.Unlock()
		return
	}

	// Phase 1: full scan (auto-refresh kicks off after scan if enabled)
	auto := cfg.AutoRefresh401
	req := scanRequest{
		Workers:        cfg.Workers,
		TimeoutSec:     cfg.TimeoutSec,
		Model:          cfg.Model,
		NamePrefix:     cfg.NamePrefix,
		DeleteStatuses: cfg.DeleteStatuses,
		AutoRefresh401: &auto,
	}
	body, _ := json.Marshal(req)
	setPipelineMsg("pipeline: starting scan")
	if _, err := handleStartScan(body); err != nil {
		sched.mu.Lock()
		sched.lastRun = time.Now().UTC()
		sched.nextRun = time.Now().UTC().Add(time.Duration(cfg.IntervalMin) * time.Minute)
		sched.lastError = err.Error()
		sched.lastMessage = "schedule scan start failed"
		sched.mu.Unlock()
		return
	}

	// Wait scan finish (max 2h)
	if !waitJobIdle(2 * time.Hour) {
		setPipelineMsg("pipeline: scan wait timeout")
		sched.mu.Lock()
		sched.lastRun = time.Now().UTC()
		sched.nextRun = time.Now().UTC().Add(time.Duration(cfg.IntervalMin) * time.Minute)
		sched.lastError = "scan wait timeout"
		sched.mu.Unlock()
		return
	}
	setPipelineMsg("pipeline: scan done")

	// Phase 2: wait SSO refresh if auto-refresh started
	if cfg.AutoRefresh401 {
		// autoRefresh may still be starting — brief settle
		time.Sleep(2 * time.Second)
		ssoJob.mu.Lock()
		running := ssoJob.running
		ssoJob.mu.Unlock()
		if running {
			setPipelineMsg("pipeline: waiting SSO 401 refresh")
			if !waitSSOIdle(2 * time.Hour) {
				setPipelineMsg("pipeline: SSO wait timeout")
			} else {
				setPipelineMsg("pipeline: SSO refresh done")
			}
		} else {
			// try explicit vault 401 refresh if scan found any
			job.mu.Lock()
			n401 := 0
			for _, r := range job.results {
				if r.HTTPStatus == 401 {
					n401++
				}
			}
			job.mu.Unlock()
			if n401 > 0 {
				setPipelineMsg(fmt.Sprintf("pipeline: refresh %d x 401 from vault", n401))
				_, _ = handleRefresh401([]byte(`{}`))
				time.Sleep(1 * time.Second)
				_ = waitSSOIdle(2 * time.Hour)
				setPipelineMsg("pipeline: SSO refresh done")
			}
		}
	}

	// Phase 3: recheck scan (no auto-refresh to avoid loop)
	if cfg.RecheckAfter401 {
		falseAuto := false
		reReq := scanRequest{
			Workers:        cfg.Workers,
			TimeoutSec:     cfg.TimeoutSec,
			Model:          cfg.Model,
			NamePrefix:     cfg.NamePrefix,
			DeleteStatuses: cfg.DeleteStatuses,
			AutoRefresh401: &falseAuto,
		}
		// wait free
		_ = waitJobIdle(5 * time.Minute)
		_ = waitSSOIdle(5 * time.Minute)
		body2, _ := json.Marshal(reReq)
		setPipelineMsg("pipeline: recheck scan")
		if _, err := handleStartScan(body2); err == nil {
			_ = waitJobIdle(2 * time.Hour)
			setPipelineMsg("pipeline: recheck done")
		} else {
			setPipelineMsg("pipeline: recheck start failed: " + err.Error())
		}
	}

	sched.mu.Lock()
	sched.lastRun = time.Now().UTC()
	sched.nextRun = time.Now().UTC().Add(time.Duration(cfg.IntervalMin) * time.Minute)
	sched.lastError = ""
	if sched.pipeline == "" {
		sched.lastMessage = "pipeline completed"
	} else {
		sched.lastMessage = sched.pipeline
	}
	sched.mu.Unlock()
}

func waitJobIdle(max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		job.mu.Lock()
		running := job.running
		job.mu.Unlock()
		if !running {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

func waitSSOIdle(max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		ssoJob.mu.Lock()
		running := ssoJob.running
		ssoJob.mu.Unlock()
		if !running {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// keep old name for any callers
func runScheduledScan(cfg scheduleConfig) { runScheduledPipeline(cfg) }
