package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- cenci automerge on|off|status (ticket #964) ---------------------------

// automergeStatusJSON mirrors the pinned --json output shape.
type automergeStatusJSON struct {
	Enabled bool   `json:"enabled"`
	Config  string `json:"config"`
	Repo    *struct {
		Root   string `json:"root"`
		Scopes []struct {
			Scope   string `json:"scope"`
			Present bool   `json:"present"`
		} `json:"scopes"`
	} `json:"repo,omitempty"`
}

func TestAutomergeOn_CreatesConfigAndPreservesKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cenci", "config.json")

	// First run creates the file and its parent dir.
	out, err := exec.Command(binaryPath, "automerge", "on", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("automerge on: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Automerge: enabled") {
		t.Errorf("automerge on output = %q, want to contain %q", out, "Automerge: enabled")
	}

	// Seed an unrelated key next to the switch, then toggle off: the key
	// must survive the raw-map round trip.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("decoding config: %v\n%s", err, data)
	}
	top["defaultAgent"] = json.RawMessage(`"claude"`)
	seeded, err := json.Marshal(top)
	if err != nil {
		t.Fatalf("re-encoding config: %v", err)
	}
	if err := os.WriteFile(configPath, seeded, 0o600); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	out, err = exec.Command(binaryPath, "automerge", "off", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("automerge off: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Automerge: disabled") {
		t.Errorf("automerge off output = %q, want to contain %q", out, "Automerge: disabled")
	}
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-reading config: %v", err)
	}
	if !strings.Contains(string(data), `"defaultAgent"`) {
		t.Errorf("unrelated defaultAgent key not preserved, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"enabled": false`) {
		t.Errorf("automerge.enabled not persisted false, got:\n%s", data)
	}
}

func TestAutomergeStatus_NoConfigFileReportsDisabled(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	out, err := exec.Command(binaryPath, "automerge", "status", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("automerge status with no config file: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Automerge: disabled") {
		t.Errorf("status output = %q, want to contain %q", out, "Automerge: disabled")
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Errorf("status must never write the config file; stat err = %v", statErr)
	}
}

func TestAutomergeStatus_JSONIncludesRepoPolicyScopes(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".cenci"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoConfig := `{
  "isMonorepo": true,
  "projects": [
    {"slug": "api", "path": "packages/api", "automerge": {"protectedPaths": ["*secret*"], "maxChangedFiles": 8, "maxDiffLines": 400}},
    {"slug": "web", "path": "apps/web"}
  ]
}`
	if err := os.WriteFile(filepath.Join(repoDir, ".cenci", "config.json"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(binaryPath, "automerge", "status", "--config", configPath, "--dir", repoDir, "--json").Output()
	if err != nil {
		t.Fatalf("automerge status --json: %v\n%s", err, out)
	}
	var got automergeStatusJSON
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if got.Enabled {
		t.Error("enabled = true with no fleet config, want false")
	}
	if got.Config != configPath {
		t.Errorf("config = %q, want %q", got.Config, configPath)
	}
	if got.Repo == nil {
		t.Fatalf("repo section missing from %s", out)
	}
	present := map[string]bool{}
	for _, s := range got.Repo.Scopes {
		present[s.Scope] = s.Present
	}
	if !present["api"] {
		t.Errorf("scope api present = %v, want true (scopes: %+v)", present["api"], got.Repo.Scopes)
	}
	if webPresent, ok := present["web"]; !ok || webPresent {
		t.Errorf("scope web = (%v, found %v), want reported absent", webPresent, ok)
	}
}

func TestAutomergeUnknownSubcommandExits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "automerge", "enable")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("automerge enable: err = %v (out %q), want exit 2", err, out)
	}
	if !strings.Contains(string(out), "unknown subcommand") {
		t.Errorf("output = %q, want an unknown-subcommand hint", out)
	}
}

func TestAutomergeNoSubcommandExits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "automerge")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("bare automerge: err = %v (out %q), want exit 2", err, out)
	}
	if !strings.Contains(string(out), "on, off, or status") {
		t.Errorf("output = %q, want a subcommand hint", out)
	}
}
