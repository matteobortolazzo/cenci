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

// planRefinedStatusJSON mirrors the pinned --json output shape. Attended is
// #1086's additive-only field: the fleet-scoped planning.attended value,
// factored into Authorized's three-factor verdict (Q1) alongside Enabled
// (dispatch.planRefined) and RepoAutonomy.
type planRefinedStatusJSON struct {
	Enabled      bool   `json:"enabled"`
	Config       string `json:"config"`
	Repo         string `json:"repo,omitempty"`
	RepoAutonomy string `json:"repo_autonomy,omitempty"`
	Authorized   *bool  `json:"authorized,omitempty"`
	Attended     bool   `json:"attended"`
}

// initRepoWithFetchedAutonomy builds a repo directory whose origin remote URL
// parses to owner/name for DetectRepoIdentity (git@github.com:owner/name.git,
// never actually fetched from) while separately, directly populating
// refs/remotes/origin/main -- the exact ref QueryRepoAutonomy reads -- from a
// real local bare repo carrying configJSON as .cenci/config.json. This
// decouples "identity" (parsed from the origin remote's URL) from "the
// remote-confirmed autonomy ref" (populated by fetching real content from an
// arbitrary source into that ref name), letting a black-box CLI test drive a
// real (non-"unreadable") RepoAutonomy verdict without a network fetch.
func initRepoWithFetchedAutonomy(t *testing.T, configJSON string) string {
	t.Helper()

	bareOrigin := filepath.Join(t.TempDir(), "bare-origin")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", bareOrigin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare %s: %v\n%s", bareOrigin, err, out)
	}

	work := t.TempDir()
	runGit(t, work, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(work, ".cenci"), 0o755); err != nil {
		t.Fatalf("mkdir .cenci: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, ".cenci", "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatalf("writing .cenci/config.json: %v", err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", "config")
	runGit(t, work, "push", bareOrigin, "main")

	repoDir := initGitRemote(t, "git@github.com:owner/name.git")
	runGit(t, repoDir, "fetch", bareOrigin, "main:refs/remotes/origin/main")
	return repoDir
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

// TestDispatchPlanRefinedStatus_AttendedSuppressesAuthorized covers the AC
// (#1086, Q1): with dispatch.planRefined on and a real, remote-confirmed
// "lean" repo autonomy verdict -- the sole combination that would otherwise
// print "authorized: yes" -- turning planning.attended on must suppress the
// combined verdict to false. Never a two-factor verdict that could disagree
// with `cenci planning attended status`.
func TestDispatchPlanRefinedStatus_AttendedSuppressesAuthorized(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	repoDir := initRepoWithFetchedAutonomy(t, `{"planning":{"autonomy":"lean"}}`)

	if out, err := exec.Command(binaryPath, "dispatch", "plan-refined", "on", "--config", configPath, "--dir", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("plan-refined on: %v\n%s", err, out)
	}

	// Sanity check: before attended is ever touched, this fleet+repo
	// combination is the sole authorizing case.
	out, err := exec.Command(binaryPath, "dispatch", "plan-refined", "status", "--config", configPath, "--dir", repoDir, "--json").Output()
	if err != nil {
		t.Fatalf("plan-refined status --json (pre-attended): %v\n%s", err, out)
	}
	var pre planRefinedStatusJSON
	if err := json.Unmarshal(out, &pre); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if pre.RepoAutonomy != "lean" {
		t.Fatalf("test setup sanity check: repo_autonomy = %q, want lean", pre.RepoAutonomy)
	}
	if pre.Authorized == nil || !*pre.Authorized {
		t.Fatalf("test setup sanity check: authorized = %v before attended, want true", pre.Authorized)
	}

	if out, err := exec.Command(binaryPath, "planning", "attended", "on", "--config", configPath, "--dir", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("planning attended on: %v\n%s", err, out)
	}

	out, err = exec.Command(binaryPath, "dispatch", "plan-refined", "status", "--config", configPath, "--dir", repoDir, "--json").Output()
	if err != nil {
		t.Fatalf("plan-refined status --json (post-attended): %v\n%s", err, out)
	}
	var post planRefinedStatusJSON
	if err := json.Unmarshal(out, &post); err != nil {
		t.Fatalf("decoding status JSON: %v\n%s", err, out)
	}
	if !post.Attended {
		t.Error("attended = false after `planning attended on`, want true")
	}
	if post.RepoAutonomy != "lean" {
		t.Errorf("repo_autonomy = %q, want lean (unnarrowed -- QueryRepoAutonomy must stay truthful)", post.RepoAutonomy)
	}
	if post.Authorized == nil || *post.Authorized {
		t.Errorf("authorized = %v, want false now that attended is on (three-factor verdict)", post.Authorized)
	}
}

// TestDispatchPlanRefinedStatus_MalformedPlanningBlockExits1 covers Q3: a
// malformed fleet planning block (non-bool attended) must make `cenci
// dispatch plan-refined status` fail loud -- exit 1 with the error on
// stderr -- rather than silently rendering as if attended were off.
func TestDispatchPlanRefinedStatus_MalformedPlanningBlockExits1(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"planning": {"attended": "yes"}}`), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cmd := exec.Command(binaryPath, "dispatch", "plan-refined", "status", "--config", configPath, "--dir", t.TempDir())
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("plan-refined status on malformed planning block: err = %v (out %q), want exit 1", err, out)
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
