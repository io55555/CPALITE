package cliproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/breaker"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/capture"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/statusruler"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

type observabilityRoundTripperProvider struct {
	mu      sync.RWMutex
	cache   map[string]http.RoundTripper
	breaker *breaker.Manager
}

func newObservabilityRoundTripperProvider(breakerMgr *breaker.Manager) *observabilityRoundTripperProvider {
	return &observabilityRoundTripperProvider{
		cache:   make(map[string]http.RoundTripper),
		breaker: breakerMgr,
	}
}

func (p *observabilityRoundTripperProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	proxyStr := strings.TrimSpace(auth.ProxyURL)
	if proxyStr == "" {
		return &observabilityRoundTripper{
			base:    http.DefaultTransport,
			auth:    snapshotAuth(auth),
			breaker: p.breaker,
		}
	}
	p.mu.RLock()
	base := p.cache[proxyStr]
	p.mu.RUnlock()
	if base == nil {
		transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyStr)
		if errBuild != nil {
			log.Errorf("%v", errBuild)
			base = http.DefaultTransport
		} else if transport != nil {
			base = transport
		}
		p.mu.Lock()
		p.cache[proxyStr] = base
		p.mu.Unlock()
	}
	return &observabilityRoundTripper{
		base:    base,
		auth:    snapshotAuth(auth),
		breaker: p.breaker,
	}
}

type observabilityRoundTripper struct {
	base    http.RoundTripper
	auth    breaker.AuthSnapshot
	breaker *breaker.Manager
}

func (rt *observabilityRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt == nil || req == nil {
		return nil, fmt.Errorf("roundtrip request is nil")
	}
	if rt.base == nil {
		rt.base = http.DefaultTransport
	}
	if rt.breaker != nil {
		if err := rt.breaker.BeforeRequest(rt.auth); err != nil {
			if session := capture.FromContext(req.Context()); session != nil {
				session.BindAuth(rt.auth.AuthID, rt.auth.AuthIndex, rt.auth.Provider, rt.auth.ProxyURL)
				session.SetError(err.Error())
			}
			return nil, err
		}
	}
	settings := capture.Settings{}
	if store := capture.DefaultStore(); store != nil {
		settings = store.Settings()
	}
	if session := capture.FromContext(req.Context()); session != nil {
		session.BindAuth(rt.auth.AuthID, rt.auth.AuthIndex, rt.auth.Provider, rt.auth.ProxyURL)
		session.CaptureUpstreamRequest(req, settings.MaxBodyBytes)
	}
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		if rt.breaker != nil {
			rt.breaker.RecordFailure(rt.auth, err.Error())
		}
		if session := capture.FromContext(req.Context()); session != nil {
			session.SetError(err.Error())
		}
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("upstream response is nil")
	}
	resp.Body = &captureBodyReadCloser{
		ReadCloser: resp.Body,
		auth:       rt.auth,
		header:     cloneHeaderValues(resp.Header),
		statusCode: resp.StatusCode,
		breaker:    rt.breaker,
		limit:      settings.MaxBodyBytes,
		ctx:        req.Context(),
	}
	return resp, nil
}

type captureBodyReadCloser struct {
	io.ReadCloser
	buf        bytes.Buffer
	limit      int
	statusCode int
	header     http.Header
	auth       breaker.AuthSnapshot
	breaker    *breaker.Manager
	ctx        context.Context
	closed     bool
	captured   bool
}

func (r *captureBodyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && (r.limit <= 0 || r.buf.Len() < r.limit) {
		remaining := r.limit - r.buf.Len()
		if r.limit <= 0 || remaining > n {
			remaining = n
		}
		if remaining > 0 {
			_, _ = r.buf.Write(p[:remaining])
		}
	}
	if errors.Is(err, io.EOF) {
		r.captureResponse()
	}
	return n, err
}

func (r *captureBodyReadCloser) Close() error {
	if r.closed {
		return r.ReadCloser.Close()
	}
	r.closed = true
	r.captureResponse()
	return r.ReadCloser.Close()
}

func (r *captureBodyReadCloser) captureResponse() {
	if r == nil || r.captured {
		return
	}
	r.captured = true
	bodyText := r.buf.String()
	if session := capture.FromContext(r.ctx); session != nil {
		session.BindAuth(r.auth.AuthID, r.auth.AuthIndex, r.auth.Provider, r.auth.ProxyURL)
		session.CaptureUpstreamResponse(r.statusCode, r.header, bodyText, r.limit)
	}
	if r.breaker != nil {
		if r.statusCode >= 502 && r.statusCode <= 504 {
			r.breaker.RecordFailure(r.auth, fmt.Sprintf("http %d", r.statusCode))
		} else {
			r.breaker.RecordSuccess(r.auth)
		}
	}
	statusruler.EvaluateResponse(r.ctx, r.auth, r.statusCode, bodyText)
}

func snapshotAuth(auth *coreauth.Auth) breaker.AuthSnapshot {
	if auth == nil {
		return breaker.AuthSnapshot{}
	}
	return breaker.AuthSnapshot{
		AuthID:    strings.TrimSpace(auth.ID),
		AuthIndex: strings.TrimSpace(auth.EnsureIndex()),
		Provider:  strings.TrimSpace(auth.Provider),
		Label:     strings.TrimSpace(auth.Label),
		ProxyURL:  strings.TrimSpace(auth.ProxyURL),
	}
}

func cloneHeaderValues(header http.Header) http.Header {
	if len(header) == 0 {
		return nil
	}
	out := make(http.Header, len(header))
	for key, values := range header {
		copied := make([]string, len(values))
		copy(copied, values)
		out[key] = copied
	}
	return out
}
