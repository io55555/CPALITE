package authrecovery

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type Loop struct {
	manager  *coreauth.Manager
	interval time.Duration
	workers  int
}

func Start(ctx context.Context, cfg *config.Config, manager *coreauth.Manager) *Loop {
	if manager == nil {
		return nil
	}
	seconds := 600
	workers := 4
	if cfg != nil {
		if cfg.QuotaRecoveryRefreshIntervalSeconds > 0 {
			seconds = cfg.QuotaRecoveryRefreshIntervalSeconds
		}
		if cfg.QuotaRecoveryRefreshWorkers > 0 {
			workers = cfg.QuotaRecoveryRefreshWorkers
		}
	}
	if workers > 16 {
		workers = 16
	}
	loop := &Loop{manager: manager, interval: time.Duration(seconds) * time.Second, workers: workers}
	go loop.run(ctx)
	return loop
}

func (l *Loop) run(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			l.round(ctx)
			timer.Reset(l.interval)
		}
	}
}

func (l *Loop) round(ctx context.Context) {
	targets := make([]*coreauth.Auth, 0)
	for _, auth := range l.manager.List() {
		if quotaTarget(auth) {
			targets = append(targets, auth)
		}
	}
	if len(targets) == 0 {
		return
	}
	jobs := make(chan *coreauth.Auth)
	var wg sync.WaitGroup
	for i := 0; i < l.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for auth := range jobs {
				l.refreshOne(ctx, auth)
			}
		}()
	}
	defer wg.Wait()
	for _, auth := range targets {
		select {
		case <-ctx.Done():
			close(jobs)
			return
		case jobs <- auth:
		}
	}
	close(jobs)
}

func (l *Loop) refreshOne(ctx context.Context, auth *coreauth.Auth) {
	exec, ok := l.manager.Executor(auth.Provider)
	if !ok || exec == nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	refreshed, err := exec.Refresh(refreshCtx, auth.Clone())
	if err != nil {
		log.Debugf("quota recovery refresh failed for %s: %v", auth.ID, err)
		return
	}
	if refreshed == nil {
		return
	}
	if quotaTarget(refreshed) && !quotaRecoveryDue(auth, time.Now()) {
		return
	}
	l.manager.MarkQuotaRecovered(ctx, auth.ID)
}

func quotaTarget(auth *coreauth.Auth) bool {
	if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if auth.Quota.Exceeded {
		return true
	}
	msg := strings.ToLower(auth.StatusMessage)
	if auth.Unavailable && (strings.Contains(msg, "quota") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "exhaust")) {
		return true
	}
	for _, state := range auth.ModelStates {
		if state != nil && state.Quota.Exceeded {
			return true
		}
	}
	return false
}

func quotaRecoveryDue(auth *coreauth.Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	due := false
	if auth.Quota.Exceeded {
		if auth.Quota.NextRecoverAt.IsZero() || auth.Quota.NextRecoverAt.After(now) {
			return false
		}
		due = true
	}
	for _, state := range auth.ModelStates {
		if state == nil || !state.Quota.Exceeded {
			continue
		}
		if state.Quota.NextRecoverAt.IsZero() || state.Quota.NextRecoverAt.After(now) {
			return false
		}
		due = true
	}
	return due
}
