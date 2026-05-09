package openai_compat_state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	_ "modernc.org/sqlite"
)

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusError    = "error"
	StatusFrozen   = "frozen"
	maxRawPacket   = 256 * 1024
)

type State struct {
	ProviderName  string    `json:"provider_name"`
	APIKey        string    `json:"api_key"`
	Enabled       bool      `json:"enabled"`
	Status        string    `json:"status"`
	StatusMessage string    `json:"status_message,omitempty"`
	FrozenUntil   time.Time `json:"frozen_until,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	RawRequest    string    `json:"raw_request,omitempty"`
	RawResponse   string    `json:"raw_response,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Service struct {
	mu      sync.RWMutex
	db      *sql.DB
	states  map[string]State
	dirty   map[string]State
	wake    chan struct{}
	done    chan struct{}
	stopped chan struct{}
	closed  sync.Once
}

var (
	defaultMu      sync.Mutex
	defaultService *Service
)

func DefaultService() *Service {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultService
}

func InitDefault(path string) error {
	service, err := New(path)
	if err != nil {
		return err
	}
	defaultMu.Lock()
	previous := defaultService
	defaultService = service
	defaultMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

func CloseDefault() error {
	defaultMu.Lock()
	if defaultService == nil {
		defaultMu.Unlock()
		return nil
	}
	service := defaultService
	defaultService = nil
	defaultMu.Unlock()
	return service.Close()
}

func New(path string) (*Service, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("openai compat key state sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Service{
		db:      db,
		states:  make(map[string]State),
		dirty:   make(map[string]State),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if err := s.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.load(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	go s.writer()
	return s, nil
}

func (s *Service) init(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS key_states (
provider_name TEXT NOT NULL,
api_key TEXT NOT NULL,
enabled INTEGER NOT NULL DEFAULT 1,
status TEXT NOT NULL DEFAULT 'active',
status_message TEXT NOT NULL DEFAULT '',
frozen_until TEXT NOT NULL DEFAULT '',
last_error TEXT NOT NULL DEFAULT '',
raw_request TEXT NOT NULL DEFAULT '',
raw_response TEXT NOT NULL DEFAULT '',
updated_at TEXT NOT NULL,
PRIMARY KEY(provider_name, api_key)
)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT provider_name, api_key, enabled, status, status_message, frozen_until, last_error, raw_request, raw_response, updated_at FROM key_states`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var st State
		var enabled int
		var frozen, updated string
		if err := rows.Scan(&st.ProviderName, &st.APIKey, &enabled, &st.Status, &st.StatusMessage, &frozen, &st.LastError, &st.RawRequest, &st.RawResponse, &updated); err != nil {
			return err
		}
		st.Enabled = enabled != 0
		st.FrozenUntil, _ = time.Parse(time.RFC3339Nano, frozen)
		st.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		s.states[stateKey(st.ProviderName, st.APIKey)] = normalizeState(st)
	}
	return rows.Err()
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.closed.Do(func() {
		close(s.done)
		<-s.stopped
	})
	s.flush(context.Background())
	return s.db.Close()
}

func (s *Service) List() []State {
	if s == nil {
		return nil
	}
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]State, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, visibleState(st, now))
	}
	return out
}

func (s *Service) Get(provider, apiKey string) (State, bool) {
	if s == nil {
		return State{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.states[stateKey(provider, apiKey)]
	if !ok {
		return State{}, false
	}
	return visibleState(st, time.Now()), true
}

func (s *Service) SetEnabled(provider, apiKey string, enabled bool) State {
	return s.update(provider, apiKey, func(st *State) {
		st.Enabled = enabled
		if enabled {
			st.Status = StatusActive
			st.StatusMessage = ""
			st.FrozenUntil = time.Time{}
			st.LastError = ""
		} else {
			st.Status = StatusDisabled
			st.StatusMessage = "disabled by user"
		}
	})
}

func (s *Service) ApplyRulers(provider config.OpenAICompatibility, apiKey string, status int, body []byte, rawRequest, rawResponse string) (State, bool) {
	if s == nil {
		return State{}, false
	}
	for _, ruler := range provider.StatusRulers {
		if !rulerMatches(ruler, status, body) {
			continue
		}
		action := strings.ToLower(strings.TrimSpace(ruler.Action))
		now := time.Now()
		st := s.update(provider.Name, apiKey, func(st *State) {
			st.Enabled = action != "disable"
			st.StatusMessage = strings.TrimSpace(ruler.Name)
			st.LastError = truncateRaw(string(body))
			st.RawRequest = truncateRaw(rawRequest)
			st.RawResponse = truncateRaw(rawResponse)
			switch {
			case action == "disable":
				st.Status = StatusDisabled
			case strings.HasPrefix(action, "freeze-"):
				st.Status = StatusFrozen
				if d, ok := parseFreeze(action); ok {
					st.FrozenUntil = now.Add(d)
				}
			default:
				st.Status = StatusError
			}
		})
		return st, true
	}
	return State{}, false
}

func (s *Service) MarkError(provider, apiKey, message, rawRequest, rawResponse string) State {
	return s.update(provider, apiKey, func(st *State) {
		st.Status = StatusError
		st.StatusMessage = message
		st.LastError = message
		st.RawRequest = truncateRaw(rawRequest)
		st.RawResponse = truncateRaw(rawResponse)
	})
}

func (s *Service) MarkSuccess(provider, apiKey, rawRequest, rawResponse string) State {
	return s.update(provider, apiKey, func(st *State) {
		st.Enabled = true
		st.Status = StatusActive
		st.StatusMessage = "test passed"
		st.FrozenUntil = time.Time{}
		st.LastError = ""
		st.RawRequest = truncateRaw(rawRequest)
		st.RawResponse = truncateRaw(rawResponse)
	})
}

func (s *Service) ApplyToAuth(auth *cliproxyauth.Auth) {
	if s == nil || auth == nil || auth.Attributes == nil {
		return
	}
	provider := strings.TrimSpace(auth.Attributes["compat_name"])
	if provider == "" {
		provider = strings.TrimSpace(auth.Attributes["provider_key"])
	}
	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	st, ok := s.Get(provider, apiKey)
	if !ok {
		return
	}
	if !st.Enabled || st.Status == StatusDisabled {
		auth.Disabled = true
		auth.Status = cliproxyauth.StatusDisabled
		auth.StatusMessage = st.StatusMessage
		return
	}
	if st.Status == StatusFrozen && st.FrozenUntil.After(time.Now()) {
		auth.Unavailable = true
		auth.NextRetryAfter = st.FrozenUntil
		auth.Status = cliproxyauth.StatusError
		auth.StatusMessage = st.StatusMessage
	}
}

func (s *Service) update(provider, apiKey string, mutate func(*State)) State {
	if s == nil {
		return State{}
	}
	key := stateKey(provider, apiKey)
	now := time.Now().UTC()
	s.mu.Lock()
	st := s.states[key]
	if st.ProviderName == "" {
		st.ProviderName = strings.TrimSpace(provider)
		st.APIKey = strings.TrimSpace(apiKey)
		st.Enabled = true
		st.Status = StatusActive
	}
	mutate(&st)
	st.UpdatedAt = now
	st = normalizeState(st)
	s.states[key] = st
	s.dirty[key] = st
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return visibleState(st, now)
}

func (s *Service) writer() {
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

func (s *Service) flush(ctx context.Context) {
	s.mu.Lock()
	pending := s.dirty
	s.dirty = make(map[string]State)
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO key_states(provider_name, api_key, enabled, status, status_message, frozen_until, last_error, raw_request, raw_response, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(provider_name, api_key) DO UPDATE SET enabled=excluded.enabled,status=excluded.status,status_message=excluded.status_message,frozen_until=excluded.frozen_until,last_error=excluded.last_error,raw_request=excluded.raw_request,raw_response=excluded.raw_response,updated_at=excluded.updated_at`)
	if err != nil {
		_ = tx.Rollback()
		return
	}
	defer stmt.Close()
	for _, st := range pending {
		_, _ = stmt.ExecContext(ctx, st.ProviderName, st.APIKey, boolInt(st.Enabled), st.Status, st.StatusMessage, formatTime(st.FrozenUntil), st.LastError, st.RawRequest, st.RawResponse, formatTime(st.UpdatedAt))
	}
	_ = tx.Commit()
}

func BuildRawRequest(req *http.Request, body []byte) string {
	if req == nil {
		return ""
	}
	var b strings.Builder
	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}
	fmt.Fprintf(&b, "%s %s HTTP/%d.%d\n", req.Method, path, req.ProtoMajor, req.ProtoMinor)
	req.Header.Write(&b)
	b.WriteByte('\n')
	b.Write(body)
	return truncateRaw(b.String())
}

func BuildRawResponse(resp *http.Response, body []byte) string {
	if resp == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/%d.%d %s\n", resp.ProtoMajor, resp.ProtoMinor, resp.Status)
	resp.Header.Write(&b)
	b.WriteByte('\n')
	b.Write(body)
	return truncateRaw(b.String())
}

func CloneRequestBody(req *http.Request) []byte {
	if req == nil || req.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func stateKey(provider, apiKey string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + strings.TrimSpace(apiKey)
}

func normalizeState(st State) State {
	if st.Status == "" {
		st.Status = StatusActive
	}
	if st.ProviderName == "" {
		st.ProviderName = "openai-compatibility"
	}
	return st
}

func visibleState(st State, now time.Time) State {
	if st.Status == StatusFrozen && !st.FrozenUntil.IsZero() && !st.FrozenUntil.After(now) && st.Enabled {
		st.Status = StatusActive
		st.StatusMessage = ""
		st.FrozenUntil = time.Time{}
	}
	return st
}

func rulerMatches(r config.OpenAICompatibilityStatusRuler, status int, body []byte) bool {
	if r.When.Status != 0 && r.When.Status != status {
		return false
	}
	if expected := strings.TrimSpace(r.When.BodyEquals); expected != "" {
		return strings.TrimSpace(string(body)) == expected
	}
	if path := strings.TrimSpace(r.When.JSONPath); path != "" {
		return gjson.GetBytes(body, path).String() == r.When.JSONEquals
	}
	return true
}

func parseFreeze(action string) (time.Duration, bool) {
	raw := strings.TrimPrefix(action, "freeze-")
	if raw == "" {
		return 0, false
	}
	unit := raw[len(raw)-1:]
	n, err := strconv.Atoi(raw[:len(raw)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch unit {
	case "h":
		return time.Duration(n) * time.Hour, true
	case "m":
		return time.Duration(n) * time.Minute, true
	case "s":
		return time.Duration(n) * time.Second, true
	default:
		return 0, false
	}
}

func truncateRaw(v string) string {
	if len(v) <= maxRawPacket {
		return v
	}
	return v[:maxRawPacket] + "\n...[truncated]"
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func MarshalStates(states []State) []byte {
	b, _ := json.Marshal(states)
	return b
}
