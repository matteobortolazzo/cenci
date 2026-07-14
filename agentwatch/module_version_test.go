package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestModuleVersion asserts that the go.mod module major version suffix
// (e.g. "/v2") matches the major version declared in the plugin manifest's
// "version" field. Go's semantic import versioning rules require a module
// at major version 2+ to carry a "/vN" suffix in its module path.
func TestModuleVersion(t *testing.T) {
	moduleMajor := readModuleMajor(t, "go.mod")
	versionMajor := readPluginVersionMajor(t, "plugin/.claude-plugin/plugin.json")

	if "v"+versionMajor != moduleMajor {
		t.Fatalf("go.mod module major version %q does not match plugin.json version major %q (\"v%s\"); update the go.mod module path to include a %q suffix", moduleMajor, versionMajor, versionMajor, "/v"+versionMajor)
	}
}

func readModuleMajor(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}

		modulePath := strings.TrimSpace(strings.TrimPrefix(line, "module "))
		segments := strings.Split(modulePath, "/")
		last := segments[len(segments)-1]
		if isVersionSegment(last) {
			return last
		}

		return "v1" // no /vN suffix means major version 1, per Go's semantic import versioning rules
	}

	t.Fatalf("no module line found in %s", path)
	return ""
}

func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func readPluginVersionMajor(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("failed to parse %s: %v", path, err)
	}

	parts := strings.Split(manifest.Version, ".")
	if len(parts) == 0 || parts[0] == "" {
		t.Fatalf("plugin.json version %q has no major version segment", manifest.Version)
	}

	return parts[0]
}
