package worktreeclean

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
)

// Fixtures for the classify/remove suites (#1265). They sit on top of the
// real-git fixtures in fixtures_test.go, which this file deliberately does not
// modify: nothing here mocks git, and the one git shim only logs argv before it
// execs the real binary.

// fixedNow is the pinned Options.Now of every classify/remove test. It lies
// after any mtime a fixture writes with the real clock, so an un-aged file
// reads as ancient (never as recent activity), and it stays below the year
// 2099 so git still accepts commit dates built from it.
var fixedNow = time.Date(2050, time.June, 15, 12, 0, 0, 0, time.UTC)

const (
	// testStaleAfter is the Options.StaleAfter every test uses unless it probes
	// the boundary.
	testStaleAfter = 7 * 24 * time.Hour
	// recentActivityGrace is the fixed grace a finished worktree gets (#1265
	// Decisions): activity newer than this keeps it.
	recentActivityGrace = time.Hour
)

// passGuard is an injected guard that never blocks.
func passGuard(context.Context, Target) (bool, error) { return false, nil }

// testOptions is the Options a fully valid caller passes: pinned clock, pass
// through guards and the given outcome map.
func testOptions(outcomes map[int]ticketstate.OutcomeResult) Options {
	return Options{
		StaleAfter: testStaleAfter,
		Now:        func() time.Time { return fixedNow },
		Outcomes:   outcomes,
		InUse:      passGuard,
		Babysit:    passGuard,
	}
}

// outcome builds the per-ticket result the ticket-state reader would return.
func outcome(o ticketstate.Outcome, heads ...string) ticketstate.OutcomeResult {
	return ticketstate.OutcomeResult{Outcome: o, Heads: heads}
}

// outcomesFor is the outcome map for a single ticket.
func outcomesFor(ticket int, r ticketstate.OutcomeResult) map[int]ticketstate.OutcomeResult {
	return map[int]ticketstate.OutcomeResult{ticket: r}
}

// ageWorktree sets the mtime of every file under the worktree and its admin
// dir to fixedNow-age. Call it after the last write of a scenario.
func ageWorktree(t *testing.T, w *fixtureWT, age time.Duration) {
	t.Helper()
	ageAll(t, fixedNow.Add(-age), w.Path, w.AdminDir)
}

// newOriginRepo is a repo whose origin holds main, so a worktree sitting at
// main's HEAD is reachable from refs/remotes/origin/main.
func newOriginRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	r := newRepo(t, "repo")
	newBareOrigin(t, r)
	return r
}

// atMainWorktree is a worktree at main's HEAD with no commits of its own: clean,
// no upstream, and reachable from the remote when the repo has an origin.
func atMainWorktree(t *testing.T, r *fixtureRepo, name, branch string) *fixtureWT {
	t.Helper()
	return addWorktree(t, r, name, branch)
}

// publishedWorktree is a worktree with one commit of its own, published to origin
// with the branch as upstream (Ahead == 0).
func publishedWorktree(t *testing.T, r *fixtureRepo, name, branch string) *fixtureWT {
	t.Helper()
	w := addWorktree(t, r, name, branch)
	w.commit(t, "work.txt", "work\n", oldCommitDate)
	w.push(t)
	return w
}

// unpushedWorktree is a worktree with one commit of its own that was never pushed.
// The content names the branch so the commit is unique: with identical content,
// date and message, a published sibling's commit has the same hash and would make
// this one reachable from the remote.
func unpushedWorktree(t *testing.T, r *fixtureRepo, name, branch string) *fixtureWT {
	t.Helper()
	w := addWorktree(t, r, name, branch)
	w.commit(t, "work.txt", "unpushed work on "+branch+"\n", oldCommitDate)
	return w
}

// classifyOne classifies w starting from the Entry production Enumerate yields.
func classifyOne(t *testing.T, r *fixtureRepo, w *fixtureWT, o Options) Classification {
	t.Helper()
	return Classify(context.Background(), r.Repo(), entryFor(t, r, w), o)
}

// wantVerdict fails unless c has the verdict and keep reason (empty for a
// removable verdict).
func wantVerdict(t *testing.T, c Classification, v Verdict, reason KeepReason) {
	t.Helper()
	if c.Verdict != v || c.Reason != reason {
		t.Fatalf("verdict = %q, reason = %q (err %v); want verdict %q, reason %q", c.Verdict, c.Reason, c.Err, v, reason)
	}
}

// reclaimAll runs the production pipeline a caller runs: Enumerate, ClassifyAll,
// Reclaim.
func reclaimAll(t *testing.T, r *fixtureRepo, o Options) ([]Classification, []Removal) {
	t.Helper()
	ctx := context.Background()
	repo, entries, err := Enumerate(ctx, r.Root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	cs := ClassifyAll(ctx, repo, entries, o)
	return cs, Reclaim(ctx, repo, cs, o)
}

func classificationFor(t *testing.T, cs []Classification, w *fixtureWT) Classification {
	t.Helper()
	for _, c := range cs {
		if c.Entry.AdminName == w.AdminName {
			return c
		}
	}
	t.Fatalf("no classification for admin entry %q in %+v", w.AdminName, cs)
	return Classification{}
}

func removalFor(t *testing.T, rs []Removal, w *fixtureWT) Removal {
	t.Helper()
	for _, rem := range rs {
		if rem.Entry.AdminName == w.AdminName {
			return rem
		}
	}
	t.Fatalf("no removal for admin entry %q in %+v", w.AdminName, rs)
	return Removal{}
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func branchExists(r *fixtureRepo, branch string) bool {
	_, err := tryGit(r.Root, oldCommitDate, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// writePipelineState writes <root>/.cenci/pipeline/<id>.json the way the
// pipeline store lays it out (schemaVersion, id, stage, updatedAt).
func writePipelineState(t *testing.T, r *fixtureRepo, id, stage string, updatedAt time.Time) string {
	t.Helper()
	body := fmt.Sprintf(`{"schemaVersion":1,"id":%q,"stage":%q,"updatedAt":%q}`,
		id, stage, updatedAt.UTC().Format(time.RFC3339Nano))
	return writePipelineRaw(t, r.Root, id, body)
}

// writePipelineRaw writes an arbitrary state file body for a ticket id.
func writePipelineRaw(t *testing.T, root, id, body string) string {
	t.Helper()
	path := filepath.Join(root, ".cenci", "pipeline", id+".json")
	writeFile(t, path, body)
	return path
}

// squashMerge squash-merges w's pushed branch into main on the remote, deletes
// the remote branch and prunes its tracking ref, the way a GitHub squash merge
// with "delete branch" leaves a clone. It returns the PR head SHA (the branch
// tip at merge time). The fixture proves its own premise: afterwards that SHA
// is reachable from no remote-tracking ref, so only the PR-head rule can make
// the worktree count as pushed.
func squashMerge(t *testing.T, r *fixtureRepo, w *fixtureWT) string {
	t.Helper()
	head := w.head(t)
	r.git(t, "merge", "--squash", w.Branch)
	r.git(t, "commit", "-q", "-m", "squash "+w.Branch)
	r.git(t, "push", "-q", "origin", "main")
	r.git(t, "push", "-q", "origin", "--delete", w.Branch)
	r.git(t, "remote", "prune", "origin")
	if out := r.git(t, "branch", "-r", "--contains", head); out != "" {
		t.Fatalf("fixture premise broken: PR head %s is still reachable from %q", head, out)
	}
	return head
}

// mergeIntoRemoteMain fast-forwards main to w's branch and pushes it, so the
// branch tip becomes reachable from refs/remotes/origin/main.
func mergeIntoRemoteMain(t *testing.T, r *fixtureRepo, w *fixtureWT) {
	t.Helper()
	r.git(t, "merge", "--ff-only", w.Branch)
	r.git(t, "push", "-q", "origin", "main")
}

// deleteRemoteBranch removes the branch from origin and prunes its tracking
// ref, leaving the local upstream configuration pointing at a missing ref.
func deleteRemoteBranch(t *testing.T, r *fixtureRepo, branch string) {
	t.Helper()
	r.git(t, "push", "-q", "origin", "--delete", branch)
	r.git(t, "remote", "prune", "origin")
}

// installGitLogger puts a `git` shim first on PATH that appends each call's
// argv to a log and then execs the real git, resolved before the shim goes on
// PATH so it cannot find itself. The returned func reads the calls logged so
// far (one argv per call). Install it after all fixture setup so only the code
// under test is logged.
func installGitLogger(t *testing.T) func() [][]string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "git-calls.log")
	script := fmt.Sprintf("{ for a in \"$@\"; do printf '%%s\\037' \"$a\"; done; printf '\\n'; } >> %s\nexec %s \"$@\"",
		exectest.ShellQuote(logPath), exectest.ShellQuote(realGit))
	installShim(t, "git", script)
	return func() [][]string {
		t.Helper()
		data, err := os.ReadFile(logPath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
		}
		return calls
	}
}

// outsideSentinel creates <tmp>/<name>/sentinel.txt plus a nested file, the
// stand-in for data outside any worktree that a symlink-following delete would
// destroy. It returns the directory.
func outsideSentinel(t *testing.T, r *fixtureRepo, name string) string {
	t.Helper()
	dir := filepath.Join(r.Tmp, name)
	writeFile(t, filepath.Join(dir, "sentinel.txt"), "must survive\n")
	writeFile(t, filepath.Join(dir, "sub", "deep.txt"), "must survive too\n")
	return dir
}

// underAny reports whether path is one of the roots or lies below one.
func underAny(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// unexpectedDiffs filters diffSnapshots output down to the changes a removal is
// NOT allowed to cause: a "removed" path must lie under one of removed, a
// "changed" path must be one of parentDirs (the directory whose entry list
// changed, so its mtime did), and tolerated (optional) accepts anything else the
// caller expects, e.g. the refs a branch deletion rewrites.
func unexpectedDiffs(diffs, removed, parentDirs []string, tolerated func(path string) bool) []string {
	var bad []string
	for _, d := range diffs {
		kind, rest, _ := strings.Cut(d, ": ")
		path, _, _ := strings.Cut(rest, "\n")
		switch {
		case kind == "removed" && underAny(path, removed):
		case kind == "changed" && slices.Contains(parentDirs, path):
		case tolerated != nil && tolerated(path):
		default:
			bad = append(bad, d)
		}
	}
	return bad
}

// installFailingGit puts a `git` shim first on PATH that exits with code (and a
// fatal message on stderr) for any call with an argument matching the shell
// glob pattern, and execs the real git otherwise. Install it after all fixture
// setup.
func installFailingGit(t *testing.T, pattern string, code int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("for a in \"$@\"; do\n  case \"$a\" in\n    %s) echo 'fatal: simulated git failure' >&2; exit %d ;;\n  esac\ndone\nexec %s \"$@\"",
		pattern, code, exectest.ShellQuote(realGit))
	installShim(t, "git", script)
}
