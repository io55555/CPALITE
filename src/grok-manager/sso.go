package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	xaiClientID          = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiOIDCIssuer        = "https://auth.x.ai"
	xaiScopes            = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"
	cliproxyBaseURL      = "https://cli-chat-proxy.grok.com/v1"
	cliproxyTokenEP      = xaiOIDCIssuer + "/oauth2/token"
	cliproxyRedirectURI  = "http://127.0.0.1:56121/callback"
	ssoDefaultDelaySec   = 0 // multi-worker 默认不强制串行间隔；单线程时仍可用
	ssoDefaultWorkers    = 4
	ssoMaxWorkers        = 32
	ssoDefaultMaxRetries = 6
)

type ssoCookieItem struct {
	SSO   string `json:"sso"`
	Email string `json:"email,omitempty"`
}

type ssoImportRequest struct {
	SSOList    string          `json:"sso_list"` // multiline: sso or sso|email or sso,email
	Cookies    []ssoCookieItem `json:"cookies"`
	OutDir     string          `json:"out_dir"`
	Workers    int             `json:"workers"`     // concurrent device-flow workers
	DelaySec   int             `json:"delay_sec"`   // stagger between launching tasks (0=full speed)
	MaxRetries int             `json:"max_retries"`
	// SkipIfOK: when true (default), skip convert if xai-*.json already exists and last scan did not mark it 401.
	SkipIfOK *bool `json:"skip_if_ok"`
	// SaveSSO: when true (default), persist SSO cookies into sso-vault.json.
	SaveSSO *bool `json:"save_sso"`
	// Force: force reconvert even if file exists and not 401.
	Force bool `json:"force"`
	// FromVault: ignore sso_list and import entries from durable vault (optionally only emails that are 401).
	FromVault bool `json:"from_vault"`
	// Only401: with FromVault or combined with scan results — only reconvert known 401 accounts.
	Only401 bool `json:"only_401"`
	// DedupeByEmail: keep last SSO per email before import (default true when sso_list provided).
	DedupeByEmail *bool `json:"dedupe_by_email"`
}

type ssoItemResult struct {
	Index    int    `json:"index"`
	Email    string `json:"email,omitempty"`
	File     string `json:"file,omitempty"`
	Path     string `json:"path,omitempty"`
	OK       bool   `json:"ok"`
	Skipped  bool   `json:"skipped,omitempty"`
	Error    string `json:"error,omitempty"`
	Message  string `json:"message,omitempty"`
	ElapsedS int64  `json:"elapsed_s"`
}

type ssoSnapshot struct {
	State       string          `json:"state"`
	Message     string          `json:"message,omitempty"`
	Error       string          `json:"error,omitempty"`
	Total       int             `json:"total"`
	Done        int             `json:"done"`
	OK          int             `json:"ok"`
	Failed      int             `json:"failed"`
	Skipped     int             `json:"skipped"`
	Workers     int             `json:"workers"`
	OutDir      string          `json:"out_dir,omitempty"`
	StartedAt   string          `json:"started_at,omitempty"`
	FinishedAt  string          `json:"finished_at,omitempty"`
	Results     []ssoItemResult `json:"results"`
	Logs        []string        `json:"logs"`
	HistoryPath string          `json:"history_path,omitempty"`
	VaultPath   string          `json:"vault_path,omitempty"`
	VaultCount  int             `json:"vault_count"`
	Persisted   bool            `json:"persisted"`
	SkipIfOK    bool            `json:"skip_if_ok"`
}

type ssoJobState struct {
	mu          sync.Mutex
	running     bool
	cancel      context.CancelFunc
	state       string
	message     string
	errText     string
	total       int
	workers     int
	done        int64
	okCount     int64
	failCount   int64
	skipCount   int64
	outDir      string
	startedAt   time.Time
	finishedAt  time.Time
	results     []ssoItemResult // fixed-size slots; Index==0 means not finished
	logs        []string
	historyPath string
	persisted   bool
	skipIfOK    bool
}

var ssoJob = &ssoJobState{state: "idle"}

func handleStartSSOImport(body []byte) ([]byte, error) {
	var req ssoImportRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", err.Error())
		}
	}
	items := parseSSOItems(req)
	if req.FromVault || (len(items) == 0 && req.Only401) {
		items = vaultItemsForImport(req.Only401)
	}
	dedupe := true
	if req.DedupeByEmail != nil {
		dedupe = *req.DedupeByEmail
	}
	droppedDup := 0
	if dedupe && len(items) > 1 {
		items, droppedDup = dedupeSSOItemsByEmail(items)
	}
	if len(items) == 0 {
		return jsonErrorEnvelope(http.StatusBadRequest, "empty",
			"请提供 sso_list / cookies，或使用 from_vault / only_401 从 SSO 历史库导入")
	}
	outDir := strings.TrimSpace(req.OutDir)
	if outDir == "" {
		outDir = defaultAuthOutDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return jsonErrorEnvelope(http.StatusBadRequest, "out_dir", "无法创建输出目录: "+err.Error())
	}
	if req.DelaySec < 0 {
		req.DelaySec = 0
	}
	if req.Workers < 1 {
		req.Workers = ssoDefaultWorkers
	}
	if req.Workers > ssoMaxWorkers {
		req.Workers = ssoMaxWorkers
	}
	if req.Workers > len(items) {
		req.Workers = len(items)
	}
	// single-thread fallback: keep a mild default stagger if user left delay at 0
	if req.Workers == 1 && req.DelaySec == 0 && ssoDefaultDelaySec > 0 {
		req.DelaySec = ssoDefaultDelaySec
	}
	if req.MaxRetries < 1 {
		req.MaxRetries = ssoDefaultMaxRetries
	}
	skipIfOK := true
	if req.SkipIfOK != nil {
		skipIfOK = *req.SkipIfOK
	}
	if req.Force {
		skipIfOK = false
	}
	saveSSO := true
	if req.SaveSSO != nil {
		saveSSO = *req.SaveSSO
	}
	if saveSSO {
		upsertVaultSSO(items)
	}

	ssoJob.mu.Lock()
	if ssoJob.running {
		ssoJob.mu.Unlock()
		return jsonErrorEnvelope(http.StatusConflict, "busy", "SSO 导入正在进行中")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ssoJob.running = true
	ssoJob.cancel = cancel
	ssoJob.state = "running"
	ssoJob.message = fmt.Sprintf("importing %d sso cookie(s) workers=%d skip_if_ok=%v", len(items), req.Workers, skipIfOK)
	ssoJob.errText = ""
	ssoJob.total = len(items)
	ssoJob.workers = req.Workers
	ssoJob.skipIfOK = skipIfOK
	atomic.StoreInt64(&ssoJob.done, 0)
	atomic.StoreInt64(&ssoJob.okCount, 0)
	atomic.StoreInt64(&ssoJob.failCount, 0)
	atomic.StoreInt64(&ssoJob.skipCount, 0)
	ssoJob.outDir = outDir
	ssoJob.startedAt = time.Now().UTC()
	ssoJob.finishedAt = time.Time{}
	ssoJob.results = make([]ssoItemResult, len(items))
	ssoJob.logs = []string{fmt.Sprintf("start: %d cookies → %s (workers=%d delay=%ds skip_if_ok=%v save_sso=%v dedupe_email_dropped=%d)",
		len(items), outDir, req.Workers, req.DelaySec, skipIfOK, saveSSO, droppedDup)}
	ssoJob.mu.Unlock()

	go runSSOImport(ctx, items, outDir, req.Workers, req.DelaySec, req.MaxRetries, skipIfOK, saveSSO)
	snap := snapshotSSO()
	if droppedDup > 0 {
		snap.Message = fmt.Sprintf("%s · email去重丢弃 %d", snap.Message, droppedDup)
	}
	return jsonManagementEnvelope(http.StatusAccepted, snap)
}

// vaultItemsForImport builds cookie list from durable vault.
// if only401: only emails that last scan marked HTTP 401 (or all vault if no scan data for that email but only401 still filters).
func vaultItemsForImport(only401 bool) []ssoCookieItem {
	vaultMu.Lock()
	v := loadVaultUnlocked()
	vaultMu.Unlock()
	var items []ssoCookieItem
	for _, e := range v.Entries {
		if strings.TrimSpace(e.SSO) == "" {
			continue
		}
		if only401 {
			if e.Email == "" {
				continue
			}
			if !credentialKnown401(e.Email, e.LastFile, e.LastFile) {
				continue
			}
		}
		items = append(items, ssoCookieItem{SSO: e.SSO, Email: e.Email})
	}
	return items
}

// handleRefresh401 starts vault-based reconvert for accounts that scan marked as 401.
// Source is always sso-vault.json (not the textarea). Requires prior import with「保存 SSO 到历史库」.
func handleRefresh401(body []byte) ([]byte, error) {
	var req ssoImportRequest
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	items := vaultItemsForImport(true)
	if len(items) == 0 {
		vpath, vcount, _ := vaultMeta()
		return jsonErrorEnvelope(http.StatusBadRequest, "no_vault_401",
			fmt.Sprintf("SSO 库中没有可重刷的 401 账号。vault=%s count=%d。请先导入并勾选「保存 SSO 到历史库」，再测活出 401。", vpath, vcount))
	}
	req.FromVault = true
	req.Only401 = true
	force := true
	req.Force = force
	skip := false
	req.SkipIfOK = &skip
	ssoLog(fmt.Sprintf("从 SSO 库重刷 401：匹配 %d 条 → vault", len(items)))
	return handleStartSSOImport(mustJSON(req))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func handleStopSSOImport() ([]byte, error) {
	ssoJob.mu.Lock()
	if ssoJob.cancel != nil {
		ssoJob.cancel()
	}
	if ssoJob.running {
		ssoJob.message = "stopping..."
	}
	ssoJob.mu.Unlock()
	return jsonManagementEnvelope(http.StatusOK, snapshotSSO())
}

func snapshotSSO() ssoSnapshot {
	ssoJob.mu.Lock()
	// only include finished slots (Index > 0), already ordered by original index
	finished := make([]ssoItemResult, 0, len(ssoJob.results))
	skipped := int(atomic.LoadInt64(&ssoJob.skipCount))
	for _, r := range ssoJob.results {
		if r.Index > 0 {
			finished = append(finished, r)
		}
	}
	snap := ssoSnapshot{
		State:       ssoJob.state,
		Message:     ssoJob.message,
		Error:       ssoJob.errText,
		Total:       ssoJob.total,
		Done:        int(atomic.LoadInt64(&ssoJob.done)),
		OK:          int(atomic.LoadInt64(&ssoJob.okCount)),
		Failed:      int(atomic.LoadInt64(&ssoJob.failCount)),
		Skipped:     skipped,
		Workers:     ssoJob.workers,
		OutDir:      ssoJob.outDir,
		Results:     finished,
		Logs:        append([]string(nil), ssoJob.logs...),
		HistoryPath: ssoJob.historyPath,
		Persisted:   ssoJob.persisted,
		SkipIfOK:    ssoJob.skipIfOK,
	}
	if !ssoJob.startedAt.IsZero() {
		snap.StartedAt = ssoJob.startedAt.Format(time.RFC3339)
	}
	if !ssoJob.finishedAt.IsZero() {
		snap.FinishedAt = ssoJob.finishedAt.Format(time.RFC3339)
	}
	// cap logs for response size
	if len(snap.Logs) > 200 {
		snap.Logs = snap.Logs[len(snap.Logs)-200:]
	}
	ssoJob.mu.Unlock()

	vaultMu.Lock()
	v := loadVaultUnlocked()
	snap.VaultCount = len(v.Entries)
	snap.VaultPath = resolvePluginDataPath(ssoVaultFileName, &vaultPath)
	vaultMu.Unlock()
	if snap.HistoryPath == "" {
		snap.HistoryPath = resolveSSOHistoryPath()
	}
	return snap
}

func parseSSOItems(req ssoImportRequest) []ssoCookieItem {
	var items []ssoCookieItem
	seen := map[string]bool{}
	add := func(sso, email string) {
		sso = strings.TrimSpace(sso)
		email = strings.TrimSpace(email)
		if sso == "" {
			return
		}
		// strip optional "sso=" prefix
		if strings.HasPrefix(strings.ToLower(sso), "sso=") {
			sso = strings.TrimSpace(sso[4:])
		}
		// cookie value only — never keep "email----" prefix accidentally
		if i := strings.Index(sso, "----"); i >= 0 {
			left, right := strings.TrimSpace(sso[:i]), strings.TrimSpace(sso[i+4:])
			if looksLikeJWT(right) {
				if email == "" && strings.Contains(left, "@") {
					email = left
				}
				sso = right
			} else if looksLikeJWT(left) {
				if email == "" && strings.Contains(right, "@") {
					email = right
				}
				sso = left
			}
		}
		if !looksLikeJWT(sso) && !strings.HasPrefix(sso, "eyJ") {
			// still allow non-JWT cookies, but reject pure emails
			if strings.Contains(sso, "@") && !strings.Contains(sso, ".") {
				return
			}
			if strings.Contains(sso, "@") && len(sso) < 80 {
				return
			}
		}
		key := sso
		if seen[key] {
			return
		}
		seen[key] = true
		items = append(items, ssoCookieItem{SSO: sso, Email: email})
	}
	for _, c := range req.Cookies {
		add(c.SSO, c.Email)
	}
	for _, line := range strings.Split(req.SSOList, "\n") {
		// strip UTF-8 BOM / zero-width chars
		line = strings.TrimPrefix(line, "\ufeff")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// formats:
		//   email----sso          (common export: accounts_sso_import.txt)
		//   sso----email
		//   email----pass----sso
		//   sso|email / sso,email / sso\temail / sso email@x.com
		//   bare sso JWT
		var sso, email string
		switch {
		case strings.Contains(line, "----"):
			parts := splitDashSep(line)
			// prefer JWT-looking segment as sso; email-looking as email
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if looksLikeJWT(p) && sso == "" {
					sso = p
					continue
				}
				if strings.Contains(p, "@") && email == "" {
					email = p
					continue
				}
			}
			// fallback: last part is sso if JWT, else first JWT-ish
			if sso == "" && len(parts) > 0 {
				last := strings.TrimSpace(parts[len(parts)-1])
				if looksLikeJWT(last) || len(last) > 40 {
					sso = last
				}
			}
			if email == "" && len(parts) > 0 {
				first := strings.TrimSpace(parts[0])
				if strings.Contains(first, "@") {
					email = first
				}
			}
		case strings.Contains(line, "|"):
			parts := strings.SplitN(line, "|", 2)
			left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if looksLikeJWT(left) || (!strings.Contains(left, "@") && strings.Contains(right, "@")) {
				sso, email = left, right
			} else if looksLikeJWT(right) {
				email, sso = left, right
			} else {
				sso, email = left, right
			}
		case strings.Contains(line, "\t"):
			parts := strings.SplitN(line, "\t", 2)
			left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if looksLikeJWT(right) || strings.Contains(left, "@") {
				email, sso = left, right
			} else {
				sso, email = left, right
			}
		case strings.Count(line, ",") == 1 && !strings.HasPrefix(line, "eyJ"):
			parts := strings.SplitN(line, ",", 2)
			left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			if looksLikeJWT(right) || strings.Contains(left, "@") {
				email, sso = left, right
			} else {
				sso, email = left, right
			}
		default:
			// "sso email@x.com" or bare sso
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.Contains(fields[len(fields)-1], "@") {
				email = fields[len(fields)-1]
				sso = strings.Join(fields[:len(fields)-1], "")
			} else if len(fields) >= 2 && strings.Contains(fields[0], "@") && looksLikeJWT(fields[len(fields)-1]) {
				email = fields[0]
				sso = fields[len(fields)-1]
			} else {
				sso = line
			}
		}
		add(sso, email)
	}
	return items
}

func splitDashSep(line string) []string {
	// split on 3+ consecutive dashes (--- or ----)
	var parts []string
	cur := strings.Builder{}
	dash := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '-' {
			dash++
			continue
		}
		if dash > 0 {
			if dash >= 3 {
				parts = append(parts, cur.String())
				cur.Reset()
			} else {
				for j := 0; j < dash; j++ {
					cur.WriteByte('-')
				}
			}
			dash = 0
		}
		cur.WriteByte(line[i])
	}
	if dash >= 3 {
		parts = append(parts, cur.String())
	} else {
		for j := 0; j < dash; j++ {
			cur.WriteByte('-')
		}
		if cur.Len() > 0 {
			parts = append(parts, cur.String())
		}
	}
	return parts
}

func looksLikeJWT(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "eyJ") {
		return strings.Count(s, ".") >= 2
	}
	// generic JWT-ish: three base64url segments
	parts := strings.Split(s, ".")
	return len(parts) == 3 && len(parts[0]) > 10 && len(parts[1]) > 10
}

func defaultAuthOutDir() string {
	for _, d := range authSearchDirs() {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "auths-xai-test")
	}
	return `C:\CLIProxyAPI-local\auths-xai-test`
}

func ssoLog(msg string) {
	ssoJob.mu.Lock()
	ssoJob.logs = append(ssoJob.logs, time.Now().Format("15:04:05")+" "+msg)
	if len(ssoJob.logs) > 500 {
		ssoJob.logs = ssoJob.logs[len(ssoJob.logs)-500:]
	}
	ssoJob.message = msg
	ssoJob.mu.Unlock()
}

func runSSOImport(ctx context.Context, items []ssoCookieItem, outDir string, workers, delaySec, maxRetries int, skipIfOK, saveSSO bool) {
	defer func() {
		ssoJob.mu.Lock()
		ssoJob.running = false
		ssoJob.cancel = nil
		if ssoJob.state == "running" {
			ssoJob.state = "done"
		}
		ssoJob.finishedAt = time.Now().UTC()
		if ssoJob.message == "" {
			ssoJob.message = "completed"
		}
		ssoJob.mu.Unlock()
		saveSSOHistory()
	}()

	if workers < 1 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}

	type job struct {
		idx  int // 0-based
		item ssoCookieItem
	}
	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	workerFn := func(workerID int) {
		defer wg.Done()
		for j := range jobs {
			if ctx.Err() != nil {
				return
			}
			started := time.Now()
			res := ssoItemResult{Index: j.idx + 1, Email: j.item.Email}

			// Pre-check: if email known and credential exists without 401 → skip Device Flow.
			if skipIfOK && strings.TrimSpace(j.item.Email) != "" {
				if skip, path, reason := shouldSkipConvert(outDir, j.item.Email); skip {
					res.OK = true
					res.Skipped = true
					res.Path = path
					res.File = filepath.Base(path)
					res.Message = "skipped: " + reason
					res.ElapsedS = int64(time.Since(started).Seconds())
					atomic.AddInt64(&ssoJob.okCount, 1)
					atomic.AddInt64(&ssoJob.skipCount, 1)
					if saveSSO {
						markVaultSkipped(j.item.Email, j.item.SSO, res.File)
					}
					ssoLog(fmt.Sprintf("[%d/%d] w%d 跳过 %s (%s)", j.idx+1, len(items), workerID, j.item.Email, reason))
					ssoJob.mu.Lock()
					if j.idx >= 0 && j.idx < len(ssoJob.results) {
						ssoJob.results[j.idx] = res
					}
					ssoJob.mu.Unlock()
					atomic.AddInt64(&ssoJob.done, 1)
					continue
				} else if reason == "known_401" {
					ssoLog(fmt.Sprintf("[%d/%d] w%d 检测到 401，重新转换 %s", j.idx+1, len(items), workerID, j.item.Email))
				}
			}

			ssoLog(fmt.Sprintf("[%d/%d] w%d 开始转换...", j.idx+1, len(items), workerID))
			path, email, err := convertOneSSO(ctx, j.item, outDir, maxRetries)
			res.ElapsedS = int64(time.Since(started).Seconds())
			if err != nil {
				res.OK = false
				res.Error = err.Error()
				res.Message = err.Error()
				atomic.AddInt64(&ssoJob.failCount, 1)
				if saveSSO {
					updateVaultImportResult(firstNonEmpty(email, j.item.Email), j.item.SSO, "", false, err.Error())
				}
				ssoLog(fmt.Sprintf("[%d/%d] w%d 失败: %v", j.idx+1, len(items), workerID, err))
			} else {
				// After convert, if skipIfOK and we only learned email late: still wrote file (OK).
				// Optional second-chance skip is not applied — convert already done.
				res.OK = true
				res.Email = email
				res.Path = path
				res.File = filepath.Base(path)
				res.Message = "ok"
				atomic.AddInt64(&ssoJob.okCount, 1)
				// Drop runtime isolation so scheduler can pick the refreshed credential.
				noteSSOSuccess(firstNonEmpty(email, j.item.Email), res.File)
				if saveSSO {
					updateVaultImportResult(email, j.item.SSO, res.File, true, "")
				}
				ssoLog(fmt.Sprintf("[%d/%d] w%d 成功 → %s", j.idx+1, len(items), workerID, filepath.Base(path)))
			}
			ssoJob.mu.Lock()
			if j.idx >= 0 && j.idx < len(ssoJob.results) {
				ssoJob.results[j.idx] = res
			}
			ssoJob.mu.Unlock()
			atomic.AddInt64(&ssoJob.done, 1)
		}
	}

	for w := 1; w <= workers; w++ {
		wg.Add(1)
		go workerFn(w)
	}

	delay := time.Duration(delaySec) * time.Second
	stopped := false
	for i, item := range items {
		if ctx.Err() != nil {
			stopped = true
			break
		}
		select {
		case <-ctx.Done():
			stopped = true
		case jobs <- job{idx: i, item: item}:
		}
		if stopped {
			break
		}
		// optional launch stagger to reduce device-flow rate limit bursts
		if delay > 0 && i < len(items)-1 {
			select {
			case <-ctx.Done():
				stopped = true
			case <-time.After(delay):
			}
			if stopped {
				break
			}
		}
	}
	close(jobs)
	wg.Wait()

	if stopped || ctx.Err() != nil {
		ssoJob.mu.Lock()
		ssoJob.state = "stopped"
		ssoJob.message = "stopped by user"
		ssoJob.mu.Unlock()
		ssoLog(fmt.Sprintf("已停止: ok=%d skip=%d fail=%d done=%d/%d",
			atomic.LoadInt64(&ssoJob.okCount), atomic.LoadInt64(&ssoJob.skipCount),
			atomic.LoadInt64(&ssoJob.failCount), atomic.LoadInt64(&ssoJob.done), len(items)))
		return
	}
	ssoLog(fmt.Sprintf("完成: ok=%d skip=%d fail=%d workers=%d",
		atomic.LoadInt64(&ssoJob.okCount), atomic.LoadInt64(&ssoJob.skipCount),
		atomic.LoadInt64(&ssoJob.failCount), workers))
}

func convertOneSSO(ctx context.Context, item ssoCookieItem, outDir string, maxRetries int) (string, string, error) {
	token, err := ssoToToken(ctx, item.SSO, maxRetries)
	if err != nil {
		return "", item.Email, err
	}
	filename, entry, err := tokenToCliproxyEntry(token, item.Email)
	if err != nil {
		return "", item.Email, err
	}
	path := filepath.Join(outDir, filename)
	raw, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", item.Email, err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", item.Email, err
	}
	email, _ := entry["email"].(string)
	return path, email, nil
}

type oauthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Email        string `json:"_email,omitempty"`
}

func ssoToToken(ctx context.Context, sso string, maxRetries int) (*oauthToken, error) {
	if maxRetries < 1 {
		maxRetries = ssoDefaultMaxRetries
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	// Seed SSO cookie for x.ai domains (cookiejar normalizes Domain).
	ssoVal := strings.TrimSpace(sso)
	for _, host := range []string{"https://accounts.x.ai/", "https://auth.x.ai/", "https://x.ai/"} {
		u, _ := url.Parse(host)
		jar.SetCookies(u, []*http.Cookie{
			{Name: "sso", Value: ssoVal, Path: "/", Domain: "x.ai", Secure: true},
			{Name: "sso", Value: ssoVal, Path: "/"}, // host-only fallback
		})
	}
	// SSO 转换/401 重刷：优先 CPA proxy-url，否则直连
	client := newHTTPClientWithProxy("", 45*time.Second, 8)
	client.Jar = jar
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 12 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}

	// Validate SSO session
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://accounts.x.ai/", nil)
	setBrowserHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accounts.x.ai: %w", err)
	}
	finalURL := resp.Request.URL.String()
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	low := strings.ToLower(finalURL)
	if strings.Contains(low, "sign-in") || strings.Contains(low, "sign-up") || strings.Contains(low, "login") {
		return nil, fmt.Errorf("sso 无效（跳转到登录页）")
	}

	var userCode, deviceCode, verifyComplete string
	interval := 5
	expiresIn := 1800

	freshDevice := func() error {
		dc, err := requestDeviceCode(ctx, client)
		if err != nil {
			return err
		}
		userCode, _ = dc["user_code"].(string)
		deviceCode, _ = dc["device_code"].(string)
		verifyComplete, _ = dc["verification_uri_complete"].(string)
		if userCode == "" || deviceCode == "" || verifyComplete == "" {
			return fmt.Errorf("device/code 响应不完整")
		}
		if v, ok := dc["interval"].(float64); ok && v > 0 {
			interval = int(v)
		}
		if v, ok := dc["expires_in"].(float64); ok && v > 0 {
			expiresIn = int(v)
		}
		// Hit verification URI with SSO session
		vreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, verifyComplete, nil)
		setBrowserHeaders(vreq)
		vresp, err := client.Do(vreq)
		if err != nil {
			return fmt.Errorf("verification_uri: %w", err)
		}
		io.Copy(io.Discard, vresp.Body)
		vresp.Body.Close()
		return nil
	}

	if err := freshDevice(); err != nil {
		return nil, err
	}

	verifyOK := false
	approveOK := false
	rateHits := 0
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// verify
		form := url.Values{"user_code": {userCode}}
		vreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, xaiOIDCIssuer+"/oauth2/device/verify", strings.NewReader(form.Encode()))
		setBrowserHeaders(vreq)
		vreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		vresp, err := client.Do(vreq)
		if err != nil {
			sleepBackoff(ctx, attempt)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(vresp.Body, 4096))
		vresp.Body.Close()
		vURL := ""
		if vresp.Request != nil && vresp.Request.URL != nil {
			vURL = vresp.Request.URL.String()
		}
		if isRateLimited(vURL, string(body)) {
			rateHits++
			sleepBackoff(ctx, attempt)
			if err := freshDevice(); err != nil {
				return nil, err
			}
			continue
		}
		if !strings.Contains(strings.ToLower(vURL+" "+string(body)), "consent") && vresp.StatusCode >= 400 {
			// some flows keep relative path; accept 200 as progress
			if vresp.StatusCode >= 400 {
				return nil, fmt.Errorf("verify 失败: status=%d url=%s", vresp.StatusCode, vURL)
			}
		}
		// if redirected away from consent without rate limit, still try approve
		verifyOK = true

		// approve
		aform := url.Values{
			"user_code":      {userCode},
			"action":         {"allow"},
			"principal_type": {"User"},
			"principal_id":   {""},
		}
		areq, _ := http.NewRequestWithContext(ctx, http.MethodPost, xaiOIDCIssuer+"/oauth2/device/approve", strings.NewReader(aform.Encode()))
		setBrowserHeaders(areq)
		areq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		aresp, err := client.Do(areq)
		if err != nil {
			sleepBackoff(ctx, attempt)
			continue
		}
		abody, _ := io.ReadAll(io.LimitReader(aresp.Body, 4096))
		aresp.Body.Close()
		aURL := ""
		if aresp.Request != nil && aresp.Request.URL != nil {
			aURL = aresp.Request.URL.String()
		}
		if isRateLimited(aURL, string(abody)) {
			rateHits++
			verifyOK = false
			sleepBackoff(ctx, attempt)
			if err := freshDevice(); err != nil {
				return nil, err
			}
			continue
		}
		if aresp.StatusCode >= 400 && !strings.Contains(strings.ToLower(aURL), "done") {
			return nil, fmt.Errorf("approve 失败: status=%d url=%s body=%s", aresp.StatusCode, aURL, trimForErr(string(abody)))
		}
		approveOK = true
		break
	}
	if !verifyOK {
		if rateHits > 0 {
			return nil, fmt.Errorf("verify 限流重试耗尽")
		}
		return nil, fmt.Errorf("verify 失败")
	}
	if !approveOK {
		if rateHits > 0 {
			return nil, fmt.Errorf("approve 限流重试耗尽")
		}
		return nil, fmt.Errorf("approve 失败")
	}

	tok, err := pollDeviceToken(ctx, client, deviceCode, interval, expiresIn)
	if err != nil {
		return nil, err
	}
	// enrich email via userinfo
	if email, err := fetchUserinfoEmail(ctx, client, tok.AccessToken); err == nil && email != "" {
		tok.Email = email
	}
	return tok, nil
}

func requestDeviceCode(ctx context.Context, client *http.Client) (map[string]any, error) {
	form := url.Values{
		"client_id": {xaiClientID},
		"scope":     {xaiScopes},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, xaiOIDCIssuer+"/oauth2/device/code", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	setBrowserHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("device/code HTTP %d: %s", resp.StatusCode, trimForErr(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func pollDeviceToken(ctx context.Context, client *http.Client, deviceCode string, interval, expiresIn int) (*oauthToken, error) {
	if interval < 2 {
		interval = 5
	}
	deadline := time.Now().Add(time.Duration(minInt(expiresIn, 120)) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {xaiClientID},
			"device_code": {deviceCode},
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cliproxyTokenEP, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			var tok oauthToken
			if err := json.Unmarshal(raw, &tok); err != nil {
				return nil, err
			}
			if tok.AccessToken == "" {
				return nil, fmt.Errorf("token 响应无 access_token")
			}
			return &tok, nil
		}
		var errObj map[string]any
		_ = json.Unmarshal(raw, &errObj)
		errCode, _ := errObj["error"].(string)
		switch errCode {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5
			continue
		default:
			return nil, fmt.Errorf("token: %s %s", errCode, trimForErr(string(raw)))
		}
	}
	return nil, fmt.Errorf("轮询 token 超时")
}

func fetchUserinfoEmail(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("empty token")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, xaiOIDCIssuer+"/oauth2/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("userinfo HTTP %d", resp.StatusCode)
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", err
	}
	email, _ := info["email"].(string)
	return strings.TrimSpace(email), nil
}

func tokenToCliproxyEntry(token *oauthToken, emailHint string) (string, map[string]any, error) {
	if token == nil || token.AccessToken == "" {
		return "", nil, fmt.Errorf("empty access_token")
	}
	accessPayload := decodeJWTPayload(token.AccessToken)
	idPayload := decodeJWTPayload(token.IDToken)
	sub := firstNonEmpty(
		strAny(accessPayload["sub"]),
		strAny(accessPayload["principal_id"]),
		strAny(idPayload["sub"]),
	)
	email := firstNonEmpty(
		emailHint,
		token.Email,
		strAny(idPayload["email"]),
		strAny(accessPayload["email"]),
	)
	expiresIn := token.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 21600
	}
	var expired string
	if exp, ok := asFloat(accessPayload["exp"]); ok {
		expired = time.Unix(int64(exp), 0).UTC().Format(time.RFC3339)
	} else {
		expired = time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
	}
	var lastRefresh string
	if iat, ok := asFloat(accessPayload["iat"]); ok {
		lastRefresh = time.Unix(int64(iat), 0).UTC().Format(time.RFC3339)
	} else {
		lastRefresh = time.Now().UTC().Format(time.RFC3339)
	}
	tt := token.TokenType
	if tt == "" {
		tt = "Bearer"
	}
	entry := map[string]any{
		"type":           "xai",
		"auth_kind":      "oauth",
		"access_token":   token.AccessToken,
		"refresh_token":  token.RefreshToken,
		"token_type":     tt,
		"expires_in":     expiresIn,
		"expired":        expired,
		"last_refresh":   lastRefresh,
		"email":          email,
		"sub":            sub,
		"base_url":       cliproxyBaseURL,
		"token_endpoint": cliproxyTokenEP,
		"redirect_uri":   cliproxyRedirectURI,
		"disabled":       false,
		"id_token":       token.IDToken,
		"headers": map[string]string{
			"x-grok-client-version":    "0.2.93",
			"x-xai-token-auth":         "xai-grok-cli",
			"x-authenticateresponse":   "authenticate-response",
			"x-grok-client-identifier": "grok-shell",
			"User-Agent":               "grok-shell/0.2.93 (linux; x86_64)",
		},
	}
	filename := cliproxyFilename(email, sub)
	return filename, entry, nil
}

func cliproxyFilename(email, sub string) string {
	email = strings.TrimSpace(email)
	sub = strings.TrimSpace(sub)
	if email != "" {
		// sanitize path-hostile chars
		safe := strings.Map(func(r rune) rune {
			switch r {
			case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
				return '_'
			default:
				return r
			}
		}, email)
		return "xai-" + safe + ".json"
	}
	if sub != "" {
		return "xai-" + sub + ".json"
	}
	return fmt.Sprintf("xai-anon_%d.json", time.Now().UnixNano()%1e9)
}

func setBrowserHeaders(req *http.Request) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
}

func isRateLimited(u, body string) bool {
	blob := strings.ToLower(u + "\n" + body)
	return strings.Contains(blob, "rate_limited") ||
		strings.Contains(blob, "rate-limited") ||
		strings.Contains(blob, "too_many_requests") ||
		strings.Contains(blob, "ratelimit") ||
		strings.Contains(blob, "\"status\":429") ||
		strings.Contains(blob, " 429 ")
}

func sleepBackoff(ctx context.Context, attempt int) {
	d := time.Duration(10*(1<<minInt(attempt-1, 4))) * time.Second
	if d > 90*time.Second {
		d = 90 * time.Second
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func decodeJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return map[string]any{}
	}
	seg := parts[1]
	switch len(seg) % 4 {
	case 2:
		seg += "=="
	case 3:
		seg += "="
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return map[string]any{}
		}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func strAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func trimForErr(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		return s[:240] + "..."
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
