package management

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const openAICompatRuntimeStateFileName = "openai-compat-runtime-state.json"
const openAICompatRuntimeStateSyncInterval = time.Second

type openAICompatRuntimeStateFile struct {
	Version   int                               `json:"version"`
	UpdatedAt time.Time                         `json:"updated_at"`
	Entries   []openAICompatRuntimeStateEntry   `json:"entries"`
}

type openAICompatRuntimeStateEntry struct {
	AuthID         string          `json:"auth_id"`
	AuthIndex      string          `json:"auth_index,omitempty"`
	Disabled       bool            `json:"disabled,omitempty"`
	Unavailable    bool            `json:"unavailable,omitempty"`
	Status         string          `json:"status,omitempty"`
	StatusMessage  string          `json:"status_message,omitempty"`
	NextRetryAfter time.Time       `json:"next_retry_after,omitempty"`
	LastError      *coreauth.Error `json:"last_error,omitempty"`
}

func isDefaultOpenAICompatRuntimeStateEntry(entry openAICompatRuntimeStateEntry) bool {
	status := strings.ToLower(strings.TrimSpace(entry.Status))
	statusMessage := strings.TrimSpace(entry.StatusMessage)
	return !entry.Disabled &&
		!entry.Unavailable &&
		(status == "" || status == string(coreauth.StatusActive)) &&
		statusMessage == "" &&
		entry.NextRetryAfter.IsZero() &&
		entry.LastError == nil
}

func (h *Handler) openAICompatRuntimeStatePath() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	configFilePath := strings.TrimSpace(h.configFilePath)
	h.mu.Unlock()
	if configFilePath == "" {
		return ""
	}
	dir := filepath.Dir(configFilePath)
	if dir == "" || dir == "." {
		if abs, err := filepath.Abs(openAICompatRuntimeStateFileName); err == nil {
			return abs
		}
		return openAICompatRuntimeStateFileName
	}
	return filepath.Join(dir, openAICompatRuntimeStateFileName)
}

func (h *Handler) persistOpenAICompatRuntimeState() error {
	if h == nil {
		return nil
	}
	path := h.openAICompatRuntimeStatePath()
	if path == "" {
		return nil
	}

	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	h.openAICompatStateMu.Lock()
	defer h.openAICompatStateMu.Unlock()
	entries := collectOpenAICompatRuntimeStateEntries(manager)
	if !h.openAICompatStateApplied {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		h.openAICompatStateApplied = true
	}
	signature, err := marshalOpenAICompatRuntimeStateSignature(entries)
	if err != nil {
		return err
	}
	if h.openAICompatStateSig == string(signature) {
		return nil
	}

	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		h.openAICompatStateSig = string(signature)
		return nil
	}

	payload, err := json.MarshalIndent(openAICompatRuntimeStateFile{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Entries:   entries,
	}, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	h.openAICompatStateSig = string(signature)
	return nil
}

func (h *Handler) applyOpenAICompatRuntimeState() {
	if h == nil {
		return
	}
	path := h.openAICompatRuntimeStatePath()
	if path == "" {
		return
	}

	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return
	}

	stateFile, err := h.loadOpenAICompatRuntimeStateFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warnf("failed to load openai compat runtime state: %v", err)
		}
		return
	}
	if len(stateFile.Entries) == 0 {
		h.openAICompatStateMu.Lock()
		h.openAICompatStateApplied = true
		h.openAICompatStateMu.Unlock()
		return
	}
	h.rememberOpenAICompatRuntimeStateEntries(stateFile.Entries)

	byID := make(map[string]openAICompatRuntimeStateEntry, len(stateFile.Entries))
	byIndex := make(map[string]openAICompatRuntimeStateEntry, len(stateFile.Entries))
	for _, entry := range stateFile.Entries {
		if id := strings.TrimSpace(entry.AuthID); id != "" {
			byID[id] = entry
		}
		if idx := strings.TrimSpace(entry.AuthIndex); idx != "" {
			byIndex[idx] = entry
		}
	}

	ctx := coreauth.WithSkipPersist(context.Background())
	matched := 0
	for _, auth := range manager.List() {
		if auth == nil || !isOpenAICompatAuth(auth) {
			continue
		}
		entry, ok := byID[strings.TrimSpace(auth.ID)]
		if !ok {
			entry, ok = byIndex[strings.TrimSpace(auth.EnsureIndex())]
		}
		if !ok {
			continue
		}
		matched++
		auth.Disabled = entry.Disabled
		auth.Unavailable = entry.Unavailable
		auth.Status = coreauth.Status(strings.TrimSpace(entry.Status))
		if auth.Status == "" {
			if auth.Disabled {
				auth.Status = coreauth.StatusDisabled
			} else {
				auth.Status = coreauth.StatusActive
			}
		}
		auth.StatusMessage = strings.TrimSpace(entry.StatusMessage)
		auth.NextRetryAfter = entry.NextRetryAfter
		auth.LastError = cloneRuntimeStateError(entry.LastError)
		auth.UpdatedAt = time.Now()
		if _, err := manager.Update(ctx, auth); err != nil {
			log.Warnf("failed to apply openai compat runtime state for %s: %v", auth.ID, err)
		}
	}
	if matched > 0 {
		h.openAICompatStateMu.Lock()
		h.openAICompatStateApplied = true
		h.openAICompatStateMu.Unlock()
	}
}

func (h *Handler) loadOpenAICompatRuntimeStateFile(path string) (*openAICompatRuntimeStateFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out openAICompatRuntimeStateFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (h *Handler) startOpenAICompatRuntimeStateSync() {
	if h == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(openAICompatRuntimeStateSyncInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.openAICompatStateMu.Lock()
			shouldApply := !h.openAICompatStateApplied
			h.openAICompatStateMu.Unlock()
			if shouldApply {
				h.applyOpenAICompatRuntimeState()
			}
			if err := h.persistOpenAICompatRuntimeState(); err != nil {
				log.Warnf("failed to sync openai compat runtime state: %v", err)
			}
		}
	}()
}

func collectOpenAICompatRuntimeStateEntries(manager *coreauth.Manager) []openAICompatRuntimeStateEntry {
	if manager == nil {
		return nil
	}
	entries := make([]openAICompatRuntimeStateEntry, 0, 16)
	for _, auth := range manager.List() {
		if !isOpenAICompatAuth(auth) || auth == nil {
			continue
		}
		entry := openAICompatRuntimeStateEntry{
			AuthID:         strings.TrimSpace(auth.ID),
			AuthIndex:      strings.TrimSpace(auth.EnsureIndex()),
			Disabled:       auth.Disabled,
			Unavailable:    auth.Unavailable,
			Status:         string(auth.Status),
			StatusMessage:  strings.TrimSpace(auth.StatusMessage),
			NextRetryAfter: auth.NextRetryAfter,
			LastError:      cloneRuntimeStateError(auth.LastError),
		}
		if isDefaultOpenAICompatRuntimeStateEntry(entry) {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].AuthID < entries[j].AuthID
	})
	return entries
}

func marshalOpenAICompatRuntimeStateSignature(entries []openAICompatRuntimeStateEntry) ([]byte, error) {
	return json.Marshal(entries)
}

func (h *Handler) rememberOpenAICompatRuntimeStateEntries(entries []openAICompatRuntimeStateEntry) {
	if h == nil {
		return
	}
	signature, err := marshalOpenAICompatRuntimeStateSignature(entries)
	if err != nil {
		return
	}
	h.openAICompatStateMu.Lock()
	h.openAICompatStateSig = string(signature)
	h.openAICompatStateApplied = true
	h.openAICompatStateMu.Unlock()
}

func cloneRuntimeStateError(src *coreauth.Error) *coreauth.Error {
	if src == nil {
		return nil
	}
	return &coreauth.Error{
		Code:       src.Code,
		Message:    src.Message,
		Retryable:  src.Retryable,
		HTTPStatus: src.HTTPStatus,
	}
}
