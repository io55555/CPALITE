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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	_ "modernc.org/sqlite"
)

// Store 维护认证文件目录的 SQLite 轻量索引；文件目录仍是唯一事实来源。
type Store struct {
	db      *sql.DB
	authDir string
	base    coreauth.Store
	cfg     *config.Config
	parser  synthesizer.PluginAuthParser
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
	Summary QuerySummary
}

// QuerySummary 记录匹配过滤条件后的全量聚合信息，不受分页影响。
type QuerySummary struct {
	Total       int
	Active      int
	Disabled    int
	Unavailable int
	ByProvider  map[string]int
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

// WrapFileStore 在文件 store 外包裹 SQLite 索引层；文件写入仍由 base 负责。
func WrapFileStore(ctx context.Context, base coreauth.Store, authDir string, cfg *config.Config, parser synthesizer.PluginAuthParser) (coreauth.Store, error) {
	if cfg == nil || !cfg.AuthIndexCache.Enabled {
		return base, nil
	}
	store, err := Open(ctx, authDir, cfg.AuthIndexCache)
	if err != nil {
		return base, err
	}
	store.base = base
	store.SetSynthesisContext(cfg, parser)
	if err = store.SyncDir(ctx); err != nil {
		_ = store.Close()
		return base, err
	}
	return store, nil
}

// SetSynthesisContext 设置按需 hydrate 时需要的运行时配置和插件解析器。
func (s *Store) SetSynthesisContext(cfg *config.Config, parser synthesizer.PluginAuthParser) {
	if s == nil {
		return
	}
	s.cfg = cfg
	s.parser = parser
}

// Close 关闭索引库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SetBaseDir 同步文件 store 的认证目录，并更新索引目录。
func (s *Store) SetBaseDir(dir string) {
	if s == nil {
		return
	}
	if setter, ok := s.base.(interface{ SetBaseDir(string) }); ok {
		setter.SetBaseDir(dir)
	}
	if strings.TrimSpace(dir) != "" {
		s.authDir = strings.TrimSpace(dir)
	}
}

// AuthDir 返回当前索引绑定的认证目录。
func (s *Store) AuthDir() string {
	if s == nil {
		return ""
	}
	return s.authDir
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
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		mtime := info.ModTime().Unix()
		size := info.Size()
		indexedIDs, errIndexed := s.indexedIDsForUnchangedFile(ctx, fullPath, mtime, size)
		if errIndexed == nil && len(indexedIDs) > 0 {
			for _, id := range indexedIDs {
				seen[id] = struct{}{}
			}
			continue
		}
		if err = s.UpsertFile(ctx, fullPath); err != nil {
			return err
		}
		indexedIDs, errIndexed = s.indexedIDsForFile(ctx, fullPath)
		if errIndexed != nil {
			return errIndexed
		}
		for _, id := range indexedIDs {
			seen[id] = struct{}{}
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
	if err = rows.Err(); err != nil {
		return fmt.Errorf("auth index: iterate indexed ids: %w", err)
	}
	for _, id := range stale {
		if err = s.deleteIndexOnly(ctx, id); err != nil {
			return err
		}
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO auth_meta(k, v) VALUES('last_full_scan_unix', ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, strconv.FormatInt(time.Now().Unix(), 10))
	return nil
}

func (s *Store) indexedIDsForUnchangedFile(ctx context.Context, path string, mtime int64, size int64) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("auth index: store is nil")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM auth_index WHERE file_path=? AND file_mtime=? AND file_size=?`, path, mtime, size)
	if err != nil {
		return nil, fmt.Errorf("auth index: query unchanged file ids: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

func (s *Store) indexedIDsForFile(ctx context.Context, path string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("auth index: store is nil")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM auth_index WHERE file_path=?`, path)
	if err != nil {
		return nil, fmt.Errorf("auth index: query file ids: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

func scanIDs(rows *sql.Rows) ([]string, error) {
	ids := make([]string, 0, 1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("auth index: scan id: %w", err)
		}
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth index: iterate ids: %w", err)
	}
	return ids, nil
}

// List 返回轻量认证对象，避免启动时常驻完整 JSON payload。
func (s *Store) List(ctx context.Context) ([]*coreauth.Auth, error) {
	if s == nil {
		return nil, fmt.Errorf("auth index: store is nil")
	}
	if err := s.SyncDir(ctx); err != nil {
		if s.base != nil {
			return s.base.List(ctx)
		}
		return nil, err
	}
	return s.ListLightweight(ctx)
}

// Save 先写入文件事实来源，再同步 SQLite 索引。
func (s *Store) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if s == nil || s.base == nil {
		return "", fmt.Errorf("auth index: base store is nil")
	}
	path, err := s.base.Save(ctx, auth)
	if err != nil {
		return path, err
	}
	if strings.TrimSpace(path) != "" {
		if errIndex := s.UpsertFile(ctx, path); errIndex != nil {
			return path, errIndex
		}
	}
	return path, nil
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
	hash := hex.EncodeToString(sum[:])
	entries, err := s.entriesFromJSON(path, data, info, hash)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("auth index: begin upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `DELETE FROM auth_payload WHERE id IN (SELECT id FROM auth_index WHERE file_path=?)`, path); err != nil {
		return fmt.Errorf("auth index: delete old payloads for file: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM auth_index WHERE file_path=?`, path); err != nil {
		return fmt.Errorf("auth index: delete old rows for file: %w", err)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Provider) == "" {
			continue
		}
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
			entry.Email, entry.ProjectID, entry.Account, entry.AccountType, entry.Priority, entry.ModTimeUnix, entry.Size, hash,
			entry.CooldownUntil, entry.UpdatedUnix)
		if err != nil {
			return fmt.Errorf("auth index: upsert index: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO auth_payload(id, json) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET json=excluded.json`, entry.ID, string(data))
		if err != nil {
			return fmt.Errorf("auth index: upsert payload: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("auth index: commit upsert: %w", err)
	}
	return nil
}

// Delete 先删除文件事实来源，再删除索引记录。
func (s *Store) Delete(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if s.base != nil {
		deleteID := id
		if filepath.IsAbs(id) {
			if rel, errRel := filepath.Rel(s.authDir, id); errRel == nil && rel != "" {
				deleteID = rel
			}
		}
		if err := s.base.Delete(ctx, deleteID); err != nil {
			return err
		}
		id = authIDForPath(filepath.Join(s.authDir, deleteID), s.authDir)
	}
	return s.deleteIndexOnly(ctx, id)
}

// DeleteIndexOnly 仅清理 SQLite 索引，供 watcher 删除事件使用。
func (s *Store) DeleteIndexOnly(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.deleteIndexOnly(ctx, id)
}

// DeleteFileIndexOnly 按真实文件路径清理该文件展开出的全部索引行。
func (s *Store) DeleteFileIndexOnly(ctx context.Context, path string) error {
	if s == nil || s.db == nil {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_payload WHERE id IN (SELECT id FROM auth_index WHERE file_path=?)`, path); err != nil {
		return fmt.Errorf("auth index: delete file payloads: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_index WHERE file_path=?`, path); err != nil {
		return fmt.Errorf("auth index: delete file index: %w", err)
	}
	return nil
}

func (s *Store) deleteIndexOnly(ctx context.Context, id string) error {
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
	summary, err := s.Summary(ctx, opts)
	if err != nil {
		return QueryResult{}, err
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
	result := QueryResult{Total: total, Summary: summary}
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

// Summary 返回匹配过滤条件后的全量健康统计，供分页列表和仪表盘复用。
func (s *Store) Summary(ctx context.Context, opts QueryOptions) (QuerySummary, error) {
	if s == nil || s.db == nil {
		return QuerySummary{}, fmt.Errorf("auth index: store is nil")
	}
	where, args := buildWhere(opts)
	var summary QuerySummary
	if err := s.db.QueryRowContext(ctx, `SELECT
		COUNT(1),
		COALESCE(SUM(CASE WHEN disabled != 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN disabled = 0 AND unavailable != 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN disabled = 0 AND unavailable = 0 THEN 1 ELSE 0 END), 0)
		FROM auth_index`+where, args...).Scan(&summary.Total, &summary.Disabled, &summary.Unavailable, &summary.Active); err != nil {
		return QuerySummary{}, fmt.Errorf("auth index: summary: %w", err)
	}
	summary.ByProvider = make(map[string]int)
	rows, err := s.db.QueryContext(ctx, `SELECT lower(provider), COUNT(1) FROM auth_index`+where+` GROUP BY lower(provider)`, args...)
	if err != nil {
		return QuerySummary{}, fmt.Errorf("auth index: provider summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var provider string
		var count int
		if errScan := rows.Scan(&provider, &count); errScan != nil {
			return QuerySummary{}, fmt.Errorf("auth index: scan provider summary: %w", errScan)
		}
		provider = strings.TrimSpace(provider)
		if provider == "" {
			provider = "unknown"
		}
		summary.ByProvider[provider] = count
	}
	if err = rows.Err(); err != nil {
		return QuerySummary{}, fmt.Errorf("auth index: iterate provider summary: %w", err)
	}
	return summary, nil
}

// ListLightweight 返回不含完整 Metadata 的轻量认证对象，用于降低常驻内存。
func (s *Store) ListLightweight(ctx context.Context) ([]*coreauth.Auth, error) {
	result, err := s.Query(ctx, QueryOptions{Page: 1, PageSize: 1_000_000})
	if err != nil {
		return nil, err
	}
	auths := make([]*coreauth.Auth, 0, len(result.Entries))
	for _, entry := range result.Entries {
		auth := authFromEntry(entry)
		if auth != nil {
			auths = append(auths, auth)
		}
	}
	return auths, nil
}

// HydrateAuth 按 ID 从 SQLite payload 或文件目录恢复完整认证对象。
func (s *Store) HydrateAuth(ctx context.Context, id string) (*coreauth.Auth, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("auth index: store is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("auth index: auth id is empty")
	}
	var (
		payload  string
		filePath string
	)
	err := s.db.QueryRowContext(ctx, `SELECT p.json, i.file_path FROM auth_payload p JOIN auth_index i ON i.id=p.id WHERE p.id=?`, id).Scan(&payload, &filePath)
	if err != nil {
		filePath = filepath.Join(s.authDir, id)
		data, errRead := os.ReadFile(filePath)
		if errRead != nil {
			return nil, fmt.Errorf("auth index: hydrate %s: query payload: %v; read file: %w", id, err, errRead)
		}
		payload = string(data)
	}
	sctx := &synthesizer.SynthesisContext{
		Config:           s.cfg,
		AuthDir:          s.authDir,
		Now:              time.Now(),
		IDGenerator:      synthesizer.NewStableIDGenerator(),
		PluginAuthParser: s.parser,
	}
	auths, err := synthesizer.SynthesizeAuthFile(sctx, filePath, []byte(payload))
	if err != nil {
		return nil, err
	}
	for _, auth := range auths {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.ID), id) {
			return auth, nil
		}
	}
	if len(auths) > 0 && auths[0] != nil {
		return auths[0], nil
	}
	return nil, fmt.Errorf("auth index: hydrate %s produced no auth", id)
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

func authFromEntry(entry Entry) *coreauth.Auth {
	id := strings.TrimSpace(entry.ID)
	provider := strings.ToLower(strings.TrimSpace(entry.Provider))
	if id == "" || provider == "" {
		return nil
	}
	status := coreauth.Status(strings.TrimSpace(entry.Status))
	if status == "" {
		status = coreauth.StatusActive
	}
	auth := &coreauth.Auth{
		ID:          id,
		Provider:    provider,
		FileName:    entry.FileName,
		Label:       firstNonEmpty(entry.Email, entry.Account, entry.ProjectID, entry.FileName),
		Status:      status,
		Disabled:    entry.Disabled,
		Unavailable: entry.Unavailable,
		Attributes: map[string]string{
			coreauth.AttributePath:          entry.FilePath,
			coreauth.AttributeSource:        entry.FilePath,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			coreauth.AttributeSQLiteStub:    "true",
		},
		CreatedAt: time.Unix(entry.ModTimeUnix, 0).UTC(),
		UpdatedAt: time.Unix(entry.ModTimeUnix, 0).UTC(),
	}
	if entry.Email != "" {
		auth.Attributes["email"] = entry.Email
	}
	if entry.ProjectID != "" {
		auth.Attributes["project_id"] = entry.ProjectID
	}
	if entry.AccountType != "" {
		auth.Attributes[coreauth.AttributeAuthKind] = entry.AccountType
	}
	if entry.Account != "" {
		auth.Attributes["account"] = entry.Account
	}
	if entry.Priority != 0 {
		auth.Attributes[coreauth.AttributeWeight] = strconv.Itoa(entry.Priority)
		auth.Attributes["priority"] = strconv.Itoa(entry.Priority)
	}
	if entry.CooldownUntil > 0 {
		auth.NextRetryAfter = time.Unix(entry.CooldownUntil, 0).UTC()
		auth.Unavailable = true
	}
	auth.EnsureIndex()
	return auth
}

func (s *Store) entriesFromJSON(path string, data []byte, info os.FileInfo, hash string) ([]Entry, error) {
	sctx := &synthesizer.SynthesisContext{
		Config:           s.cfg,
		AuthDir:          s.authDir,
		Now:              info.ModTime(),
		IDGenerator:      synthesizer.NewStableIDGenerator(),
		PluginAuthParser: s.parser,
	}
	auths, err := synthesizer.SynthesizeAuthFile(sctx, path, data)
	if err != nil {
		return nil, fmt.Errorf("auth index: synthesize %s: %w", filepath.Base(path), err)
	}
	if err == nil && len(auths) > 0 {
		entries := make([]Entry, 0, len(auths))
		for _, auth := range auths {
			if entry, ok := entryFromAuth(path, data, info, hash, auth); ok {
				entries = append(entries, entry)
			}
		}
		return entries, nil
	}
	entry := entryFromJSON(path, s.authDir, data, info, hash)
	if strings.TrimSpace(entry.Provider) == "" {
		return nil, nil
	}
	return []Entry{entry}, nil
}

func entryFromAuth(path string, data []byte, info os.FileInfo, hash string, auth *coreauth.Auth) (Entry, bool) {
	if auth == nil {
		return Entry{}, false
	}
	id := strings.TrimSpace(auth.ID)
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if id == "" || provider == "" {
		return Entry{}, false
	}
	fileName := strings.TrimSpace(auth.FileName)
	if fileName == "" {
		fileName = filepath.Base(path)
	}
	accountType, account := auth.AccountInfo()
	entry := Entry{
		ID:            id,
		FileName:      fileName,
		FilePath:      path,
		Provider:      provider,
		Disabled:      auth.Disabled || auth.Status == coreauth.StatusDisabled,
		Unavailable:   auth.Unavailable,
		Status:        strings.TrimSpace(string(auth.Status)),
		Email:         firstNonEmpty(metadataString(auth.Metadata, "email"), authAttribute(auth, "email")),
		ProjectID:     firstNonEmpty(metadataString(auth.Metadata, "project_id"), authAttribute(auth, "project_id")),
		Account:       firstNonEmpty(account, metadataString(auth.Metadata, "account")),
		AccountType:   firstNonEmpty(accountType, metadataString(auth.Metadata, "account_type"), metadataString(auth.Metadata, "auth_kind"), authAttribute(auth, coreauth.AttributeAuthKind)),
		Priority:      parsePriority(auth, data),
		Size:          info.Size(),
		ModTimeUnix:   info.ModTime().Unix(),
		CooldownUntil: auth.NextRetryAfter.Unix(),
		UpdatedUnix:   time.Now().Unix(),
	}
	_ = hash
	if entry.Status == "" {
		if entry.Disabled {
			entry.Status = string(coreauth.StatusDisabled)
		} else {
			entry.Status = string(coreauth.StatusActive)
		}
	}
	if !auth.NextRetryAfter.After(time.Now()) {
		entry.CooldownUntil = 0
	}
	if entry.Account == "" && strings.EqualFold(entry.AccountType, coreauth.AuthKindOAuth) {
		entry.Account = entry.Email
	}
	return entry, true
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[key])
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 || key == "" {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func parsePriority(auth *coreauth.Auth, data []byte) int {
	if auth != nil {
		if priority := authAttribute(auth, coreauth.AttributeWeight); priority != "" {
			if parsed, err := strconv.Atoi(priority); err == nil {
				return parsed
			}
		}
		if priority := authAttribute(auth, "priority"); priority != "" {
			if parsed, err := strconv.Atoi(priority); err == nil {
				return parsed
			}
		}
	}
	if priority := gjson.GetBytes(data, "priority"); priority.Exists() {
		switch priority.Type {
		case gjson.Number:
			return int(priority.Int())
		case gjson.String:
			if parsed, err := strconv.Atoi(strings.TrimSpace(priority.String())); err == nil {
				return parsed
			}
		}
	}
	return 0
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
