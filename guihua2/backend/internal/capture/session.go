package capture

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type sessionContextKey struct{}

type Session struct {
	mu sync.Mutex

	createdAt  time.Time
	startedAt  time.Time
	requestID  string
	method     string
	path       string
	query      string

	authID     string
	authIndex  string
	provider   string
	accessProvider string
	token      string
	apiKey     string
	proxyURL   string

	requestHeaders string
	requestBody    string

	upstreamRequestURL string
	upstreamRequestHeaders string
	upstreamRequestBody string
	upstreamStatusCode int
	upstreamResponseHeaders string
	upstreamResponseBody string

	responseHeaders string
	responseBody    string
	statusCode      int
	errorText       string
}

func WithSession(ctx context.Context, session *Session) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionContextKey{}, session)
}

func FromContext(ctx context.Context) *Session {
	if ctx == nil {
		return nil
	}
	session, _ := ctx.Value(sessionContextKey{}).(*Session)
	return session
}

func NewSession(req *http.Request, requestID string, limit int) *Session {
	session := &Session{
		createdAt: time.Now().UTC(),
		startedAt: time.Now(),
		requestID: strings.TrimSpace(requestID),
	}
	if req == nil || req.URL == nil {
		return session
	}
	session.method = req.Method
	session.path = req.URL.Path
	session.query = req.URL.RawQuery
	session.requestHeaders = headersToJSON(req.Header, limit)
	if req.Body != nil {
		body, restoreBody := readAndRestoreBody(req.Body, limit)
		req.Body = restoreBody
		session.requestBody = body
	}
	return session
}

func (s *Session) SetAccessInfo(provider, token, apiKey string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.accessProvider = strings.TrimSpace(provider)
	s.token = strings.TrimSpace(token)
	s.apiKey = strings.TrimSpace(apiKey)
	s.mu.Unlock()
}

func (s *Session) BindAuth(authID, authIndex, provider, proxyURL string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.authID = strings.TrimSpace(authID)
	s.authIndex = strings.TrimSpace(authIndex)
	s.provider = strings.TrimSpace(provider)
	s.proxyURL = strings.TrimSpace(proxyURL)
	s.mu.Unlock()
}

func (s *Session) CaptureUpstreamRequest(req *http.Request, limit int) {
	if s == nil || req == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.URL != nil {
		s.upstreamRequestURL = req.URL.String()
	}
	s.upstreamRequestHeaders = headersToJSON(req.Header, limit)
	if req.Body != nil {
		body, restoreBody := readAndRestoreBody(req.Body, limit)
		req.Body = restoreBody
		s.upstreamRequestBody = body
	}
}

func (s *Session) CaptureUpstreamResponse(statusCode int, header http.Header, body string, limit int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.upstreamStatusCode = statusCode
	s.upstreamResponseHeaders = headersToJSON(header, limit)
	s.upstreamResponseBody = trimForStorage(body, limit)
	s.mu.Unlock()
}

func (s *Session) SetError(errText string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.errorText = trimForStorage(errText, 8192)
	s.mu.Unlock()
}

func (s *Session) Finalize(statusCode int, header http.Header, responseBody string, limit int) Record {
	if s == nil {
		return Record{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCode = statusCode
	s.responseHeaders = headersToJSON(header, limit)
	s.responseBody = trimForStorage(responseBody, limit)
	success := statusCode > 0 && statusCode < http.StatusBadRequest && s.errorText == ""
	return Record{
		CreatedAt:               s.createdAt,
		RequestID:               s.requestID,
		Method:                  s.method,
		Path:                    s.path,
		Query:                   s.query,
		StatusCode:              statusCode,
		Success:                 success,
		DurationMs:              time.Since(s.startedAt).Milliseconds(),
		Provider:                s.provider,
		AccessProvider:          s.accessProvider,
		AuthID:                  s.authID,
		AuthIndex:               s.authIndex,
		Token:                   s.token,
		APIKey:                  s.apiKey,
		ProxyURL:                s.proxyURL,
		ErrorText:               s.errorText,
		RequestHeaders:          s.requestHeaders,
		RequestBody:             s.requestBody,
		UpstreamRequestURL:      s.upstreamRequestURL,
		UpstreamRequestHeaders:  s.upstreamRequestHeaders,
		UpstreamRequestBody:     s.upstreamRequestBody,
		UpstreamStatusCode:      s.upstreamStatusCode,
		UpstreamResponseHeaders: s.upstreamResponseHeaders,
		UpstreamResponseBody:    s.upstreamResponseBody,
		ResponseHeaders:         s.responseHeaders,
		ResponseBody:            s.responseBody,
	}
}

func readAndRestoreBody(body io.ReadCloser, limit int) (string, io.ReadCloser) {
	if body == nil {
		return "", nil
	}
	defer func() { _ = body.Close() }()
	raw, _ := io.ReadAll(body)
	restored := io.NopCloser(bytes.NewReader(raw))
	if limit > 0 && len(raw) > limit {
		raw = raw[:limit]
	}
	return string(raw), restored
}
