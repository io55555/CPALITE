package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	grokmanager "grok-manager/lib"
)

const builtinGrokManagerPath = "builtin://grok-manager"

type builtinGrokManagerClient struct {
	host *Host
}

func isBuiltinPluginPath(path string) bool {
	return strings.HasPrefix(strings.TrimSpace(path), "builtin://")
}

func preferBuiltinGrokManager() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv("CPA_GROK_MANAGER_BUILTIN"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	// Default: always prefer in-process builtin so Alpine/musl works without dlopen.
	// Dynamic .so remains available for external plugin-store installs when builtin is forced off.
	return true
}

func injectBuiltinGrokManager(files []pluginFile, items map[string]runtimeItemConfig) []pluginFile {
	item, ok := items[grokmanager.Name()]
	if !ok || !item.Enabled {
		return files
	}
	if !preferBuiltinGrokManager() {
		// only inject if no filesystem plugin was selected
		for _, f := range files {
			if f.ID == grokmanager.Name() {
				return files
			}
		}
	}
	out := make([]pluginFile, 0, len(files)+1)
	for _, f := range files {
		if f.ID == grokmanager.Name() {
			continue
		}
		out = append(out, f)
	}
	out = append(out, pluginFile{
		ID:      grokmanager.Name(),
		Path:    builtinGrokManagerPath,
		Version: grokmanager.Version(),
	})
	return out
}

func openBuiltinPlugin(file pluginFile, host *Host) (pluginClient, error) {
	switch file.ID {
	case grokmanager.Name(), "grok-manager":
		return openBuiltinGrokManager(host)
	default:
		return nil, fmt.Errorf("unknown builtin plugin %s", file.ID)
	}
}

func openBuiltinGrokManager(host *Host) (pluginClient, error) {
	if host == nil {
		return nil, fmt.Errorf("nil plugin host")
	}
	client := &builtinGrokManagerClient{host: host}
	grokmanager.SetHostCaller(client.hostCaller)
	grokmanager.Init()
	return client, nil
}

func (c *builtinGrokManagerClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	_ = ctx
	raw, err := grokmanager.HandleMethod(method, request)
	if err != nil {
		return marshalRPCError("plugin_error", err.Error()), nil
	}
	return raw, nil
}

func (c *builtinGrokManagerClient) Shutdown() {
	grokmanager.Shutdown()
}

func (c *builtinGrokManagerClient) hostCaller(method string, payload any) (json.RawMessage, error) {
	if c == nil || c.host == nil {
		return nil, fmt.Errorf("builtin grok-manager host is unavailable")
	}
	reqBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx := withHostCallbackPluginID(context.Background(), grokmanager.Name())
	raw, errCall := c.host.callFromPlugin(ctx, method, reqBytes)
	if errCall != nil {
		// callFromPlugin may return bare errors; normalize to envelope decode path
		raw = marshalRPCError("host_call_failed", errCall.Error())
	}
	return grokmanager.DecodeHostEnvelope(raw)
}

// keep a reference so -trimpath builds still show platform in diagnostics if needed
var _ = runtime.GOOS
