// Package cliproxy provides the core service implementation for the CLI Proxy API.
// It includes service lifecycle management, authentication handling, file watching,
// and integration with various AI service providers through a unified interface.
package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/authrecovery"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/openai_compat_state"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	log "github.com/sirupsen/logrus"
)

// Service wraps the proxy server lifecycle so external programs can embed the CLI proxy.
// It manages the complete lifecycle including authentication, file watching, HTTP server,
// and integration with various AI service providers.
type Service struct {
	// cfg holds the current application configuration.
	cfg *config.Config

	// cfgMu protects concurrent access to the configuration.
	cfgMu sync.RWMutex

	// configUpdateMu serializes config updates across watcher + home.
	configUpdateMu sync.Mutex

	// executorRegistrationMu serializes executor registration/rebinding.
	executorRegistrationMu sync.Mutex

	// configPath is the path to the configuration file.
	configPath string

	// tokenProvider handles loading token-based clients.
	tokenProvider TokenClientProvider

	// apiKeyProvider handles loading API key-based clients.
	apiKeyProvider APIKeyClientProvider

	// watcherFactory creates file watcher instances.
	watcherFactory WatcherFactory

	// hooks provides lifecycle callbacks.
	hooks Hooks

	// serverOptions contains additional server configuration options.
	serverOptions []api.ServerOption

	// server is the HTTP API server instance.
	server *api.Server

	// pprofServer manages the optional pprof HTTP debug server.
	pprofServer *pprofServer

	// serverErr channel for server startup/shutdown errors.
	serverErr chan error

	// watcher handles file system monitoring.
	watcher *WatcherWrapper

	// watcherCancel cancels the watcher context.
	watcherCancel context.CancelFunc

	// authUpdates channel for authentication updates.
	authUpdates chan watcher.AuthUpdate

	// authQueueStop cancels the auth update queue processing.
	authQueueStop context.CancelFunc

	// authManager handles legacy authentication operations.
	authManager *sdkAuth.Manager

	// accessManager handles request authentication providers.
	accessManager *sdkaccess.Manager

	// coreManager handles core authentication and execution.
	coreManager *coreauth.Manager

	// shutdownOnce ensures shutdown is called only once.
	shutdownOnce sync.Once

	// wsGateway manages websocket Gemini providers.
	wsGateway *wsrelay.Manager

	homeClient       *home.Client
	homeCancel       context.CancelFunc
	homeLogForwarder *logging.HomeAppLogForwarder
	pluginHost       *pluginhost.Host

	homePluginSyncMu    sync.Mutex
	homePluginSyncKey   string
	homePluginSyncFetch func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error)
}

type executorRegistrationOptions struct {
	includeBaseline   bool
	includePlugins    bool
	auths             []*coreauth.Auth
	forceReplaceAuths bool
}

var registerPluginExecutors = func(host *pluginhost.Host, manager *coreauth.Manager) {
	if host == nil || manager == nil {
		return
	}
	host.RegisterExecutors(manager, registry.GetGlobalRegistry())
}

// RegisterUsagePlugin registers a usage plugin on the global usage manager.
// This allows external code to monitor API usage and token consumption.
//
// Parameters:
//   - plugin: The usage plugin to register
func (s *Service) RegisterUsagePlugin(plugin usage.Plugin) {
	usage.RegisterPlugin(plugin)
}

// newDefaultAuthManager creates a default authentication manager with all supported providers.
func newDefaultAuthManager() *sdkAuth.Manager {
	return sdkAuth.NewManager(
		sdkAuth.GetTokenStore(),
		sdkAuth.NewGeminiAuthenticator(),
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewXAIAuthenticator(),
	)
}

func (s *Service) ensureAuthUpdateQueue(ctx context.Context) {
	if s == nil {
		return
	}
	if s.authUpdates == nil {
		s.authUpdates = make(chan watcher.AuthUpdate, 256)
	}
	if s.authQueueStop != nil {
		return
	}
	queueCtx, cancel := context.WithCancel(ctx)
	s.authQueueStop = cancel
	go s.consumeAuthUpdates(queueCtx)
}

func (s *Service) consumeAuthUpdates(ctx context.Context) {
	ctx = coreauth.WithSkipPersist(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-s.authUpdates:
			if !ok {
				return
			}
			s.handleAuthUpdate(ctx, update)
		labelDrain:
			for {
				select {
				case nextUpdate := <-s.authUpdates:
					s.handleAuthUpdate(ctx, nextUpdate)
				default:
					break labelDrain
				}
			}
		}
	}
}

func (s *Service) emitAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.watcher != nil && s.watcher.DispatchRuntimeAuthUpdate(update) {
		return
	}
	if s.authUpdates != nil {
		select {
		case s.authUpdates <- update:
			return
		default:
			log.Debugf("auth update queue saturated, applying inline action=%v id=%s", update.Action, update.ID)
		}
	}
	s.handleAuthUpdate(ctx, update)
}

func (s *Service) handleAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	if s == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || s.coreManager == nil {
		return
	}
	switch update.Action {
	case watcher.AuthUpdateActionAdd, watcher.AuthUpdateActionModify:
		if update.Auth == nil || update.Auth.ID == "" {
			return
		}
		s.applyCoreAuthAddOrUpdate(ctx, update.Auth)
	case watcher.AuthUpdateActionDelete:
		id := update.ID
		if id == "" && update.Auth != nil {
			id = update.Auth.ID
		}
		if id == "" {
			return
		}
		s.applyCoreAuthRemoval(ctx, id)
	default:
		log.Debugf("received unknown auth update action: %v", update.Action)
	}
}

func (s *Service) ensureWebsocketGateway() {
	if s == nil {
		return
	}
	if s.wsGateway != nil {
		return
	}
	opts := wsrelay.Options{
		Path:           "/v1/ws",
		OnConnected:    s.wsOnConnected,
		OnDisconnected: s.wsOnDisconnected,
		LogDebugf:      log.Debugf,
		LogInfof:       log.Infof,
		LogWarnf:       log.Warnf,
	}
	s.wsGateway = wsrelay.NewManager(opts)
}

func (s *Service) wsOnConnected(channelID string) {
	if s == nil || channelID == "" {
		return
	}
	if !strings.HasPrefix(strings.ToLower(channelID), "aistudio-") {
		return
	}
	if s.coreManager != nil {
		if existing, ok := s.coreManager.GetByID(channelID); ok && existing != nil {
			if !existing.Disabled && existing.Status == coreauth.StatusActive {
				return
			}
		}
	}
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:         channelID,  // keep channel identifier as ID
		Provider:   "aistudio", // logical provider for switch routing
		Label:      channelID,  // display original channel id
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"email": channelID}, // metadata drives logging and usage tracking
	}
	log.Infof("websocket provider connected: %s", channelID)
	s.emitAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     auth.ID,
		Auth:   auth,
	})
}

func (s *Service) wsOnDisconnected(channelID string, reason error) {
	if s == nil || channelID == "" {
		return
	}
	if reason != nil {
		if strings.Contains(reason.Error(), "replaced by new connection") {
			log.Infof("websocket provider replaced: %s", channelID)
			return
		}
		log.Warnf("websocket provider disconnected: %s (%v)", channelID, reason)
	} else {
		log.Infof("websocket provider disconnected: %s", channelID)
	}
	ctx := context.Background()
	s.emitAuthUpdate(ctx, watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionDelete,
		ID:     channelID,
	})
}

func (s *Service) applyCoreAuthAddOrUpdate(ctx context.Context, auth *coreauth.Auth) {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return
	}
	auth = auth.Clone()
	s.ensureExecutorsForAuth(auth)

	// IMPORTANT: Update coreManager FIRST, before model registration.
	// This ensures that configuration changes (proxy_url, prefix, etc.) take effect
	// immediately for API calls, rather than waiting for model registration to complete.
	op := "register"
	var err error
	if existing, ok := s.coreManager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		if !existing.Disabled && existing.Status != coreauth.StatusDisabled && !auth.Disabled && auth.Status != coreauth.StatusDisabled {
			auth.LastRefreshedAt = existing.LastRefreshedAt
			auth.NextRefreshAfter = existing.NextRefreshAfter
			// 磁盘热加载常丢失运行时冷却字段，先从现有运行态回填，再交给 Update 合并
			if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
			if auth.NextRetryAfter.IsZero() && existing.NextRetryAfter.After(time.Now()) {
				auth.NextRetryAfter = existing.NextRetryAfter
				auth.Unavailable = existing.Unavailable
				if auth.Status == coreauth.StatusActive || auth.Status == "" {
					auth.Status = existing.Status
				}
				if strings.TrimSpace(auth.StatusMessage) == "" {
					auth.StatusMessage = existing.StatusMessage
				}
				if existing.Quota.Exceeded {
					auth.Quota = existing.Quota
				}
			}
		}
		op = "update"
		_, err = s.coreManager.Update(ctx, auth)
	} else {
		_, err = s.coreManager.Register(ctx, auth)
	}
	if err != nil {
		log.Errorf("failed to %s auth %s: %v", op, auth.ID, err)
		current, ok := s.coreManager.GetByID(auth.ID)
		if !ok || current.Disabled {
			GlobalModelRegistry().UnregisterClient(auth.ID)
			return
		}
		auth = current
	}

	// Register models after auth is updated in coreManager.
	// This operation may block on network calls, but the auth configuration
	// is already effective at this point.
	s.registerModelsForAuth(ctx, auth)
	s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)

	// Refresh the scheduler entry so that the auth's supportedModelSet is rebuilt
	// from the now-populated global model registry. Without this, newly added auths
	// have an empty supportedModelSet (because Register/Update upserts into the
	// scheduler before registerModelsForAuth runs) and are invisible to the scheduler.
	s.coreManager.RefreshSchedulerEntry(auth.ID)
}

func (s *Service) applyCoreAuthRemoval(ctx context.Context, id string) {
	if s == nil || id == "" {
		return
	}
	if s.coreManager == nil {
		return
	}
	GlobalModelRegistry().UnregisterClient(id)
	if existing, ok := s.coreManager.GetByID(id); ok && existing != nil {
		existing.Disabled = true
		existing.Status = coreauth.StatusDisabled
		if _, err := s.coreManager.Update(ctx, existing); err != nil {
			log.Errorf("failed to disable auth %s: %v", id, err)
		}
		if strings.EqualFold(strings.TrimSpace(existing.Provider), "codex") {
			executor.CloseCodexWebsocketSessionsForAuthID(existing.ID, "auth_removed")
			s.ensureExecutorsForAuth(existing)
		}
	}
}

func (s *Service) applyRetryConfig(cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	maxInterval := time.Duration(cfg.MaxRetryInterval) * time.Second
	s.coreManager.SetRetryConfig(cfg.RequestRetry, maxInterval, cfg.MaxRetryCredentials)
}

func openAICompatInfoFromAuth(a *coreauth.Auth) (providerKey string, compatName string, ok bool) {
	if a == nil {
		return "", "", false
	}
	if len(a.Attributes) > 0 {
		providerKey = strings.TrimSpace(a.Attributes["provider_key"])
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return strings.ToLower(providerKey), compatName, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		return "openai-compatibility", strings.TrimSpace(a.Label), true
	}
	return "", "", false
}

func logOpenAICompatModelSnapshot(a *coreauth.Auth, providerKey string, modelCount int, action string) {
	if a == nil || a.Attributes == nil {
		return
	}
	compatName := strings.TrimSpace(a.Attributes["compat_name"])
	if compatName == "" && strings.TrimSpace(a.Attributes["provider_key"]) == "" {
		return
	}
	log.WithFields(log.Fields{
		"auth_id":      a.ID,
		"provider":     a.Provider,
		"provider_key": strings.TrimSpace(providerKey),
		"compat_name":  compatName,
		"api_key":      strings.TrimSpace(a.Attributes["api_key"]),
		"model_count":  modelCount,
	}).Infof("OpenAI兼容凭证模型快照%s", action)
}

// rebindExecutors refreshes provider executors so they observe the latest configuration.
func (s *Service) rebindExecutors() {
	if s == nil || s.coreManager == nil {
		return
	}
	auths := s.coreManager.List()
	reboundCodex := false
	for _, auth := range auths {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			if reboundCodex {
				continue
			}
			reboundCodex = true
		}
		s.ensureExecutorsForAuthWithMode(auth, true)
	}
}

func (s *Service) applyConfigUpdate(newCfg *config.Config) {
	if s == nil {
		return
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	previousStrategy := ""
	var previousSessionAffinity bool
	var previousSessionAffinityTTL string
	s.cfgMu.RLock()
	if s.cfg != nil {
		previousStrategy = strings.ToLower(strings.TrimSpace(s.cfg.Routing.Strategy))
		previousSessionAffinity = s.cfg.Routing.SessionAffinity
		previousSessionAffinityTTL = s.cfg.Routing.SessionAffinityTTL
	}
	s.cfgMu.RUnlock()

	if newCfg == nil {
		s.cfgMu.RLock()
		newCfg = s.cfg
		s.cfgMu.RUnlock()
	}
	if newCfg == nil {
		return
	}

	nextStrategy := strings.ToLower(strings.TrimSpace(newCfg.Routing.Strategy))
	normalizeStrategy := func(strategy string) string {
		switch strategy {
		case "fill-first", "fillfirst", "ff":
			return "fill-first"
		default:
			return "round-robin"
		}
	}
	previousStrategy = normalizeStrategy(previousStrategy)
	nextStrategy = normalizeStrategy(nextStrategy)

	nextSessionAffinity := newCfg.Routing.SessionAffinity
	nextSessionAffinityTTL := newCfg.Routing.SessionAffinityTTL

	selectorChanged := previousStrategy != nextStrategy ||
		previousSessionAffinity != nextSessionAffinity ||
		previousSessionAffinityTTL != nextSessionAffinityTTL

	if s.coreManager != nil && selectorChanged {
		var selector coreauth.Selector
		switch nextStrategy {
		case "fill-first":
			selector = &coreauth.FillFirstSelector{}
		default:
			selector = &coreauth.RoundRobinSelector{}
		}

		if nextSessionAffinity {
			ttl := time.Hour
			if ttlStr := strings.TrimSpace(nextSessionAffinityTTL); ttlStr != "" {
				if parsed, err := time.ParseDuration(ttlStr); err == nil && parsed > 0 {
					ttl = parsed
				}
			}
			selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
				Fallback: selector,
				TTL:      ttl,
			})
		}

		s.coreManager.SetSelector(selector)
	}

	s.applyRetryConfig(newCfg)
	s.applyPprofConfig(newCfg)
	if s.server != nil {
		s.server.UpdateClients(newCfg)
	}
	s.cfgMu.Lock()
	s.cfg = newCfg
	s.cfgMu.Unlock()
	if s.coreManager != nil {
		s.coreManager.SetConfig(newCfg)
		s.coreManager.SetOAuthModelAlias(newCfg.OAuthModelAlias)
	}
	ctx := coreauth.WithSkipPersist(context.Background())
	// Keep plugin runtime/menus in sync when config.yaml is hot-reloaded.
	if s.pluginHost != nil {
		s.pluginHost.ApplyConfig(ctx, newCfg)
	}
	s.syncPluginRuntimeConfig(ctx)
	var auths []*coreauth.Auth
	if s.coreManager != nil {
		auths = s.coreManager.List()
	}
	s.registerAvailableExecutors(context.Background(), executorRegistrationOptions{
		includeBaseline:   newCfg.Home.Enabled,
		forceReplaceAuths: true,
		auths:             auths,
	})
	s.registerConfigAPIKeyAuths(ctx, newCfg)
	s.syncPluginModelRuntime(ctx)
	s.rebindExecutors()
}

func forceHomeRuntimeConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.APIKeys = nil
	cfg.UsageStatisticsEnabled = true
	cfg.DisableCooling = true
	cfg.SaveCooldownStatus = false
	cfg.WebsocketAuth = false
	cfg.EnableGeminiCLIEndpoint = false
	cfg.RemoteManagement.AllowRemote = false
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.Plugins.StoreAuth = nil
}

func (s *Service) syncPluginRuntimeConfig(ctx context.Context) bool {
	if s == nil || s.coreManager == nil {
		return false
	}
	if s.pluginHost == nil {
		s.coreManager.SetPluginScheduler(nil)
		return false
	}
	s.coreManager.SetPluginScheduler(s.pluginHost)
	return true
}

func (s *Service) registerConfigAPIKeyAuths(ctx context.Context, cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	configSynth := synthesizer.NewConfigSynthesizer()
	auths, errSynthesize := configSynth.Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		log.Warnf("failed to synthesize config API key auths: %v", errSynthesize)
		return
	}
	for _, auth := range auths {
		if !coreauth.IsConfigAPIKeyAuth(auth) {
			continue
		}
		s.applyCoreAuthAddOrUpdate(ctx, auth)
	}
}

func (s *Service) syncPluginModelRuntime(ctx context.Context) {
	if s == nil || s.pluginHost == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.pluginHost.RegisterModels(ctx, registry.GetGlobalRegistry())
	s.registerAvailableExecutors(ctx, executorRegistrationOptions{
		includeBaseline:   s.cfg != nil && s.cfg.Home.Enabled,
		includePlugins:    true,
		forceReplaceAuths: true,
		auths:             s.coreManager.List(),
	})
	for _, auth := range s.coreManager.List() {
		s.registerModelsForAuth(ctx, auth)
		if auth != nil {
			s.coreManager.RefreshSchedulerEntry(auth.ID)
		}
	}
}

func (s *Service) applyHomeOverlay(remoteCfg *config.Config) {
	if errApply := s.applyHomeOverlayContext(context.Background(), remoteCfg); errApply != nil {
		log.Warnf("failed to apply home config payload: %v", errApply)
	}
}

func (s *Service) applyHomeOverlayContext(ctx context.Context, remoteCfg *config.Config) error {
	if s == nil || remoteCfg == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.cfgMu.RLock()
	baseCfg := s.cfg
	s.cfgMu.RUnlock()
	if baseCfg == nil {
		return nil
	}

	merged := *remoteCfg
	merged.Host = baseCfg.Host
	merged.Port = baseCfg.Port
	merged.TLS = baseCfg.TLS
	merged.Home = baseCfg.Home
	storeAuth := merged.Plugins.StoreAuth
	forceHomeRuntimeConfig(&merged)
	syncCfg := merged
	syncCfg.Plugins.StoreAuth = storeAuth

	logHomeConfigChanges(baseCfg, &merged)
	report, syncKey, didSync, errSync := s.syncHomePlugins(ctx, &syncCfg)
	if errSync != nil {
		log.Warnf("failed to sync home plugins: %v", errSync)
	}
	s.applyConfigUpdate(&merged)
	var errLoad error
	if didSync {
		errLoad = homeplugins.MarkLoadResults(&report, s.pluginHost)
		if errLoad != nil {
			log.Warnf("failed to load home plugins after config update: %v", errLoad)
		}
	}
	if strings.TrimSpace(report.Task) != "" {
		s.reportHomePluginStatus(ctx, &merged, report)
		if errSync == nil && errLoad == nil {
			s.markHomePluginsSynced(syncKey)
		}
	}
	s.processHomePluginTasks(ctx, &merged)
	return nil
}

func logHomeConfigChanges(oldCfg, newCfg *config.Config) {
	if oldCfg == nil || newCfg == nil || !newCfg.Home.Enabled || (!oldCfg.Debug && !newCfg.Debug) {
		return
	}

	details := diff.BuildConfigChangeDetails(oldCfg, newCfg)
	if len(details) == 0 {
		return
	}

	if newCfg.Debug && !log.IsLevelEnabled(log.DebugLevel) {
		util.SetLogLevel(newCfg)
	}

	log.Debugf("home config changes detected:")
	for _, detail := range details {
		log.Debugf("  %s", detail)
	}
}

func (s *Service) startHomeUsageForwarder(ctx context.Context, client *home.Client) {
	if s == nil || client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sleep := func(d time.Duration) bool {
		if d <= 0 {
			return true
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if !client.HeartbeatOK() {
				if !sleep(time.Second) {
					return
				}
				continue
			}

			items := redisqueue.PopOldest(64)
			if len(items) == 0 {
				if !sleep(500 * time.Millisecond) {
					return
				}
				continue
			}

			for i := range items {
				if errPush := client.LPushUsage(ctx, items[i]); errPush != nil {
					for j := i; j < len(items); j++ {
						redisqueue.Enqueue(items[j])
					}
					if !sleep(time.Second) {
						return
					}
					break
				}
			}
		}
	}()
}

func (s *Service) startHomeSubscriber(ctx context.Context) {
	if s == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || !cfg.Home.Enabled {
		return
	}

	if s.homeCancel != nil {
		s.homeCancel()
		s.homeCancel = nil
	}
	if s.homeClient != nil {
		s.homeClient.Close()
		s.homeClient = nil
	}
	if s.homeLogForwarder != nil {
		s.homeLogForwarder.Stop()
		s.homeLogForwarder = nil
	}

	homeCtx := ctx
	if homeCtx == nil {
		homeCtx = context.Background()
	}
	homeCtx, cancel := context.WithCancel(homeCtx)
	s.homeCancel = cancel

	client := home.New(cfg.Home)
	s.homeClient = client
	home.SetCurrent(client)

	go client.StartConfigSubscriber(homeCtx, func(raw []byte) error {
		parsed, err := config.ParseConfigBytes(raw)
		if err != nil {
			log.Warnf("failed to parse home config payload: %v", err)
			return err
		}
		s.applyHomeOverlay(parsed)
		return nil
	})
	s.startHomeUsageForwarder(homeCtx, client)
	s.homeLogForwarder = logging.StartHomeAppLogForwarder(0)
}

// Run starts the service and blocks until the context is cancelled or the server stops.
// It initializes all components including authentication, file watching, HTTP server,
// and starts processing requests. The method blocks until the context is cancelled.
//
// Parameters:
//   - ctx: The context for controlling the service lifecycle
//
// Returns:
//   - error: An error if the service fails to start or run
func (s *Service) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("cliproxy: service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	usage.StartDefault(ctx)
	homeEnabled := s.cfg != nil && s.cfg.Home.Enabled
	if homeEnabled {
		forceHomeRuntimeConfig(s.cfg)
		redisqueue.SetUsageStatisticsEnabled(true)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	defer func() {
		if err := s.Shutdown(shutdownCtx); err != nil {
			log.Errorf("service shutdown returned error: %v", err)
		}
	}()

	if !homeEnabled {
		if errEnsureAuthDir := s.ensureAuthDir(); errEnsureAuthDir != nil {
			return errEnsureAuthDir
		}
	}

	s.applyRetryConfig(s.cfg)

	if s.coreManager != nil && !homeEnabled {
		if errLoad := s.coreManager.Load(ctx); errLoad != nil {
			log.Warnf("failed to load auth store: %v", errLoad)
		}
		if errRestore := s.coreManager.RestoreCooldownStates(ctx); errRestore != nil {
			log.Warnf("failed to restore cooldown states: %v", errRestore)
		}
	}

	if !homeEnabled {
		tokenResult, err := s.tokenProvider.Load(ctx, s.cfg)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if tokenResult == nil {
			tokenResult = &TokenClientResult{}
		}

		apiKeyResult, err := s.apiKeyProvider.Load(ctx, s.cfg)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if apiKeyResult == nil {
			apiKeyResult = &APIKeyClientResult{}
		}
	}

	// legacy clients removed; no caches to refresh

	// handlers no longer depend on legacy clients; pass nil slice initially
	s.server = api.NewServer(s.cfg, s.coreManager, s.accessManager, s.configPath, s.serverOptions...)

	if s.authManager == nil {
		s.authManager = newDefaultAuthManager()
	}

	if homeEnabled {
		s.startHomeSubscriber(ctx)
	}

	s.ensureWebsocketGateway()
	if s.server != nil && s.wsGateway != nil {
		s.server.AttachWebsocketRoute(s.wsGateway.Path(), s.wsGateway.Handler())
		s.server.SetWebsocketAuthChangeHandler(func(oldEnabled, newEnabled bool) {
			if oldEnabled == newEnabled {
				return
			}
			if !oldEnabled && newEnabled {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if errStop := s.wsGateway.Stop(ctx); errStop != nil {
					log.Warnf("failed to reset websocket connections after ws-auth change %t -> %t: %v", oldEnabled, newEnabled, errStop)
					return
				}
				log.Debugf("ws-auth enabled; existing websocket sessions terminated to enforce authentication")
				return
			}
			log.Debugf("ws-auth disabled; existing websocket sessions remain connected")
		})
	}

	if homeEnabled {
		s.registerAvailableExecutors(ctx, executorRegistrationOptions{
			includeBaseline:   true,
			forceReplaceAuths: true,
		})
		// Home mode does not expose in-process Redis RESP usage output; usage is forwarded to home instead.
		redisqueue.SetEnabled(true)
	}

	if s.hooks.OnBeforeStart != nil {
		s.hooks.OnBeforeStart(s.cfg)
	}

	// Register callback for startup and periodic model catalog refresh.
	// When remote model definitions change, re-register models for affected providers.
	// This intentionally rebuilds per-auth model availability from the latest catalog
	// snapshot instead of preserving prior registry suppression state.
	registry.SetModelRefreshCallback(func(changedProviders []string) {
		if s == nil || s.coreManager == nil || len(changedProviders) == 0 {
			return
		}

		providerSet := make(map[string]bool, len(changedProviders))
		for _, p := range changedProviders {
			providerSet[strings.ToLower(strings.TrimSpace(p))] = true
		}

		auths := s.coreManager.List()
		refreshed := 0
		for _, item := range auths {
			if item == nil || item.ID == "" {
				continue
			}
			auth, ok := s.coreManager.GetByID(item.ID)
			if !ok || auth == nil || auth.Disabled {
				continue
			}
			provider := strings.ToLower(strings.TrimSpace(auth.Provider))
			if !providerSet[provider] {
				continue
			}
			if s.refreshModelRegistrationForAuth(auth) {
				refreshed++
			}
		}

		if refreshed > 0 {
			log.Infof("re-registered models for %d auth(s) due to model catalog changes: %v", refreshed, changedProviders)
		}
	})

	s.serverErr = make(chan error, 1)
	go func() {
		if errStart := s.server.Start(); errStart != nil {
			s.serverErr <- errStart
		} else {
			s.serverErr <- nil
		}
	}()

	time.Sleep(100 * time.Millisecond)
	fmt.Printf("API server started successfully on: %s:%d\n", s.cfg.Host, s.cfg.Port)

	s.applyPprofConfig(s.cfg)

	if s.hooks.OnAfterStart != nil {
		s.hooks.OnAfterStart(s)
	}

	if !homeEnabled {
		var watcherWrapper *WatcherWrapper
		reloadCallback := func(newCfg *config.Config) { s.applyConfigUpdate(newCfg) }

		watcherWrapper, errCreate := s.watcherFactory(s.configPath, s.cfg.AuthDir, reloadCallback)
		if errCreate != nil {
			return fmt.Errorf("cliproxy: failed to create watcher: %w", errCreate)
		}
		s.watcher = watcherWrapper
		s.ensureAuthUpdateQueue(ctx)
		if s.authUpdates != nil {
			watcherWrapper.SetAuthUpdateQueue(s.authUpdates)
		}
		watcherWrapper.SetConfig(s.cfg)

		watcherCtx, watcherCancel := context.WithCancel(context.Background())
		s.watcherCancel = watcherCancel
		if errStart := watcherWrapper.Start(watcherCtx); errStart != nil {
			return fmt.Errorf("cliproxy: failed to start watcher: %w", errStart)
		}
		log.Info("file watcher started for config and auth directory changes")
	}

	// Prefer core auth manager auto refresh if available.
	if s.coreManager != nil && !homeEnabled {
		interval := 15 * time.Minute
		s.coreManager.StartAutoRefresh(context.Background(), interval)
		log.Infof("core auth auto-refresh started (interval=%s)", interval)
		authrecovery.Start(ctx, s.cfg, s.coreManager)
	}

	select {
	case <-ctx.Done():
		log.Debug("service context cancelled, shutting down...")
		return ctx.Err()
	case errServer := <-s.serverErr:
		return errServer
	}
}

// Shutdown gracefully stops background workers and the HTTP server.
// It ensures all resources are properly cleaned up and connections are closed.
// The shutdown is idempotent and can be called multiple times safely.
//
// Parameters:
//   - ctx: The context for controlling the shutdown timeout
//
// Returns:
//   - error: An error if shutdown fails
func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var shutdownErr error
	s.shutdownOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}

		if s.homeCancel != nil {
			s.homeCancel()
			s.homeCancel = nil
		}
		if s.homeClient != nil {
			s.homeClient.Close()
			s.homeClient = nil
		}
		if s.homeLogForwarder != nil {
			s.homeLogForwarder.Stop()
			s.homeLogForwarder = nil
		}
		home.ClearCurrent()

		// legacy refresh loop removed; only stopping core auth manager below

		if s.watcherCancel != nil {
			s.watcherCancel()
		}
		if s.coreManager != nil {
			s.coreManager.StopAutoRefresh()
		}
		if s.watcher != nil {
			if err := s.watcher.Stop(); err != nil {
				log.Errorf("failed to stop file watcher: %v", err)
				shutdownErr = err
			}
		}
		if s.wsGateway != nil {
			if err := s.wsGateway.Stop(ctx); err != nil {
				log.Errorf("failed to stop websocket gateway: %v", err)
				if shutdownErr == nil {
					shutdownErr = err
				}
			}
		}
		if s.authQueueStop != nil {
			s.authQueueStop()
			s.authQueueStop = nil
		}

		if errShutdownPprof := s.shutdownPprof(ctx); errShutdownPprof != nil {
			log.Errorf("failed to stop pprof server: %v", errShutdownPprof)
			if shutdownErr == nil {
				shutdownErr = errShutdownPprof
			}
		}

		// no legacy clients to persist

		if s.server != nil {
			shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := s.server.Stop(shutdownCtx); err != nil {
				log.Errorf("error stopping API server: %v", err)
				if shutdownErr == nil {
					shutdownErr = err
				}
			}
		}

		usage.StopDefault()
		if err := internalusage.CloseDefaultStore(); err != nil && shutdownErr == nil {
			shutdownErr = err
		}
		if err := openai_compat_state.CloseDefault(); err != nil && shutdownErr == nil {
			shutdownErr = err
		}
	})
	return shutdownErr
}

func (s *Service) ensureAuthDir() error {
	info, err := os.Stat(s.cfg.AuthDir)
	if err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(s.cfg.AuthDir, 0o755); mkErr != nil {
				return fmt.Errorf("cliproxy: failed to create auth directory %s: %w", s.cfg.AuthDir, mkErr)
			}
			log.Infof("created missing auth directory: %s", s.cfg.AuthDir)
			return nil
		}
		return fmt.Errorf("cliproxy: error checking auth directory %s: %w", s.cfg.AuthDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cliproxy: auth path exists but is not a directory: %s", s.cfg.AuthDir)
	}
	return nil
}
