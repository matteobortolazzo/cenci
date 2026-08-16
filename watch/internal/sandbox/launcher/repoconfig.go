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
	configPath := filepath.Join(repoRoot, ".cenci", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w", configPath, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return false, fmt.Errorf("%s: top level must be an object: %w", configPath, err)
		}
		return false, fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if top == nil {
		return false, fmt.Errorf("%s: top level must be an object", configPath)
	}

	sandboxRaw, ok := top["sandbox"]
	if !ok {
		return false, nil
	}
	var sandboxObj map[string]json.RawMessage
	if err := json.Unmarshal(sandboxRaw, &sandboxObj); err != nil {
		return false, fmt.Errorf("%s: \"sandbox\" must be an object: %w", configPath, err)
	}

	valueRaw, ok := sandboxObj[key]
	if !ok {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(valueRaw, &value); err != nil {
		return false, fmt.Errorf("%s: %q must be a boolean: %w", configPath, key, err)
	}
	return value, nil
}
