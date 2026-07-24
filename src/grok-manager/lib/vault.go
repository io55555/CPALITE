package grokmanager

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ssoVaultFileName   = "sso-vault.json"
	ssoHistoryFileName = "last-sso-import.json"
)

// ssoVaultEntry is a durable SSO cookie record keyed primarily by email.
type ssoVaultEntry struct {
	Email        string `json:"email,omitempty"`
	SSO          string `json:"sso"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	LastImportAt string `json:"last_import_at,omitempty"`
	LastFile     string `json:"last_file,omitempty"`
	LastOK       bool   `json:"last_ok"`
	LastError    string `json:"last_error,omitempty"`
	LastHTTP     int    `json:"last_http,omitempty"`
	Skipped      int    `json:"skip_count,omitempty"`
	ConvertOK    int    `json:"convert_ok_count,omitempty"`
	FailStreak   int    `json:"fail_streak,omitempty"` // consecutive convert failures
}

type ssoVaultFile struct {
	SavedAt string          `json:"saved_at"`
	Entries []ssoVaultEntry `json:"entries"`
}

type persistedSSOImport struct {
	SavedAt    string          `json:"saved_at"`
	State      string          `json:"state"`
	Message    string          `json:"message,omitempty"`
	Error      string          `json:"error,omitempty"`
	Total      int             `json:"total"`
	Done       int             `json:"done"`
	OK         int             `json:"ok"`
	Failed     int             `json:"failed"`
	Skipped    int             `json:"skipped"`
	Workers    int             `json:"workers"`
	OutDir     string          `json:"out_dir,omitempty"`
	StartedAt  string          `json:"started_at,omitempty"`
	FinishedAt string          `json:"finished_at,omitempty"`
	Results    []ssoItemResult `json:"results"`
	Logs       []string        `json:"logs,omitempty"`
	VaultPath  string          `json:"vault_path,omitempty"`
	HistoryPath string         `json:"history_path,omitempty"`
}

var (
	vaultMu   sync.Mutex
	vaultPath string
	ssoHistPath string
)

func pluginDataCandidates(fileName string) []string {
	var out []string
	if wd, err := os.Getwd(); err == nil {
		out = append(out,
			filepath.Join(wd, "plugins", "grok-manager", fileName),
			filepath.Join(wd, "plugins-data", "grok-manager", fileName),
		)
	}
	out = append(out,
		filepath.Join(`C:\CLIProxyAPI-local\plugins\grok-manager`, fileName),
		filepath.Join(`/root/.cli-proxy-api/plugins/grok-manager`, fileName),
	)
	return out
}

func resolvePluginDataPath(fileName string, cached *string) string {
	if cached != nil && *cached != "" {
		return *cached
	}
	for _, p := range pluginDataCandidates(fileName) {
		if st, err := os.Stat(filepath.Dir(p)); err == nil && st.IsDir() {
			if cached != nil {
				*cached = p
			}
			return p
		}
	}
	p := pluginDataCandidates(fileName)[0]
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if cached != nil {
		*cached = p
	}
	return p
}

func resolveVaultPath() string {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	return resolvePluginDataPath(ssoVaultFileName, &vaultPath)
}

func resolveSSOHistoryPath() string {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	return resolvePluginDataPath(ssoHistoryFileName, &ssoHistPath)
}

func loadVault() ssoVaultFile {
	path := resolveVaultPath()
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return ssoVaultFile{Entries: nil}
	}
	var v ssoVaultFile
	if err := json.Unmarshal(raw, &v); err != nil {
		return ssoVaultFile{Entries: nil}
	}
	return v
}

func saveVault(v ssoVaultFile) error {
	path := resolveVaultPath()
	v.SavedAt = time.Now().UTC().Format(time.RFC3339)
	// stable order: email then sso prefix
	sort.SliceStable(v.Entries, func(i, j int) bool {
		ei, ej := strings.ToLower(v.Entries[i].Email), strings.ToLower(v.Entries[j].Email)
		if ei != ej {
			return ei < ej
		}
		return v.Entries[i].SSO < v.Entries[j].SSO
	})
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return os.WriteFile(path, raw, 0o600)
	}
	return nil
}

// upsertVaultSSO merges cookies into the durable vault (keyed by email, fallback sso).
func upsertVaultSSO(items []ssoCookieItem) {
	if len(items) == 0 {
		return
	}
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	now := time.Now().UTC().Format(time.RFC3339)
	indexEmail := map[string]int{}
	indexSSO := map[string]int{}
	for i, e := range v.Entries {
		if e.Email != "" {
			indexEmail[strings.ToLower(e.Email)] = i
		}
		if e.SSO != "" {
			indexSSO[e.SSO] = i
		}
	}
	for _, it := range items {
		sso := strings.TrimSpace(it.SSO)
		email := strings.TrimSpace(it.Email)
		if sso == "" {
			continue
		}
		idx := -1
		if email != "" {
			if i, ok := indexEmail[strings.ToLower(email)]; ok {
				idx = i
			}
		}
		if idx < 0 {
			if i, ok := indexSSO[sso]; ok {
				idx = i
			}
		}
		if idx >= 0 {
			e := v.Entries[idx]
			e.SSO = sso
			if email != "" {
				e.Email = email
			}
			e.UpdatedAt = now
			v.Entries[idx] = e
			if e.Email != "" {
				indexEmail[strings.ToLower(e.Email)] = idx
			}
			indexSSO[sso] = idx
			continue
		}
		e := ssoVaultEntry{Email: email, SSO: sso, UpdatedAt: now}
		v.Entries = append(v.Entries, e)
		n := len(v.Entries) - 1
		if email != "" {
			indexEmail[strings.ToLower(email)] = n
		}
		indexSSO[sso] = n
	}
	_ = saveVaultUnlocked(v)
}

// updateVaultImportResult records convert outcome for an email/sso.
func updateVaultImportResult(email, sso, file string, ok bool, errMsg string) {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	now := time.Now().UTC().Format(time.RFC3339)
	email = strings.TrimSpace(email)
	sso = strings.TrimSpace(sso)
	idx := -1
	for i, e := range v.Entries {
		if email != "" && strings.EqualFold(e.Email, email) {
			idx = i
			break
		}
		if sso != "" && e.SSO == sso {
			idx = i
			break
		}
	}
	if idx < 0 {
		if sso == "" {
			return
		}
		v.Entries = append(v.Entries, ssoVaultEntry{Email: email, SSO: sso, UpdatedAt: now})
		idx = len(v.Entries) - 1
	}
	e := v.Entries[idx]
	if email != "" {
		e.Email = email
	}
	if sso != "" {
		e.SSO = sso
	}
	e.LastImportAt = now
	e.UpdatedAt = now
	e.LastOK = ok
	e.LastError = errMsg
	if file != "" {
		e.LastFile = file
	}
	if ok {
		e.ConvertOK++
		e.FailStreak = 0
	} else {
		e.FailStreak++
	}
	v.Entries[idx] = e
	_ = saveVaultUnlocked(v)
}

func markVaultSkipped(email, sso, file string) {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	now := time.Now().UTC().Format(time.RFC3339)
	email = strings.TrimSpace(email)
	sso = strings.TrimSpace(sso)
	for i, e := range v.Entries {
		if (email != "" && strings.EqualFold(e.Email, email)) || (sso != "" && e.SSO == sso) {
			e.Skipped++
			e.UpdatedAt = now
			if file != "" {
				e.LastFile = file
			}
			if email != "" {
				e.Email = email
			}
			v.Entries[i] = e
			_ = saveVaultUnlocked(v)
			return
		}
	}
}

func loadVaultUnlocked() ssoVaultFile {
	path := resolvePluginDataPath(ssoVaultFileName, &vaultPath)
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return ssoVaultFile{}
	}
	var v ssoVaultFile
	if err := json.Unmarshal(raw, &v); err != nil {
		return ssoVaultFile{}
	}
	return v
}

func saveVaultUnlocked(v ssoVaultFile) error {
	path := resolvePluginDataPath(ssoVaultFileName, &vaultPath)
	v.SavedAt = time.Now().UTC().Format(time.RFC3339)
	sort.SliceStable(v.Entries, func(i, j int) bool {
		ei, ej := strings.ToLower(v.Entries[i].Email), strings.ToLower(v.Entries[j].Email)
		if ei != ej {
			return ei < ej
		}
		return v.Entries[i].SSO < v.Entries[j].SSO
	})
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return os.WriteFile(path, raw, 0o600)
	}
	return nil
}

// vaultLookupByEmail returns SSO cookie for email (case-insensitive).
func vaultLookupByEmail(email string) (ssoVaultEntry, bool) {
	email = strings.TrimSpace(email)
	if email == "" {
		return ssoVaultEntry{}, false
	}
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	for _, e := range v.Entries {
		if strings.EqualFold(e.Email, email) && strings.TrimSpace(e.SSO) != "" {
			return e, true
		}
	}
	return ssoVaultEntry{}, false
}

// vaultMeta returns path/count/saved_at without full entries (for status banner).
func vaultMeta() (path string, count int, savedAt string) {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	path = resolvePluginDataPath(ssoVaultFileName, &vaultPath)
	return path, len(v.Entries), v.SavedAt
}

// vaultPublicSummary masks SSO values for API/UI (paginated via query).
func vaultPublicSummary(q url.Values) map[string]any {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	return vaultPublicSummaryUnlocked(v, q)
}

func maskSSO(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 16 {
		if s == "" {
			return ""
		}
		return s[:minInt(4, len(s))] + "…"
	}
	return s[:10] + "…" + s[len(s)-6:]
}

func saveSSOHistory() {
	path := resolveSSOHistoryPath()
	ssoJob.mu.Lock()
	finished := make([]ssoItemResult, 0, len(ssoJob.results))
	skipped := 0
	for _, r := range ssoJob.results {
		if r.Index > 0 {
			finished = append(finished, r)
			if r.Skipped {
				skipped++
			}
		}
	}
	payload := persistedSSOImport{
		SavedAt:     time.Now().UTC().Format(time.RFC3339),
		State:       ssoJob.state,
		Message:     ssoJob.message,
		Error:       ssoJob.errText,
		Total:       ssoJob.total,
		Done:        int(atomicLoadDone()),
		OK:          int(atomicLoadOK()),
		Failed:      int(atomicLoadFail()),
		Skipped:     skipped,
		Workers:     ssoJob.workers,
		OutDir:      ssoJob.outDir,
		Results:     finished,
		Logs:        append([]string(nil), ssoJob.logs...),
		VaultPath:   resolveVaultPath(),
		HistoryPath: path,
	}
	if !ssoJob.startedAt.IsZero() {
		payload.StartedAt = ssoJob.startedAt.Format(time.RFC3339)
	}
	if !ssoJob.finishedAt.IsZero() {
		payload.FinishedAt = ssoJob.finishedAt.Format(time.RFC3339)
	}
	// cap logs on disk
	if len(payload.Logs) > 300 {
		payload.Logs = payload.Logs[len(payload.Logs)-300:]
	}
	ssoJob.mu.Unlock()

	raw, err := json.MarshalIndent(payload, "", "  ")
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
	ssoJob.mu.Lock()
	ssoJob.historyPath = path
	ssoJob.persisted = true
	ssoJob.mu.Unlock()
}

func atomicLoadDone() int64 {
	return atomic.LoadInt64(&ssoJob.done)
}
func atomicLoadOK() int64 {
	return atomic.LoadInt64(&ssoJob.okCount)
}
func atomicLoadFail() int64 {
	return atomic.LoadInt64(&ssoJob.failCount)
}

// loadSSOHistoryOnStart restores last import into memory for UI.
func loadSSOHistoryOnStart() {
	for _, p := range pluginDataCandidates(ssoHistoryFileName) {
		raw, err := os.ReadFile(p)
		if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var saved persistedSSOImport
		if err := json.Unmarshal(raw, &saved); err != nil {
			continue
		}
		ssoJob.mu.Lock()
		if ssoJob.running {
			ssoJob.mu.Unlock()
			return
		}
		ssoJob.historyPath = p
		ssoJob.persisted = true
		ssoJob.state = firstNonEmpty(saved.State, "done")
		if ssoJob.state == "running" {
			ssoJob.state = "done"
		}
		ssoJob.message = firstNonEmpty(saved.Message, "loaded last sso import history")
		ssoJob.errText = saved.Error
		ssoJob.total = saved.Total
		atomic.StoreInt64(&ssoJob.done, int64(saved.Done))
		atomic.StoreInt64(&ssoJob.okCount, int64(saved.OK))
		atomic.StoreInt64(&ssoJob.failCount, int64(saved.Failed))
		ssoJob.workers = saved.Workers
		ssoJob.outDir = saved.OutDir
		ssoJob.results = append([]ssoItemResult(nil), saved.Results...)
		ssoJob.logs = append([]string(nil), saved.Logs...)
		if saved.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, saved.StartedAt); err == nil {
				ssoJob.startedAt = t
			}
		}
		if saved.FinishedAt != "" {
			if t, err := time.Parse(time.RFC3339, saved.FinishedAt); err == nil {
				ssoJob.finishedAt = t
			}
		}
		ssoJob.mu.Unlock()
		vaultMu.Lock()
		ssoHistPath = p
		vaultMu.Unlock()
		return
	}
}

// credentialKnown401 reports whether last scan marked this account as HTTP 401.
func credentialKnown401(email, fileName, path string) bool {
	job.mu.Lock()
	defer job.mu.Unlock()
	email = strings.TrimSpace(email)
	base := filepath.Base(firstNonEmpty(fileName, path))
	for _, r := range job.results {
		if r.HTTPStatus != 401 {
			continue
		}
		if email != "" && strings.EqualFold(strings.TrimSpace(r.Email), email) {
			return true
		}
		rb := filepath.Base(firstNonEmpty(r.Name, r.File, r.Path))
		if base != "" && base != "." && strings.EqualFold(rb, base) {
			return true
		}
	}
	return false
}

// findExistingAuthFile looks for xai-{email}.json (and loose matches) under outDir.
func findExistingAuthFile(outDir, email string) (string, bool) {
	email = strings.TrimSpace(email)
	if email == "" || outDir == "" {
		return "", false
	}
	name := cliproxyFilename(email, "")
	p := filepath.Join(outDir, name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	// case-insensitive scan of small dirs
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return "", false
	}
	want := strings.ToLower(name)
	emailLow := strings.ToLower(email)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.ToLower(n) == want {
			return filepath.Join(outDir, n), true
		}
		if strings.HasPrefix(strings.ToLower(n), "xai-") && strings.Contains(strings.ToLower(n), emailLow) {
			return filepath.Join(outDir, n), true
		}
	}
	return "", false
}

// shouldSkipConvert: skip when credential file exists AND last scan did not report 401.
// Missing file or known 401 → convert. Exists without 401 evidence → skip.
func shouldSkipConvert(outDir, email string) (skip bool, path, reason string) {
	p, ok := findExistingAuthFile(outDir, email)
	if !ok {
		return false, "", "missing_file"
	}
	if credentialKnown401(email, filepath.Base(p), p) {
		return false, p, "known_401"
	}
	return true, p, "exists_not_401"
}
