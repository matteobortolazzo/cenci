package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultSandboxPluginsOrder is the default sandbox plugin pair, in the
// order every golden create-arg/refresh-command assertion depends on.
var defaultSandboxPluginsOrder = []string{"cenci", "cenci-watch"}

// allowedSandboxPlugins is the closed set of plugin names ".cenci/
// config.json"'s "sandbox.plugins" may name — any other string (including
// the "cenci-sandbox" near-miss) is a hard error (#1002).
var allowedSandboxPlugins = map[string]bool{
	"cenci":       true,
	"cenci-watch": true,
}

// DefaultSandboxPlugins returns the default sandbox plugin pair
// (["cenci", "cenci-watch"]) as a fresh, non-nil slice on every call, so no
// caller can mutate a shared backing array (watch/docs/go-gotchas.md
// nil-slice/JSON rule).
func DefaultSandboxPlugins() []string {
	out := make([]string, len(defaultSandboxPluginsOrder))
	copy(out, defaultSandboxPluginsOrder)
	return out
}

// RepoSandboxPlugins reads <repoRoot>/.cenci/config.json's "sandbox.plugins"
// key, mirroring RepoDindConfig's exact two-stage decode and failure
// taxonomy (dind.go:26-66): an absent config file, an absent "sandbox" key,
// or an absent "plugins" key all resolve to the safe default pair
// (DefaultSandboxPlugins(), nil error). An explicit "plugins" array resolves
// to exactly the subset it names, verbatim in its own order (never
// re-sorted, never deduped) — including an empty array, which is a
// DIFFERENT valid resolution than the default, not a synonym for it. Every
// other failure class — an unreadable file, unparsable JSON, a non-object
// top level, a wrong-typed "sandbox"/"plugins" field, a non-string element,
// an unrecognized plugin name (outside {"cenci","cenci-watch"}), or a
// duplicate name — resolves to (nil, error): a plain, path-bearing,
// non-usage error (IsUsage(err) is false), so corrupt or malformed config
// hard-fails the caller (exit 1) instead of silently launching with a
// default or deduped plugin list (#632, #1002).
func RepoSandboxPlugins(repoRoot string) ([]string, error) {
	configPath := filepath.Join(repoRoot, ".cenci", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultSandboxPlugins(), nil
		}
		return nil, fmt.Errorf("reading %s: %w", configPath, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("%s: top level must be an object: %w", configPath, err)
		}
		return nil, fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if top == nil {
		return nil, fmt.Errorf("%s: top level must be an object", configPath)
	}

	sandboxRaw, ok := top["sandbox"]
	if !ok {
		return DefaultSandboxPlugins(), nil
	}
	var sandboxObj map[string]json.RawMessage
	if err := json.Unmarshal(sandboxRaw, &sandboxObj); err != nil {
		return nil, fmt.Errorf("%s: \"sandbox\" must be an object: %w", configPath, err)
	}

	pluginsRaw, ok := sandboxObj["plugins"]
	if !ok {
		return DefaultSandboxPlugins(), nil
	}
	var plugins []string
	if err := json.Unmarshal(pluginsRaw, &plugins); err != nil {
		return nil, fmt.Errorf("%s: \"plugins\" must be an array of strings: %w", configPath, err)
	}
	if plugins == nil {
		plugins = []string{}
	}

	seen := make(map[string]bool, len(plugins))
	for _, name := range plugins {
		if !allowedSandboxPlugins[name] {
			return nil, fmt.Errorf("%s: \"plugins\" names an unrecognized plugin %q; allowed values are \"cenci\" and \"cenci-watch\"", configPath, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: \"plugins\" names %q as a duplicate", configPath, name)
		}
		seen[name] = true
	}

	return plugins, nil
}

// ResolveSandboxPlugins resolves the sandbox plugin list for scope, mirroring
// ResolveDind's repo-scope-only read gate (dind.go:93-99): in legacy
// (non-repo) scope the default pair applies and RepoSandboxPlugins is never
// invoked at all — a corrupt config.json outside repo scope must never hard-
// fail a launch that never needed to read it. In repo scope,
// RepoSandboxPlugins' result (value or error) is returned unchanged.
func ResolveSandboxPlugins(scope Scope) ([]string, error) {
	if scope.WorkspaceScope != "repo" {
		return DefaultSandboxPlugins(), nil
	}
	return RepoSandboxPlugins(scope.RepoRoot)
}

// sandboxPluginsEnvValue formats plugins for the CENCI_SANDBOX_PLUGINS
// create-time env value: space-separated, in plugins' own order (never
// re-sorted). An empty or nil list formats to "" — the caller is
// responsible for always emitting the "-e CENCI_SANDBOX_PLUGINS=" flag even
// then; the env var is always set, including for the default and empty
// lists (only its absence, in a running container predating this ticket,
// means "legacy container").
func sandboxPluginsEnvValue(plugins []string) string {
	return strings.Join(plugins, " ")
}

// sandboxPluginsDiffer reports whether resolved (the launch's own resolved
// plugin list) drifts from a running container's actual CENCI_SANDBOX_
// PLUGINS create-time env value, for the attach-path drift warning. The
// comparison is set-wise (order-insensitive), so element order alone never
// produces a spurious warning. When runningFound is false — the running
// container carries no CENCI_SANDBOX_PLUGINS at all (a legacy container,
// predating this ticket) — running is ignored and resolved is compared
// against DefaultSandboxPlugins() instead (Q&A #5).
func sandboxPluginsDiffer(resolved []string, running string, runningFound bool) bool {
	var runningSet []string
	if runningFound {
		if running != "" {
			runningSet = strings.Split(running, " ")
		}
	} else {
		runningSet = DefaultSandboxPlugins()
	}

	resolvedSet := make(map[string]bool, len(resolved))
	for _, p := range resolved {
		resolvedSet[p] = true
	}
	runningSetMap := make(map[string]bool, len(runningSet))
	for _, p := range runningSet {
		runningSetMap[p] = true
	}

	if len(resolvedSet) != len(runningSetMap) {
		return true
	}
	for p := range resolvedSet {
		if !runningSetMap[p] {
			return true
		}
	}
	return false
}
