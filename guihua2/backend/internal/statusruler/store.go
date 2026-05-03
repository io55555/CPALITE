package statusruler

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/breaker"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	_ "modernc.org/sqlite"
)

type Rule struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Provider        string    `json:"provider,omitempty"`
	AuthIndex       string    `json:"auth_index,omitempty"`
	StatusCode      int       `json:"status_code,omitempty"`
	BodyContains    string    `json:"body_contains,omitempty"`
	Action          string    `json:"action"`
	CooldownSeconds int       `json:"cooldown_seconds,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type Hit struct {
	ID         int64     `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	RuleID     int64     `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	Action     string    `json:"action"`
	Provider   string    `json:"provider,omitempty"`
	AuthID     string    `json:"auth_id,omitempty"`
	AuthIndex  string    `json:"auth_index,omitempty"`
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"`
}

type Runtime struct {
	store   *Store
	authMgr *coreauth.Manager
	breaker *breaker.Manager
}

type Store struct {
	db *sql.DB
}

var (
	defaultStoreMu sync.RWMutex
	defaultStore   *Store
	runtimeMu      sync.RWMutex
	defaultRuntime Runtime
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

func ConfigureRuntime(authMgr *coreauth.Manager, breakerMgr *breaker.Manager) {
	runtimeMu.Lock()
	defaultRuntime = Runtime{
		store:   DefaultStore(),
		authMgr: authMgr,
		breaker: breakerMgr,
	}
	runtimeMu.Unlock()
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
		return nil, fmt.Errorf("status-ruler sqlite path is empty")
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
	return store, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS status_rules (
id INTEGER PRIMARY KEY AUTOINCREMENT,
name TEXT NOT NULL DEFAULT '',
enabled INTEGER NOT NULL DEFAULT 1,
provider TEXT NOT NULL DEFAULT '',
auth_index TEXT NOT NULL DEFAULT '',
status_code INTEGER NOT NULL DEFAULT 0,
body_contains TEXT NOT NULL DEFAULT '',
action TEXT NOT NULL DEFAULT 'log_only',
cooldown_seconds INTEGER NOT NULL DEFAULT 0,
created_at TEXT NOT NULL,
updated_at TEXT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS status_rule_hits (
id INTEGER PRIMARY KEY AUTOINCREMENT,
created_at TEXT NOT NULL,
rule_id INTEGER NOT NULL DEFAULT 0,
rule_name TEXT NOT NULL DEFAULT '',
action TEXT NOT NULL DEFAULT '',
provider TEXT NOT NULL DEFAULT '',
auth_id TEXT NOT NULL DEFAULT '',
auth_index TEXT NOT NULL DEFAULT '',
status_code INTEGER NOT NULL DEFAULT 0,
message TEXT NOT NULL DEFAULT ''
)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, enabled, provider, auth_index, status_code, body_contains, action, cooldown_seconds, created_at, updated_at FROM status_rules ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Rule, 0, 32)
	for rows.Next() {
		var enabledInt int
		var createdAt, updatedAt string
		var item Rule
		if err := rows.Scan(&item.ID, &item.Name, &enabledInt, &item.Provider, &item.AuthIndex, &item.StatusCode, &item.BodyContains, &item.Action, &item.CooldownSeconds, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabledInt != 0
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpsertRule(ctx context.Context, rule Rule) (Rule, error) {
	now := time.Now().UTC()
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Provider = strings.TrimSpace(rule.Provider)
	rule.AuthIndex = strings.TrimSpace(rule.AuthIndex)
	rule.BodyContains = strings.TrimSpace(rule.BodyContains)
	rule.Action = normalizeAction(rule.Action)
	if rule.ID <= 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO status_rules (name, enabled, provider, auth_index, status_code, body_contains, action, cooldown_seconds, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rule.Name, boolToInt(rule.Enabled), rule.Provider, rule.AuthIndex, rule.StatusCode, rule.BodyContains, rule.Action, rule.CooldownSeconds, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return Rule{}, err
		}
		id, _ := res.LastInsertId()
		rule.ID = id
	} else {
		if _, err := s.db.ExecContext(ctx, `UPDATE status_rules SET name = ?, enabled = ?, provider = ?, auth_index = ?, status_code = ?, body_contains = ?, action = ?, cooldown_seconds = ?, updated_at = ? WHERE id = ?`,
			rule.Name, boolToInt(rule.Enabled), rule.Provider, rule.AuthIndex, rule.StatusCode, rule.BodyContains, rule.Action, rule.CooldownSeconds, now.Format(time.RFC3339Nano), rule.ID); err != nil {
			return Rule{}, err
		}
	}
	rule.CreatedAt = now
	rule.UpdatedAt = now
	return rule, nil
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM status_rules WHERE id = ?`, id)
	return err
}

func (s *Store) ListHits(ctx context.Context, limit int) ([]Hit, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, rule_id, rule_name, action, provider, auth_id, auth_index, status_code, message FROM status_rule_hits ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Hit, 0, limit)
	for rows.Next() {
		var createdAt string
		var item Hit
		if err := rows.Scan(&item.ID, &createdAt, &item.RuleID, &item.RuleName, &item.Action, &item.Provider, &item.AuthID, &item.AuthIndex, &item.StatusCode, &item.Message); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) InsertHit(ctx context.Context, hit Hit) error {
	if hit.CreatedAt.IsZero() {
		hit.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO status_rule_hits (created_at, rule_id, rule_name, action, provider, auth_id, auth_index, status_code, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hit.CreatedAt.Format(time.RFC3339Nano), hit.RuleID, hit.RuleName, hit.Action, hit.Provider, hit.AuthID, hit.AuthIndex, hit.StatusCode, strings.TrimSpace(hit.Message))
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func EvaluateResponse(ctx context.Context, auth breaker.AuthSnapshot, statusCode int, body string) {
	runtimeMu.RLock()
	runtime := defaultRuntime
	runtimeMu.RUnlock()
	if runtime.store == nil {
		return
	}
	rules, err := runtime.store.ListRules(ctx)
	if err != nil {
		return
	}
	bodyLower := strings.ToLower(body)
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if rule.Provider != "" && !strings.EqualFold(rule.Provider, auth.Provider) {
			continue
		}
		if rule.AuthIndex != "" && !strings.EqualFold(rule.AuthIndex, auth.AuthIndex) {
			continue
		}
		if rule.StatusCode != 0 && rule.StatusCode != statusCode {
			continue
		}
		if rule.BodyContains != "" && !strings.Contains(bodyLower, strings.ToLower(rule.BodyContains)) {
			continue
		}
		_ = runtime.store.InsertHit(ctx, Hit{
			CreatedAt:  time.Now().UTC(),
			RuleID:     rule.ID,
			RuleName:   rule.Name,
			Action:     rule.Action,
			Provider:   auth.Provider,
			AuthID:     auth.AuthID,
			AuthIndex:  auth.AuthIndex,
			StatusCode: statusCode,
			Message:    rule.BodyContains,
		})
		applyRuleAction(ctx, runtime, auth, rule)
	}
}

func applyRuleAction(ctx context.Context, runtime Runtime, auth breaker.AuthSnapshot, rule Rule) {
	switch normalizeAction(rule.Action) {
	case "breaker_open":
		if runtime.breaker != nil {
			runtime.breaker.ForceOpen(auth, time.Duration(rule.CooldownSeconds)*time.Second, "status-ruler:"+rule.Name)
		}
	case "freeze_auth", "disable_auth":
		if runtime.authMgr == nil || auth.AuthID == "" {
			return
		}
		current, ok := runtime.authMgr.GetByID(auth.AuthID)
		if !ok || current == nil {
			return
		}
		current.UpdatedAt = time.Now()
		current.Status = coreauth.StatusError
		current.Unavailable = true
		current.StatusMessage = "status-ruler:" + rule.Name
		if normalizeAction(rule.Action) == "disable_auth" {
			current.Disabled = true
			current.Status = coreauth.StatusDisabled
			current.NextRetryAfter = time.Time{}
		} else if rule.CooldownSeconds > 0 {
			current.NextRetryAfter = time.Now().Add(time.Duration(rule.CooldownSeconds) * time.Second)
		}
		_, _ = runtime.authMgr.Update(coreauth.WithSkipPersist(ctx), current)
	}
}

func normalizeAction(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "breaker_open", "freeze_auth", "disable_auth":
		return action
	default:
		return "log_only"
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
