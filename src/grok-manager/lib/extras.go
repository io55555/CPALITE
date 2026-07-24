package grokmanager

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- SSO parse preview (dedup stats before import) ----

type ssoPreviewRequest struct {
	SSOList string `json:"sso_list"`
}

type ssoPreviewLine struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
	Sample string `json:"sample,omitempty"`
}

type ssoPreviewResult struct {
	RawLines       int              `json:"raw_lines"`
	BlankSkipped   int              `json:"blank_skipped"`
	Valid          int              `json:"valid"`
	UniqueEmail    int              `json:"unique_email"`
	NoEmail        int              `json:"no_email"`
	DupEmail       int              `json:"dup_email"`
	DupSSO         int              `json:"dup_sso"`
	Invalid        int              `json:"invalid"`
	WillImport     int              `json:"will_import"` // after email-dedup prefer last
	InvalidSamples []ssoPreviewLine `json:"invalid_samples,omitempty"`
	DupSamples     []ssoPreviewLine `json:"dup_samples,omitempty"`
	SampleOK       []string         `json:"sample_ok,omitempty"` // email----sso masked
	Note           string           `json:"note"`
}

func handleSSOPreview(body []byte) ([]byte, error) {
	var req ssoPreviewRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", err.Error())
		}
	}
	return jsonManagementEnvelope(http.StatusOK, previewSSOList(req.SSOList))
}

func previewSSOList(raw string) ssoPreviewResult {
	// Reuse parseSSOItems which already dedups by SSO value.
	items := parseSSOItems(ssoImportRequest{SSOList: raw})
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := ssoPreviewResult{
		RawLines: len(lines),
		Valid:    len(items),
		Note:     "will_import = 按 email 去重后保留最后一条；无 email 的 JWT 按 sso 去重保留。",
	}
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" || strings.HasPrefix(strings.TrimSpace(ln), "#") {
			out.BlankSkipped++
		}
	}

	// Re-parse line-by-line for invalid/dup diagnostics
	seenSSO := map[string]int{}
	seenEmail := map[string]int{}
	type hold struct {
		email, sso string
		line       int
	}
	var holds []hold
	for i, line := range lines {
		line = strings.TrimPrefix(line, "\ufeff")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tmp := parseSSOItems(ssoImportRequest{SSOList: line})
		if len(tmp) == 0 {
			out.Invalid++
			if len(out.InvalidSamples) < 30 {
				samp := line
				if len(samp) > 80 {
					samp = samp[:80] + "…"
				}
				out.InvalidSamples = append(out.InvalidSamples, ssoPreviewLine{Line: i + 1, Reason: "unparseable", Sample: samp})
			}
			continue
		}
		it := tmp[0]
		holds = append(holds, hold{email: it.Email, sso: it.SSO, line: i + 1})
		if it.Email == "" {
			out.NoEmail++
		}
		if prev, ok := seenSSO[it.SSO]; ok {
			out.DupSSO++
			if len(out.DupSamples) < 20 {
				out.DupSamples = append(out.DupSamples, ssoPreviewLine{Line: i + 1, Reason: fmt.Sprintf("dup_sso of line %d", prev), Sample: maskSSO(it.SSO)})
			}
		} else {
			seenSSO[it.SSO] = i + 1
		}
		em := strings.ToLower(strings.TrimSpace(it.Email))
		if em != "" {
			if prev, ok := seenEmail[em]; ok {
				out.DupEmail++
				if len(out.DupSamples) < 30 {
					out.DupSamples = append(out.DupSamples, ssoPreviewLine{Line: i + 1, Reason: fmt.Sprintf("dup_email of line %d", prev), Sample: em})
				}
			} else {
				seenEmail[em] = i + 1
			}
		}
	}
	out.UniqueEmail = len(seenEmail)

	// will_import: last wins per email; no-email by sso
	final := map[string]hold{}
	for _, h := range holds {
		key := strings.ToLower(strings.TrimSpace(h.email))
		if key == "" {
			key = "sso:" + h.sso
		}
		final[key] = h
	}
	out.WillImport = len(final)
	for _, h := range final {
		if len(out.SampleOK) >= 5 {
			break
		}
		em := h.email
		if em == "" {
			em = "(no-email)"
		}
		out.SampleOK = append(out.SampleOK, em+"----"+maskSSO(h.sso))
	}
	return out
}

// dedupeSSOItemsByEmail keeps last item per email (or per sso if no email).
func dedupeSSOItemsByEmail(items []ssoCookieItem) (out []ssoCookieItem, dropped int) {
	order := make([]string, 0, len(items))
	last := map[string]ssoCookieItem{}
	for _, it := range items {
		key := strings.ToLower(strings.TrimSpace(it.Email))
		if key == "" {
			key = "sso:" + it.SSO
		}
		if _, ok := last[key]; !ok {
			order = append(order, key)
		} else {
			dropped++
		}
		last[key] = it
	}
	out = make([]ssoCookieItem, 0, len(order))
	for _, k := range order {
		out = append(out, last[k])
	}
	return out, dropped
}

// ---- Vault manage ----

type vaultDeleteRequest struct {
	Emails       []string `json:"emails"`
	OnlyFailed   bool     `json:"only_failed"`
	OnlyHTTP     []int    `json:"only_http"`
	OnlyNoOK     bool     `json:"only_not_ok"`
	FailStreakGE int      `json:"fail_streak_ge"` // delete if fail_streak >= N
}

func handleVaultDelete(body []byte) ([]byte, error) {
	var req vaultDeleteRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", err.Error())
		}
	}
	emailSet := map[string]struct{}{}
	for _, e := range req.Emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" {
			emailSet[e] = struct{}{}
		}
	}
	httpSet := map[int]struct{}{}
	for _, h := range req.OnlyHTTP {
		httpSet[h] = struct{}{}
	}

	hasFilter := len(emailSet) > 0 || req.OnlyFailed || req.OnlyNoOK || len(httpSet) > 0 || req.FailStreakGE > 0
	if !hasFilter {
		return jsonErrorEnvelope(http.StatusBadRequest, "bad_request", "provide emails or a filter (only_failed / only_http / fail_streak_ge)")
	}

	vaultMu.Lock()
	defer vaultMu.Unlock()
	v := loadVaultUnlocked()
	kept := make([]ssoVaultEntry, 0, len(v.Entries))
	removed := 0
	for _, e := range v.Entries {
		drop := false
		em := strings.ToLower(strings.TrimSpace(e.Email))
		if len(emailSet) > 0 {
			if _, ok := emailSet[em]; ok {
				drop = true
			}
		}
		if req.OnlyFailed && (!e.LastOK && e.LastError != "") {
			drop = true
		}
		if req.OnlyNoOK && !e.LastOK {
			drop = true
		}
		if len(httpSet) > 0 {
			if _, ok := httpSet[e.LastHTTP]; ok {
				drop = true
			}
		}
		if req.FailStreakGE > 0 && e.FailStreak >= req.FailStreakGE {
			drop = true
		}
		// When emails list is provided alone, only drop matching emails.
		// When filters are mixed with emails, email match OR filter match.
		if len(emailSet) > 0 && !req.OnlyFailed && !req.OnlyNoOK && len(httpSet) == 0 && req.FailStreakGE <= 0 {
			_, drop = emailSet[em]
		}
		if drop {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	v.Entries = kept
	_ = saveVaultUnlocked(v)
	return jsonManagementEnvelope(http.StatusOK, map[string]any{
		"ok": true, "removed": removed, "remaining": len(kept), "vault": vaultPublicSummaryUnlocked(v, nil),
	})
}

func vaultPublicSummaryUnlocked(v ssoVaultFile, q url.Values) map[string]any {
	// caller must hold vaultMu or pass consistent snapshot
	type pub struct {
		Email        string `json:"email,omitempty"`
		SSOMasked    string `json:"sso_masked,omitempty"`
		HasSSO       bool   `json:"has_sso"`
		UpdatedAt    string `json:"updated_at,omitempty"`
		LastImportAt string `json:"last_import_at,omitempty"`
		LastFile     string `json:"last_file,omitempty"`
		LastOK       bool   `json:"last_ok"`
		LastError    string `json:"last_error,omitempty"`
		LastHTTP     int    `json:"last_http,omitempty"`
		ConvertOK    int    `json:"convert_ok_count,omitempty"`
		Skipped      int    `json:"skip_count,omitempty"`
		FailStreak   int    `json:"fail_streak,omitempty"`
	}
	pq := parsePageQuery(q)
	listAll := make([]pub, 0, len(v.Entries))
	withSSO, withEmail, failN, http401, streakN := 0, 0, 0, 0, 0
	for _, e := range v.Entries {
		has := strings.TrimSpace(e.SSO) != ""
		if has {
			withSSO++
		}
		if strings.TrimSpace(e.Email) != "" {
			withEmail++
		}
		if !e.LastOK && e.LastError != "" {
			failN++
		}
		if e.LastHTTP == 401 {
			http401++
		}
		if e.FailStreak >= 3 {
			streakN++
		}
		item := pub{
			Email: e.Email, SSOMasked: maskSSO(e.SSO), HasSSO: has,
			UpdatedAt: e.UpdatedAt, LastImportAt: e.LastImportAt, LastFile: e.LastFile,
			LastOK: e.LastOK, LastError: e.LastError, LastHTTP: e.LastHTTP,
			ConvertOK: e.ConvertOK, Skipped: e.Skipped, FailStreak: e.FailStreak,
		}
		// filter
		switch pq.Filter {
		case "http401":
			if e.LastHTTP != 401 {
				continue
			}
		case "failed":
			if e.LastOK || e.LastError == "" {
				continue
			}
		case "not_ok":
			if e.LastOK {
				continue
			}
		case "fail_streak":
			if e.FailStreak < 3 {
				continue
			}
		}
		if pq.Q != "" {
			qq := strings.ToLower(pq.Q)
			if !strings.Contains(strings.ToLower(e.Email), qq) &&
				!strings.Contains(strings.ToLower(e.LastFile), qq) {
				continue
			}
		}
		listAll = append(listAll, item)
	}
	pageItems, match, pages, page := slicePage(listAll, pq.Page, pq.PageSize)
	return map[string]any{
		"saved_at": v.SavedAt, "count": len(v.Entries), "with_sso": withSSO, "with_email": withEmail,
		"failed_count": failN, "http_401_count": http401, "fail_streak_count": streakN,
		"vault_path": resolvePluginDataPath(ssoVaultFileName, &vaultPath),
		"persisted":  len(v.Entries) > 0 || v.SavedAt != "",
		"entries":    pageItems,
		"page":       page,
		"page_size":  pq.PageSize,
		"pages":      pages,
		"match":      match,
		"filter":     pq.Filter,
		"q":          pq.Q,
	}
}

type vaultExportRequest struct {
	Filter string   `json:"filter"` // all | failed | http401 | not_ok | emails
	Emails []string `json:"emails"`
}

func handleVaultExport(body []byte) ([]byte, error) {
	var req vaultExportRequest
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req)
	}
	if req.Filter == "" {
		req.Filter = "all"
	}
	emailSet := map[string]struct{}{}
	for _, e := range req.Emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" {
			emailSet[e] = struct{}{}
		}
	}
	vaultMu.Lock()
	v := loadVaultUnlocked()
	vaultMu.Unlock()

	var lines []string
	for _, e := range v.Entries {
		em := strings.TrimSpace(e.Email)
		sso := strings.TrimSpace(e.SSO)
		if sso == "" {
			continue
		}
		keep := false
		switch req.Filter {
		case "all":
			keep = true
		case "failed":
			keep = !e.LastOK && e.LastError != ""
		case "http401":
			keep = e.LastHTTP == 401
		case "not_ok":
			keep = !e.LastOK
		case "emails":
			_, keep = emailSet[strings.ToLower(em)]
		default:
			keep = true
		}
		if !keep {
			continue
		}
		if em == "" {
			lines = append(lines, sso)
		} else {
			lines = append(lines, em+"----"+sso)
		}
	}
	sort.Strings(lines)
	text := strings.Join(lines, "\n")
	if text != "" {
		text += "\n"
	}
	return jsonManagementEnvelope(http.StatusOK, map[string]any{
		"ok": true, "count": len(lines), "filter": req.Filter, "text": text,
	})
}

// ---- Paths / diagnostics ----

func handlePathsInfo() ([]byte, error) {
	vpath, vcount, vsaved := vaultMeta()
	sch := scheduleSnapshot()
	job.mu.Lock()
	hist := job.historyPath
	job.mu.Unlock()
	if hist == "" {
		hist = resolvePluginDataPath(historyFileName, nil)
	}
	outDirs := authSearchDirs()
	return jsonManagementEnvelope(http.StatusOK, map[string]any{
		"plugin":         pluginName,
		"version":        pluginVersion,
		"vault_path":     vpath,
		"vault_count":    vcount,
		"vault_saved_at": vsaved,
		"scan_history":   hist,
		"sso_history":    resolveSSOHistoryPath(),
		"schedule_path":  sch.ConfigPath,
		"bans_path":      bansFilePath(),
		"bans_count":     runtimeBans.count(),
		"auth_dirs":      outDirs,
		"default_out":    defaultAuthOutDir(),
		"wd":             func() string { wd, _ := os.Getwd(); return wd }(),
	})
}

// ---- Backup zip ----

func handleBackup() ([]byte, error) {
	files := []struct {
		name string
		path string
	}{
		{"sso-vault.json", resolveVaultPath()},
		{"last-scan.json", resolvePluginDataPath(historyFileName, nil)},
		{"last-sso-import.json", resolveSSOHistoryPath()},
		{"schedule.json", resolvePluginDataPath(scheduleFileName, nil)},
		{"bans.json", bansFilePath()},
	}
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	added := 0
	var names []string
	for _, f := range files {
		raw, err := os.ReadFile(f.path)
		if err != nil || len(raw) == 0 {
			continue
		}
		w, err := zw.Create(f.name)
		if err != nil {
			continue
		}
		if _, err := w.Write(raw); err != nil {
			continue
		}
		added++
		names = append(names, f.name)
	}
	// also write a manifest
	manifest, _ := json.MarshalIndent(map[string]any{
		"plugin":  pluginName,
		"version": pluginVersion,
		"saved":   time.Now().UTC().Format(time.RFC3339),
		"files":   names,
	}, "", "  ")
	if w, err := zw.Create("manifest.json"); err == nil {
		_, _ = w.Write(manifest)
	}
	_ = zw.Close()
	if added == 0 {
		return jsonErrorEnvelope(http.StatusNotFound, "empty", "没有可备份的数据文件")
	}
	// store under plugin data dir
	outName := fmt.Sprintf("backup-%s.zip", time.Now().UTC().Format("20060102-150405"))
	outPath := resolvePluginDataPath(outName, nil)
	_ = os.MkdirAll(filepath.Dir(outPath), 0o755)
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		return jsonErrorEnvelope(http.StatusInternalServerError, "write", err.Error())
	}
	return jsonManagementEnvelope(http.StatusOK, map[string]any{
		"ok": true, "path": outPath, "files": names, "bytes": len(buf.Bytes()), "filename": outName,
	})
}
