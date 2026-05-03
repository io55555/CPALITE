package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("usage sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("usage sqlite mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("usage sqlite open: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) initSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage sqlite store is nil")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS usage_records (
id INTEGER PRIMARY KEY AUTOINCREMENT,
timestamp TEXT NOT NULL,
api_key TEXT NOT NULL DEFAULT '',
provider TEXT NOT NULL DEFAULT '',
model TEXT NOT NULL DEFAULT '',
source TEXT NOT NULL DEFAULT '',
auth_index TEXT NOT NULL DEFAULT '',
auth_type TEXT NOT NULL DEFAULT '',
endpoint TEXT NOT NULL DEFAULT '',
request_id TEXT NOT NULL DEFAULT '',
latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
first_byte_latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (first_byte_latency_ms >= 0),
generation_ms INTEGER NOT NULL DEFAULT 0 CHECK (generation_ms >= 0),
thinking_effort TEXT NOT NULL DEFAULT '',
input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
reasoning_tokens INTEGER NOT NULL DEFAULT 0 CHECK (reasoning_tokens >= 0),
cached_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cached_tokens >= 0),
total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
failed INTEGER NOT NULL DEFAULT 0 CHECK (failed IN (0, 1))
)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_timestamp ON usage_records(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_grouping ON usage_records(api_key, endpoint, provider, model)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_auth_time ON usage_records(auth_index, timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_records_request_id ON usage_records(request_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("usage sqlite init schema: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Insert(ctx context.Context, record PersistedRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("usage sqlite store is nil")
	}
	record.Tokens = normaliseTokenStats(record.Tokens)
	record.LatencyMs = nonNegative(record.LatencyMs)
	record.FirstByteLatencyMs = nonNegative(record.FirstByteLatencyMs)
	record.GenerationMs = nonNegative(record.GenerationMs)
	timestamp := record.Timestamp.UTC()
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO usage_records (
	timestamp, api_key, provider, model, source, auth_index, auth_type, endpoint, request_id,
	latency_ms, first_byte_latency_ms, generation_ms, thinking_effort,
	input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp.Format(sqliteTimestampLayout),
		strings.TrimSpace(record.APIKey),
		strings.TrimSpace(record.Provider),
		strings.TrimSpace(record.Model),
		strings.TrimSpace(record.Source),
		strings.TrimSpace(record.AuthIndex),
		strings.TrimSpace(record.AuthType),
		strings.TrimSpace(record.Endpoint),
		strings.TrimSpace(record.RequestID),
		record.LatencyMs,
		record.FirstByteLatencyMs,
		record.GenerationMs,
		strings.TrimSpace(record.ThinkingEffort),
		record.Tokens.InputTokens,
		record.Tokens.OutputTokens,
		record.Tokens.ReasoningTokens,
		record.Tokens.CachedTokens,
		record.Tokens.TotalTokens,
		boolToInt(record.Failed),
	)
	if err != nil {
		return fmt.Errorf("usage sqlite insert: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Query(ctx context.Context, rng QueryRange) (APIUsage, error) {
	if s == nil || s.db == nil {
		return APIUsage{}, nil
	}
	query := `
SELECT timestamp, api_key, endpoint, provider, model, source, auth_index,
       request_id, latency_ms, first_byte_latency_ms, generation_ms, thinking_effort,
       input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed
FROM usage_records`
	args := make([]any, 0, 2)
	where := make([]string, 0, 2)
	if rng.Start != nil && !rng.Start.IsZero() {
		where = append(where, "timestamp >= ?")
		args = append(args, rng.Start.UTC().Format(sqliteTimestampLayout))
	}
	if rng.End != nil && !rng.End.IsZero() {
		where = append(where, "timestamp < ?")
		args = append(args, rng.End.UTC().Format(sqliteTimestampLayout))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY timestamp ASC, rowid ASC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage sqlite query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := APIUsage{}
	for rows.Next() {
		var (
			timestampText       string
			apiKey              string
			endpoint            string
			provider            string
			model               string
			failedInt           int
			detail              RequestDetail
		)
		if err := rows.Scan(
			&timestampText,
			&apiKey,
			&endpoint,
			&provider,
			&model,
			&detail.Source,
			&detail.AuthIndex,
			&detail.RequestID,
			&detail.LatencyMs,
			&detail.FirstByteLatencyMs,
			&detail.GenerationMs,
			&detail.ThinkingEffort,
			&detail.Tokens.InputTokens,
			&detail.Tokens.OutputTokens,
			&detail.Tokens.ReasoningTokens,
			&detail.Tokens.CachedTokens,
			&detail.Tokens.TotalTokens,
			&failedInt,
		); err != nil {
			return nil, fmt.Errorf("usage sqlite scan: %w", err)
		}
		parsed, err := time.Parse(sqliteTimestampLayout, timestampText)
		if err != nil {
			return nil, fmt.Errorf("usage sqlite parse timestamp: %w", err)
		}
		detail.Timestamp = parsed.UTC()
		detail.Tokens = normaliseTokenStats(detail.Tokens)
		detail.LatencyMs = nonNegative(detail.LatencyMs)
		detail.FirstByteLatencyMs = nonNegative(detail.FirstByteLatencyMs)
		detail.GenerationMs = nonNegative(detail.GenerationMs)
		detail.Failed = failedInt != 0

		key := groupingKey(apiKey, endpoint, provider)
		modelKey := strings.TrimSpace(model)
		if modelKey == "" {
			modelKey = "unknown"
		}
		if result[key] == nil {
			result[key] = map[string][]RequestDetail{}
		}
		result[key][modelKey] = append(result[key][modelKey], detail)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage sqlite rows: %w", err)
	}
	return result, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func groupingKey(apiKey, endpoint, provider string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		return apiKey
	}
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		return endpoint
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		return provider
	}
	return "unknown"
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
