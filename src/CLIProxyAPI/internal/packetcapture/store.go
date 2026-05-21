package packetcapture

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	defaultLimit       = 500
	maxLimit           = 5000
	timestampLayout    = time.RFC3339Nano
	maxPacketTextBytes = 512 * 1024
	rulesCacheTTL      = time.Second
)

type Store struct {
	db              *sql.DB
	mu              sync.Mutex
	pending         []Record
	rulesCache      []Rule
	rulesCacheUntil time.Time
	wake            chan struct{}
	done            chan struct{}
	stopped         chan struct{}
	closeOnce       sync.Once
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("packet capture path is empty")
	}
	if err := preparePath(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("packet capture sqlite open: %w", err)
	}
	db.SetMaxOpenConns(4)
	store := &Store{
		db:      db,
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	go store.writer()
	return store, nil
}

func preparePath(path string) error {
	dir := filepath.Clean(filepath.Dir(path))
	if dir != "." && filepath.Dir(dir) != dir {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("packet capture sqlite mkdir: %w", err)
		}
		_ = os.Chmod(dir, 0o700)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("packet capture sqlite create: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func (s *Store) initSchema(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS packet_records (
	id TEXT PRIMARY KEY,
	timestamp TEXT NOT NULL,
	request_id TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	user_token TEXT NOT NULL DEFAULT '',
	auth_label TEXT NOT NULL DEFAULT '',
	auth_type TEXT NOT NULL DEFAULT '',
	auth_index TEXT NOT NULL DEFAULT '',
	api_key TEXT NOT NULL DEFAULT '',
	client_ua TEXT NOT NULL DEFAULT '',
	endpoint TEXT NOT NULL DEFAULT '',
	upstream_status_code INTEGER NOT NULL DEFAULT 0,
	failed INTEGER NOT NULL DEFAULT 0,
	total_bytes INTEGER NOT NULL DEFAULT 0,
	client_request TEXT NOT NULL DEFAULT '',
	upstream_request TEXT NOT NULL DEFAULT '',
	upstream_response TEXT NOT NULL DEFAULT '',
	client_response TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS idx_packet_records_timestamp ON packet_records(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_packet_records_filter ON packet_records(model, source, provider, upstream_status_code, failed)`,
		`CREATE TABLE IF NOT EXISTS rules (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	record_history INTEGER NOT NULL DEFAULT 1,
	priority INTEGER NOT NULL DEFAULT 100,
	provider TEXT NOT NULL DEFAULT '',
	provider_keyword TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	model_keyword TEXT NOT NULL DEFAULT '',
	packet TEXT NOT NULL DEFAULT 'client_request',
	part TEXT NOT NULL DEFAULT 'body',
	json_path TEXT NOT NULL DEFAULT '',
	header TEXT NOT NULL DEFAULT '',
	operator TEXT NOT NULL DEFAULT 'contains',
	value TEXT NOT NULL DEFAULT '',
	value_number REAL NOT NULL DEFAULT 0,
	action TEXT NOT NULL DEFAULT 'record',
	replacement TEXT NOT NULL DEFAULT '',
	replace_limit INTEGER NOT NULL DEFAULT 0,
	cooldown_seconds INTEGER NOT NULL DEFAULT 0,
	target TEXT NOT NULL DEFAULT '',
	notes TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_rules_enabled_priority ON rules(enabled, priority)`,
		`CREATE TABLE IF NOT EXISTS trigger_records (
	id TEXT PRIMARY KEY,
	rule_id TEXT NOT NULL,
	rule_name TEXT NOT NULL,
	record_id TEXT NOT NULL,
	timestamp TEXT NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	account TEXT NOT NULL DEFAULT '',
	packet TEXT NOT NULL DEFAULT '',
	packet_name TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	cooldown_seconds INTEGER NOT NULL DEFAULT 0
)`,
		`ALTER TABLE trigger_records ADD COLUMN account TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE trigger_records ADD COLUMN packet TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE trigger_records ADD COLUMN packet_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE trigger_records ADD COLUMN cooldown_seconds INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_trigger_records_timestamp ON trigger_records(timestamp)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				continue
			}
			return fmt.Errorf("packet capture init schema: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE rules ADD COLUMN record_history INTEGER NOT NULL DEFAULT 1`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return fmt.Errorf("packet capture migrate rules record_history: %w", err)
	}
	return nil
}

func (s *Store) Enabled(ctx context.Context) bool {
	if s == nil || s.db == nil {
		return false
	}
	var value string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='capture_enabled'`).Scan(&value); err != nil {
		return false
	}
	return value == "1" || strings.EqualFold(value, "true")
}

func (s *Store) SetEnabled(ctx context.Context, enabled bool) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("packet capture store unavailable")
	}
	value := "0"
	if enabled {
		value = "1"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('capture_enabled', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, value)
	return err
}

func (s *Store) Insert(ctx context.Context, record Record) error {
	if s == nil || s.db == nil {
		return nil
	}
	if strings.TrimSpace(record.ID) == "" {
		record.ID = uuid.NewString()
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}
	record.Packets = truncatePackets(record.Packets)
	record.TotalBytes = packetBytes(record.Packets)
	s.mu.Lock()
	s.pending = append(s.pending, record)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *Store) writer() {
	ticker := time.NewTicker(time.Second)
	defer func() {
		ticker.Stop()
		close(s.stopped)
	}()
	for {
		select {
		case <-s.done:
			return
		case <-s.wake:
		case <-ticker.C:
		}
		select {
		case <-ticker.C:
		case <-s.done:
			return
		}
		s.flush(context.Background())
	}
}

func (s *Store) flush(ctx context.Context) {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.restore(pending)
		return
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO packet_records (
id,timestamp,request_id,provider,source,model,user_token,auth_label,auth_type,auth_index,api_key,client_ua,endpoint,
upstream_status_code,failed,total_bytes,client_request,upstream_request,upstream_response,client_response,summary
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		s.restore(pending)
		return
	}
	for _, record := range pending {
		if _, err := stmt.ExecContext(ctx,
			record.ID, record.Timestamp.UTC().Format(timestampLayout), record.RequestID, record.Provider, record.Source, record.Model,
			record.UserToken, record.AuthLabel, record.AuthType, record.AuthIndex, record.APIKey, record.ClientUA, record.Endpoint,
			record.UpstreamStatusCode, boolInt(record.Failed), record.TotalBytes,
			record.Packets.ClientRequest, record.Packets.UpstreamRequest, record.Packets.UpstreamResponse, record.Packets.ClientResponse, record.Summary,
		); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			s.restore(pending)
			return
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		s.restore(pending)
	}
}

func (s *Store) restore(records []Record) {
	s.mu.Lock()
	s.pending = append(records, s.pending...)
	s.mu.Unlock()
}

func (s *Store) Query(ctx context.Context, opts QueryOptions) ([]RecordSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	s.flush(ctx)
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	query := `SELECT id,timestamp,request_id,provider,source,model,user_token,auth_label,auth_type,auth_index,api_key,client_ua,endpoint,upstream_status_code,failed,total_bytes,summary FROM packet_records`
	where, args := buildRecordWhere(opts)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY timestamp DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordSummary{}
	for rows.Next() {
		var item RecordSummary
		var ts string
		var failed int
		if err := rows.Scan(&item.ID, &ts, &item.RequestID, &item.Provider, &item.Source, &item.Model, &item.UserToken, &item.AuthLabel, &item.AuthType, &item.AuthIndex, &item.APIKey, &item.ClientUA, &item.Endpoint, &item.UpstreamStatusCode, &failed, &item.TotalBytes, &item.Summary); err != nil {
			return nil, err
		}
		item.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		item.Failed = failed != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func buildRecordWhere(opts QueryOptions) ([]string, []any) {
	where := []string{}
	args := []any{}
	if v := strings.TrimSpace(opts.Model); v != "" && v != "__all__" {
		where = append(where, "model = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(opts.Source); v != "" && v != "__all__" {
		where = append(where, "source = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(opts.Provider); v != "" && v != "__all__" {
		where = append(where, "provider = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(opts.RequestID); v != "" {
		where = append(where, "request_id = ?")
		args = append(args, v)
	}
	switch strings.TrimSpace(opts.Result) {
	case "success":
		where = append(where, "failed = 0")
	case "failed":
		where = append(where, "failed = 1")
	}
	return where, args
}

func (s *Store) Get(ctx context.Context, id string) (Record, bool, error) {
	var record Record
	if s == nil || s.db == nil || strings.TrimSpace(id) == "" {
		return record, false, nil
	}
	s.flush(ctx)
	row := s.db.QueryRowContext(ctx, `SELECT id,timestamp,request_id,provider,source,model,user_token,auth_label,auth_type,auth_index,api_key,client_ua,endpoint,upstream_status_code,failed,total_bytes,client_request,upstream_request,upstream_response,client_response,summary FROM packet_records WHERE id=?`, id)
	var ts string
	var failed int
	err := row.Scan(&record.ID, &ts, &record.RequestID, &record.Provider, &record.Source, &record.Model, &record.UserToken, &record.AuthLabel, &record.AuthType, &record.AuthIndex, &record.APIKey, &record.ClientUA, &record.Endpoint, &record.UpstreamStatusCode, &failed, &record.TotalBytes, &record.Packets.ClientRequest, &record.Packets.UpstreamRequest, &record.Packets.UpstreamResponse, &record.Packets.ClientResponse, &record.Summary)
	if err == sql.ErrNoRows {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	record.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	record.Failed = failed != 0
	return record, true, nil
}

func (s *Store) GetByRequestID(ctx context.Context, requestID string) (Record, bool, error) {
	var record Record
	if s == nil || s.db == nil || strings.TrimSpace(requestID) == "" {
		return record, false, nil
	}
	s.flush(ctx)
	row := s.db.QueryRowContext(ctx, `SELECT id,timestamp,request_id,provider,source,model,user_token,auth_label,auth_type,auth_index,api_key,client_ua,endpoint,upstream_status_code,failed,total_bytes,client_request,upstream_request,upstream_response,client_response,summary FROM packet_records WHERE request_id=? ORDER BY timestamp DESC, id DESC LIMIT 1`, requestID)
	var ts string
	var failed int
	err := row.Scan(&record.ID, &ts, &record.RequestID, &record.Provider, &record.Source, &record.Model, &record.UserToken, &record.AuthLabel, &record.AuthType, &record.AuthIndex, &record.APIKey, &record.ClientUA, &record.Endpoint, &record.UpstreamStatusCode, &failed, &record.TotalBytes, &record.Packets.ClientRequest, &record.Packets.UpstreamRequest, &record.Packets.UpstreamResponse, &record.Packets.ClientResponse, &record.Summary)
	if err == sql.ErrNoRows {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	record.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	record.Failed = failed != 0
	return record, true, nil
}

func (s *Store) Delete(ctx context.Context, ids []string) (DeleteResult, error) {
	result := DeleteResult{Missing: []string{}}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM packet_records WHERE id=?`, id)
		if err != nil {
			return result, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			result.Missing = append(result.Missing, id)
		}
		result.Deleted += n
	}
	return result, nil
}

func (s *Store) DeleteAll(ctx context.Context) (DeleteResult, error) {
	var result DeleteResult
	s.mu.Lock()
	result.Deleted += int64(len(s.pending))
	s.pending = nil
	s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM packet_records`)
	if err != nil {
		return result, err
	}
	n, _ := res.RowsAffected()
	result.Deleted += n
	return result, nil
}

func (s *Store) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,enabled,record_history,priority,provider,provider_keyword,model,model_keyword,packet,part,json_path,header,operator,value,value_number,action,replacement,replace_limit,cooldown_seconds,target,notes,created_at,updated_at FROM rules ORDER BY priority ASC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

func (s *Store) EnabledRules(ctx context.Context) ([]Rule, error) {
	if cached, ok := s.cachedEnabledRules(); ok {
		return cached, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,enabled,record_history,priority,provider,provider_keyword,model,model_keyword,packet,part,json_path,header,operator,value,value_number,action,replacement,replace_limit,cooldown_seconds,target,notes,created_at,updated_at FROM rules WHERE enabled=1 ORDER BY priority ASC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.setEnabledRulesCache(out)
	return out, nil
}

func (s *Store) cachedEnabledRules() ([]Rule, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().After(s.rulesCacheUntil) {
		return nil, false
	}
	return append([]Rule(nil), s.rulesCache...), true
}

func (s *Store) setEnabledRulesCache(rules []Rule) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.rulesCache = append([]Rule(nil), rules...)
	s.rulesCacheUntil = time.Now().Add(rulesCacheTTL)
	s.mu.Unlock()
}

func (s *Store) invalidateRulesCache() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.rulesCache = nil
	s.rulesCacheUntil = time.Time{}
	s.mu.Unlock()
}

type ruleScanner interface {
	Scan(dest ...any) error
}

func scanRule(scanner ruleScanner) (Rule, error) {
	var rule Rule
	var enabled int
	var recordHistory int
	var created, updated string
	err := scanner.Scan(&rule.ID, &rule.Name, &enabled, &recordHistory, &rule.Priority, &rule.Provider, &rule.ProviderKeyword, &rule.Model, &rule.ModelKeyword, &rule.Packet, &rule.Part, &rule.JSONPath, &rule.Header, &rule.Operator, &rule.Value, &rule.ValueNumber, &rule.Action, &rule.Replacement, &rule.ReplaceLimit, &rule.CooldownSeconds, &rule.Target, &rule.Notes, &created, &updated)
	if err != nil {
		return rule, err
	}
	rule.Enabled = enabled != 0
	rule.RecordHistory = recordHistory != 0
	rule.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	rule.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return rule, nil
}

func (s *Store) UpsertRule(ctx context.Context, rule Rule) (Rule, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(rule.ID) == "" {
		rule.ID = uuid.NewString()
		rule.CreatedAt = now
	} else if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	rule.UpdatedAt = now
	if strings.TrimSpace(rule.Name) == "" {
		rule.Name = "未命名规则"
	}
	if strings.TrimSpace(rule.Packet) == "" {
		rule.Packet = "client_request"
	}
	if strings.TrimSpace(rule.Part) == "" {
		rule.Part = "body"
	}
	if strings.TrimSpace(rule.Operator) == "" {
		rule.Operator = "contains"
	}
	if strings.TrimSpace(rule.Action) == "" {
		rule.Action = "record"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO rules(id,name,enabled,record_history,priority,provider,provider_keyword,model,model_keyword,packet,part,json_path,header,operator,value,value_number,action,replacement,replace_limit,cooldown_seconds,target,notes,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,record_history=excluded.record_history,priority=excluded.priority,provider=excluded.provider,provider_keyword=excluded.provider_keyword,model=excluded.model,model_keyword=excluded.model_keyword,packet=excluded.packet,part=excluded.part,json_path=excluded.json_path,header=excluded.header,operator=excluded.operator,value=excluded.value,value_number=excluded.value_number,action=excluded.action,replacement=excluded.replacement,replace_limit=excluded.replace_limit,cooldown_seconds=excluded.cooldown_seconds,target=excluded.target,notes=excluded.notes,updated_at=excluded.updated_at`,
		rule.ID, rule.Name, boolInt(rule.Enabled), boolInt(rule.RecordHistory), rule.Priority, rule.Provider, rule.ProviderKeyword, rule.Model, rule.ModelKeyword, rule.Packet, rule.Part, rule.JSONPath, rule.Header, rule.Operator, rule.Value, rule.ValueNumber, rule.Action, rule.Replacement, rule.ReplaceLimit, rule.CooldownSeconds, rule.Target, rule.Notes, rule.CreatedAt.Format(timestampLayout), rule.UpdatedAt.Format(timestampLayout))
	if err == nil {
		s.invalidateRulesCache()
	}
	return rule, err
}

func (s *Store) DeleteRule(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id=?`, strings.TrimSpace(id))
	if err == nil {
		s.invalidateRulesCache()
	}
	return err
}

func (s *Store) InsertTrigger(ctx context.Context, trigger TriggerRecord) error {
	if strings.TrimSpace(trigger.ID) == "" {
		trigger.ID = uuid.NewString()
	}
	if trigger.Timestamp.IsZero() {
		trigger.Timestamp = time.Now().UTC()
	}
	trigger.Packet = truncate(trigger.Packet)
	_, err := s.db.ExecContext(ctx, `INSERT INTO trigger_records(id,rule_id,rule_name,record_id,timestamp,action,target,account,packet,packet_name,detail,cooldown_seconds) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, trigger.ID, trigger.RuleID, trigger.RuleName, trigger.RecordID, trigger.Timestamp.Format(timestampLayout), trigger.Action, trigger.Target, trigger.Account, trigger.Packet, trigger.PacketName, trigger.Detail, trigger.CooldownSeconds)
	return err
}

func (s *Store) ListTriggers(ctx context.Context, limit int) ([]TriggerRecord, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,rule_id,rule_name,record_id,timestamp,action,target,account,packet,packet_name,detail,cooldown_seconds FROM trigger_records ORDER BY timestamp DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TriggerRecord
	for rows.Next() {
		var item TriggerRecord
		var ts string
		if err := rows.Scan(&item.ID, &item.RuleID, &item.RuleName, &item.RecordID, &ts, &item.Action, &item.Target, &item.Account, &item.Packet, &item.PacketName, &item.Detail, &item.CooldownSeconds); err != nil {
			return nil, err
		}
		item.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTriggers(ctx context.Context, ids []string) (DeleteResult, error) {
	if s == nil || s.db == nil {
		return DeleteResult{}, nil
	}
	unique := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	var result DeleteResult
	for _, id := range unique {
		res, err := s.db.ExecContext(ctx, `DELETE FROM trigger_records WHERE id=?`, id)
		if err != nil {
			return result, err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			result.Missing = append(result.Missing, id)
			continue
		}
		result.Deleted += affected
	}
	return result, nil
}

func (s *Store) DeleteAllTriggers(ctx context.Context) (DeleteResult, error) {
	if s == nil || s.db == nil {
		return DeleteResult{}, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM trigger_records`)
	if err != nil {
		return DeleteResult{}, err
	}
	affected, _ := res.RowsAffected()
	return DeleteResult{Deleted: affected}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.done)
		<-s.stopped
	})
	s.flush(context.Background())
	return s.db.Close()
}

func truncatePackets(p PacketSet) PacketSet {
	p.ClientRequest = truncate(p.ClientRequest)
	p.UpstreamRequest = truncate(p.UpstreamRequest)
	p.UpstreamResponse = truncate(p.UpstreamResponse)
	p.ClientResponse = truncate(p.ClientResponse)
	return p
}

func truncate(v string) string {
	if len(v) <= maxPacketTextBytes {
		return v
	}
	return v[:maxPacketTextBytes]
}

func packetBytes(p PacketSet) int64 {
	return int64(len(p.ClientRequest) + len(p.UpstreamRequest) + len(p.UpstreamResponse) + len(p.ClientResponse))
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
