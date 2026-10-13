package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// repoSandboxBoolConfig reads a boolean key out of
// <repoRoot>/.cenci/config.json's "sandbox" object, mirroring
// internal/run/template.go's Load pattern (stdlib-only JSON read), and
// returns the resolved boolean plus an error.
//
// Only an absent config file, an absent "sandbox"/<key> key, and an explicit
// `false` resolve to a safe (false, nil) off state. Every other failure
// class — an unreadable file, unparsable JSON, a non-object top level, or a
// wrong-typed "sandbox"/<key> field — resolves to (false, error): a plain,
// path-bearing, non-usage error (IsUsage(err) is false), so corrupt or
// malformed config hard-fails the caller (exit 1) instead of silently
// launching with the feature off (#632). A two-stage decode
// (map[string]any/json.RawMessage) is used instead of a typed struct so a
// wrong-typed field is distinguishable from an absent one.
//
// Shared by every `sandbox.<key>` boolean reader (RepoDindConfig,
// RepoAzureConfig) so a fix to the failure-classification rules above lands
// once rather than per-key.
func repoSandboxBoolConfig(repoRoot, key string) (bool, error) {
	sandboxObj, present, err := readRepoSandboxObject(repoRoot)
	if err != nil || !present {
		return false, err
	}

	valueRaw, ok := sandboxObj[key]
	if !ok {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(valueRaw, &value); err != nil {
		return false, fmt.Errorf("%s: %q must be a boolean: %w", repoConfigPath(repoRoot), key, err)
	}
	return value, nil
}

// repoConfigPath is <repoRoot>/.cenci/config.json, the file every
// `sandbox.<key>` reader decodes.
func repoConfigPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".cenci", "config.json")
}

// readRepoSandboxObject is the shared first half of every `sandbox.<key>`
// reader (repoSandboxBoolConfig, RepoSandboxPlugins, RepoSandboxLimits): it
// reads <repoRoot>/.cenci/config.json and returns its "sandbox" object
// undecoded per key, so each caller can classify its own wrong-typed value.
// present is false with a nil error when the config file or its "sandbox" key
// is absent (the caller applies its default). An unreadable file, unparsable
// JSON, a non-object top level, or a non-object "sandbox" is a plain,
// path-bearing, non-usage error (#632).
func readRepoSandboxObject(repoRoot string) (sandboxObj map[string]json.RawMessage, present bool, err error) {
	configPath := repoConfigPath(repoRoot)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading %s: %w", configPath, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return nil, false, fmt.Errorf("%s: top level must be an object: %w", configPath, err)
		}
		return nil, false, fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if top == nil {
		return nil, false, fmt.Errorf("%s: top level must be an object", configPath)
	}

	sandboxRaw, ok := top["sandbox"]
	if !ok {
		return nil, false, nil
	}
	if err := json.Unmarshal(sandboxRaw, &sandboxObj); err != nil {
		return nil, false, fmt.Errorf("%s: \"sandbox\" must be an object: %w", configPath, err)
	}
	return sandboxObj, true, nil
}
