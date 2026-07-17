package dispatch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

	tickets, err := collectRepoTickets("o/r")
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

// TestGitCommitsBehindPathAware exercises gitCommitsBehind against a real
// temporary repo: with paths it counts only commits touching those paths;
// without paths it keeps the whole-repo count.
func TestGitCommitsBehindPathAware(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	commitFile(t, dir, "base.txt", "base")
	sha := gitTest(t, dir, "rev-parse", "HEAD")
	commitFile(t, dir, "watch/main.go", "watch change")
	commitFile(t, dir, "flow/skill.md", "flow change one")
	commitFile(t, dir, "flow/other.md", "flow change two")

	if got := gitCommitsBehind(dir, sha, nil); got != 3 {
		t.Errorf("whole-repo count = %d, want 3", got)
	}
	if got := gitCommitsBehind(dir, sha, []string{"watch"}); got != 1 {
		t.Errorf("watch-scoped count = %d, want 1", got)
	}
	if got := gitCommitsBehind(dir, sha, []string{"flow"}); got != 2 {
		t.Errorf("flow-scoped count = %d, want 2", got)
	}
	if got := gitCommitsBehind(dir, sha, []string{"untouched"}); got != 0 {
		t.Errorf("untouched-path count = %d, want 0", got)
	}
	if got := gitCommitsBehind(dir, sha, []string{"watch", "flow"}); got != 3 {
		t.Errorf("multi-path count = %d, want 3", got)
	}
}
