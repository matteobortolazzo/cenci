package dispatch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/planfile"
)

func installFakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestCurrentGitHubLogin(t *testing.T) {
	t.Run("returns trimmed active login", func(t *testing.T) {
		installFakeGH(t, "printf 'OctoCat\\n'\n")
		got, err := currentGitHubLogin()
		if err != nil {
			t.Fatalf("currentGitHubLogin returned unexpected error: %v", err)
		}
		if got != "OctoCat" {
			t.Errorf("login = %q, want OctoCat", got)
		}
	})

	t.Run("fails closed on gh error", func(t *testing.T) {
		installFakeGH(t, "exit 1\n")
		if _, err := currentGitHubLogin(); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("rejects empty login", func(t *testing.T) {
		installFakeGH(t, "exit 0\n")
		if _, err := currentGitHubLogin(); err == nil || !strings.Contains(err.Error(), "empty login") {
			t.Fatalf("error = %v, want empty-login error", err)
		}
	})
}

func TestCollectRepoTicketsIncludesAssignees(t *testing.T) {
	installFakeGH(t, `
case "$1 $2" in
  "issue list") printf '[{"number":42,"title":"Fix thing","labels":[{"name":"Planned"}],"assignees":[{"login":"octocat"}]}]' ;;
  "pr list") printf '[]' ;;
  *) exit 1 ;;
esac
`)

	tickets, err := collectRepoTickets(RepoConfig{Repo: "o/r"})
	if err != nil {
		t.Fatalf("collectRepoTickets returned unexpected error: %v", err)
	}
	if len(tickets) != 1 || !equalStrings(tickets[0].Assignees, []string{"octocat"}) {
		t.Fatalf("tickets = %+v, want one ticket assigned to octocat", tickets)
	}
}

// TestCollectTicketsAttemptsEveryRepoOnFailure locks in CollectTickets' best-
// effort contract (ticket #122): a failure on one repo must not stop
// collection from being attempted on the rest, and every per-repo failure
// must be joined into the returned error so the caller's log names every
// failing repo, not just the first. collectRepoTickets shells out to gh with
// no injection seam (by design, out of scope here), so this drives it with
// repos gh cannot resolve (nonexistent owner/repo, and no gh auth in the test
// environment either way) to get a deterministic per-repo error from each.
func TestCollectTicketsAttemptsEveryRepoOnFailure(t *testing.T) {
	repos := []RepoConfig{
		{Repo: "o/nonexistent-a", Dir: t.TempDir()},
		{Repo: "o/nonexistent-b", Dir: t.TempDir()},
		{Repo: "o/nonexistent-c", Dir: t.TempDir()},
	}

	tickets, err := CollectTickets(repos)

	if err == nil {
		t.Fatal("expected a joined error from three failing repos, got nil")
	}
	if len(tickets) != 0 {
		t.Errorf("expected no tickets from all-failing repos, got %+v", tickets)
	}

	msg := err.Error()
	for _, rc := range repos {
		if !strings.Contains(msg, rc.Repo) {
			t.Errorf("joined error %q must name every failing repo, missing %q", msg, rc.Repo)
		}
	}
}

// TestCollectTicketsJoinsMultipleErrors is a basic sanity check that the
// errors.Join composition used by CollectTickets behaves as errors.Join
// contracts: unwrapping the joined error yields every individual per-repo
// error, and errors.Is finds each of them.
func TestCollectTicketsJoinsMultipleErrors(t *testing.T) {
	repos := []RepoConfig{
		{Repo: "o/nonexistent-x", Dir: t.TempDir()},
		{Repo: "o/nonexistent-y", Dir: t.TempDir()},
	}

	_, err := CollectTickets(repos)
	if err == nil {
		t.Fatal("expected a non-nil joined error")
	}

	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("expected an errors.Join-composed error implementing Unwrap() []error, got %T", err)
	}
	unwrapped := joined.Unwrap()
	if len(unwrapped) != len(repos) {
		t.Fatalf("expected %d unwrapped errors, got %d: %v", len(repos), len(unwrapped), unwrapped)
	}
	for i, rc := range repos {
		if !strings.Contains(unwrapped[i].Error(), rc.Repo) {
			t.Errorf("unwrapped error[%d] = %q, want it to name %q", i, unwrapped[i].Error(), rc.Repo)
		}
		if !errors.Is(err, unwrapped[i]) {
			t.Errorf("errors.Is must find the per-repo error for %q in the joined error", rc.Repo)
		}
	}
}

// writePlan writes a plan file into <dir>/.plans/<name> for ReadPlans tests.
func writePlan(t *testing.T, dir, name, content string) {
	t.Helper()
	plansDir := filepath.Join(dir, ".plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plansDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadPlansStalenessPaths locks in the stalenessPaths contract: a plan
// listing paths has them parsed (comma-split, trimmed, empties dropped) and
// passed to the commitsBehind seam; a plan without the key passes no paths,
// preserving whole-repo staleness counting for existing plan files.
func TestReadPlansStalenessPaths(t *testing.T) {
	dir := t.TempDir()
	writePlan(t, dir, "42-scoped.md", `---
ticketId: 42
status: planned
planCommitSha: aaa111
stalenessPaths: watch, plugin/hooks,
---
body
`)
	writePlan(t, dir, "43-whole-repo.md", `---
ticketId: 43
status: planned
planCommitSha: bbb222
---
body
`)

	pathsBySha := make(map[string][]string)
	plans, err := ReadPlans("o/r", dir, func(sha string, paths []string) int {
		pathsBySha[sha] = paths
		return 7
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d: %+v", len(plans), plans)
	}

	scoped := plans[0]
	if want := []string{"watch", "plugin/hooks"}; !equalStrings(scoped.StalenessPaths, want) {
		t.Errorf("scoped plan StalenessPaths = %v, want %v", scoped.StalenessPaths, want)
	}
	if scoped.CommitsBehind != 7 {
		t.Errorf("scoped plan CommitsBehind = %d, want 7 (from the seam)", scoped.CommitsBehind)
	}
	if got := pathsBySha["aaa111"]; !equalStrings(got, []string{"watch", "plugin/hooks"}) {
		t.Errorf("commitsBehind received paths %v for aaa111, want [watch plugin/hooks]", got)
	}

	whole := plans[1]
	if len(whole.StalenessPaths) != 0 {
		t.Errorf("plan without stalenessPaths must have none, got %v", whole.StalenessPaths)
	}
	if got, ok := pathsBySha["bbb222"]; !ok || len(got) != 0 {
		t.Errorf("commitsBehind for bbb222 must be called with no paths, got %v (called=%v)", got, ok)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// gitTest runs a git command in dir with a pinned identity, failing the test on
// error and returning trimmed stdout.
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitFile makes one commit touching a single file under dir.
func commitFile(t *testing.T, dir, rel, msg string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", rel)
	gitTest(t, dir, "commit", "-m", msg)
}

// TestGitCommitsBehindPathAware exercises planfile.CommitsBehind (via
// ReadPlans' default commitsBehind wiring) against a real temporary repo:
// with paths it counts only commits touching those paths; without paths it
// keeps the whole-repo count. The path-scoping behavior itself is unit
// tested at its new home (internal/planfile/frontmatter_test.go); this test
// stays here as dispatch's own regression guard that ReadPlans' default
// wiring still calls through to it correctly.
func TestGitCommitsBehindPathAware(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	commitFile(t, dir, "base.txt", "base")
	sha := gitTest(t, dir, "rev-parse", "HEAD")
	commitFile(t, dir, "watch/main.go", "watch change")
	commitFile(t, dir, "flow/skill.md", "flow change one")
	commitFile(t, dir, "flow/other.md", "flow change two")

	if got, err := planfile.CommitsBehind(dir, sha, nil); err != nil || got != 3 {
		t.Errorf("whole-repo count = %d, err = %v, want 3, nil", got, err)
	}
	if got, err := planfile.CommitsBehind(dir, sha, []string{"watch"}); err != nil || got != 1 {
		t.Errorf("watch-scoped count = %d, err = %v, want 1, nil", got, err)
	}
	if got, err := planfile.CommitsBehind(dir, sha, []string{"flow"}); err != nil || got != 2 {
		t.Errorf("flow-scoped count = %d, err = %v, want 2, nil", got, err)
	}
	if got, err := planfile.CommitsBehind(dir, sha, []string{"untouched"}); err != nil || got != 0 {
		t.Errorf("untouched-path count = %d, err = %v, want 0, nil", got, err)
	}
	if got, err := planfile.CommitsBehind(dir, sha, []string{"watch", "flow"}); err != nil || got != 3 {
		t.Errorf("multi-path count = %d, err = %v, want 3, nil", got, err)
	}
}

// -- #732: probeStage (the collector's persisted-stage classifier) ---------

// writeStateFile writes a raw pipeline state file at
// <dir>/.cenci/pipeline/<number>.json, driving probeStage directly against a
// real on-disk file rather than through gh.
func writeStateFile(t *testing.T, dir string, number int, content string) {
	t.Helper()
	pipelineDir := filepath.Join(dir, ".cenci", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pipelineDir, strconv.Itoa(number)+".json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProbeStage_PresentKnownStage covers the AC's "readable, known stage"
// case: the persisted stage is recorded verbatim and classified Present.
func TestProbeStage_PresentKnownStage(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, 42, `{"schemaVersion":2,"id":"42","stage":"plan_approved"}`)

	stage, probe := probeStage(dir, 42)
	if stage != "plan_approved" || probe != StageProbePresent {
		t.Errorf(`probeStage = (%q, %q), want ("plan_approved", StageProbePresent)`, stage, probe)
	}
}

// TestProbeStage_FileAbsent_MapsToAbsent covers the AC's "no state file"
// case: GetArtifacts returns StageNew with a nil error, and that must map to
// StageProbeAbsent -- the deliberate permissive exception, since
// .cenci/pipeline/ is gitignored and "no pipeline run here" is the normal
// case that must not block dispatch.
func TestProbeStage_FileAbsent_MapsToAbsent(t *testing.T) {
	dir := t.TempDir()

	stage, probe := probeStage(dir, 42)
	if stage != "new" || probe != StageProbeAbsent {
		t.Errorf(`probeStage(missing file) = (%q, %q), want ("new", StageProbeAbsent)`, stage, probe)
	}
}

// TestProbeStage_LiteralNewStage_MapsToAbsent covers the AC's "a persisted
// stage that is literally new also maps to StageProbeAbsent" case: a state
// file that genuinely exists but records stage "new" must be
// indistinguishable from no state file at all.
func TestProbeStage_LiteralNewStage_MapsToAbsent(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, 42, `{"schemaVersion":2,"id":"42","stage":"new"}`)

	stage, probe := probeStage(dir, 42)
	if stage != "new" || probe != StageProbeAbsent {
		t.Errorf(`probeStage(stage="new") = (%q, %q), want ("new", StageProbeAbsent)`, stage, probe)
	}
}

// TestProbeStage_CorruptJSON_MapsToError covers the "unreadable/undecodable"
// half of the AC's default-deny case: a state file that fails to decode
// must classify StageProbeError, not be silently treated as absent.
func TestProbeStage_CorruptJSON_MapsToError(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, 42, `{not valid json`)

	stage, probe := probeStage(dir, 42)
	if stage != "" || probe != StageProbeError {
		t.Errorf(`probeStage(corrupt json) = (%q, %q), want ("", StageProbeError)`, stage, probe)
	}
}

// TestProbeStage_UnknownStageString_MapsToErrorWithStageRecorded covers the
// AC's "a stage value not registered in pipeline's stage order" case: the
// offending value must still be recorded verbatim (for logging) even though
// the probe classifies it as an error.
func TestProbeStage_UnknownStageString_MapsToErrorWithStageRecorded(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, 42, `{"schemaVersion":2,"id":"42","stage":"bogus"}`)

	stage, probe := probeStage(dir, 42)
	if stage != "bogus" || probe != StageProbeError {
		t.Errorf(`probeStage(unknown stage "bogus") = (%q, %q), want ("bogus", StageProbeError)`, stage, probe)
	}
}

// TestProbeStage_EmptyStageField_MapsToError covers a state file with the
// stage field absent/empty: it decodes successfully but is not a known
// stage, so it must classify StageProbeError, not Absent (only a genuinely
// missing file or an explicit stage:"new" get the permissive Absent
// classification).
func TestProbeStage_EmptyStageField_MapsToError(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, 42, `{"schemaVersion":2,"id":"42"}`)

	stage, probe := probeStage(dir, 42)
	if stage != "" || probe != StageProbeError {
		t.Errorf(`probeStage(empty/absent stage field) = (%q, %q), want ("", StageProbeError)`, stage, probe)
	}
}

// TestProbeStage_EmptyDir_NeverProbes_NoCwdRelativeRead covers the AC's
// "when rc.Dir is empty, the collector does not probe" requirement: dir=="""
// must never let pipeline.GetArtifacts fall back to resolving a repo root
// from the process cwd, which would read an unrelated repo's state. Proven
// by chdir'ing into a real repo dir that DOES have a finalized state at
// ticket 42, and asserting probeStage("", 42) still reports Absent rather
// than leaking that cwd-relative state in.
func TestProbeStage_EmptyDir_NeverProbes_NoCwdRelativeRead(t *testing.T) {
	repoDir := t.TempDir()
	writeStateFile(t, repoDir, 42, `{"schemaVersion":2,"id":"42","stage":"finalized"}`)

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })

	stage, probe := probeStage("", 42)
	if stage != "" || probe != StageProbeAbsent {
		t.Errorf(`probeStage("", 42) with cwd containing a finalized state = (%q, %q), want ("", StageProbeAbsent) -- dir="" must never probe`, stage, probe)
	}
}
