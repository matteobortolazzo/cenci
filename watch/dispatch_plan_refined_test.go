package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- cenci dispatch plan-refined on|off|status (ticket #964) ---------------

// planRefinedStatusJSON mirrors the pinned --json output shape.
type planRefinedStatusJSON struct {
	Enabled      bool   `json:"enabled"`
	Config       string `json:"config"`
	Repo         string `json:"repo,omitempty"`
	RepoAutonomy string `json:"repo_autonomy,omitempty"`
	Authorized   *bool  `json:"authorized,omitempty"`
}

func TestDispatchPlanRefinedOnOff_WritesFlagAndPreservesKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cenci", "config.json")

	out, err := exec.Command(binaryPath, "dispatch", "plan-refined", "on", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("plan-refined on: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Planning pickup (dispatch.planRefined): enabled") {
		t.Errorf("plan-refined on output = %q, want the enabled state line", out)
	}

	// Enroll writes into the same dispatch block; both keys must coexist.
	repoDir := initGitRemote(t, "git@github.com:owner/name.git")
	if out, err := exec.Command(binaryPath, "dispatch", "enroll", "--dir", repoDir, "--config", configPath).CombinedOutput(); err != nil {
		t.Fatalf("enroll after plan-refined on: %v\n%s", err, out)
	}

	out, err = exec.Command(binaryPath, "dispatch", "plan-refined", "off", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("plan-refined off: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Planning pickup (dispatch.planRefined): disabled") {
		t.Errorf("plan-refined off output = %q, want the disabled state line", out)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !strings.Contains(string(data), `"repos"`) || !strings.Contains(string(data), "owner/name") {
		t.Errorf("dispatch.repos enrollment not preserved through plan-refined off, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"planRefined": false`) {
		t.Errorf("dispatch.planRefined not persisted false, got:\n%s", data)
	}
}

func TestDispatchPlanRefinedStatus_JSONReportsRepoAutonomy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	// A plain directory (not a git repo): the fleet flag still resolves and
	// the repo fields stay absent rather than erroring — status must work
	// from anywhere.
	out, err := exec.Command(binaryPath, "dispatch", "plan-refined", "status", "--config", configPath, "--dir", t.TempDir(), "--json").Output()
	if err != nil {
		t.Fatalf("plan-refined status --json (non-repo): %v\n%s", err, out)
	}
	var got planRefinedStatusJSON
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if got.Enabled {
		t.Error("enabled = true with no fleet config, want false")
	}
	if got.Repo != "" || got.RepoAutonomy != "" || got.Authorized != nil {
		t.Errorf("repo fields = (%q, %q, %v) for a non-repo dir, want absent", got.Repo, got.RepoAutonomy, got.Authorized)
	}

	// Inside a git repo the autonomy verdict is reported; with no fetched
	// origin/main ref it must fail closed as "unreadable" and authorized
	// must be false even with the fleet flag on.
	repoDir := initGitRemote(t, "git@github.com:owner/name.git")
	if out, err := exec.Command(binaryPath, "dispatch", "plan-refined", "on", "--config", configPath, "--dir", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("plan-refined on: %v\n%s", err, out)
	}
	out, err = exec.Command(binaryPath, "dispatch", "plan-refined", "status", "--config", configPath, "--dir", repoDir, "--json").Output()
	if err != nil {
		t.Fatalf("plan-refined status --json (repo): %v\n%s", err, out)
	}
	got = planRefinedStatusJSON{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if !got.Enabled {
		t.Error("enabled = false after plan-refined on, want true")
	}
	if got.Repo != "owner/name" {
		t.Errorf("repo = %q, want owner/name", got.Repo)
	}
	if got.RepoAutonomy != "unreadable" {
		t.Errorf("repo_autonomy = %q, want unreadable (no origin/main ref fetched)", got.RepoAutonomy)
	}
	if got.Authorized == nil || *got.Authorized {
		t.Errorf("authorized = %v, want false (fleet flag alone never authorizes)", got.Authorized)
	}
}

func TestDispatchPlanRefinedUnknownSubcommandExits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "dispatch", "plan-refined", "enable")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("plan-refined enable: err = %v (out %q), want exit 2", err, out)
	}
	if !strings.Contains(string(out), "unknown subcommand") {
		t.Errorf("output = %q, want an unknown-subcommand hint", out)
	}
}
