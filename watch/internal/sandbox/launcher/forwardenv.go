package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// sandboxEnvConfigPath resolves the user fleet config that holds
// sandboxEnv.forward: $XDG_CONFIG_HOME/cenci/config.json, else
// ~/.config/cenci/config.json (run.DefaultConfigPath, the resolver
// resolveAttendedFlag already reaches through the dispatch package). A var so
// a test can point it elsewhere; "" (no home directory) resolves to the
// built-in secret set, the same as an absent file.
var sandboxEnvConfigPath = run.DefaultConfigPath

// sandboxEnvForwardKey is the config key, as the errors, the warning and the
// BoundaryWeakening Option name it.
const sandboxEnvForwardKey = "sandboxEnv.forward"

// forwardNamePattern is the grammar of a forwardable env var name. A name
// beginning with "-" would be parsed by the runtime CLI as a flag after "-e"
// (an argv-injection path), and one containing "=" would turn the bare
// "-e NAME" token into a value-bearing one, so both are refused by the pattern
// itself.
var forwardNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedForwardName reports whether name belongs to the launcher.
// CENCI_ATTENDED, CENCI_LAUNCH_ID, CENCI_SANDBOX* and TMUX_PANE are launcher-
// owned exec env (assembleExecEnv, the launch-ID tag): a user entry overriding
// one would defeat the dispatch attended pin and the reaper's launch-ID
// liveness.
func reservedForwardName(name string) bool {
	return name == "TMUX_PANE" || strings.HasPrefix(name, "CENCI_")
}

// validateForwardName is the one validator for a configured forward name,
// shared by the reader (readSandboxEnvForward) and the merge
// (resolveSecretEnvSet) so a name that bypassed the reader is still refused.
// The returned error names the offending entry quoted, without a config path
// (the reader adds it).
//
// An entry containing "=" is reported as NAME=<redacted>: everything after the
// first "=" is dropped before it can reach an error, so a user who pasted
// NAME=value into the config never sees (or logs) the value.
func validateForwardName(name string) error {
	if idx := strings.IndexByte(name, '='); idx >= 0 {
		return fmt.Errorf("%s entry %q must be an environment variable name, not NAME=value; the config stores names only", sandboxEnvForwardKey, name[:idx]+"=<redacted>")
	}
	if !forwardNamePattern.MatchString(name) {
		return fmt.Errorf("%s entry %q is not a valid environment variable name (must match %s)", sandboxEnvForwardKey, name, forwardNamePattern)
	}
	if reservedForwardName(name) {
		return fmt.Errorf("%s entry %q is reserved for the launcher", sandboxEnvForwardKey, name)
	}
	return nil
}

// validateForwardNames validates every name and rejects a repeated one, as
// RepoSandboxPlugins does for sandbox.plugins: a duplicate inside the list is a
// config error, not silently deduped.
func validateForwardNames(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if err := validateForwardName(name); err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("%s names %q as a duplicate", sandboxEnvForwardKey, name)
		}
		seen[name] = true
	}
	return nil
}

// readSandboxEnvForward reads sandboxEnv.forward out of the fleet config at
// path and returns the validated, ordered name list.
//
// Only an absent config file, an empty path, an absent "sandboxEnv" or
// "forward" key, and an explicit null or [] resolve to (nil, nil) — the
// built-in set. Every other failure class — an unreadable file, unparsable
// JSON, a non-object top level, a non-object "sandboxEnv", a non-array
// "forward", a non-string element, an invalid, reserved or "="-bearing name, or
// a duplicate — resolves to (nil, error): a plain, path-bearing, non-usage
// error (IsUsage(err) is false), so a malformed allowlist hard-fails the
// caller (exit 1) instead of launching without it. A silently dropped allowlist
// would hide the config error behind a confusing in-container failure, and an
// error can never widen what crosses the boundary. A two-stage RawMessage
// decode is used so a wrong-typed value is distinguishable from an absent one
// (the same shape as readRepoSandboxObject, #632).
func readSandboxEnvForward(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("%s: top level must be an object: %w", path, err)
		}
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if top == nil {
		return nil, fmt.Errorf("%s: top level must be an object", path)
	}

	sandboxEnvRaw, ok := top["sandboxEnv"]
	if !ok {
		return nil, nil
	}
	// "sandboxEnv": null decodes to a nil map and no error, which reads as an
	// absent block (built-in set), like RepoSandboxPlugins' null handling.
	var sandboxEnv map[string]json.RawMessage
	if err := json.Unmarshal(sandboxEnvRaw, &sandboxEnv); err != nil {
		return nil, fmt.Errorf("%s: \"sandboxEnv\" must be an object: %w", path, err)
	}

	forwardRaw, ok := sandboxEnv["forward"]
	if !ok {
		return nil, nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(forwardRaw, &elements); err != nil {
		return nil, fmt.Errorf("%s: %q must be an array of strings: %w", path, sandboxEnvForwardKey, err)
	}

	names := make([]string, 0, len(elements))
	for i, element := range elements {
		// A JSON null would decode into a string as "" with no error, then fail
		// the name check with a misleading empty-entry message; refuse it as the
		// wrong type it is.
		var name string
		if bytes.Equal(bytes.TrimSpace(element), []byte("null")) {
			return nil, fmt.Errorf("%s: %s[%d] must be a string, got null", path, sandboxEnvForwardKey, i)
		}
		if err := json.Unmarshal(element, &name); err != nil {
			return nil, fmt.Errorf("%s: %s[%d] must be a string: %w", path, sandboxEnvForwardKey, i, err)
		}
		names = append(names, name)
	}
	if err := validateForwardNames(names); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(names) == 0 {
		return nil, nil
	}
	return names, nil
}

// loadSecretEnvSet resolves the secret-name set a launch, dry-run or audit
// operates on: the built-in names merged with the user's sandboxEnv.forward.
// It is the only production caller of readSandboxEnvForward and
// resolveSecretEnvSet, so every operation reads the config once, before any
// runtime call, and a malformed config hard-fails them all alike.
func loadSecretEnvSet() (secretEnvSet, error) {
	configured, err := readSandboxEnvForward(sandboxEnvConfigPath())
	if err != nil {
		return secretEnvSet{}, err
	}
	return resolveSecretEnvSet(configured)
}
