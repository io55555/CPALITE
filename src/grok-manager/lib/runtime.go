package grokmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	abiVersion         uint32 = 1
	pluginName                = "grok-manager"
	pluginVersion             = "1.3.7"
	managementBasePath        = "/plugins/grok-manager"
	resourcePanelPath         = "/panel"
	xaiProvider               = "xai"
	historyFileName           = "last-scan.json"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}
type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      metadata                 `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}
type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields"`
}
type configField struct {
	Key         string `json:"Key"`
	Label       string `json:"Label"`
	Type        string `json:"Type"`
	Required    bool   `json:"Required"`
	Description string `json:"Description"`
}
type registrationCapabilities struct {
	UsagePlugin   bool `json:"usage_plugin"`
	Scheduler     bool `json:"scheduler"`
	ManagementAPI bool `json:"management_api"`
}
type managementRoute struct {
	Method      string `json:"Method,omitempty"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}
type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}
type managementRegistration struct {
	Routes    []managementRoute    `json:"routes,omitempty"`
	Resources []managementResource `json:"resources,omitempty"`
}
type managementRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers"`
	Query   url.Values  `json:"Query"`
	Body    []byte      `json:"Body"`
}
type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}
type authListResponse struct {
	Files []authFile `json:"files"`
}
type authFile struct {
	Account   string `json:"account"`
	AuthIndex string `json:"auth_index"`
	Disabled  bool   `json:"disabled"`
	Email     string `json:"email"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Provider  string `json:"provider"`
	Type      string `json:"type"`
	Status    string `json:"status"`
}
type authGetRequest struct {
	AuthIndex string `json:"auth_index"`
}
type authGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}
type authTokenConfig struct {
	AccessToken string         `json:"access_token"`
	BaseURL     string         `json:"base_url"`
	Email       string         `json:"email"`
	ProxyURL    string         `json:"proxy_url"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}
type scanRequest struct {
	Workers         int    `json:"workers"`
	TimeoutSec      int    `json:"timeout_sec"`
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	ClientVersion   string `json:"client_version"`
	MaxOutputTok    int    `json:"max_output_tokens"`
	DeleteStatuses  []int  `json:"delete_statuses"`
	NamePrefix      string `json:"name_prefix"`
	IncludeDisabled bool   `json:"include_disabled"`
	// AutoRefresh401: after scan, auto re-convert accounts with HTTP 401 using SSO vault (default true).
	AutoRefresh401 *bool `json:"auto_refresh_401"`
	// SyncToBans: after scan, reconcile isolation from results (default true). Plan B.
	// Deprecated in favor of SyncMode; false forces off.
	SyncToBans *bool `json:"sync_to_bans"`
	// SyncMode: off | candidates | all
	//   candidates (default): only DELETE_CANDIDATE (401/402/403 by default) → isolation
	//   all: all classifiable failures including 429
	//   off: do not write isolation (mid-scan or end)
	SyncMode string `json:"sync_mode"`
	// UnbanHealthy: when syncing, drop isolation for accounts that probed healthy (default true).
	UnbanHealthy *bool `json:"unban_healthy"`
}
type probeResult struct {
	AuthIndex   string `json:"auth_index,omitempty"`
	AuthID      string `json:"auth_id,omitempty"` // host canonical id (prefer for autoban)
	Name        string `json:"name,omitempty"`
	File        string `json:"file,omitempty"`
	Path        string `json:"path,omitempty"`
	Email       string `json:"email,omitempty"`
	HTTPStatus  int    `json:"http_status"`
	Action      string `json:"action"`
	// Status is a stable account state machine label (item 12).
	// healthy | unauthorized | payment | forbidden | rate_limited | network | error | unknown
	Status      string `json:"status,omitempty"`
	Advice      string `json:"advice,omitempty"`
	HasVaultSSO bool   `json:"has_vault_sso"`
	Summary     string `json:"summary,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms"`
	Error       string `json:"error,omitempty"`
}
type scanSummary struct {
	OK               int            `json:"ok"`
	DeleteCandidates int            `json:"delete_candidates"`
	Kept             int            `json:"kept"`
	Errors           int            `json:"errors"`
	HTTP             map[string]int `json:"http"`
	// ByStatus counts account lifecycle states (item 12).
	ByStatus         map[string]int `json:"by_status,omitempty"`
	// VaultMatch: among unauthorized(401), how many have SSO in vault for auto-refresh.
	VaultMatch401    int            `json:"vault_match_401"`
	VaultMiss401     int            `json:"vault_miss_401"`
}
type jobSnapshot struct {
	State          string         `json:"state"`
	Message        string         `json:"message,omitempty"`
	Error          string         `json:"error,omitempty"`
	Total          int            `json:"total"`
	Done           int            `json:"done"`
	Workers        int            `json:"workers"`
	StartedAt      string         `json:"started_at,omitempty"`
	FinishedAt     string         `json:"finished_at,omitempty"`
	Summary        scanSummary    `json:"summary"`
	Candidates     []probeResult  `json:"candidates"`
	Results        []probeResult  `json:"results,omitempty"`
	DeleteStatuses []int          `json:"delete_statuses,omitempty"`
	HistoryPath    string         `json:"history_path,omitempty"`
	Persisted      bool           `json:"persisted"`
	HistorySavedAt string         `json:"history_saved_at,omitempty"`
	ResultCount    int            `json:"result_count"`
	PluginVersion  string         `json:"plugin_version"`
	VaultPath      string         `json:"vault_path,omitempty"`
	VaultCount     int            `json:"vault_count"`
	VaultSavedAt   string         `json:"vault_saved_at,omitempty"`
	Schedule       schedulePublic `json:"schedule"`
	// Last scan→ban reconciliation (plan B).
	ScanSync *scanBanSyncResult `json:"scan_sync,omitempty"`
}
type deleteRequest struct {
	Mode   string   `json:"mode"` // candidates | names | status
	Names  []string `json:"names"`
	Status int      `json:"status"` // for mode=status (HTTP code)
}
type deleteResponse struct {
	Deleted      int      `json:"deleted"`
	Failed       int      `json:"failed"`
	Errors       []string `json:"errors,omitempty"`
	DeletedPaths []string `json:"deleted_paths,omitempty"`
}
type persistedScan struct {
	SavedAt        string        `json:"saved_at"`
	State          string        `json:"state"`
	Message        string        `json:"message,omitempty"`
	Error          string        `json:"error,omitempty"`
	Total          int           `json:"total"`
	Done           int           `json:"done"`
	Workers        int           `json:"workers"`
	StartedAt      string        `json:"started_at,omitempty"`
	FinishedAt     string        `json:"finished_at,omitempty"`
	DeleteStatuses []int         `json:"delete_statuses,omitempty"`
	Results        []probeResult `json:"results"`
}
type jobState struct {
	mu             sync.Mutex
	running        bool
	cancel         context.CancelFunc
	state          string
	message        string
	errText        string
	total          int
	done           int64
	workers        int
	startedAt      time.Time
	finishedAt     time.Time
	deleteStatuses map[int]bool
	results        []probeResult
	historyPath    string
	persisted      bool
	historySavedAt string
	lastScanSync   *scanBanSyncResult
}

var job = &jobState{state: "idle", deleteStatuses: map[int]bool{401: true, 402: true, 403: true}}

type HostCaller func(method string, payload any) (json.RawMessage, error)

var hostCaller HostCaller = callHostUnavailable

var historyLoadOnce sync.Once

// ensureHistoryLoaded loads last-scan / last-sso / schedule from disk once.
// Also called on every status request so a late page refresh still sees history
// even if init raced or CPA re-registered the plugin without a full process restart.
func ensureHistoryLoaded() {
	historyLoadOnce.Do(func() {
		loadHistoryOnStart()
		loadSSOHistoryOnStart()
		loadBansOnStart()
		loadProbeHistoryOnStart()
		loadScheduleOnStart()
		startScheduleLoop()
		startRecheck429Loop()
	})
}

// Name returns the stable plugin ID used by CPA.
func Name() string { return pluginName }

// Version returns the plugin semantic version.
func Version() string { return pluginVersion }

// ABIVersion returns the cliproxy plugin ABI version.
func ABIVersion() uint32 { return abiVersion }

// SetHostCaller installs the host callback implementation.
func SetHostCaller(fn HostCaller) {
	if fn == nil {
		hostCaller = callHostUnavailable
		return
	}
	hostCaller = fn
}

// Init loads on-disk state and starts background loops.
func Init() { ensureHistoryLoaded() }

// Shutdown cancels in-flight jobs.
func Shutdown() {
	job.mu.Lock()
	if job.cancel != nil {
		job.cancel()
	}
	job.mu.Unlock()
}

// HandleMethod is the plugin ABI method dispatcher.
func HandleMethod(method string, request []byte) ([]byte, error) {
	return handleMethod(method, request)
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return okEnvelope(registration{
			SchemaVersion: 1,
			Metadata: metadata{
				Name:             pluginName,
				Version:          pluginVersion,
				Author:           "1296018244",
				GitHubRepository: "https://github.com/1296018244/grok-manager",
				Logo:             "",
				ConfigFields:     []configField{},
			},
			Capabilities: registrationCapabilities{
				UsagePlugin:   true,
				Scheduler:     true,
				ManagementAPI: true,
			},
		})
	case "usage.handle":
		return handleUsage(request)
	case "scheduler.pick":
		return handleSchedulerPick(request)
	case "management.register":
		return okEnvelope(managementRegistration{
			Routes: []managementRoute{
				{Method: http.MethodGet, Path: managementBasePath + "/status", Description: "Scan status (summary only)"},
				{Method: http.MethodGet, Path: managementBasePath + "/results", Description: "Paginated scan results"},
				{Method: http.MethodGet, Path: managementBasePath + "/auth-files", Description: "Paginated xAI credential file list"},
				{Method: http.MethodPost, Path: managementBasePath + "/scan", Description: "Start live probe"},
				{Method: http.MethodPost, Path: managementBasePath + "/stop", Description: "Stop scan"},
				{Method: http.MethodPost, Path: managementBasePath + "/delete", Description: "Delete candidates"},
				{Method: http.MethodPost, Path: managementBasePath + "/sso-import", Description: "SSO cookie → xai oauth auth files"},
				{Method: http.MethodGet, Path: managementBasePath + "/sso-status", Description: "SSO import status"},
				{Method: http.MethodPost, Path: managementBasePath + "/sso-stop", Description: "Stop SSO import"},
				{Method: http.MethodGet, Path: managementBasePath + "/sso-vault", Description: "SSO vault (paginated)"},
				{Method: http.MethodPost, Path: managementBasePath + "/sso-vault-delete", Description: "Delete vault entries by email/filter"},
				{Method: http.MethodPost, Path: managementBasePath + "/sso-vault-export", Description: "Export vault as email----sso text"},
				{Method: http.MethodPost, Path: managementBasePath + "/sso-preview", Description: "Preview SSO list (dedup/invalid stats)"},
				{Method: http.MethodPost, Path: managementBasePath + "/sso-refresh-401", Description: "Re-convert vault accounts with last-scan 401"},
				{Method: http.MethodGet, Path: managementBasePath + "/schedule", Description: "Get scheduled scan config"},
				{Method: http.MethodPost, Path: managementBasePath + "/schedule", Description: "Update scheduled scan config"},
				{Method: http.MethodGet, Path: managementBasePath + "/bans", Description: "Runtime bans (paginated)"},
				{Method: http.MethodPost, Path: managementBasePath + "/bans-prune", Description: "Drop isolation rows for deleted credential files"},
				{Method: http.MethodPost, Path: managementBasePath + "/bans-recheck-429", Description: "Probe 429 bans (or selected/status via body)"},
				{Method: http.MethodPost, Path: managementBasePath + "/bans-probe", Description: "Probe isolated credentials (selected auth_ids / status / all 429)"},
				{Method: http.MethodGet, Path: managementBasePath + "/bans-probe-history", Description: "List isolation probe history sessions"},
				{Method: http.MethodPost, Path: managementBasePath + "/unban", Description: "Release isolated credentials"},
				{Method: http.MethodPost, Path: managementBasePath + "/unban-all", Description: "Release all isolated credentials"},
				{Method: http.MethodPost, Path: managementBasePath + "/bans-delete", Description: "Delete credential files and drop isolation rows"},
				{Method: http.MethodPost, Path: managementBasePath + "/bans-sync-scan", Description: "Reconcile isolation from last scan results (plan B)"},
				{Method: http.MethodPost, Path: managementBasePath + "/bans-import", Description: "Import ban snapshot"},
				{Method: http.MethodGet, Path: managementBasePath + "/paths", Description: "Show vault/history/auth paths"},
				{Method: http.MethodPost, Path: managementBasePath + "/backup", Description: "Zip vault+scan+schedule+bans backup"},
			},
			Resources: []managementResource{
				{Path: resourcePanelPath, Menu: "Grok Manager", Description: "Grok live-check / cleanup / SSO / vault / runtime autoban"},
			},
		})
	case "management.handle":
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("decode management request: %w", err)
		}
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	if path == "" {
		path = resourcePanelPath
	}
	switch {
	case routeHasSuffix(path, resourcePanelPath) || path == managementBasePath:
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		return okEnvelope(managementResponse{
			StatusCode: 200,
			Headers:    http.Header{"content-type": []string{"text/html; charset=utf-8"}},
			Body:       []byte(panelHTML),
		})
	case routeHasSuffix(path, "/status"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		ensureHistoryLoaded()
		// If memory is empty but disk has a file (e.g. plugin process state reset), reload.
		reloadHistoryIfEmpty()
		return jsonManagementEnvelope(http.StatusOK, snapshotJob(false))
	case routeHasSuffix(path, "/results"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		ensureHistoryLoaded()
		reloadHistoryIfEmpty()
		return jsonManagementEnvelope(http.StatusOK, snapshotResults(req.Query))
	case routeHasSuffix(path, "/auth-files"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		return handleAuthFiles(req.Query)
	case routeHasSuffix(path, "/scan"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleStartScan(req.Body)
	case routeHasSuffix(path, "/stop"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		stopJob("stopped by user")
		return jsonManagementEnvelope(http.StatusOK, snapshotJob(false))
	case routeHasSuffix(path, "/delete"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleDelete(req.Body)
	case routeHasSuffix(path, "/sso-import"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleStartSSOImport(req.Body)
	case routeHasSuffix(path, "/sso-status"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		ensureHistoryLoaded()
		return jsonManagementEnvelope(http.StatusOK, snapshotSSO())
	case routeHasSuffix(path, "/sso-stop"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleStopSSOImport()
	case routeHasSuffix(path, "/sso-vault-delete"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleVaultDelete(req.Body)
	case routeHasSuffix(path, "/sso-vault-export"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleVaultExport(req.Body)
	case routeHasSuffix(path, "/sso-vault"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		return jsonManagementEnvelope(http.StatusOK, vaultPublicSummary(req.Query))
	case routeHasSuffix(path, "/sso-preview"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleSSOPreview(req.Body)
	case routeHasSuffix(path, "/sso-refresh-401"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleRefresh401(req.Body)
	case routeHasSuffix(path, "/schedule"):
		if method == http.MethodGet {
			ensureHistoryLoaded()
			return jsonManagementEnvelope(http.StatusOK, scheduleSnapshot())
		}
		if method == http.MethodPost {
			return handleSetSchedule(req.Body)
		}
		return methodNotAllowed([]string{http.MethodGet, http.MethodPost})
	case routeHasSuffix(path, "/bans-recheck-429"), routeHasSuffix(path, "/bans-probe"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleRecheck429(req.Body)
	case routeHasSuffix(path, "/bans-probe-history"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		return handleProbeHistory(req.Query)
	case routeHasSuffix(path, "/bans-prune"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		removed, before, ok := pruneOrphanBans()
		return jsonManagementEnvelope(http.StatusOK, map[string]any{
			"ok":            true,
			"host_list_ok":  ok,
			"removed":       removed,
			"before":        before,
			"after":         before - removed,
			"status":        autobanSnapshot(url.Values{"skip_prune": []string{"1"}}),
			"message":       map[bool]string{true: "已同步：移除无凭证隔离", false: "host.auth.list 失败，未清理"}[ok],
		})
	case routeHasSuffix(path, "/bans"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		return jsonManagementEnvelope(http.StatusOK, autobanSnapshot(req.Query))
	case routeHasSuffix(path, "/unban-all"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return jsonManagementEnvelope(http.StatusOK, map[string]any{
			"ok": true, "removed": runtimeBans.clearAll(), "status": autobanSnapshot(nil),
		})
	case routeHasSuffix(path, "/bans-delete"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleBansDelete(req.Body, req.Query)
	case routeHasSuffix(path, "/bans-sync-scan"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleBansSyncScan(req.Body)
	case routeHasSuffix(path, "/unban"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleAutobanUnban(req.Body, req.Query)
	case routeHasSuffix(path, "/bans-import"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleAutobanImport(req.Body)
	case routeHasSuffix(path, "/paths"):
		if method != http.MethodGet {
			return methodNotAllowed([]string{http.MethodGet})
		}
		return handlePathsInfo()
	case routeHasSuffix(path, "/backup"):
		if method != http.MethodPost {
			return methodNotAllowed([]string{http.MethodPost})
		}
		return handleBackup()
	default:
		return jsonErrorEnvelope(http.StatusNotFound, "not_found", "unknown path: "+path)
	}
}

func handleStartScan(body []byte) ([]byte, error) {
	var req scanRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", err.Error())
		}
	}
	if req.Workers < 1 {
		req.Workers = 16
	}
	if req.Workers > 128 {
		req.Workers = 128
	}
	if req.TimeoutSec < 3 {
		req.TimeoutSec = 20
	}
	if req.Model == "" {
		req.Model = "grok-4.5"
	}
	if req.Prompt == "" {
		req.Prompt = "ping"
	}
	if req.ClientVersion == "" {
		req.ClientVersion = "0.2.93"
	}
	if req.MaxOutputTok < 1 {
		req.MaxOutputTok = 1
	}
	if len(req.DeleteStatuses) == 0 {
		req.DeleteStatuses = []int{401, 402, 403}
	}

	job.mu.Lock()
	if job.running {
		job.mu.Unlock()
		return jsonErrorEnvelope(http.StatusConflict, "busy", "scan already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.running = true
	job.cancel = cancel
	job.state = "running"
	job.message = "listing auth files"
	job.errText = ""
	job.total = 0
	atomic.StoreInt64(&job.done, 0)
	job.workers = req.Workers
	job.startedAt = time.Now().UTC()
	job.finishedAt = time.Time{}
	job.results = nil
	job.deleteStatuses = map[int]bool{}
	for _, s := range req.DeleteStatuses {
		job.deleteStatuses[s] = true
	}
	job.mu.Unlock()
	go runScan(ctx, req)
	return jsonManagementEnvelope(http.StatusAccepted, snapshotJob(false))
}

func stopJob(msg string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.cancel != nil {
		job.cancel()
	}
	if job.running {
		job.message = msg
	}
}

func runScan(ctx context.Context, req scanRequest) {
	defer func() {
		job.mu.Lock()
		job.running = false
		job.cancel = nil
		if job.state == "running" {
			job.state = "done"
		}
		job.finishedAt = time.Now().UTC()
		if job.message == "" {
			job.message = "completed"
		}
		job.mu.Unlock()
		// Final persist AFTER state flips to done (previous bug saved while still "running").
		saveHistory()
	}()

	authResp, err := callHostAuthList()
	if err != nil {
		job.mu.Lock()
		job.state = "error"
		job.errText = "host.auth.list: " + err.Error()
		job.message = job.errText
		job.mu.Unlock()
		return
	}

	var targets []authFile
	prefix := strings.TrimSpace(req.NamePrefix)
	for _, f := range authResp.Files {
		if !isXAIAuth(f) {
			continue
		}
		if f.Disabled && !req.IncludeDisabled {
			continue
		}
		name := firstNonEmpty(f.Name, f.ID, f.Email)
		if prefix != "" {
			low := strings.ToLower(prefix)
			if !strings.HasPrefix(strings.ToLower(name), low) && !strings.HasPrefix(strings.ToLower(f.Email), low) {
				continue
			}
		}
		targets = append(targets, f)
	}
	sort.Slice(targets, func(i, j int) bool {
		return firstNonEmpty(targets[i].Name, targets[i].ID) < firstNonEmpty(targets[j].Name, targets[j].ID)
	})

	job.mu.Lock()
	job.total = len(targets)
	job.message = fmt.Sprintf("probing %d accounts", len(targets))
	job.mu.Unlock()
	if len(targets) == 0 {
		job.mu.Lock()
		job.message = "no xai auth files matched"
		job.mu.Unlock()
		return
	}

	// 共享 client 仅作占位；probeOne 按认证文件/CPA 代理优先级单独建连
	client := newHTTPClientWithProxy("", time.Duration(req.TimeoutSec)*time.Second, req.Workers*2)
	jobs := make(chan authFile)
	results := make(chan probeResult, req.Workers*2)
	var wg sync.WaitGroup
	for i := 0; i < req.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				res := probeOne(ctx, client, file, req)
				atomic.AddInt64(&job.done, 1)
				results <- res
			}
		}()
	}
	go func() {
		defer close(results)
		for _, file := range targets {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return
			case jobs <- file:
			}
		}
		close(jobs)
		wg.Wait()
	}()

	syncMode := effectiveScanSyncMode(req)
	collected := make([]probeResult, 0, len(targets))
	vaultEmails := vaultEmailSet()
	for res := range results {
		enrichProbeResult(&res, vaultEmails)
		// Mid-scan isolation feed (respects sync mode; default only candidates).
		if shouldNoteBanFromScan(res, syncMode) {
			noteScanBan(res)
		}
		collected = append(collected, res)
		// Incremental persist so a crash / refresh mid-scan still has history.
		if len(collected)%50 == 0 {
			job.mu.Lock()
			job.results = append([]probeResult(nil), collected...)
			job.mu.Unlock()
			saveHistory()
		}
	}
	sort.Slice(collected, func(i, j int) bool {
		return firstNonEmpty(collected[i].Name, collected[i].Email) < firstNonEmpty(collected[j].Name, collected[j].Email)
	})
	job.mu.Lock()
	job.results = collected
	if ctx.Err() != nil && job.state == "running" {
		job.state = "stopped"
		job.message = "stopped"
	}
	job.mu.Unlock()
	saveHistory()
	rememberProbeFromScanResults(collected)
	syncVaultHTTPFromScan(collected)

	// Plan B: reconcile isolation from full scan results (mode: candidates|all|off).
	unbanHealthy := true
	if req.UnbanHealthy != nil {
		unbanHealthy = *req.UnbanHealthy
	}
	if syncMode != "off" && ctx.Err() == nil && len(collected) > 0 {
		syncRes := syncScanResultsToBans(collected, unbanHealthy, syncMode)
		job.mu.Lock()
		cp := syncRes
		job.lastScanSync = &cp
		if job.state == "done" || job.state == "running" {
			job.message = fmt.Sprintf("completed · 同步隔离[%s] 写入%d 解禁%d", syncMode, syncRes.Banned, syncRes.Unbanned)
		}
		job.mu.Unlock()
	}

	autoRefresh := true
	if req.AutoRefresh401 != nil {
		autoRefresh = *req.AutoRefresh401
	}
	if autoRefresh && ctx.Err() == nil {
		go autoRefresh401FromVault(req.Workers)
	}
}

// syncVaultHTTPFromScan writes last HTTP status into SSO vault entries by email.
func syncVaultHTTPFromScan(results []probeResult) {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	if len(v.Entries) == 0 {
		return
	}
	byEmail := map[string]int{}
	for i, e := range v.Entries {
		if e.Email != "" {
			byEmail[strings.ToLower(e.Email)] = i
		}
	}
	changed := false
	for _, r := range results {
		em := strings.TrimSpace(r.Email)
		if em == "" || r.HTTPStatus <= 0 {
			continue
		}
		i, ok := byEmail[strings.ToLower(em)]
		if !ok {
			continue
		}
		e := v.Entries[i]
		e.LastHTTP = r.HTTPStatus
		if r.File != "" || r.Name != "" {
			e.LastFile = firstNonEmpty(r.File, r.Name, filepath.Base(r.Path))
		}
		v.Entries[i] = e
		changed = true
	}
	if changed {
		_ = saveVaultUnlocked(v)
	}
}

// autoRefresh401FromVault re-imports SSO→xai for accounts that just probed as 401.
func autoRefresh401FromVault(workers int) {
	job.mu.Lock()
	var emails []string
	for _, r := range job.results {
		if r.HTTPStatus == 401 && strings.TrimSpace(r.Email) != "" {
			emails = append(emails, strings.TrimSpace(r.Email))
		}
	}
	job.mu.Unlock()
	if len(emails) == 0 {
		return
	}

	// Build items from vault for these emails only.
	want := map[string]bool{}
	for _, e := range emails {
		want[strings.ToLower(e)] = true
	}
	vaultMu.Lock()
	v := loadVaultUnlocked()
	vaultMu.Unlock()
	var items []ssoCookieItem
	for _, e := range v.Entries {
		if e.Email == "" || strings.TrimSpace(e.SSO) == "" {
			continue
		}
		if want[strings.ToLower(e.Email)] {
			items = append(items, ssoCookieItem{SSO: e.SSO, Email: e.Email})
		}
	}
	if len(items) == 0 {
		ssoLog(fmt.Sprintf("auto-refresh-401: 扫描到 %d 个 401，但 SSO vault 中无匹配 email（请先导入并保存 SSO）", len(emails)))
		return
	}

	ssoJob.mu.Lock()
	busy := ssoJob.running
	ssoJob.mu.Unlock()
	if busy {
		ssoLog(fmt.Sprintf("auto-refresh-401: SSO 任务忙，跳过自动刷新（%d 个可刷新）", len(items)))
		return
	}

	outDir := defaultAuthOutDir()
	if workers < 1 {
		workers = ssoDefaultWorkers
	}
	if workers > ssoMaxWorkers {
		workers = ssoMaxWorkers
	}
	if workers > len(items) {
		workers = len(items)
	}
	skipFalse := false
	saveTrue := true
	body, _ := json.Marshal(ssoImportRequest{
		Cookies:    items,
		OutDir:     outDir,
		Workers:    workers,
		MaxRetries: ssoDefaultMaxRetries,
		SkipIfOK:   &skipFalse, // force reconvert 401
		SaveSSO:    &saveTrue,
		Force:      true,
		Only401:    false,
	})
	ssoLog(fmt.Sprintf("auto-refresh-401: 开始用 vault 重刷 %d 个 401 账号 → %s", len(items), outDir))
	if _, err := handleStartSSOImport(body); err != nil {
		ssoLog("auto-refresh-401 启动失败: " + err.Error())
	}
}

func probeOne(ctx context.Context, client *http.Client, file authFile, req scanRequest) probeResult {
	started := time.Now()
	res := probeResult{
		AuthIndex: file.AuthIndex,
		AuthID:    firstNonEmpty(file.ID, file.AuthIndex),
		Name:      firstNonEmpty(file.Name, file.ID),
		File:      firstNonEmpty(file.Name, file.ID),
		Path:      file.Path,
		Email:     firstNonEmpty(file.Email, file.Account, file.Label),
	}
	tokenCfg, path, err := loadAuthToken(file)
	if path != "" {
		res.Path = path
	}
	if err != nil {
		res.Action = "ERROR"
		res.Error = err.Error()
		res.ElapsedMS = time.Since(started).Milliseconds()
		return res
	}
	if res.Email == "" {
		res.Email = tokenCfg.Email
	}
	if tokenCfg.AccessToken == "" {
		res.Action = "ERROR"
		res.Error = "missing access_token"
		res.ElapsedMS = time.Since(started).Milliseconds()
		return res
	}
	endpoint := strings.TrimRight(tokenCfg.BaseURL, "/")
	if endpoint == "" {
		endpoint = "https://cli-chat-proxy.grok.com/v1"
	}
	endpoint += "/responses"
	body, _ := json.Marshal(map[string]any{
		"model":             req.Model,
		"input":             req.Prompt,
		"max_output_tokens": req.MaxOutputTok,
		"store":             false,
	})
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		res.Action = "ERROR"
		res.Error = err.Error()
		res.ElapsedMS = time.Since(started).Milliseconds()
		return res
	}
	httpReq.Header.Set("Authorization", "Bearer "+tokenCfg.AccessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-grok-client-version", req.ClientVersion)
	// 优先认证文件代理，其次 CPA proxy-url，最后直连
	probeClient := newHTTPClientWithProxy(tokenCfg.effectiveProxyURL(), time.Duration(req.TimeoutSec)*time.Second, 4)
	if probeClient == nil {
		probeClient = client
	}
	resp, err := probeClient.Do(httpReq)
	res.ElapsedMS = time.Since(started).Milliseconds()
	if err != nil {
		res.Action = "KEEP"
		res.Summary = "request failed: " + err.Error()
		return res
	}
	defer resp.Body.Close()
	res.HTTPStatus = resp.StatusCode
	res.Summary = compactResponse(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.Action = "OK"
		return res
	}
	job.mu.Lock()
	del := job.deleteStatuses[resp.StatusCode]
	job.mu.Unlock()
	if del {
		res.Action = "DELETE_CANDIDATE"
		return res
	}
	res.Action = "KEEP"
	return res
}

// classifyAccountStatus maps probe outcome to a stable lifecycle label (item 12).
func classifyAccountStatus(r probeResult) string {
	if r.Action == "ERROR" || (r.Error != "" && r.HTTPStatus == 0 && r.Action != "OK") {
		return "error"
	}
	switch r.HTTPStatus {
	case 200, 201, 204:
		return "healthy"
	case 401:
		return "unauthorized"
	case 402:
		return "payment"
	case 403:
		return "forbidden"
	case 429:
		return "rate_limited"
	case 0:
		low := strings.ToLower(r.Summary + " " + r.Error)
		if strings.Contains(low, "request failed") || strings.Contains(low, "timeout") || strings.Contains(low, "connection") {
			return "network"
		}
		return "unknown"
	default:
		if r.Action == "OK" {
			return "healthy"
		}
		return "unknown"
	}
}

func adviceForStatus(status string, hasVault bool) string {
	switch status {
	case "healthy":
		return "正常可用"
	case "unauthorized":
		if hasVault {
			return "401：可用 SSO 库自动重刷 CPA 凭证"
		}
		return "401：SSO 库无匹配 email，需先导入并勾选「保存到历史库」"
	case "payment":
		return "402：额度/订阅问题，SSO 重登通常无效，建议删除或换号"
	case "forbidden":
		return "403：账号被拒，恢复希望低，可删除"
	case "rate_limited":
		return "429：限流/免费额度用尽，保留账号，稍后重试"
	case "network":
		return "网络/超时，非凭证失效，保留后重测"
	case "error":
		return "读文件或 token 异常，检查 JSON"
	default:
		return "状态未知，建议再测活"
	}
}

func enrichProbeResult(r *probeResult, vaultEmails map[string]bool) {
	if r == nil {
		return
	}
	em := strings.ToLower(strings.TrimSpace(r.Email))
	if vaultEmails != nil && em != "" {
		r.HasVaultSSO = vaultEmails[em]
	}
	if r.Status == "" {
		r.Status = classifyAccountStatus(*r)
	}
	r.Advice = adviceForStatus(r.Status, r.HasVaultSSO)
}

func vaultEmailSet() map[string]bool {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	m := make(map[string]bool, len(v.Entries))
	for _, e := range v.Entries {
		em := strings.ToLower(strings.TrimSpace(e.Email))
		if em != "" && strings.TrimSpace(e.SSO) != "" {
			m[em] = true
		}
	}
	return m
}

func loadAuthToken(file authFile) (authTokenConfig, string, error) {
	var cfg authTokenConfig
	path := strings.TrimSpace(file.Path)
	if strings.TrimSpace(file.AuthIndex) != "" {
		got, err := callHostAuthGet(file.AuthIndex)
		if err == nil {
			if strings.TrimSpace(got.Path) != "" {
				path = got.Path
			}
			if len(bytes.TrimSpace(got.JSON)) > 0 {
				if err := json.Unmarshal(got.JSON, &cfg); err != nil {
					return cfg, path, err
				}
				return cfg, path, nil
			}
		}
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, path, err
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, path, err
		}
		return cfg, path, nil
	}
	name := firstNonEmpty(file.Name, file.ID)
	if name == "" {
		return cfg, path, fmt.Errorf("auth json unavailable")
	}
	candidates := []string{
		filepath.Join("/root/.cli-proxy-api", name),
		filepath.Join("/root/.cli-proxy-api", name+".json"),
	}
	// Windows local auth dirs
	for _, base := range []string{
		`C:\CLIProxyAPI-local\auths-xai-test`,
		`C:\CLIProxyAPI-local\auths`,
	} {
		candidates = append(candidates, filepath.Join(base, name), filepath.Join(base, name+".json"))
	}
	for _, c := range candidates {
		raw, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, c, err
		}
		return cfg, c, nil
	}
	return cfg, path, fmt.Errorf("auth json unavailable for %s", name)
}

func handleDelete(body []byte) ([]byte, error) {
	var req deleteRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", err.Error())
		}
	}
	job.mu.Lock()
	if job.running {
		job.mu.Unlock()
		return jsonErrorEnvelope(http.StatusConflict, "busy", "cannot delete while scan is running")
	}
	results := append([]probeResult(nil), job.results...)
	job.mu.Unlock()

	var targets []probeResult
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case "", "candidates":
		for _, r := range results {
			if r.Action == "DELETE_CANDIDATE" {
				targets = append(targets, r)
			}
		}
		if len(targets) == 0 {
			return jsonErrorEnvelope(http.StatusBadRequest, "no_candidates",
				"当前没有可删候选。请先测活，或用 mode=names 指定文件名。历史结果会保存在 last-scan.json。")
		}
	case "names":
		want := map[string]bool{}
		for _, n := range req.Names {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			want[n] = true
			want[filepath.Base(n)] = true
		}
		matched := map[string]bool{}
		for _, r := range results {
			keys := []string{r.Name, r.File, r.AuthIndex, r.Path, filepath.Base(r.Path)}
			hit := false
			for _, k := range keys {
				if k != "" && want[k] {
					hit = true
					break
				}
			}
			if hit {
				targets = append(targets, r)
				for _, k := range keys {
					if k != "" {
						matched[k] = true
					}
				}
			}
		}
		// Even without in-memory scan results, allow deleting by filename on disk.
		for n := range want {
			if matched[n] || matched[filepath.Base(n)] {
				continue
			}
			base := filepath.Base(n)
			targets = append(targets, probeResult{
				Name:   base,
				File:   base,
				Path:   n,
				Action: "DELETE_CANDIDATE",
			})
		}
		if len(targets) == 0 {
			return jsonErrorEnvelope(http.StatusBadRequest, "no_targets", "names 为空或未匹配到任何凭证")
		}
	case "status":
		code := req.Status
		if code <= 0 {
			return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", "status HTTP code required")
		}
		for _, r := range results {
			if r.HTTPStatus == code {
				targets = append(targets, r)
			}
		}
		if len(targets) == 0 {
			return jsonErrorEnvelope(http.StatusBadRequest, "no_targets", fmt.Sprintf("no results with HTTP %d", code))
		}
	default:
		return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", "mode must be candidates, names, or status")
	}

	// Enrich paths via host.auth.list when possible.
	if list, err := callHostAuthList(); err == nil {
		byName := map[string]authFile{}
		for _, f := range list.Files {
			for _, k := range []string{f.Name, f.ID, f.AuthIndex, filepath.Base(f.Path)} {
				if strings.TrimSpace(k) != "" {
					byName[k] = f
				}
			}
		}
		for i := range targets {
			t := &targets[i]
			if f, ok := byName[firstNonEmpty(t.Name, t.File, t.AuthIndex, filepath.Base(t.Path))]; ok {
				if t.AuthIndex == "" {
					t.AuthIndex = f.AuthIndex
				}
				if t.Path == "" && f.Path != "" {
					t.Path = f.Path
				}
				if t.Name == "" {
					t.Name = firstNonEmpty(f.Name, f.ID)
				}
			}
			if t.AuthIndex != "" {
				if got, err := callHostAuthGet(t.AuthIndex); err == nil && strings.TrimSpace(got.Path) != "" {
					t.Path = got.Path
				}
			}
		}
	}

	resp := deleteResponse{}
	deletedKeys := map[string]bool{}
	for _, t := range targets {
		paths := candidateDeletePaths(t)
		removedAny := false
		var lastErr error

		// Prefer host-managed delete when auth_index is known.
		if t.AuthIndex != "" {
			for _, method := range []string{"host.auth.delete", "host.auth.remove", "host.auth.file.delete"} {
				if _, err := hostCaller(method, map[string]any{
					"auth_index": t.AuthIndex,
					"name":       firstNonEmpty(t.Name, t.File),
					"path":       t.Path,
				}); err == nil {
					removedAny = true
					break
				} else {
					lastErr = err
				}
			}
		}

		for _, path := range paths {
			if path == "" {
				continue
			}
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				lastErr = err
				continue
			}
			if err := os.Remove(path); err != nil {
				lastErr = err
				continue
			}
			if _, err := os.Stat(path); err == nil {
				lastErr = fmt.Errorf("file still exists after remove: %s", path)
				continue
			}
			removedAny = true
			resp.DeletedPaths = append(resp.DeletedPaths, path)
		}

		key := firstNonEmpty(t.Name, t.File, t.Path, t.AuthIndex)
		if removedAny {
			resp.Deleted++
			deletedKeys[key] = true
			deletedKeys[filepath.Base(key)] = true
		} else {
			resp.Failed++
			msg := key
			if lastErr != nil {
				msg += ": " + lastErr.Error()
			} else if len(paths) == 0 {
				msg += ": empty path (host 未返回 path，磁盘也未找到)"
			} else {
				msg += ": not found on disk: " + strings.Join(paths, " | ")
			}
			resp.Errors = append(resp.Errors, msg)
		}
	}
	if resp.Deleted > 0 {
		job.mu.Lock()
		kept := make([]probeResult, 0, len(job.results))
		for _, r := range job.results {
			key := firstNonEmpty(r.Name, r.File, r.Path, r.AuthIndex)
			base := filepath.Base(key)
			if deletedKeys[key] || deletedKeys[base] || deletedKeys[r.Name] || deletedKeys[r.File] || deletedKeys[r.AuthIndex] {
				continue
			}
			kept = append(kept, r)
		}
		job.results = kept
		leftCand := 0
		for _, r := range kept {
			if r.Action == "DELETE_CANDIDATE" {
				leftCand++
			}
		}
		job.message = fmt.Sprintf("deleted %d file(s), candidates left=%d", resp.Deleted, leftCand)
		job.mu.Unlock()
		saveHistory()
	}
	return jsonManagementEnvelope(http.StatusOK, resp)
}

func candidateDeletePaths(t probeResult) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		cands := []string{p}
		if !strings.HasSuffix(strings.ToLower(p), ".json") {
			cands = append(cands, p+".json")
		}
		for _, c := range cands {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	add(t.Path)
	name := firstNonEmpty(t.Name, t.File, filepath.Base(t.Path))
	if name != "" {
		base := filepath.Base(name)
		for _, dir := range authSearchDirs() {
			add(filepath.Join(dir, base))
			add(filepath.Join(dir, name))
		}
	}
	return out
}

func authSearchDirs() []string {
	dirs := []string{
		`C:\CLIProxyAPI-local\auths-xai-test`,
		`C:\CLIProxyAPI-local\auths`,
		`/root/.cli-proxy-api`,
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs,
			filepath.Join(wd, "auths-xai-test"),
			filepath.Join(wd, "auths"),
			filepath.Join(wd, "auth-dir"),
		)
	}
	return dirs
}

func historyCandidates() []string {
	var out []string
	if wd, err := os.Getwd(); err == nil {
		out = append(out,
			filepath.Join(wd, "plugins", "grok-manager", historyFileName),
			filepath.Join(wd, "plugins-data", "grok-manager", historyFileName),
		)
	}
	out = append(out,
		filepath.Join(`C:\CLIProxyAPI-local\plugins\grok-manager`, historyFileName),
		filepath.Join(`/root/.cli-proxy-api/plugins/grok-manager`, historyFileName),
	)
	return out
}

func resolveHistoryPath() string {
	job.mu.Lock()
	if job.historyPath != "" {
		p := job.historyPath
		job.mu.Unlock()
		return p
	}
	job.mu.Unlock()
	for _, p := range historyCandidates() {
		if st, err := os.Stat(filepath.Dir(p)); err == nil && st.IsDir() {
			return p
		}
	}
	p := historyCandidates()[0]
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return p
}

func loadHistoryOnStart() {
	for _, p := range historyCandidates() {
		raw, err := os.ReadFile(p)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var saved persistedScan
		if err := json.Unmarshal(raw, &saved); err != nil {
			continue
		}
		if len(saved.Results) == 0 && saved.Total == 0 {
			continue
		}
		vaultEmails := vaultEmailSet()
		enriched := make([]probeResult, len(saved.Results))
		for i := range saved.Results {
			enriched[i] = saved.Results[i]
			enrichProbeResult(&enriched[i], vaultEmails)
		}
		job.mu.Lock()
		if job.running {
			job.mu.Unlock()
			return
		}
		// Prefer the file that actually has results; don't clobber a richer in-memory set.
		if len(job.results) > len(saved.Results) {
			job.mu.Unlock()
			return
		}
		job.historyPath = p
		job.persisted = true
		job.historySavedAt = saved.SavedAt
		job.state = firstNonEmpty(saved.State, "done")
		if job.state == "running" {
			job.state = "done"
			if saved.Done >= saved.Total && saved.Total > 0 {
				job.message = firstNonEmpty(saved.Message, "loaded last scan history (was mid-save)")
			} else {
				job.message = firstNonEmpty(saved.Message, "loaded last scan history")
			}
		} else {
			job.message = firstNonEmpty(saved.Message, "loaded last scan history")
		}
		job.errText = saved.Error
		job.total = saved.Total
		done := saved.Done
		if done == 0 && len(saved.Results) > 0 {
			done = len(saved.Results)
		}
		atomic.StoreInt64(&job.done, int64(done))
		job.workers = saved.Workers
		job.results = enriched
		if saved.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, saved.StartedAt); err == nil {
				job.startedAt = t
			}
		}
		if saved.FinishedAt != "" {
			if t, err := time.Parse(time.RFC3339, saved.FinishedAt); err == nil {
				job.finishedAt = t
			}
		}
		if len(saved.DeleteStatuses) > 0 {
			job.deleteStatuses = map[int]bool{}
			for _, s := range saved.DeleteStatuses {
				job.deleteStatuses[s] = true
			}
		}
		job.mu.Unlock()
		// Seed credential-list "最近测活" from last full scan.
		rememberProbeFromScanResults(enriched)
		return
	}
}

// reloadHistoryIfEmpty re-reads disk when memory has no results (common after page refresh
// if the plugin was re-inited without going through historyLoadOnce again in same process,
// or if a previous version failed to load). Forces a re-scan of candidate paths.
func reloadHistoryIfEmpty() {
	job.mu.Lock()
	empty := !job.running && len(job.results) == 0
	job.mu.Unlock()
	if !empty {
		return
	}
	// Bypass Once: try load again from disk.
	for _, p := range historyCandidates() {
		raw, err := os.ReadFile(p)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var saved persistedScan
		if err := json.Unmarshal(raw, &saved); err != nil || len(saved.Results) == 0 {
			continue
		}
		vaultEmails := vaultEmailSet()
		enriched := make([]probeResult, len(saved.Results))
		for i := range saved.Results {
			enriched[i] = saved.Results[i]
			enrichProbeResult(&enriched[i], vaultEmails)
		}
		job.mu.Lock()
		if job.running || len(job.results) > 0 {
			job.mu.Unlock()
			return
		}
		job.historyPath = p
		job.persisted = true
		job.historySavedAt = saved.SavedAt
		job.state = firstNonEmpty(saved.State, "done")
		if job.state == "running" {
			job.state = "done"
		}
		job.message = firstNonEmpty(saved.Message, "reloaded last-scan.json after empty memory")
		job.errText = saved.Error
		job.total = saved.Total
		done := saved.Done
		if done == 0 {
			done = len(saved.Results)
		}
		atomic.StoreInt64(&job.done, int64(done))
		job.workers = saved.Workers
		job.results = enriched
		if saved.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, saved.StartedAt); err == nil {
				job.startedAt = t
			}
		}
		if saved.FinishedAt != "" {
			if t, err := time.Parse(time.RFC3339, saved.FinishedAt); err == nil {
				job.finishedAt = t
			}
		}
		job.mu.Unlock()
		return
	}
}

func saveHistory() {
	path := resolveHistoryPath()
	job.mu.Lock()
	statuses := make([]int, 0, len(job.deleteStatuses))
	for s := range job.deleteStatuses {
		statuses = append(statuses, s)
	}
	sort.Ints(statuses)
	// Never persist "running" as final state when done==total — avoids empty-looking reloads.
	state := job.state
	if state == "running" && int(atomic.LoadInt64(&job.done)) >= job.total && job.total > 0 && len(job.results) >= job.total {
		state = "done"
	}
	savedAt := time.Now().UTC().Format(time.RFC3339)
	payload := persistedScan{
		SavedAt:        savedAt,
		State:          state,
		Message:        job.message,
		Error:          job.errText,
		Total:          job.total,
		Done:           int(atomic.LoadInt64(&job.done)),
		Workers:        job.workers,
		DeleteStatuses: statuses,
		Results:        append([]probeResult(nil), job.results...),
	}
	if !job.startedAt.IsZero() {
		payload.StartedAt = job.startedAt.Format(time.RFC3339)
	}
	if !job.finishedAt.IsZero() {
		payload.FinishedAt = job.finishedAt.Format(time.RFC3339)
	} else if state == "done" || state == "stopped" || state == "error" {
		payload.FinishedAt = savedAt
	}
	job.mu.Unlock()

	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.WriteFile(path, raw, 0o644)
		_ = os.Remove(tmp)
	}
	job.mu.Lock()
	job.historyPath = path
	job.persisted = true
	job.historySavedAt = savedAt
	job.mu.Unlock()
}

// buildJobSummary enriches results and computes summary under job.mu (caller holds lock).
func buildJobSummary(vaultEmails map[string]bool) (sum scanSummary, results []probeResult, cands []probeResult) {
	sum = scanSummary{HTTP: map[string]int{}, ByStatus: map[string]int{}}
	results = make([]probeResult, len(job.results))
	cands = make([]probeResult, 0)
	for i, r := range job.results {
		enrichProbeResult(&r, vaultEmails)
		results[i] = r
		switch r.Action {
		case "OK":
			sum.OK++
		case "DELETE_CANDIDATE":
			sum.DeleteCandidates++
			cands = append(cands, r)
		case "ERROR":
			sum.Errors++
		default:
			sum.Kept++
		}
		if r.HTTPStatus > 0 {
			sum.HTTP[strconv.Itoa(r.HTTPStatus)]++
		}
		st := r.Status
		if st == "" {
			st = "unknown"
		}
		sum.ByStatus[st]++
		if st == "unauthorized" {
			if r.HasVaultSSO {
				sum.VaultMatch401++
			} else {
				sum.VaultMiss401++
			}
		}
	}
	return sum, results, cands
}

// snapshotJob returns scan status. When includeLists is false (default for /status),
// results/candidates arrays are omitted so polling stays small.
func snapshotJob(includeLists bool) jobSnapshot {
	vaultEmails := vaultEmailSet()
	vpath, vcount, vsaved := vaultMeta()
	sch := scheduleSnapshot()

	job.mu.Lock()
	defer job.mu.Unlock()
	sum, results, cands := buildJobSummary(vaultEmails)
	statuses := make([]int, 0, len(job.deleteStatuses))
	for s := range job.deleteStatuses {
		statuses = append(statuses, s)
	}
	sort.Ints(statuses)
	snap := jobSnapshot{
		State:          job.state,
		Message:        job.message,
		Error:          job.errText,
		Total:          job.total,
		Done:           int(atomic.LoadInt64(&job.done)),
		Workers:        job.workers,
		Summary:        sum,
		DeleteStatuses: statuses,
		HistoryPath:    job.historyPath,
		Persisted:      job.persisted,
		HistorySavedAt: job.historySavedAt,
		ResultCount:    len(job.results),
		PluginVersion:  pluginVersion,
		VaultPath:      vpath,
		VaultCount:     vcount,
		VaultSavedAt:   vsaved,
		Schedule:       sch,
		ScanSync:       job.lastScanSync,
	}
	if includeLists {
		sort.Slice(cands, func(i, j int) bool {
			if cands[i].HTTPStatus != cands[j].HTTPStatus {
				return cands[i].HTTPStatus < cands[j].HTTPStatus
			}
			return cands[i].Email < cands[j].Email
		})
		if len(cands) > 500 {
			cands = cands[:500]
		}
		const maxResults = 5000
		if len(results) > maxResults {
			results = results[:maxResults]
		}
		snap.Candidates = cands
		snap.Results = append([]probeResult(nil), results...)
	}
	if !job.startedAt.IsZero() {
		snap.StartedAt = job.startedAt.Format(time.RFC3339)
	}
	if !job.finishedAt.IsZero() {
		snap.FinishedAt = job.finishedAt.Format(time.RFC3339)
	}
	return snap
}

type resultsPage struct {
	Results   []probeResult  `json:"results"`
	Page      int            `json:"page"`
	PageSize  int            `json:"page_size"`
	Pages     int            `json:"pages"`
	Match     int            `json:"match"`
	Total     int            `json:"total"`
	Filter    string         `json:"filter"`
	Q         string         `json:"q,omitempty"`
	Counts    map[string]int `json:"counts"`
	Summary   scanSummary    `json:"summary"`
	State     string         `json:"state"`
	ResultCnt int            `json:"result_count"`
}

func matchScanFilter(r probeResult, filter string) bool {
	st := r.Status
	if st == "" {
		st = "unknown"
	}
	switch filter {
	case "", "all":
		return true
	case "cand":
		return r.Action == "DELETE_CANDIDATE"
	case "healthy", "unauthorized", "forbidden", "payment", "rate_limited", "network", "error":
		return st == filter
	case "vault_miss":
		return st == "unauthorized" && !r.HasVaultSSO
	case "vault_hit":
		return st == "unauthorized" && r.HasVaultSSO
	default:
		return true
	}
}

func matchScanQuery(r probeResult, q string) bool {
	if q == "" {
		return true
	}
	q = strings.ToLower(q)
	fields := []string{r.Email, r.Name, r.File, r.AuthIndex, r.Path, r.Advice, r.Summary, r.Error, r.Status}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// authFileRow is one credential file for the scan-page "凭证列表" tab.
type authFileRow struct {
	Email       string `json:"email,omitempty"`
	Name        string `json:"name,omitempty"`
	Path        string `json:"path,omitempty"`
	AuthIndex   string `json:"auth_index,omitempty"`
	ID          string `json:"id,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Disabled    bool   `json:"disabled"`
	BanCode     int    `json:"ban_code,omitempty"`
	BanSource   string `json:"ban_source,omitempty"`
	BanRemain   string `json:"ban_remain,omitempty"`
	ScanStatus  string `json:"scan_status,omitempty"`
	ScanHTTP    int    `json:"scan_http,omitempty"`
	ScanAction  string `json:"scan_action,omitempty"` // still_429 | unbanned | reclassified | skipped | ok
	ScanDetail  string `json:"scan_detail,omitempty"`
	ScanAt      string `json:"scan_at,omitempty"`
}

// lastProbeHit is the latest live-probe outcome for a credential key (email/name/id).
type lastProbeHit struct {
	HTTP   int    `json:"http"`
	Status string `json:"status,omitempty"`
	Action string `json:"action,omitempty"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at,omitempty"`
}

var (
	lastProbeMu   sync.RWMutex
	lastProbeByKey = map[string]lastProbeHit{} // lower-case keys
)

func rememberProbeHit(keys []string, hit lastProbeHit) {
	if hit.At == "" {
		hit.At = time.Now().Format(time.RFC3339)
	}
	lastProbeMu.Lock()
	defer lastProbeMu.Unlock()
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		lastProbeByKey[k] = hit
	}
}

func lookupProbeHit(keys ...string) (lastProbeHit, bool) {
	lastProbeMu.RLock()
	defer lastProbeMu.RUnlock()
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if h, ok := lastProbeByKey[k]; ok {
			return h, true
		}
	}
	return lastProbeHit{}, false
}

// rebuildLastProbeFromHistory fills cache from newest probe-history sessions first.
func rebuildLastProbeFromHistory() {
	probeHistMu.Lock()
	sessions := append([]recheck429Result(nil), probeHist...)
	probeHistMu.Unlock()
	// oldest first so newer overwrites
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i]
		at := s.LastRun
		for _, d := range s.Details {
			st := ""
			if d.HTTPStatus >= 200 && d.HTTPStatus < 300 {
				st = "healthy"
			} else if d.HTTPStatus == 401 {
				st = "unauthorized"
			} else if d.HTTPStatus == 402 {
				st = "payment"
			} else if d.HTTPStatus == 403 {
				st = "forbidden"
			} else if d.HTTPStatus == 429 {
				st = "rate_limited"
			}
			rememberProbeHit([]string{d.Email, d.AuthID}, lastProbeHit{
				HTTP: d.HTTPStatus, Status: st, Action: d.Action, Detail: d.Detail, At: at,
			})
		}
	}
}

// rememberProbeFromScanResults indexes full-scan rows into last-probe cache.
func rememberProbeFromScanResults(results []probeResult) {
	at := time.Now().Format(time.RFC3339)
	for _, r := range results {
		st := firstNonEmpty(r.Status, classifyAccountStatus(r))
		act := r.Action
		if act == "OK" || st == "healthy" {
			act = "ok"
		}
		rememberProbeHit(
			[]string{r.Email, r.Name, r.File, r.AuthID, r.AuthIndex, filepath.Base(r.Path)},
			lastProbeHit{HTTP: r.HTTPStatus, Status: st, Action: act, Detail: firstNonEmpty(r.Advice, r.Summary, r.Error), At: at},
		)
	}
}

func handleAuthFiles(query url.Values) ([]byte, error) {
	pq := parsePageQuery(query)
	list, err := callHostAuthList()
	if err != nil {
		return jsonErrorEnvelope(http.StatusBadGateway, "auth_list_failed", err.Error())
	}

	// Optional last-scan index by email / filename for quick status.
	ensureHistoryLoaded()
	reloadHistoryIfEmpty()
	scanByEmail := map[string]probeResult{}
	scanByName := map[string]probeResult{}
	job.mu.Lock()
	for _, r := range job.results {
		if em := strings.ToLower(strings.TrimSpace(r.Email)); em != "" {
			scanByEmail[em] = r
		}
		for _, k := range []string{r.Name, r.File, filepath.Base(r.Path)} {
			k = strings.ToLower(strings.TrimSpace(k))
			if k != "" {
				scanByName[k] = r
			}
		}
	}
	job.mu.Unlock()

	now := time.Now()
	bans := runtimeBans.snapshot(now)

	rows := make([]authFileRow, 0, len(list.Files))
	disabledN, bannedN := 0, 0
	for _, f := range list.Files {
		if !isXAIAuth(f) {
			continue
		}
		email := strings.TrimSpace(firstNonEmpty(f.Email, f.Account))
		name := firstNonEmpty(f.Name, filepath.Base(f.Path), f.ID)
		row := authFileRow{
			Email:     email,
			Name:      name,
			Path:      f.Path,
			AuthIndex: firstNonEmpty(f.AuthIndex, f.ID),
			ID:        f.ID,
			Provider:  firstNonEmpty(f.Provider, f.Type, "xai"),
			Disabled:  f.Disabled,
		}
		if f.Disabled {
			disabledN++
		}
		// ban overlay
		emKey := strings.ToLower(email)
		for _, key := range []string{emKey, name, strings.ToLower(name), f.ID, f.AuthIndex, filepath.Base(f.Path)} {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if e, ok := bans[key]; ok {
				row.BanCode = e.StatusCode
				row.BanSource = e.Source
				if e.ResetAt.After(now) {
					row.BanRemain = formatRemain(e.ResetAt.Sub(now))
				}
				bannedN++
				break
			}
			// try email index via clearByEmail style - scan bans by email field
		}
		if row.BanCode == 0 && emKey != "" {
			for _, e := range bans {
				if strings.ToLower(strings.TrimSpace(e.Email)) == emKey {
					row.BanCode = e.StatusCode
					row.BanSource = e.Source
					if e.ResetAt.After(now) {
						row.BanRemain = formatRemain(e.ResetAt.Sub(now))
					}
					bannedN++
					break
				}
			}
		}
		// recent probe overlay: prefer live-probe/history cache, then full-scan results
		if h, ok := lookupProbeHit(emKey, name, strings.ToLower(name), f.ID, f.AuthIndex, filepath.Base(f.Path)); ok {
			row.ScanHTTP = h.HTTP
			row.ScanStatus = h.Status
			row.ScanAction = h.Action
			row.ScanDetail = h.Detail
			row.ScanAt = h.At
		} else if r, ok := scanByEmail[emKey]; ok {
			row.ScanStatus = firstNonEmpty(r.Status, classifyAccountStatus(r))
			row.ScanHTTP = r.HTTPStatus
			row.ScanAction = r.Action
			row.ScanDetail = firstNonEmpty(r.Advice, r.Summary, r.Error)
		} else if r, ok := scanByName[strings.ToLower(name)]; ok {
			row.ScanStatus = firstNonEmpty(r.Status, classifyAccountStatus(r))
			row.ScanHTTP = r.HTTPStatus
			row.ScanAction = r.Action
			row.ScanDetail = firstNonEmpty(r.Advice, r.Summary, r.Error)
		}

		if pq.Q != "" {
			ql := strings.ToLower(pq.Q)
			blob := strings.ToLower(strings.Join([]string{row.Email, row.Name, row.Path, row.ID, row.AuthIndex}, " "))
			if !strings.Contains(blob, ql) {
				continue
			}
		}
		// filter: all | enabled | disabled | banned
		switch strings.ToLower(strings.TrimSpace(pq.Filter)) {
		case "enabled":
			if f.Disabled {
				continue
			}
		case "disabled":
			if !f.Disabled {
				continue
			}
		case "banned":
			if row.BanCode == 0 {
				continue
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Email != rows[j].Email {
			return rows[i].Email < rows[j].Email
		}
		return rows[i].Name < rows[j].Name
	})

	// keys_only=1: return all matching selection keys (for "select all filtered").
	keysOnly := false
	if query != nil {
		v := strings.ToLower(strings.TrimSpace(query.Get("keys_only")))
		keysOnly = v == "1" || v == "true" || v == "yes"
	}
	if keysOnly {
		keys := make([]string, 0, len(rows))
		for _, row := range rows {
			k := strings.TrimSpace(firstNonEmpty(row.Email, row.Name, row.ID, row.AuthIndex, row.Path))
			if k != "" {
				keys = append(keys, k)
			}
		}
		return jsonManagementEnvelope(http.StatusOK, map[string]any{
			"ok":        true,
			"keys_only": true,
			"keys":      keys,
			"match":     len(keys),
			"filter":    pq.Filter,
			"q":         pq.Q,
		})
	}

	pageItems, match, pages, page := slicePage(rows, pq.Page, pq.PageSize)
	return jsonManagementEnvelope(http.StatusOK, map[string]any{
		"files":     pageItems,
		"page":      page,
		"page_size": pq.PageSize,
		"pages":     pages,
		"match":     match,
		"total":     match, // after filter
		"all_total": len(list.Files),
		"xai_total": func() int {
			n := 0
			for _, f := range list.Files {
				if isXAIAuth(f) {
					n++
				}
			}
			return n
		}(),
		"disabled": disabledN,
		"banned":   bannedN,
		"filter":   pq.Filter,
		"q":        pq.Q,
	})
}

func formatRemain(d time.Duration) string {
	if d < 0 {
		return "0"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h >= 48 {
		return fmt.Sprintf("%dd%dh", h/24, h%24)
	}
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func snapshotResults(query url.Values) resultsPage {
	pq := parsePageQuery(query)
	vaultEmails := vaultEmailSet()

	job.mu.Lock()
	sum, results, _ := buildJobSummary(vaultEmails)
	state := job.state
	totalAll := len(job.results)
	job.mu.Unlock()

	counts := map[string]int{
		"all": totalAll, "cand": sum.DeleteCandidates,
		"healthy": sum.ByStatus["healthy"], "unauthorized": sum.ByStatus["unauthorized"],
		"forbidden": sum.ByStatus["forbidden"], "payment": sum.ByStatus["payment"],
		"rate_limited": sum.ByStatus["rate_limited"],
		"vault_miss":   sum.VaultMiss401, "vault_hit": sum.VaultMatch401,
	}

	filtered := make([]probeResult, 0, len(results))
	for _, r := range results {
		if !matchScanFilter(r, pq.Filter) {
			continue
		}
		if !matchScanQuery(r, pq.Q) {
			continue
		}
		filtered = append(filtered, r)
	}
	pageItems, match, pages, page := slicePage(filtered, pq.Page, pq.PageSize)
	return resultsPage{
		Results:   pageItems,
		Page:      page,
		PageSize:  pq.PageSize,
		Pages:     pages,
		Match:     match,
		Total:     totalAll,
		Filter:    pq.Filter,
		Q:         pq.Q,
		Counts:    counts,
		Summary:   sum,
		State:     state,
		ResultCnt: totalAll,
	}
}

var (
	authListCacheMu   sync.Mutex
	authListCache     authListResponse
	authListCacheAt   time.Time
	authListCacheTTL  = 8 * time.Second
)

// invalidateAuthListCache forces next callHostAuthList to hit host again.
func invalidateAuthListCache() {
	authListCacheMu.Lock()
	authListCacheAt = time.Time{}
	authListCache = authListResponse{}
	authListCacheMu.Unlock()
}

func callHostAuthList() (authListResponse, error) {
	authListCacheMu.Lock()
	if !authListCacheAt.IsZero() && time.Since(authListCacheAt) < authListCacheTTL && len(authListCache.Files) > 0 {
		cp := authListCache
		authListCacheMu.Unlock()
		return cp, nil
	}
	authListCacheMu.Unlock()

	result, err := hostCaller("host.auth.list", map[string]any{})
	if err != nil {
		return authListResponse{}, err
	}
	var resp authListResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return authListResponse{}, fmt.Errorf("decode host.auth.list: %w", err)
	}
	authListCacheMu.Lock()
	authListCache = resp
	authListCacheAt = time.Now()
	authListCacheMu.Unlock()
	return resp, nil
}

func callHostAuthGet(authIndex string) (authGetResponse, error) {
	result, err := hostCaller("host.auth.get", authGetRequest{AuthIndex: authIndex})
	if err != nil {
		return authGetResponse{}, err
	}
	var resp authGetResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return authGetResponse{}, fmt.Errorf("decode host.auth.get: %w", err)
	}
	return resp, nil
}

func callHostUnavailable(method string, payload any) (json.RawMessage, error) {
	return nil, fmt.Errorf("host caller is not configured for method %s", method)
}

func callHost(method string, payload any) (json.RawMessage, error) {
	return callHostUnavailable(method, payload)
}

// DecodeHostEnvelope parses a host RPC envelope and returns result JSON.
func DecodeHostEnvelope(raw []byte) (json.RawMessage, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback failed")
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func isXAIAuth(file authFile) bool {
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(file.Provider, file.Type)))
	name := strings.ToLower(firstNonEmpty(file.Name, file.ID))
	return provider == xaiProvider || provider == "x-ai" || provider == "grok" ||
		strings.HasPrefix(name, "xai-") || strings.HasPrefix(name, "xai.")
}

func compactResponse(reader io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(reader, 2048))
	if err != nil {
		return "read failed: " + err.Error()
	}
	text := strings.Join(strings.Fields(strings.ReplaceAll(string(data), "\r", "")), " ")
	if len(text) > 300 {
		return text[:300] + "..."
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func routeHasSuffix(path, suffix string) bool {
	path = strings.ToLower(strings.TrimRight(strings.TrimSpace(path), "/"))
	suffix = strings.ToLower(strings.TrimRight(strings.TrimSpace(suffix), "/"))
	if suffix == "" {
		return path == ""
	}
	return path == suffix || strings.HasSuffix(path, suffix)
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func jsonManagementEnvelope(statusCode int, v any) ([]byte, error) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{
		StatusCode: statusCode,
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
		Body:       jsonBytes,
	})
}

func jsonErrorEnvelope(statusCode int, code, message string) ([]byte, error) {
	return jsonManagementEnvelope(statusCode, map[string]any{"ok": false, "code": code, "message": message})
}

func methodNotAllowed(allowed []string) ([]byte, error) {
	return okEnvelope(managementResponse{
		StatusCode: http.StatusMethodNotAllowed,
		Headers: http.Header{
			"allow":        []string{strings.Join(allowed, ", ")},
			"content-type": []string{"application/json; charset=utf-8"},
		},
		Body: []byte(`{"ok":false,"code":"method_not_allowed","message":"method not allowed"}`),
	})
}

