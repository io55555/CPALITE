package capture

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Settings struct {
	Enabled      bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
	MaxBodyBytes int  `json:"max_body_bytes"`
}

type Record struct {
	ID                     int64     `json:"id"`
	CreatedAt              time.Time `json:"created_at"`
	RequestID              string    `json:"request_id"`
	Method                 string    `json:"method"`
	Path                   string    `json:"path"`
	Query                  string    `json:"query,omitempty"`
	StatusCode             int       `json:"status_code"`
	Success                bool      `json:"success"`
	DurationMs             int64     `json:"duration_ms"`
	Provider               string    `json:"provider,omitempty"`
	AccessProvider         string    `json:"access_provider,omitempty"`
	AuthID                 string    `json:"auth_id,omitempty"`
	AuthIndex              string    `json:"auth_index,omitempty"`
	Token                  string    `json:"token,omitempty"`
	APIKey                 string    `json:"api_key,omitempty"`
	ProxyURL               string    `json:"proxy_url,omitempty"`
	ErrorText              string    `json:"error_text,omitempty"`
	RequestHeaders         string    `json:"request_headers,omitempty"`
	RequestBody            string    `json:"request_body,omitempty"`
	UpstreamRequestURL     string    `json:"upstream_request_url,omitempty"`
	UpstreamRequestHeaders string    `json:"upstream_request_headers,omitempty"`
	UpstreamRequestBody    string    `json:"upstream_request_body,omitempty"`
	UpstreamStatusCode     int       `json:"upstream_status_code"`
	UpstreamResponseHeaders string   `json:"upstream_response_headers,omitempty"`
	UpstreamResponseBody   string    `json:"upstream_response_body,omitempty"`
	ResponseHeaders        string    `json:"response_headers,omitempty"`
	ResponseBody           string    `json:"response_body,omitempty"`
}

type ListFilter struct {
	Query      string
	FailedOnly bool
	Limit      int
	Offset     int
}

type Store struct {
	db         *sql.DB
	settingsMu sync.RWMutex
	settings   Settings
}

var (
	defaultStoreMu sync.RWMutex
	defaultStore   *Store
)

func InitDefaultStore(path string) error {
	store, err := NewStore(path)
	if err != nil {
		return err
	}
	return replaceDefaultStore(store)
}

func DefaultStore() *Store {
	defaultStoreMu.RLock()
	defer defaultStoreMu.RUnlock()
	return defaultStore
}

func CloseDefaultStore() error {
	return replaceDefaultStore(nil)
}

func replaceDefaultStore(next *Store) error {
	defaultStoreMu.Lock()
	prev := defaultStore
	defaultStore = next
	defaultStoreMu.Unlock()
	if prev != nil {
		return prev.Close()
	}
	return nil
}

func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("capture sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.loadSettings(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS capture_records (
id INTEGER PRIMARY KEY AUTOINCREMENT,
created_at TEXT NOT NULL,
request_id TEXT NOT NULL DEFAULT '',
method TEXT NOT NULL DEFAULT '',
path TEXT NOT NULL DEFAULT '',
query_text TEXT NOT NULL DEFAULT '',
status_code INTEGER NOT NULL DEFAULT 0,
success INTEGER NOT NULL DEFAULT 0,
duration_ms INTEGER NOT NULL DEFAULT 0,
provider TEXT NOT NULL DEFAULT '',
access_provider TEXT NOT NULL DEFAULT '',
auth_id TEXT NOT NULL DEFAULT '',
auth_index TEXT NOT NULL DEFAULT '',
token_text TEXT NOT NULL DEFAULT '',
api_key TEXT NOT NULL DEFAULT '',
proxy_url TEXT NOT NULL DEFAULT '',
error_text TEXT NOT NULL DEFAULT '',
request_headers TEXT NOT NULL DEFAULT '',
request_body TEXT NOT NULL DEFAULT '',
upstream_request_url TEXT NOT NULL DEFAULT '',
upstream_request_headers TEXT NOT NULL DEFAULT '',
upstream_request_body TEXT NOT NULL DEFAULT '',
upstream_status_code INTEGER NOT NULL DEFAULT 0,
upstream_response_headers TEXT NOT NULL DEFAULT '',
upstream_response_body TEXT NOT NULL DEFAULT '',
response_headers TEXT NOT NULL DEFAULT '',
response_body TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS idx_capture_created_at ON capture_records(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_capture_request_id ON capture_records(request_id)`,
		`CREATE INDEX IF NOT EXISTS idx_capture_auth_index ON capture_records(auth_index)`,
		`CREATE TABLE IF NOT EXISTS capture_settings (
singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
enabled INTEGER NOT NULL DEFAULT 0,
retention_days INTEGER NOT NULL DEFAULT 7,
max_body_bytes INTEGER NOT NULL DEFAULT 65536
)`,
		`INSERT INTO capture_settings(singleton, enabled, retention_days, max_body_bytes)
SELECT 1, 0, 7, 65536
WHERE NOT EXISTS (SELECT 1 FROM capture_settings WHERE singleton = 1)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadSettings(ctx context.Context) error {
	row := s.db.QueryRowContext(ctx, `SELECT enabled, retention_days, max_body_bytes FROM capture_settings WHERE singleton = 1`)
	var enabledInt int
	var settings Settings
	if err := row.Scan(&enabledInt, &settings.RetentionDays, &settings.MaxBodyBytes); err != nil {
		return err
	}
	settings.Enabled = enabledInt != 0
	s.settingsMu.Lock()
	s.settings = normalizeSettings(settings)
	s.settingsMu.Unlock()
	return nil
}

func (s *Store) Settings() Settings {
	if s == nil {
		return normalizeSettings(Settings{})
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

func (s *Store) UpdateSettings(ctx context.Context, settings Settings) (Settings, error) {
	if s == nil || s.db == nil {
		return Settings{}, fmt.Errorf("capture store is nil")
	}
	settings = normalizeSettings(settings)
	_, err := s.db.ExecContext(ctx, `UPDATE capture_settings SET enabled = ?, retention_days = ?, max_body_bytes = ? WHERE singleton = 1`,
		boolToInt(settings.Enabled), settings.RetentionDays, settings.MaxBodyBytes)
	if err != nil {
		return Settings{}, err
	}
	s.settingsMu.Lock()
	s.settings = settings
	s.settingsMu.Unlock()
	return settings, nil
}

func (s *Store) Insert(ctx context.Context, record Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	settings := s.Settings()
	if !settings.Enabled {
		return nil
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	record.RequestHeaders = trimForStorage(record.RequestHeaders, settings.MaxBodyBytes)
	record.RequestBody = trimForStorage(record.RequestBody, settings.MaxBodyBytes)
	record.UpstreamRequestHeaders = trimForStorage(record.UpstreamRequestHeaders, settings.MaxBodyBytes)
	record.UpstreamRequestBody = trimForStorage(record.UpstreamRequestBody, settings.MaxBodyBytes)
	record.UpstreamResponseHeaders = trimForStorage(record.UpstreamResponseHeaders, settings.MaxBodyBytes)
	record.UpstreamResponseBody = trimForStorage(record.UpstreamResponseBody, settings.MaxBodyBytes)
	record.ResponseHeaders = trimForStorage(record.ResponseHeaders, settings.MaxBodyBytes)
	record.ResponseBody = trimForStorage(record.ResponseBody, settings.MaxBodyBytes)
	record.ErrorText = trimForStorage(record.ErrorText, settings.MaxBodyBytes)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO capture_records (
created_at, request_id, method, path, query_text, status_code, success, duration_ms,
provider, access_provider, auth_id, auth_index, token_text, api_key, proxy_url, error_text,
request_headers, request_body, upstream_request_url, upstream_request_headers, upstream_request_body,
upstream_status_code, upstream_response_headers, upstream_response_body, response_headers, response_body
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.RequestID,
		record.Method,
		record.Path,
		record.Query,
		record.StatusCode,
		boolToInt(record.Success),
		record.DurationMs,
		record.Provider,
		record.AccessProvider,
		record.AuthID,
		record.AuthIndex,
		record.Token,
		record.APIKey,
		record.ProxyURL,
		record.ErrorText,
		record.RequestHeaders,
		record.RequestBody,
		record.UpstreamRequestURL,
		record.UpstreamRequestHeaders,
		record.UpstreamRequestBody,
		record.UpstreamStatusCode,
		record.UpstreamResponseHeaders,
		record.UpstreamResponseBody,
		record.ResponseHeaders,
		record.ResponseBody,
	); err != nil {
		return err
	}
	return s.cleanupOld(ctx, settings.RetentionDays)
}

func (s *Store) cleanupOld(ctx context.Context, retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `DELETE FROM capture_records WHERE created_at < ?`, cutoff)
	return err
}

func (s *Store) List(ctx context.Context, filter ListFilter) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	query := `SELECT id, created_at, request_id, method, path, query_text, status_code, success, duration_ms,
provider, access_provider, auth_id, auth_index, token_text, api_key, proxy_url, error_text
FROM capture_records`
	args := make([]any, 0, 4)
	where := make([]string, 0, 2)
	if filter.FailedOnly {
		where = append(where, "success = 0")
	}
	if q := strings.TrimSpace(filter.Query); q != "" {
		where = append(where, `(request_id LIKE ? OR auth_index LIKE ? OR token_text LIKE ? OR api_key LIKE ? OR provider LIKE ? OR request_body LIKE ? OR upstream_response_body LIKE ? OR response_body LIKE ?)`)
		like := "%" + q + "%"
		for i := 0; i < 8; i++ {
			args = append(args, like)
		}
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := make([]Record, 0, limit)
	for rows.Next() {
		var createdAt string
		var successInt int
		var item Record
		if err := rows.Scan(
			&item.ID, &createdAt, &item.RequestID, &item.Method, &item.Path, &item.Query,
			&item.StatusCode, &successInt, &item.DurationMs, &item.Provider, &item.AccessProvider,
			&item.AuthID, &item.AuthIndex, &item.Token, &item.APIKey, &item.ProxyURL, &item.ErrorText,
		); err != nil {
			return nil, err
		}
		item.Success = successInt != 0
		if parsed, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			item.CreatedAt = parsed
		}
		records = append(records, item)
	}
	return records, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*Record, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, created_at, request_id, method, path, query_text, status_code, success, duration_ms,
provider, access_provider, auth_id, auth_index, token_text, api_key, proxy_url, error_text,
request_headers, request_body, upstream_request_url, upstream_request_headers, upstream_request_body,
upstream_status_code, upstream_response_headers, upstream_response_body, response_headers, response_body
FROM capture_records WHERE id = ?`, id)
	var createdAt string
	var successInt int
	var item Record
	if err := row.Scan(
		&item.ID, &createdAt, &item.RequestID, &item.Method, &item.Path, &item.Query, &item.StatusCode, &successInt, &item.DurationMs,
		&item.Provider, &item.AccessProvider, &item.AuthID, &item.AuthIndex, &item.Token, &item.APIKey, &item.ProxyURL, &item.ErrorText,
		&item.RequestHeaders, &item.RequestBody, &item.UpstreamRequestURL, &item.UpstreamRequestHeaders, &item.UpstreamRequestBody,
		&item.UpstreamStatusCode, &item.UpstreamResponseHeaders, &item.UpstreamResponseBody, &item.ResponseHeaders, &item.ResponseBody,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	item.Success = successInt != 0
	if parsed, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		item.CreatedAt = parsed
	}
	return &item, nil
}

func (s *Store) Clear(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM capture_records`)
	return err
}

func (s *Store) Export(ctx context.Context, filter ListFilter) (string, error) {
	records, err := s.List(ctx, filter)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(records))
	for _, item := range records {
		full, err := s.Get(ctx, item.ID)
		if err != nil || full == nil {
			continue
		}
		raw, _ := json.Marshal(full)
		lines = append(lines, string(raw))
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func normalizeSettings(settings Settings) Settings {
	if settings.RetentionDays < 0 {
		settings.RetentionDays = 0
	}
	if settings.MaxBodyBytes <= 0 {
		settings.MaxBodyBytes = 65536
	}
	return settings
}

func trimForStorage(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...[truncated]"
}

func headersToJSON(header http.Header, limit int) string {
	if len(header) == 0 {
		return ""
	}
	raw, _ := json.Marshal(header)
	return trimForStorage(string(raw), limit)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
