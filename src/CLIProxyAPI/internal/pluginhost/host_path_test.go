package pluginhost

import "testing"

func TestCleanPluginPathPreservesBuiltinURI(t *testing.T) {
	const path = "builtin://grok-manager"
	if got := cleanPluginPath(path); got != path {
		t.Fatalf("cleanPluginPath(%q) = %q, want unchanged", path, got)
	}
}
