package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const authRuntimeStateFileName = "auth-runtime-state.json"

type persistedAuthRuntimeStateFile struct {
	Entries map[string]persistedAuthRuntimeState `json:"entries"`
}

type persistedAuthRuntimeState struct {
	Disabled       bool                 `json:"disabled,omitempty"`
	Unavailable    bool                 `json:"unavailable,omitempty"`
	Status         cliproxyauth.Status  `json:"status,omitempty"`
	StatusMessage  string               `json:"status_message,omitempty"`
	LastError      *cliproxyauth.Error  `json:"last_error,omitempty"`
	Quota          cliproxyauth.QuotaState `json:"quota,omitempty"`
	NextRetryAfter time.Time            `json:"next_retry_after,omitempty"`
	UpdatedAt      time.Time            `json:"updated_at,omitempty"`
}

// FileTokenStore persists token records and auth metadata using the filesystem as backing storage.
type FileTokenStore struct {
	mu      sync.Mutex
	dirLock sync.RWMutex
	baseDir string
}

// NewFileTokenStore creates a token store that saves credentials to disk through the
// TokenStorage implementation embedded in the token record.
func NewFileTokenStore() *FileTokenStore {
	return &FileTokenStore{}
}

// SetBaseDir updates the default directory used for auth JSON persistence when no explicit path is provided.
func (s *FileTokenStore) SetBaseDir(dir string) {
	s.dirLock.Lock()
	s.baseDir = strings.TrimSpace(dir)
	s.dirLock.Unlock()
}

// Save persists token storage and metadata to the resolved auth file path.
func (s *FileTokenStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("auth filestore: missing file path attribute for %s", auth.ID)
	}

	if auth.Disabled {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			if err = s.persistRuntimeState(auth); err != nil {
				return "", err
			}
			return "", nil
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("auth filestore: create dir failed: %w", err)
	}

	// metadataSetter is a private interface for TokenStorage implementations that support metadata injection.
	type metadataSetter interface {
		SetMetadata(map[string]any)
	}

	switch {
	case auth.Storage != nil:
		if setter, ok := auth.Storage.(metadataSetter); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if err = auth.Storage.SaveTokenToFile(path); err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		auth.Metadata["disabled"] = auth.Disabled
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("auth filestore: marshal metadata failed: %w", errMarshal)
		}
		if existing, errRead := os.ReadFile(path); errRead == nil {
			if jsonEqual(existing, raw) {
				if err = s.persistRuntimeStateLocked(auth); err != nil {
					return "", err
				}
				return path, nil
			}
			file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
			if errOpen != nil {
				return "", fmt.Errorf("auth filestore: open existing failed: %w", errOpen)
			}
			if _, errWrite := file.Write(raw); errWrite != nil {
				_ = file.Close()
				return "", fmt.Errorf("auth filestore: write existing failed: %w", errWrite)
			}
			if errClose := file.Close(); errClose != nil {
				return "", fmt.Errorf("auth filestore: close existing failed: %w", errClose)
			}
			return path, nil
		} else if !os.IsNotExist(errRead) {
			return "", fmt.Errorf("auth filestore: read existing failed: %w", errRead)
		}
		if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
			return "", fmt.Errorf("auth filestore: write file failed: %w", errWrite)
		}
	default:
		return "", fmt.Errorf("auth filestore: nothing to persist for %s", auth.ID)
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["path"] = path

	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}

	if err = s.persistRuntimeStateLocked(auth); err != nil {
		return "", err
	}

	return path, nil
}

// List enumerates all auth JSON files under the configured directory.
func (s *FileTokenStore) List(ctx context.Context) ([]*cliproxyauth.Auth, error) {
	dir := s.baseDirSnapshot()
	if dir == "" {
		return nil, fmt.Errorf("auth filestore: directory not configured")
	}
	entries := make([]*cliproxyauth.Auth, 0)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		auth, err := s.readAuthFile(path, dir)
		if err != nil {
			return nil
		}
		if auth != nil {
			entries = append(entries, auth)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.applyRuntimeState(entries)
	return entries, nil
}

// Delete removes the auth file.
func (s *FileTokenStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("auth filestore: id is empty")
	}
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("auth filestore: delete failed: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.updateRuntimeStateLocked(id, nil)
	return nil
}

func (s *FileTokenStore) resolveDeletePath(id string) (string, error) {
	if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
		return id, nil
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, id), nil
}

func (s *FileTokenStore) readAuthFile(path, baseDir string) (*cliproxyauth.Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	metadata := make(map[string]any)
	if err = json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("unmarshal auth json: %w", err)
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	if provider == "antigravity" || provider == "gemini" {
		projectID := ""
		if pid, ok := metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(pid)
		}
		if projectID == "" {
			accessToken := extractAccessToken(metadata)
			// For gemini type, the stored access_token is likely expired (~1h lifetime).
			// Refresh it using the long-lived refresh_token before querying.
			if provider == "gemini" {
				if tokenMap, ok := metadata["token"].(map[string]any); ok {
					if refreshed, errRefresh := refreshGeminiAccessToken(tokenMap, http.DefaultClient); errRefresh == nil {
						accessToken = refreshed
					}
				}
			}
			if accessToken != "" {
				fetchedProjectID, errFetch := FetchAntigravityProjectID(context.Background(), accessToken, http.DefaultClient)
				if errFetch == nil && strings.TrimSpace(fetchedProjectID) != "" {
					metadata["project_id"] = strings.TrimSpace(fetchedProjectID)
					if raw, errMarshal := json.Marshal(metadata); errMarshal == nil {
						if file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600); errOpen == nil {
							_, _ = file.Write(raw)
							_ = file.Close()
						}
					}
				}
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	id := s.idFor(path, baseDir)
	disabled, _ := metadata["disabled"].(bool)
	status := cliproxyauth.StatusActive
	if disabled {
		status = cliproxyauth.StatusDisabled
	}
	auth := &cliproxyauth.Auth{
		ID:               id,
		Provider:         provider,
		FileName:         id,
		Label:            s.labelFor(metadata),
		Status:           status,
		Disabled:         disabled,
		Attributes:       map[string]string{"path": path},
		Metadata:         metadata,
		CreatedAt:        info.ModTime(),
		UpdatedAt:        info.ModTime(),
		LastRefreshedAt:  time.Time{},
		NextRefreshAfter: time.Time{},
	}
	if email, ok := metadata["email"].(string); ok && email != "" {
		auth.Attributes["email"] = email
	}
	cliproxyauth.ApplyCustomHeadersFromMetadata(auth)
	return auth, nil
}

func (s *FileTokenStore) applyRuntimeState(entries []*cliproxyauth.Auth) {
	if len(entries) == 0 {
		return
	}
	stateFile, err := s.readRuntimeStateFile()
	if err != nil || len(stateFile.Entries) == 0 {
		return
	}
	for _, auth := range entries {
		if auth == nil || isOpenAICompatAuth(auth) {
			continue
		}
		state, ok := stateFile.Entries[strings.TrimSpace(auth.ID)]
		if !ok {
			continue
		}
		auth.Disabled = state.Disabled
		auth.Unavailable = state.Unavailable
		auth.Status = state.Status
		if auth.Status == "" {
			if auth.Disabled {
				auth.Status = cliproxyauth.StatusDisabled
			} else {
				auth.Status = cliproxyauth.StatusActive
			}
		}
		auth.StatusMessage = strings.TrimSpace(state.StatusMessage)
		auth.Quota = state.Quota
		auth.NextRetryAfter = state.NextRetryAfter
		if state.LastError != nil {
			auth.LastError = &cliproxyauth.Error{
				Code:       state.LastError.Code,
				Message:    state.LastError.Message,
				Retryable:  state.LastError.Retryable,
				HTTPStatus: state.LastError.HTTPStatus,
			}
		} else {
			auth.LastError = nil
		}
		if !state.UpdatedAt.IsZero() {
			auth.UpdatedAt = state.UpdatedAt.UTC()
		}
	}
}

func (s *FileTokenStore) persistRuntimeState(auth *cliproxyauth.Auth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistRuntimeStateLocked(auth)
}

func (s *FileTokenStore) persistRuntimeStateLocked(auth *cliproxyauth.Auth) error {
	if auth == nil || isOpenAICompatAuth(auth) {
		return nil
	}
	state := runtimeStateForAuth(auth)
	return s.updateRuntimeStateLocked(auth.ID, state)
}

func (s *FileTokenStore) updateRuntimeStateLocked(authID string, state *persistedAuthRuntimeState) error {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	stateFile, err := s.readRuntimeStateFileLocked()
	if err != nil {
		return err
	}
	if stateFile.Entries == nil {
		stateFile.Entries = make(map[string]persistedAuthRuntimeState)
	}
	if state == nil {
		delete(stateFile.Entries, authID)
	} else {
		stateFile.Entries[authID] = *state
	}
	return s.writeRuntimeStateFileLocked(stateFile)
}

func (s *FileTokenStore) readRuntimeStateFile() (*persistedAuthRuntimeStateFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readRuntimeStateFileLocked()
}

func (s *FileTokenStore) readRuntimeStateFileLocked() (*persistedAuthRuntimeStateFile, error) {
	path, err := s.runtimeStatePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &persistedAuthRuntimeStateFile{Entries: map[string]persistedAuthRuntimeState{}}, nil
		}
		return nil, fmt.Errorf("auth filestore: read runtime state failed: %w", err)
	}
	var stateFile persistedAuthRuntimeStateFile
	if err := json.Unmarshal(raw, &stateFile); err != nil {
		return nil, fmt.Errorf("auth filestore: decode runtime state failed: %w", err)
	}
	if stateFile.Entries == nil {
		stateFile.Entries = map[string]persistedAuthRuntimeState{}
	}
	return &stateFile, nil
}

func (s *FileTokenStore) writeRuntimeStateFileLocked(stateFile *persistedAuthRuntimeStateFile) error {
	path, err := s.runtimeStatePath()
	if err != nil {
		return err
	}
	if stateFile == nil || len(stateFile.Entries) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("auth filestore: remove runtime state failed: %w", err)
		}
		return nil
	}
	raw, err := json.MarshalIndent(stateFile, "", "  ")
	if err != nil {
		return fmt.Errorf("auth filestore: encode runtime state failed: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("auth filestore: write runtime state failed: %w", err)
	}
	return nil
}

func (s *FileTokenStore) runtimeStatePath() (string, error) {
	dir := s.baseDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, authRuntimeStateFileName), nil
}

func runtimeStateForAuth(auth *cliproxyauth.Auth) *persistedAuthRuntimeState {
	if auth == nil {
		return nil
	}
	status := auth.Status
	if status == "" {
		status = cliproxyauth.StatusActive
	}
	statusMessage := strings.TrimSpace(auth.StatusMessage)
	lastError := cloneRuntimeError(auth.LastError)
	if !auth.Disabled &&
		!auth.Unavailable &&
		status == cliproxyauth.StatusActive &&
		statusMessage == "" &&
		lastError == nil &&
		!auth.Quota.Exceeded &&
		strings.TrimSpace(auth.Quota.Reason) == "" &&
		auth.Quota.NextRecoverAt.IsZero() &&
		auth.Quota.BackoffLevel == 0 &&
		auth.NextRetryAfter.IsZero() {
		return nil
	}
	return &persistedAuthRuntimeState{
		Disabled:       auth.Disabled,
		Unavailable:    auth.Unavailable,
		Status:         status,
		StatusMessage:  statusMessage,
		LastError:      lastError,
		Quota:          auth.Quota,
		NextRetryAfter: auth.NextRetryAfter,
		UpdatedAt:      auth.UpdatedAt.UTC(),
	}
}

func cloneRuntimeError(err *cliproxyauth.Error) *cliproxyauth.Error {
	if err == nil {
		return nil
	}
	return &cliproxyauth.Error{
		Code:       err.Code,
		Message:    err.Message,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
}

func isOpenAICompatAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return true
	}
	return auth.Attributes != nil && strings.TrimSpace(auth.Attributes["compat_name"]) != ""
}

func (s *FileTokenStore) idFor(path, baseDir string) string {
	id := path
	if baseDir != "" {
		if rel, errRel := filepath.Rel(baseDir, path); errRel == nil && rel != "" {
			id = rel
		}
	}
	// On Windows, normalize ID casing to avoid duplicate auth entries caused by case-insensitive paths.
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func (s *FileTokenStore) resolveAuthPath(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			return p, nil
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		if filepath.IsAbs(fileName) {
			return fileName, nil
		}
		if dir := s.baseDirSnapshot(); dir != "" {
			return filepath.Join(dir, fileName), nil
		}
		return fileName, nil
	}
	if auth.ID == "" {
		return "", fmt.Errorf("auth filestore: missing id")
	}
	if filepath.IsAbs(auth.ID) {
		return auth.ID, nil
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, auth.ID), nil
}

func (s *FileTokenStore) labelFor(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata["label"].(string); ok && v != "" {
		return v
	}
	if v, ok := metadata["email"].(string); ok && v != "" {
		return v
	}
	if project, ok := metadata["project_id"].(string); ok && project != "" {
		return project
	}
	return ""
}

func (s *FileTokenStore) baseDirSnapshot() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.baseDir
}

func extractAccessToken(metadata map[string]any) string {
	if at, ok := metadata["access_token"].(string); ok {
		if v := strings.TrimSpace(at); v != "" {
			return v
		}
	}
	if tokenMap, ok := metadata["token"].(map[string]any); ok {
		if at, ok := tokenMap["access_token"].(string); ok {
			if v := strings.TrimSpace(at); v != "" {
				return v
			}
		}
	}
	return ""
}

func refreshGeminiAccessToken(tokenMap map[string]any, httpClient *http.Client) (string, error) {
	refreshToken, _ := tokenMap["refresh_token"].(string)
	clientID, _ := tokenMap["client_id"].(string)
	clientSecret, _ := tokenMap["client_secret"].(string)
	tokenURI, _ := tokenMap["token_uri"].(string)

	if refreshToken == "" || clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("missing refresh credentials")
	}
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}

	resp, err := httpClient.PostForm(tokenURI, data)
	if err != nil {
		return "", fmt.Errorf("refresh request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh failed: status %d", resp.StatusCode)
	}

	var result map[string]any
	if errUnmarshal := json.Unmarshal(body, &result); errUnmarshal != nil {
		return "", fmt.Errorf("decode refresh response: %w", errUnmarshal)
	}

	newAccessToken, _ := result["access_token"].(string)
	if newAccessToken == "" {
		return "", fmt.Errorf("no access_token in refresh response")
	}

	tokenMap["access_token"] = newAccessToken
	return newAccessToken, nil
}

// jsonEqual compares two JSON blobs by parsing them into Go objects and deep comparing.
func jsonEqual(a, b []byte) bool {
	var objA any
	var objB any
	if err := json.Unmarshal(a, &objA); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &objB); err != nil {
		return false
	}
	return deepEqualJSON(objA, objB)
}

func deepEqualJSON(a, b any) bool {
	switch valA := a.(type) {
	case map[string]any:
		valB, ok := b.(map[string]any)
		if !ok || len(valA) != len(valB) {
			return false
		}
		for key, subA := range valA {
			subB, ok1 := valB[key]
			if !ok1 || !deepEqualJSON(subA, subB) {
				return false
			}
		}
		return true
	case []any:
		sliceB, ok := b.([]any)
		if !ok || len(valA) != len(sliceB) {
			return false
		}
		for i := range valA {
			if !deepEqualJSON(valA[i], sliceB[i]) {
				return false
			}
		}
		return true
	case float64:
		valB, ok := b.(float64)
		if !ok {
			return false
		}
		return valA == valB
	case string:
		valB, ok := b.(string)
		if !ok {
			return false
		}
		return valA == valB
	case bool:
		valB, ok := b.(bool)
		if !ok {
			return false
		}
		return valA == valB
	case nil:
		return b == nil
	default:
		return false
	}
}
