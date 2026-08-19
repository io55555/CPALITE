package authindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
	_ "modernc.org/sqlite"
)

// Store 维护认证文件目录的 SQLite 轻量索引，文件目录仍是唯一事实来源。
type Store struct {
	db      *sql.DB
	authDir string
}

// Entry 是管理列表可直接返回的轻量认证文件条目。
type Entry struct {
	ID            string
	FileName      string
	FilePath      string
	Provider      string
	Disabled      bool
	Unavailable   bool
	Status        string
	Email         string
	ProjectID     string
	Account       string
	AccountType   string
	Priority      int
	Size          int64
	ModTimeUnix   int64
	CooldownUntil int64
	UpdatedUnix   int64
}

// QueryOptions 描述管理列表分页和过滤条件。
type QueryOptions struct {
	Page         int
	PageSize     int
	Provider     string
	Disabled     *bool
	Keyword      string
	CooldownOnly bool
}

// QueryResult 是分页查询结果。
type QueryResult struct {
	Entries []Entry
	Total   int
}

// Open 打开或创建认证索引库。
func Open(ctx context.Context, authDir string, cfg config.AuthIndexCacheConfig) (*Store, error) {
	authDir = strings.TrimSpace(authDir)
	if authDir == "" {
		return nil, fmt.Errorf("auth index: auth dir is empty")
	}
	dbPath := strings.TrimSpace(cfg.DBPath)
	if dbPath == "" {
		dbPath = filepath.Join(authDir, ".auth-cache", "index.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("auth index: create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("auth index: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, authDir: authDir}
	if err = store.configure(ctx, cfg); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = store.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close 关闭索引库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) configure(ctx context.Context, cfg config.AuthIndexCacheConfig) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("auth index: store is nil")
	}
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	if cfg.PageCacheKB > 0 {
		pragmas = append(pragmas, fmt.Sprintf("PRAGMA cache_size=-%d", cfg.PageCacheKB))
	}
	for _, stmt := range pragmas {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("auth index: configure sqlite: %w", err)
		}
	}
	return nil
}

func (s *Store) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS auth_index (
			id TEXT PRIMARY KEY,
			file_name TEXT NOT NULL,
			file_path TEXT NOT NULL,
			provider TEXT NOT NULL,
			disabled INTEGER NOT NULL DEFAULT 0,
			unavailable INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			project_id TEXT NOT NULL DEFAULT '',
			account TEXT NOT NULL DEFAULT '',
			account_type TEXT NOT NULL DEFAULT '',
			priority INTEGER NOT NULL DEFAULT 0,
			file_mtime INTEGER NOT NULL,
			file_size INTEGER NOT NULL,
			file_sha256 TEXT NOT NULL,
			cooldown_until INTEGER NOT NULL DEFAULT 0,
			updated_unix INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS auth_payload (
			id TEXT PRIMARY KEY,
			json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS auth_meta (
			k TEXT PRIMARY KEY,
			v TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_index_provider_disabled_cooldown ON auth_index(provider, disabled, cooldown_until)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_index_file_name ON auth_index(file_name)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_index_file_stat ON auth_index(file_mtime, file_size)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("auth index: ensure schema: %w", err)
		}
	}
	return nil
}

// SyncDir 增量对账认证目录，仅重读新增或 stat 变化的 JSON 文件。
func (s *Store) SyncDir(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("auth index: store is nil")
	}
	entries, err := os.ReadDir(s.authDir)
	if err != nil {
		return fmt.Errorf("auth index: read auth dir: %w", err)
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		fullPath := filepath.Join(s.authDir, entry.Name())
		id := authIDForPath(fullPath, s.authDir)
		seen[id] = struct{}{}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		mtime := info.ModTime().Unix()
		size := info.Size()
		var count int
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM auth_index WHERE id=? AND file_mtime=? AND file_size=?`, id, mtime, size).Scan(&count)
		if err == nil && count > 0 {
			continue
		}
		if err = s.UpsertFile(ctx, fullPath); err != nil {
			return err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM auth_index`)
	if err != nil {
		return fmt.Errorf("auth index: list indexed ids: %w", err)
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var id string
		if errScan := rows.Scan(&id); errScan != nil {
			return fmt.Errorf("auth index: scan id: %w", errScan)
		}
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		if err = s.Delete(ctx, id); err != nil {
			return err
		}
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO auth_meta(k, v) VALUES('last_full_scan_unix', ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, strconv.FormatInt(time.Now().Unix(), 10))
	return nil
}

// UpsertFile 重读单个认证文件并写入索引。
func (s *Store) UpsertFile(ctx context.Context, path string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("auth index: store is nil")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("auth index: read file %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("auth index: stat file %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	entry := entryFromJSON(path, s.authDir, data, info, hex.EncodeToString(sum[:]))
	if entry.Provider == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth index: begin upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO auth_index (
		id, file_name, file_path, provider, disabled, unavailable, status, email, project_id, account, account_type,
		priority, file_mtime, file_size, file_sha256, cooldown_until, updated_unix
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		file_name=excluded.file_name,
		file_path=excluded.file_path,
		provider=excluded.provider,
		disabled=excluded.disabled,
		unavailable=excluded.unavailable,
		status=excluded.status,
		email=excluded.email,
		project_id=excluded.project_id,
		account=excluded.account,
		account_type=excluded.account_type,
		priority=excluded.priority,
		file_mtime=excluded.file_mtime,
		file_size=excluded.file_size,
		file_sha256=excluded.file_sha256,
		cooldown_until=excluded.cooldown_until,
		updated_unix=excluded.updated_unix`,
		entry.ID, entry.FileName, entry.FilePath, entry.Provider, boolInt(entry.Disabled), boolInt(entry.Unavailable), entry.Status,
		entry.Email, entry.ProjectID, entry.Account, entry.AccountType, entry.Priority, entry.ModTimeUnix, entry.Size, strings.TrimSpace(hash),
		entry.CooldownUntil, entry.UpdatedUnix)
	if err != nil {
		return fmt.Errorf("auth index: upsert index: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO auth_payload(id, json) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET json=excluded.json`, entry.ID, string(data))
	if err != nil {
		return fmt.Errorf("auth index: upsert payload: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("auth index: commit upsert: %w", err)
	}
	return nil
}

// Delete 从索引中删除认证文件记录。
func (s *Store) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_payload WHERE id=?`, id); err != nil {
		return fmt.Errorf("auth index: delete payload: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_index WHERE id=?`, id); err != nil {
		return fmt.Errorf("auth index: delete index: %w", err)
	}
	return nil
}

// Query 按索引列分页查询认证文件。
func (s *Store) Query(ctx context.Context, opts QueryOptions) (QueryResult, error) {
	if s == nil || s.db == nil {
		return QueryResult{}, fmt.Errorf("auth index: store is nil")
	}
	where, args := buildWhere(opts)
	countSQL := `SELECT COUNT(1) FROM auth_index` + where
	var total int
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return QueryResult{}, fmt.Errorf("auth index: count: %w", err)
	}
	if opts.Page <= 0 {
		opts.Page = 1
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 200
	}
	offset := (opts.Page - 1) * opts.PageSize
	querySQL := `SELECT id, file_name, file_path, provider, disabled, unavailable, status, email, project_id, account, account_type,
		priority, file_mtime, file_size, cooldown_until, updated_unix FROM auth_index` + where + ` ORDER BY lower(file_name) ASC LIMIT ? OFFSET ?`
	queryArgs := append(append([]any(nil), args...), opts.PageSize, offset)
	rows, err := s.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return QueryResult{}, fmt.Errorf("auth index: query: %w", err)
	}
	defer rows.Close()
	result := QueryResult{Total: total}
	for rows.Next() {
		var entry Entry
		var disabled, unavailable int
		if errScan := rows.Scan(&entry.ID, &entry.FileName, &entry.FilePath, &entry.Provider, &disabled, &unavailable, &entry.Status,
			&entry.Email, &entry.ProjectID, &entry.Account, &entry.AccountType, &entry.Priority, &entry.ModTimeUnix, &entry.Size,
			&entry.CooldownUntil, &entry.UpdatedUnix); errScan != nil {
			return QueryResult{}, fmt.Errorf("auth index: scan row: %w", errScan)
		}
		entry.Disabled = disabled != 0
		entry.Unavailable = unavailable != 0
		result.Entries = append(result.Entries, entry)
	}
	if err = rows.Err(); err != nil {
		return QueryResult{}, fmt.Errorf("auth index: rows: %w", err)
	}
	return result, nil
}

func buildWhere(opts QueryOptions) (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if provider := strings.ToLower(strings.TrimSpace(opts.Provider)); provider != "" {
		clauses = append(clauses, "lower(provider)=?")
		args = append(args, provider)
	}
	if opts.Disabled != nil {
		clauses = append(clauses, "disabled=?")
		args = append(args, boolInt(*opts.Disabled))
	}
	if keyword := strings.ToLower(strings.TrimSpace(opts.Keyword)); keyword != "" {
		clauses = append(clauses, "(lower(file_name) LIKE ? OR lower(email) LIKE ? OR lower(project_id) LIKE ? OR lower(account) LIKE ?)")
		like := "%" + keyword + "%"
		args = append(args, like, like, like, like)
	}
	if opts.CooldownOnly {
		clauses = append(clauses, "cooldown_until>?")
		args = append(args, time.Now().Unix())
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func entryFromJSON(path, authDir string, data []byte, info os.FileInfo, hash string) Entry {
	name := filepath.Base(path)
	entry := Entry{
		ID:          authIDForPath(path, authDir),
		FileName:    name,
		FilePath:    path,
		Provider:    strings.ToLower(strings.TrimSpace(gjson.GetBytes(data, "type").String())),
		Email:       strings.TrimSpace(gjson.GetBytes(data, "email").String()),
		ProjectID:   strings.TrimSpace(gjson.GetBytes(data, "project_id").String()),
		Account:     strings.TrimSpace(gjson.GetBytes(data, "account").String()),
		AccountType: firstNonEmpty(gjson.GetBytes(data, "account_type").String(), gjson.GetBytes(data, "accountType").String(), gjson.GetBytes(data, "auth_kind").String(), gjson.GetBytes(data, "authKind").String()),
		Status:      strings.TrimSpace(gjson.GetBytes(data, "status").String()),
		Size:        info.Size(),
		ModTimeUnix: info.ModTime().Unix(),
		UpdatedUnix: time.Now().Unix(),
	}
	_ = hash
	if entry.Provider == "gemini" && entry.ProjectID != "" && strings.TrimSpace(gjson.GetBytes(data, "api_key").String()) == "" && strings.TrimSpace(gjson.GetBytes(data, "apiKey").String()) == "" {
		if entry.AccountType == "" {
			entry.AccountType = "oauth"
		}
		if entry.Account == "" {
			entry.Account = entry.Email
		}
	}
	entry.Disabled = gjson.GetBytes(data, "disabled").Bool()
	entry.Unavailable = gjson.GetBytes(data, "unavailable").Bool()
	if entry.Status == "" {
		if entry.Disabled {
			entry.Status = "disabled"
		} else {
			entry.Status = "active"
		}
	}
	if priority := gjson.GetBytes(data, "priority"); priority.Exists() {
		switch priority.Type {
		case gjson.Number:
			entry.Priority = int(priority.Int())
		case gjson.String:
			if parsed, err := strconv.Atoi(strings.TrimSpace(priority.String())); err == nil {
				entry.Priority = parsed
			}
		}
	}
	for _, key := range []string{"cooldown_until", "next_retry_after", "next_retry_unix"} {
		if unix := parseTimeishUnix(gjson.GetBytes(data, key)); unix > entry.CooldownUntil {
			entry.CooldownUntil = unix
		}
	}
	return entry
}

func authIDForPath(path, authDir string) string {
	id := path
	if authDir != "" {
		if rel, err := filepath.Rel(authDir, path); err == nil && rel != "" {
			id = rel
		}
	}
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func parseTimeishUnix(value gjson.Result) int64 {
	if !value.Exists() {
		return 0
	}
	switch value.Type {
	case gjson.Number:
		return value.Int()
	case gjson.String:
		raw := strings.TrimSpace(value.String())
		if raw == "" {
			return 0
		}
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return unix
		}
		if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			return ts.Unix()
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
