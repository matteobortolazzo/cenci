package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- cenci planning attended on|off|status (#1086) --------------------------
//
// Modeled on watch/dispatch_plan_refined_test.go's cenci dispatch plan-refined
// on|off|status suite. Q2's pinned --json key set (mirroring
// runDispatchPlanRefined's own key names): attended, config, repo,
// repo_autonomy, authorized, plus plan_refined. repo/repo_autonomy/authorized
// stay omitempty outside a git repo; plan_refined is a fleet-scoped flag like
// attended and config, always present.

// planningAttendedStatusJSON mirrors the pinned --json output shape.
type planningAttendedStatusJSON struct {
	Attended     bool   `json:"attended"`
	Config       string `json:"config"`
	Repo         string `json:"repo,omitempty"`
	RepoAutonomy string `json:"repo_autonomy,omitempty"`
	Authorized   *bool  `json:"authorized,omitempty"`
	PlanRefined  bool   `json:"plan_refined"`
}

func TestPlanningAttendedOnOff_WritesFlagAndPreservesKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cenci", "config.json")

	out, err := exec.Command(binaryPath, "planning", "attended", "on", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("planning attended on: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Attended mode (planning.attended): enabled") {
		t.Errorf("planning attended on output = %q, want the enabled state line", out)
	}

	// Enroll writes into the dispatch block; both keys must coexist.
	repoDir := initGitRemote(t, "git@github.com:owner/name.git")
	if out, err := exec.Command(binaryPath, "dispatch", "enroll", "--dir", repoDir, "--config", configPath).CombinedOutput(); err != nil {
		t.Fatalf("enroll after planning attended on: %v\n%s", err, out)
	}
	if out, err := exec.Command(binaryPath, "automerge", "on", "--config", configPath).CombinedOutput(); err != nil {
		t.Fatalf("automerge on after planning attended on: %v\n%s", err, out)
	}

	out, err = exec.Command(binaryPath, "planning", "attended", "off", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("planning attended off: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Attended mode (planning.attended): disabled") {
		t.Errorf("planning attended off output = %q, want the disabled state line", out)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	if !strings.Contains(string(data), `"repos"`) || !strings.Contains(string(data), "owner/name") {
		t.Errorf("dispatch.repos enrollment not preserved through planning attended off, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"automerge"`) {
		t.Errorf("automerge block not preserved through planning attended off, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"attended": false`) {
		t.Errorf("planning.attended not persisted false, got:\n%s", data)
	}
}

func TestPlanningAttendedOnTwice_ByteIdentical(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	if out, err := exec.Command(binaryPath, "planning", "attended", "on", "--config", configPath, "--dir", t.TempDir()).CombinedOutput(); err != nil {
		t.Fatalf("planning attended on (first): %v\n%s", err, out)
	}
	first, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	if out, err := exec.Command(binaryPath, "planning", "attended", "on", "--config", configPath, "--dir", t.TempDir()).CombinedOutput(); err != nil {
		t.Fatalf("planning attended on (second): %v\n%s", err, out)
	}
	second, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("second `planning attended on` changed file content:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestPlanningAttendedStatus_JSONNonRepoDirOmitsRepoFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	// A plain directory (not a git repo): the fleet flag still resolves and
	// the repo fields stay absent rather than erroring — status must work
	// from anywhere.
	out, err := exec.Command(binaryPath, "planning", "attended", "status", "--config", configPath, "--dir", t.TempDir(), "--json").Output()
	if err != nil {
		t.Fatalf("planning attended status --json (non-repo): %v\n%s", err, out)
	}
	var got planningAttendedStatusJSON
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if got.Attended {
		t.Error("attended = true with no fleet config, want false")
	}
	if got.Config == "" {
		t.Error("config = \"\", want the resolved config path")
	}
	if got.Repo != "" || got.RepoAutonomy != "" || got.Authorized != nil {
		t.Errorf("repo fields = (%q, %q, %v) for a non-repo dir, want absent", got.Repo, got.RepoAutonomy, got.Authorized)
	}
}

func TestPlanningAttendedStatus_JSONReportsRepoAutonomyAndCombinedVerdict(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	repoDir := initGitRemote(t, "git@github.com:owner/name.git")

	if out, err := exec.Command(binaryPath, "planning", "attended", "on", "--config", configPath, "--dir", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("planning attended on: %v\n%s", err, out)
	}
	if out, err := exec.Command(binaryPath, "dispatch", "plan-refined", "on", "--config", configPath, "--dir", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("dispatch plan-refined on: %v\n%s", err, out)
	}

	out, err := exec.Command(binaryPath, "planning", "attended", "status", "--config", configPath, "--dir", repoDir, "--json").Output()
	if err != nil {
		t.Fatalf("planning attended status --json (repo): %v\n%s", err, out)
	}
	var got planningAttendedStatusJSON
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if !got.Attended {
		t.Error("attended = false after `planning attended on`, want true")
	}
	if !got.PlanRefined {
		t.Error("plan_refined = false after `dispatch plan-refined on`, want true")
	}
	if got.Repo != "owner/name" {
		t.Errorf("repo = %q, want owner/name", got.Repo)
	}
	// No origin/main ref has ever been fetched in this fresh repo, so the
	// autonomy probe fails closed as "unreadable" — same fail-closed contract
	// QueryRepoAutonomy gives `dispatch plan-refined status`.
	if got.RepoAutonomy != "unreadable" {
		t.Errorf("repo_autonomy = %q, want unreadable (no origin/main ref fetched)", got.RepoAutonomy)
	}
	// Three-factor verdict (Q1): attended being ON must suppress
	// "authorized: yes" no matter what plan_refined/repo_autonomy say.
	if got.Authorized == nil || *got.Authorized {
		t.Errorf("authorized = %v, want false (attended suppresses pickups here)", got.Authorized)
	}
}

func TestPlanningAttendedStatus_OffPrintsEnableHint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")

	out, err := exec.Command(binaryPath, "planning", "attended", "status", "--config", configPath, "--dir", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("planning attended status: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Attended mode (planning.attended): disabled") {
		t.Errorf("planning attended status output = %q, want the disabled state line", out)
	}
	if !strings.Contains(string(out), "enable with: cenci planning attended on") {
		t.Errorf("planning attended status output = %q, want the enable-with hint when disabled", out)
	}
}

// TestPlanningAttendedStatus_MalformedConfigExits1 covers Q3: a malformed
// planning block (a non-bool attended value) makes the strict CLI/status
// reader error, and `cenci planning attended status` must surface it on
// stderr and exit 1 -- never a silent "off".
func TestPlanningAttendedStatus_MalformedConfigExits1(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"planning": {"attended": "yes"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cmd := exec.Command(binaryPath, "planning", "attended", "status", "--config", configPath, "--dir", t.TempDir())
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("planning attended status on malformed config: err = %v (out %q), want exit 1", err, out)
	}
}

func TestPlanningAttendedUnknownSubcommandExits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "planning", "attended", "enable")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("planning attended enable: err = %v (out %q), want exit 2", err, out)
	}
	if !strings.Contains(string(out), "unknown subcommand") {
		t.Errorf("output = %q, want an unknown-subcommand hint", out)
	}
}

// TestPlanningAttendedExtraPositionalArgsExits2 covers the AC: unexpected
// positional args exit 2 with a one-line stderr hint (rejectExtra).
func TestPlanningAttendedExtraPositionalArgsExits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "planning", "attended", "on", "extra-arg")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("planning attended on extra-arg: err = %v (out %q), want exit 2", err, out)
	}
	if !strings.Contains(string(out), "unexpected argument") {
		t.Errorf("output = %q, want an unexpected-argument hint", out)
	}
}

// TestPlanningNoSubcommandExits2 and TestPlanningAttendedNoVerbExits2 cover
// the plan's Assumption: `cenci planning` with no subcommand, and `cenci
// planning attended` with no verb, both exit 2 with a one-line hint -- the
// same two-level shape `cenci dispatch plan-refined` uses.
func TestPlanningNoSubcommandExits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "planning")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("planning (no subcommand): err = %v (out %q), want exit 2", err, out)
	}
}

func TestPlanningAttendedNoVerbExits2(t *testing.T) {
	cmd := exec.Command(binaryPath, "planning", "attended")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("planning attended (no verb): err = %v (out %q), want exit 2", err, out)
	}
}
