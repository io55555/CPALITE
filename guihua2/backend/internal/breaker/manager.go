package breaker

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultFailureThreshold = 2
	defaultCooldown         = 2 * time.Minute
)

type AuthSnapshot struct {
	AuthID    string `json:"auth_id"`
	AuthIndex string `json:"auth_index"`
	Provider  string `json:"provider"`
	Label     string `json:"label,omitempty"`
	ProxyURL  string `json:"proxy_url,omitempty"`
}

type State struct {
	Scope          string    `json:"scope"`
	Key            string    `json:"key"`
	AuthID         string    `json:"auth_id,omitempty"`
	AuthIndex      string    `json:"auth_index,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	ProxyURL       string    `json:"proxy_url,omitempty"`
	Status         string    `json:"status"`
	FailureCount   int       `json:"failure_count"`
	LastFailureAt  time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt  time.Time `json:"last_success_at,omitempty"`
	CooldownUntil  time.Time `json:"cooldown_until,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	ProbeInFlight  bool      `json:"probe_in_flight"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Manager struct {
	mu               sync.Mutex
	failureThreshold int
	cooldown         time.Duration
	states           map[string]*State
}

func NewManager() *Manager {
	return &Manager{
		failureThreshold: defaultFailureThreshold,
		cooldown:         defaultCooldown,
		states:           make(map[string]*State),
	}
}

var (
	defaultManagerMu sync.RWMutex
	defaultManager   = NewManager()
)

func DefaultManager() *Manager {
	defaultManagerMu.RLock()
	defer defaultManagerMu.RUnlock()
	return defaultManager
}

func SetDefaultManager(manager *Manager) {
	if manager == nil {
		manager = NewManager()
	}
	defaultManagerMu.Lock()
	defaultManager = manager
	defaultManagerMu.Unlock()
}

func (m *Manager) BeforeRequest(auth AuthSnapshot) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, key := range candidateStateKeys(auth) {
		state := m.states[key]
		if state == nil {
			continue
		}
		switch state.Status {
		case "open":
			if state.CooldownUntil.After(now) {
				return fmt.Errorf("breaker open for %s until %s: %s", state.Scope, state.CooldownUntil.UTC().Format(time.RFC3339), state.LastError)
			}
			state.Status = "half-open"
			state.ProbeInFlight = true
			state.UpdatedAt = now
		case "half-open":
			if state.ProbeInFlight {
				return fmt.Errorf("breaker half-open probe in progress for %s", state.Scope)
			}
			state.ProbeInFlight = true
			state.UpdatedAt = now
		}
	}
	return nil
}

func (m *Manager) RecordSuccess(auth AuthSnapshot) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, key := range candidateStateKeys(auth) {
		state := m.ensureState(auth, key)
		state.Status = "closed"
		state.FailureCount = 0
		state.LastSuccessAt = now
		state.CooldownUntil = time.Time{}
		state.LastError = ""
		state.ProbeInFlight = false
		state.UpdatedAt = now
	}
}

func (m *Manager) RecordFailure(auth AuthSnapshot, errText string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, key := range candidateStateKeys(auth) {
		state := m.ensureState(auth, key)
		state.FailureCount++
		state.LastFailureAt = now
		state.LastError = strings.TrimSpace(errText)
		state.ProbeInFlight = false
		if state.Status == "half-open" || state.FailureCount >= m.failureThreshold {
			state.Status = "open"
			state.CooldownUntil = now.Add(m.cooldown)
		} else {
			state.Status = "closed"
		}
		state.UpdatedAt = now
	}
}

func (m *Manager) ForceOpen(auth AuthSnapshot, cooldown time.Duration, reason string) {
	if m == nil {
		return
	}
	if cooldown <= 0 {
		cooldown = m.cooldown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, key := range candidateStateKeys(auth) {
		state := m.ensureState(auth, key)
		state.Status = "open"
		state.CooldownUntil = now.Add(cooldown)
		state.LastFailureAt = now
		state.LastError = strings.TrimSpace(reason)
		state.ProbeInFlight = false
		state.UpdatedAt = now
	}
}

func (m *Manager) Reset(scope, key string) {
	if m == nil {
		return
	}
	scope = strings.TrimSpace(scope)
	key = strings.TrimSpace(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	for stateKey, state := range m.states {
		if state == nil {
			continue
		}
		if scope != "" && state.Scope != scope {
			continue
		}
		if key != "" && state.Key != key {
			continue
		}
		delete(m.states, stateKey)
	}
}

func (m *Manager) Snapshot() []State {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make([]State, 0, len(m.states))
	for stateKey, state := range m.states {
		if state == nil {
			delete(m.states, stateKey)
			continue
		}
		if state.Status == "open" && !state.CooldownUntil.IsZero() && !state.CooldownUntil.After(now) && !state.ProbeInFlight {
			state.Status = "half-open"
			state.UpdatedAt = now
		}
		out = append(out, *state)
	}
	return out
}

func (m *Manager) ensureState(auth AuthSnapshot, compositeKey string) *State {
	state := m.states[compositeKey]
	if state != nil {
		return state
	}
	scope, key := splitCompositeKey(compositeKey)
	state = &State{
		Scope:     scope,
		Key:       key,
		AuthID:    auth.AuthID,
		AuthIndex: auth.AuthIndex,
		Provider:  auth.Provider,
		ProxyURL:  auth.ProxyURL,
		Status:    "closed",
		UpdatedAt: time.Now(),
	}
	m.states[compositeKey] = state
	return state
}

func candidateStateKeys(auth AuthSnapshot) []string {
	keys := make([]string, 0, 2)
	if proxy := strings.TrimSpace(auth.ProxyURL); proxy != "" && !strings.EqualFold(proxy, "direct") {
		keys = append(keys, composeStateKey("proxy", proxy))
	}
	if id := strings.TrimSpace(auth.AuthID); id != "" {
		keys = append(keys, composeStateKey("auth", id))
	}
	return keys
}

func composeStateKey(scope, key string) string {
	return strings.TrimSpace(scope) + "|" + strings.TrimSpace(key)
}

func splitCompositeKey(composite string) (string, string) {
	parts := strings.SplitN(composite, "|", 2)
	if len(parts) != 2 {
		return "", composite
	}
	return parts[0], parts[1]
}
