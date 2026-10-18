package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tmuxfe "github.com/matteobortolazzo/cenci/watch/v2/internal/frontend/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ticketstate"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux/tmuxtest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/worktreeclean"
)

// The worktree-cleanup pass (#1267) removes the finished and stale worktrees of
// every registered repo, irreversibly. Every case below drives the whole pass over
// real git repos and real worktrees on disk: only the GitHub reader, the tmux pane
// list and the sandbox gate are fakes. Each case asserts on what is on disk and in
// the log, and each negative case carries a control (a worktree in the same pass
// that must be removed, or a persisted registry entry) so that "nothing was
// removed" cannot pass because the pass never ran.

// wtOldCommitDate (2020-01-01T00:00:00Z) dates every fixture commit, so commit
// age never decides a verdict; worktree age is set through file mtimes.
const wtOldCommitDate int64 = 1577836800

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// isolateGitEnv keeps the real-git fixtures independent of the developer's git
// configuration and of any ambient repo-location variable.
func isolateGitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for the real-git fixtures: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.invalid")
	for _, k := range []string{
		"GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE", "GIT_OPTIONAL_LOCKS", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
	} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// wtGit runs `git -C dir args...` with both dates pinned to wtOldCommitDate.
func wtGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	stamp := fmt.Sprintf("@%d +0000", wtOldCommitDate)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, stderr.String())
	}
	return strings.TrimRight(string(out), "\n")
}

func wtWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wtRealPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// wtRepo is a real main checkout whose origin names acme/<name> (or has none).
type wtRepo struct {
	t    *testing.T
	Root string
	Repo ticketstate.Repo
}

// wtTree is a real linked worktree of a wtRepo.
type wtTree struct {
	t        *testing.T
	Repo     *wtRepo
	Name     string
	Branch   string
	Path     string
	AdminDir string
}

func (r *wtRepo) addWorktree(name, branch string) *wtTree {
	r.t.Helper()
	path := filepath.Join(r.Root, ".worktrees", name)
	wtGit(r.t, r.Root, "worktree", "add", "-q", "-b", branch, path)
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		r.t.Fatal(err)
	}
	admin, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		r.t.Fatalf("%s/.git = %q, want a gitdir: pointer", path, data)
	}
	return &wtTree{t: r.t, Repo: r, Name: name, Branch: branch, Path: path, AdminDir: admin}
}

func (w *wtTree) head() string { return wtGit(w.t, w.Path, "rev-parse", "HEAD") }

// commit writes file and commits it (old commit date).
func (w *wtTree) commit(rel, content string) {
	w.t.Helper()
	wtWriteFile(w.t, filepath.Join(w.Path, rel), content)
	wtGit(w.t, w.Path, "add", rel)
	wtGit(w.t, w.Path, "commit", "-q", "-m", "add "+rel)
}

// age sets the mtime of every file under the worktree and its admin dir to
// now-d. Call it after the last write of a scenario.
func (w *wtTree) age(d time.Duration) {
	w.t.Helper()
	when := time.Now().Add(-d)
	for _, root := range []string{w.Path, w.AdminDir} {
		err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.Type()&os.ModeSymlink != 0 {
				return nil
			}
			return os.Chtimes(p, when, when)
		})
		if err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *wtTree) exists() bool      { _, err := os.Lstat(w.Path); return err == nil }
func (w *wtTree) adminExists() bool { _, err := os.Lstat(w.AdminDir); return err == nil }

func (w *wtTree) branchExists() bool {
	return exec.Command("git", "-C", w.Repo.Root, "show-ref", "--verify", "--quiet", "refs/heads/"+w.Branch).Run() == nil
}

// fakeOutcomeReader is the worktree-cleanup pass's GitHub reader double: it
// answers ReadOutcomes from a script and records every call, so tests can assert
// both the verdict-driven removals and "makes no GitHub reads". An unscripted
// number answers Unknown with an error, as the real reader fails open. Safe for
// concurrent use: the loop tests call it from worker goroutines.
type fakeOutcomeReader struct {
	mu      sync.Mutex
	results map[ticketstate.Repo]map[int]ticketstate.OutcomeResult
	calls   []readCall
	// beforeRead, when set, runs in every ReadOutcomes after the call is recorded
	// and outside the lock, for tests that need a blocking reader.
	beforeRead func(ctx context.Context)
}

var _ outcomeReader = (*fakeOutcomeReader)(nil)

func (f *fakeOutcomeReader) ReadOutcomes(ctx context.Context, repo ticketstate.Repo, nums []int) map[int]ticketstate.OutcomeResult {
	f.mu.Lock()
	f.calls = append(f.calls, readCall{Repo: repo, Nums: slices.Clone(nums)})
	f.mu.Unlock()
	if f.beforeRead != nil {
		f.beforeRead(ctx)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[int]ticketstate.OutcomeResult, len(nums))
	for _, n := range nums {
		res, ok := f.results[repo][n]
		if !ok {
			res = ticketstate.OutcomeResult{Err: fmt.Errorf("fakeOutcomeReader: no scripted outcome for %s/%s#%d", repo.Owner, repo.Name, n)}
		}
		out[n] = res
	}
	return out
}

func (f *fakeOutcomeReader) script(repo ticketstate.Repo, n int, res ticketstate.OutcomeResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = make(map[ticketstate.Repo]map[int]ticketstate.OutcomeResult)
	}
	if f.results[repo] == nil {
		f.results[repo] = make(map[int]ticketstate.OutcomeResult)
	}
	f.results[repo][n] = res
}

// merged scripts ticket n of r as a merged PR whose head is w's HEAD.
func (f *fakeOutcomeReader) merged(r *wtRepo, n int, w *wtTree) {
	f.script(r.Repo, n, ticketstate.OutcomeResult{Outcome: ticketstate.OutcomePRMerged, Heads: []string{w.head()}})
}

func (f *fakeOutcomeReader) open(r *wtRepo, n int) {
	f.script(r.Repo, n, ticketstate.OutcomeResult{Outcome: ticketstate.OutcomeOpen})
}

func (f *fakeOutcomeReader) recorded() []readCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeOutcomeReader) readsFor(repo ticketstate.Repo) int {
	n := 0
	for _, c := range f.recorded() {
		if c.Repo == repo {
			n++
		}
	}
	return n
}

// paneLister is the worktree pass's tmux pane-list double: it counts reads
// (atomically, for the loop tests) and answers a settable list and error.
type paneLister struct {
	mu    sync.Mutex
	panes []tmux.LivePane
	err   error
	calls atomic.Int64
}

func (l *paneLister) list() ([]tmux.LivePane, error) {
	l.calls.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.panes), l.err
}

func (l *paneLister) set(panes []tmux.LivePane, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.panes, l.err = panes, err
}

// wtFixture is a test daemon wired to the real resolver, the real git-backed
// classification and removal, and the doubles above, over XDG directories private
// to the test.
type wtFixture struct {
	t        *testing.T
	d        *Daemon
	mc       *tmuxtest.MockClient
	tracker  *wcFixture // tracks agent windows the way the window-cleanup suite does
	reader   *fakeOutcomeReader
	panes    *paneLister
	base     string // holds every repo of the fixture, so registry order is controllable
	stateDir string
	cfgDir   string
}

// newWTFixture is a synchronous test daemon (newTestDaemon) wired for the pass.
func newWTFixture(t *testing.T) *wtFixture {
	t.Helper()
	mc := &tmuxtest.MockClient{}
	return newWTFixtureFor(t, newTestDaemon(mc), mc)
}

// newWTFixtureFor wires an already built daemon (for the loop tests, one built
// with newDaemon and the production goroutine runner).
func newWTFixtureFor(t *testing.T, d *Daemon, mc *tmuxtest.MockClient) *wtFixture {
	t.Helper()
	isolateGitEnv(t)
	// The ambient environment must not decide anything: CENCI_SANDBOX=1 is set in
	// the dev container, and the XDG directories are where the registry and the
	// user config live (watch/docs/test-isolation.md).
	t.Setenv("CENCI_SANDBOX", "")
	fx := &wtFixture{
		t: t, d: d, mc: mc,
		tracker:  &wcFixture{t: t, d: d, mc: mc},
		reader:   &fakeOutcomeReader{},
		panes:    &paneLister{},
		base:     wtRealPath(t, t.TempDir()),
		stateDir: wtRealPath(t, t.TempDir()),
		cfgDir:   wtRealPath(t, t.TempDir()),
	}
	t.Setenv("XDG_STATE_HOME", fx.stateDir)
	t.Setenv("XDG_CONFIG_HOME", fx.cfgDir)
	fx.wire(d, mc)
	return fx
}

// wire points d's worktree-cleanup seams at the fixture's doubles and private
// directories.
func (fx *wtFixture) wire(d *Daemon, mc *tmuxtest.MockClient) {
	fx.d, fx.mc = d, mc
	fx.tracker = &wcFixture{t: fx.t, d: d, mc: mc}
	d.closeGuard = nil
	d.worktreeReader = fx.reader
	d.newWorktreeResolver = func() repoResolver { return &ticketstate.Resolver{} }
	d.worktreeListPanes = fx.panes.list
	d.worktreeInSandbox = func() bool { return false }
	d.worktreeRegistryPath = filepath.Join(fx.stateDir, "cenci", "worktree-repos.json")
	d.worktreeUserConfigPath = filepath.Join(fx.cfgDir, "cenci", "config.json")
}

// restart is a new daemon over the same state directories, as after a restart:
// no sessions, no windows, only what was persisted.
func (fx *wtFixture) restart() *wtFixture {
	fx.t.Helper()
	mc := &tmuxtest.MockClient{}
	next := *fx
	next.wire(newTestDaemon(mc), mc)
	return &next
}

func (fx *wtFixture) addRepo(name, configJSON string) *wtRepo {
	fx.t.Helper()
	return fx.addRepoWithOrigin(name, configJSON, "https://github.com/acme/"+name+".git")
}

// addRepoWithOrigin creates a main checkout at <base>/<name> with one commit and a
// remote-tracking ref at HEAD (so a worktree at HEAD counts as pushed). An empty
// origin leaves the repo without an origin remote.
func (fx *wtFixture) addRepoWithOrigin(name, configJSON, origin string) *wtRepo {
	fx.t.Helper()
	root := filepath.Join(fx.base, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		fx.t.Fatal(err)
	}
	wtGit(fx.t, root, "init", "-q", "-b", "main")
	wtWriteFile(fx.t, filepath.Join(root, "tracked.txt"), "tracked\n")
	wtWriteFile(fx.t, filepath.Join(root, ".gitignore"), ".worktrees/\n.cenci/\n")
	wtGit(fx.t, root, "add", "-A")
	wtGit(fx.t, root, "commit", "-q", "-m", "init")
	wtGit(fx.t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	if origin != "" {
		wtGit(fx.t, root, "remote", "add", "origin", origin)
	}
	if configJSON != "" {
		wtWriteFile(fx.t, filepath.Join(root, ".cenci", "config.json"), configJSON)
	}
	return &wtRepo{t: fx.t, Root: root, Repo: ticketstate.Repo{Owner: "acme", Name: name}}
}

// mergedWorktree is a clean worktree for ticket n, idle for 2h (beyond the 1h
// grace of a finished worktree, far below the 7d stale default), whose ticket the
// reader reports as a merged PR with the worktree's own HEAD.
func (fx *wtFixture) mergedWorktree(r *wtRepo, ticket int) *wtTree {
	fx.t.Helper()
	w := r.addWorktree(fmt.Sprintf("%d-work", ticket), fmt.Sprintf("branch-%d", ticket))
	w.age(2 * time.Hour)
	fx.reader.merged(r, ticket, w)
	return w
}

// stuckWorktree is a finished worktree (merged PR, idle 2h) that cannot be
// removed: it holds a tracked directory that was made unwritable, so deleting its
// file fails. The test is skipped where permissions do not bind (root).
func (fx *wtFixture) stuckWorktree(r *wtRepo, ticket int) *wtTree {
	fx.t.Helper()
	w := r.addWorktree(fmt.Sprintf("%d-stuck", ticket), fmt.Sprintf("branch-%d", ticket))
	w.commit("locked/file.txt", "tracked\n")
	locked := filepath.Join(w.Path, "locked")
	if err := os.Chmod(locked, 0o555); err != nil {
		fx.t.Fatal(err)
	}
	fx.t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	probe := filepath.Join(locked, "probe")
	if err := os.WriteFile(probe, nil, 0o644); err == nil {
		_ = os.Remove(probe)
		fx.t.Skip("cannot make a directory unwritable here (running as root?)")
	}
	w.age(2 * time.Hour)
	fx.reader.merged(r, ticket, w)
	return w
}

// openWorktree is the same worktree with its ticket still open: always kept.
func (fx *wtFixture) openWorktree(r *wtRepo, ticket int) *wtTree {
	fx.t.Helper()
	w := r.addWorktree(fmt.Sprintf("%d-work", ticket), fmt.Sprintf("branch-%d", ticket))
	w.age(2 * time.Hour)
	fx.reader.open(r, ticket)
	return w
}

// registerViaDispatch writes the user config with the repos as dispatch.repos[].
func (fx *wtFixture) registerViaDispatch(repos ...*wtRepo) {
	fx.t.Helper()
	type entry struct {
		Repo    string `json:"repo"`
		Dir     string `json:"dir"`
		Session string `json:"session"`
	}
	entries := make([]entry, 0, len(repos))
	for _, r := range repos {
		entries = append(entries, entry{Repo: r.Repo.Owner + "/" + r.Repo.Name, Dir: r.Root, Session: "main"})
	}
	body, err := json.Marshal(map[string]any{"dispatch": map[string]any{"repos": entries}})
	if err != nil {
		fx.t.Fatal(err)
	}
	wtWriteFile(fx.t, fx.d.worktreeUserConfigPath, string(body))
}

// seedRegistry writes the registry file directly, as a previous daemon (or a
// hostile process) would have left it.
func (fx *wtFixture) seedRegistry(dirs ...string) {
	fx.t.Helper()
	body, err := json.Marshal(map[string]any{"schemaVersion": 1, "repos": dirs})
	if err != nil {
		fx.t.Fatal(err)
	}
	wtWriteFile(fx.t, fx.d.worktreeRegistryPath, string(body))
}

func (fx *wtFixture) registry() []string {
	fx.t.Helper()
	got, err := worktreeclean.LoadRegistry(fx.d.worktreeRegistryPath)
	if err != nil {
		fx.t.Fatalf("LoadRegistry: %v", err)
	}
	return got
}

// evaluate starts the pass and returns the evaluate step's result. The test
// daemon's runner is synchronous, so the worker has finished when start returns.
func (fx *wtFixture) evaluate() worktreeEvalResult {
	fx.t.Helper()
	fx.d.startWorktreeCleanup()
	select {
	case res := <-fx.d.worktreeEvalResults:
		return res
	default:
		fx.t.Fatal("the pass delivered no evaluate result")
		return worktreeEvalResult{}
	}
}

// finish runs the loop's guard re-check and, when it hands survivors to the
// remove step, the loop's reaction to that step reporting done.
func (fx *wtFixture) finish(res worktreeEvalResult) {
	fx.t.Helper()
	fx.d.applyWorktreeGuards(res)
	select {
	case <-fx.d.worktreeRemoveDone:
		fx.d.finishWorktreeCleanup()
	default:
	}
	if fx.d.worktreeInFlight {
		fx.t.Fatal("a pass is still in flight after its last step")
	}
}

// runPass runs one whole pass: start, evaluate, guard re-check, remove.
func (fx *wtFixture) runPass() { fx.t.Helper(); fx.finish(fx.evaluate()) }

func (fx *wtFixture) requireRemoved(trees ...*wtTree) {
	fx.t.Helper()
	for _, w := range trees {
		if w.exists() {
			fx.t.Errorf("worktree %s is still on disk, want it removed", w.Path)
		}
		if w.adminExists() {
			fx.t.Errorf("admin dir %s of a removed worktree is still on disk", w.AdminDir)
		}
	}
}

func (fx *wtFixture) requireKept(trees ...*wtTree) {
	fx.t.Helper()
	for _, w := range trees {
		if !w.exists() || !w.adminExists() {
			fx.t.Errorf("worktree %s (exists %v, admin dir exists %v) was removed, want it kept", w.Path, w.exists(), w.adminExists())
		}
	}
}

// hasLine reports whether one log line contains every part.
func hasLine(logs string, parts ...string) bool {
	for _, line := range strings.Split(logs, "\n") {
		if !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(line, p) }) {
			return true
		}
	}
	return false
}

// removedLine finds the removal log line of path with the given reason and
// returns the bytes it reports as freed.
func removedLine(logs, path, reason string) (freed int64, ok bool) {
	re := regexp.MustCompile(`worktree-cleanup: removed ` + regexp.QuoteMeta(strconv.Quote(path)) + `: ` + regexp.QuoteMeta(reason) + `, freed ([0-9]+) bytes`)
	m := re.FindStringSubmatch(logs)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return n, err == nil
}

// ---------------------------------------------------------------------------
// The pass: removals and what is logged
// ---------------------------------------------------------------------------

// AC5 (and the removal-logging AC): a pass over a registered real-git repo with a
// fake reader removes a merged PR's worktree that has been inactive for over 1h,
// and keeps the worktree of a ticket that has a live window. The removal is logged
// with its path, its reason and the bytes freed, without -verbose.
func TestWorktreeCleanup_RemovesAMergedWorktreeAndKeepsOneWithALiveTicketWindow(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	merged := fx.mergedWorktree(r, 782)
	live := fx.mergedWorktree(r, 783)
	fx.registerViaDispatch(r)
	fx.tracker.addWindow(783, "")
	if fx.d.cfg.Verbose {
		t.Fatal("fixture premise broken: the removal log must be asserted without -verbose")
	}
	logs := captureLog(t)

	fx.runPass()

	fx.requireRemoved(merged)
	fx.requireKept(live)
	freed, ok := removedLine(logs.String(), merged.Path, "finished")
	if !ok {
		t.Fatalf("log has no removal line for %s with reason finished; log:\n%s", merged.Path, logs)
	}
	if freed <= 0 {
		t.Errorf("removal logged %d bytes freed, want the worktree's size", freed)
	}
	if hasLine(logs.String(), "removed", strconv.Quote(live.Path)) {
		t.Errorf("the kept worktree %s is logged as removed:\n%s", live.Path, logs)
	}
	if merged.branchExists() {
		t.Errorf("branch %s of a finished worktree is still there, want it deleted with the worktree", merged.Branch)
	}
	calls := fx.reader.recorded()
	if len(calls) != 1 || calls[0].Repo != r.Repo || !slices.Equal(sorted(calls[0].Nums), []int{782, 783}) {
		t.Errorf("reader calls = %+v, want one call for %v with tickets [782 783]", calls, r.Repo)
	}
}

func sorted(nums []int) []int {
	out := slices.Clone(nums)
	slices.Sort(out)
	return out
}

// The fixtures encode verdicts every case below relies on (a merged worktree idle
// for 2h is finished, an old ticketless worktree is stale, a worktree of a repo
// without an origin would be stale-removable if it were processed). This pins them
// against the classifier alone, without the daemon, so a fixture that drifts fails
// here by name instead of making a "kept" assertion pass for the wrong reason. It
// passes before the pass exists, by design.
func TestWorktreeCleanupFixtures_ClassifyAsTheCasesAssume(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	merged := fx.mergedWorktree(r, 782)
	open := fx.openWorktree(r, 790)
	stuck := r.addWorktree("781-stuck", "branch-781")
	stuck.commit("locked/file.txt", "tracked\n")
	stuck.age(2 * time.Hour)
	fx.reader.merged(r, 781, stuck)
	idle := r.addWorktree("scratch", "scratch-branch")
	idle.age(30 * 24 * time.Hour)
	twoHours := r.addWorktree("twohours", "twohours-branch")
	twoHours.age(2 * time.Hour)
	recent := r.addWorktree("recent", "recent-branch")
	recent.age(10 * time.Minute)
	noOrigin := fx.addRepoWithOrigin("noorigin", "", "")
	noOriginIdle := noOrigin.addWorktree("scratch", "scratch-branch")
	noOriginIdle.age(30 * 24 * time.Hour)

	classify := func(repo *wtRepo, staleAfter time.Duration) map[string]worktreeclean.Classification {
		t.Helper()
		pass := func(context.Context, worktreeclean.Target) (bool, error) { return false, nil }
		main, entries, err := worktreeclean.Enumerate(context.Background(), repo.Root)
		if err != nil {
			t.Fatalf("Enumerate: %v", err)
		}
		out := make(map[string]worktreeclean.Classification)
		for _, c := range worktreeclean.ClassifyAll(context.Background(), main, entries, worktreeclean.Options{
			StaleAfter: staleAfter, Outcomes: fx.reader.results[repo.Repo], InUse: pass, Babysit: pass,
		}) {
			out[c.Entry.Name] = c
		}
		return out
	}
	const defaultStale = 7 * 24 * time.Hour
	byDefault := classify(r, defaultStale)
	byHour := classify(r, time.Hour)
	for _, tt := range []struct {
		name    string
		got     worktreeclean.Classification
		verdict worktreeclean.Verdict
		reason  worktreeclean.KeepReason
	}{
		{"merged ticket idle 2h", byDefault[merged.Name], worktreeclean.VerdictFinished, ""},
		{"merged ticket with an unpushed commit covered by the PR head", byDefault[stuck.Name], worktreeclean.VerdictFinished, ""},
		{"open ticket", byDefault[open.Name], worktreeclean.VerdictKeep, worktreeclean.KeepActive},
		{"ticketless idle 30d, default staleAfter", byDefault[idle.Name], worktreeclean.VerdictStale, ""},
		{"ticketless idle 2h, default staleAfter", byDefault[twoHours.Name], worktreeclean.VerdictKeep, worktreeclean.KeepActive},
		{"ticketless idle 2h, staleAfter 1h", byHour[twoHours.Name], worktreeclean.VerdictStale, ""},
		{"ticketless idle 10m, staleAfter 1h", byHour[recent.Name], worktreeclean.VerdictKeep, worktreeclean.KeepActive},
		{"idle worktree of a repo without an origin", classify(noOrigin, defaultStale)[noOriginIdle.Name], worktreeclean.VerdictStale, ""},
	} {
		if tt.got.Verdict != tt.verdict || tt.got.Reason != tt.reason {
			t.Errorf("%s: verdict %q reason %q (err %v), want verdict %q reason %q", tt.name, tt.got.Verdict, tt.got.Reason, tt.got.Err, tt.verdict, tt.reason)
		}
	}
}

// Whatever tmux shows to be in use is kept: a window named for the ticket, a pane
// whose directory is the worktree or inside it. A sibling that merely shares the
// path prefix and another ticket's window do not block it. The control worktree is
// removed in every row.
func TestWorktreeCleanup_KeepsWhatTmuxShowsInUse(t *testing.T) {
	tests := []struct {
		name       string
		windowName string
		cwd        string // "{wt}" is the target worktree's path
		wantKept   bool
	}{
		{"window named for the ticket", "784-implement", "/home/dev", true},
		{"pane in the worktree", "sh", "{wt}", true},
		{"pane in a subdirectory of the worktree", "sh", "{wt}/pkg/sub", true},
		{"pane in a sibling sharing the path prefix", "sh", "{wt}-other/sub", false},
		{"window of another ticket sharing the digits", "7840-implement", "/home/dev", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWTFixture(t)
			r := fx.addRepo("alpha", "")
			target := fx.mergedWorktree(r, 784)
			control := fx.mergedWorktree(r, 785)
			fx.registerViaDispatch(r)
			fx.panes.set([]tmux.LivePane{{PaneID: "%1", WindowName: tt.windowName, CurrentPath: strings.ReplaceAll(tt.cwd, "{wt}", target.Path)}}, nil)

			fx.runPass()

			fx.requireRemoved(control)
			if tt.wantKept {
				fx.requireKept(target)
			} else {
				fx.requireRemoved(target)
			}
		})
	}
}

// A worktree idle past staleAfter is removed as stale (reason logged, branch kept
// so the work can be restored); one inside the window is left alone.
func TestWorktreeCleanup_StaleWorktreeIsRemovedLoggedAndKeepsItsBranch(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", `{"worktreeCleanup":{"staleAfter":"1h"}}`)
	idle := r.addWorktree("scratch", "scratch-branch")
	idle.age(2 * time.Hour)
	recent := r.addWorktree("recent", "recent-branch")
	recent.age(10 * time.Minute)
	fx.registerViaDispatch(r)
	logs := captureLog(t)

	fx.runPass()

	fx.requireRemoved(idle)
	fx.requireKept(recent)
	if freed, ok := removedLine(logs.String(), idle.Path, "stale"); !ok || freed <= 0 {
		t.Errorf("removal line for %s with reason stale and a positive size = (%d, %v); log:\n%s", idle.Path, freed, ok, logs)
	}
	if !idle.branchExists() {
		t.Errorf("branch %s of a stale worktree was deleted, want it kept so the work can be restored", idle.Branch)
	}
}

// Removals are always logged; kept worktrees only with -verbose.
func TestWorktreeCleanup_KeptWorktreesAreLoggedOnlyWithVerbose(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%v", verbose), func(t *testing.T) {
			fx := newWTFixture(t)
			fx.d.cfg.Verbose = verbose
			r := fx.addRepo("alpha", "")
			removed := fx.mergedWorktree(r, 782)
			kept := fx.openWorktree(r, 790)
			fx.registerViaDispatch(r)
			logs := captureLog(t)

			fx.runPass()

			fx.requireRemoved(removed)
			fx.requireKept(kept)
			if _, ok := removedLine(logs.String(), removed.Path, "finished"); !ok {
				t.Errorf("the removal is not logged (verbose=%v); log:\n%s", verbose, logs)
			}
			keptLogged := hasLine(logs.String(), "worktree-cleanup:", strconv.Quote(kept.Path))
			if keptLogged != verbose {
				t.Errorf("kept worktree logged = %v with verbose=%v, want %v; log:\n%s", keptLogged, verbose, verbose, logs)
			}
		})
	}
}

// A keep that carries an error (here an unreadable pipeline state file) is a read
// failure and is always logged with its quoted worktree path, redacted; a plain
// policy keep (an open ticket) stays verbose-only.
func TestWorktreeCleanup_KeptWorktreeWithAReadFailureIsLoggedWithoutVerbose(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	unreadable := fx.mergedWorktree(r, 782)
	policy := fx.openWorktree(r, 790)
	wtWriteFile(t, filepath.Join(r.Root, ".cenci", "pipeline", "782.json"), "not json at all")
	fx.registerViaDispatch(r)
	logs := captureLog(t)

	fx.runPass()

	fx.requireKept(unreadable, policy)
	if !hasLine(logs.String(), "worktree-cleanup:", "kept", strconv.Quote(unreadable.Path), "decode") {
		t.Errorf("the read failure is not logged without -verbose; log:\n%s", logs)
	}
	if hasLine(logs.String(), "worktree-cleanup:", strconv.Quote(policy.Path)) {
		t.Errorf("a plain policy keep is logged without -verbose; log:\n%s", logs)
	}
}

// ---------------------------------------------------------------------------
// Per-repo configuration
// ---------------------------------------------------------------------------

// worktreeCleanup.enabled: false means no GitHub reads and no removals for that
// repo (the control repo next to it is cleaned); the skip is logged only with
// -verbose.
func TestWorktreeCleanup_OptedOutRepoMakesNoReadsAndRemovesNothing(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%v", verbose), func(t *testing.T) {
			fx := newWTFixture(t)
			fx.d.cfg.Verbose = verbose
			optedOut := fx.addRepo("alpha", `{"worktreeCleanup":{"enabled":false}}`)
			optedOutWT := fx.mergedWorktree(optedOut, 782)
			optedOutStale := optedOut.addWorktree("scratch", "scratch-branch")
			optedOutStale.age(30 * 24 * time.Hour)
			control := fx.addRepo("beta", "")
			controlWT := fx.mergedWorktree(control, 783)
			fx.registerViaDispatch(optedOut, control)
			logs := captureLog(t)

			fx.runPass()

			fx.requireRemoved(controlWT)
			fx.requireKept(optedOutWT, optedOutStale)
			if n := fx.reader.readsFor(optedOut.Repo); n != 0 {
				t.Errorf("reader called %d times for the opted-out repo, want none", n)
			}
			logged := strings.Contains(logs.String(), optedOut.Root)
			if logged != verbose {
				t.Errorf("opted-out repo mentioned in the log = %v with verbose=%v, want %v; log:\n%s", logged, verbose, verbose, logs)
			}
		})
	}
}

// A malformed repo config skips that repo for the pass and is logged (without
// -verbose); the other repo is still cleaned. A malformed staleAfter must not be
// swapped for the default.
func TestWorktreeCleanup_MalformedRepoConfigSkipsThatRepoAndLogsIt(t *testing.T) {
	tests := []struct{ name, config string }{
		{"not JSON", `{"worktreeCleanup":`},
		{"enabled is not a boolean", `{"worktreeCleanup":{"enabled":"yes"}}`},
		{"staleAfter is not positive", `{"worktreeCleanup":{"staleAfter":"-1h"}}`},
		{"the block is not an object", `{"worktreeCleanup":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWTFixture(t)
			broken := fx.addRepo("alpha", tt.config)
			brokenWT := fx.mergedWorktree(broken, 782)
			control := fx.addRepo("beta", "")
			controlWT := fx.mergedWorktree(control, 783)
			fx.registerViaDispatch(broken, control)
			logs := captureLog(t)

			fx.runPass()

			fx.requireRemoved(controlWT)
			fx.requireKept(brokenWT)
			if n := fx.reader.readsFor(broken.Repo); n != 0 {
				t.Errorf("reader called %d times for the repo with a malformed config, want none", n)
			}
			if !hasLine(logs.String(), "worktree-cleanup:", broken.Root) {
				t.Errorf("no log line names the skipped repo %s; log:\n%s", broken.Root, logs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The registry
// ---------------------------------------------------------------------------

// dispatch.repos[].dir from the user config is registered without any window, and
// persisted.
func TestWorktreeCleanup_DispatchConfigReposAreRegisteredPersistedAndProcessed(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.registerViaDispatch(r)
	if _, err := os.Stat(fx.d.worktreeRegistryPath); !os.IsNotExist(err) {
		t.Fatalf("fixture premise broken: the registry exists before the first pass (err %v)", err)
	}

	fx.runPass()

	fx.requireRemoved(w)
	if got := fx.registry(); !slices.Equal(got, []string{r.Root}) {
		t.Errorf("persisted registry = %v, want [%s]", got, r.Root)
	}
}

// A repo registered through a window's @cenci-repo-dir stays registered after the
// window closes and after a daemon restart. The window was launched inside a
// worktree of the repo, so what is registered must be the main checkout (the
// worktree itself is about to be reclaimed).
func TestWorktreeCleanup_RepoFromAWindowStaysRegisteredAfterTheWindowClosesAndTheDaemonRestarts(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	first := fx.openWorktree(r, 782)
	second := fx.openWorktree(r, 783)
	window := fx.tracker.addNamedWindow("999-implement", first.Path)

	fx.runPass()

	fx.requireKept(first, second)
	if got := fx.registry(); !slices.Equal(got, []string{r.Root}) {
		t.Fatalf("persisted registry = %v, want the main checkout [%s], not the worktree the window ran in", got, r.Root)
	}

	// The window closes: no session, no repo dir left anywhere but the registry.
	fx.tracker.event(window, "SessionEnd")
	if len(fx.d.sessions) != 0 {
		t.Fatalf("fixture premise broken: %d sessions left after SessionEnd", len(fx.d.sessions))
	}
	fx.reader.merged(r, 782, first)
	fx.runPass()
	fx.requireRemoved(first)
	fx.requireKept(second)

	// The daemon restarts: a new daemon with no window reads the registry file.
	fx.reader.merged(r, 783, second)
	restarted := fx.restart()
	restarted.runPass()
	restarted.requireRemoved(second)
}

// Entries whose directory no longer exists are dropped from the persisted
// registry; the other repo is still processed.
func TestWorktreeCleanup_RegistryDropsReposWhoseDirectoryIsGone(t *testing.T) {
	fx := newWTFixture(t)
	gone := fx.addRepo("alpha", "")
	keep := fx.addRepo("beta", "")
	fx.openWorktree(keep, 783)
	fx.registerViaDispatch(gone, keep)
	fx.runPass()
	if got := fx.registry(); !slices.Equal(got, []string{gone.Root, keep.Root}) {
		t.Fatalf("fixture premise broken: registry = %v, want both repos", got)
	}
	if err := os.RemoveAll(gone.Root); err != nil {
		t.Fatal(err)
	}
	merged := fx.mergedWorktree(keep, 784)

	fx.runPass()

	fx.requireRemoved(merged)
	if got := fx.registry(); !slices.Equal(got, []string{keep.Root}) {
		t.Errorf("persisted registry = %v, want only [%s]", got, keep.Root)
	}
}

// A toplevel from @cenci-repo-dir is registered only after it passes validation:
// a directory that is not a git repo, a relative path and a path with a newline
// are never registered. The control repo's window registers and is cleaned.
// Failing values are logged only with -verbose, always quoted.
func TestWorktreeCleanup_WindowDirsThatFailValidationAreNeverRegistered(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%v", verbose), func(t *testing.T) {
			fx := newWTFixture(t)
			fx.d.cfg.Verbose = verbose
			r := fx.addRepo("alpha", "")
			w := fx.mergedWorktree(r, 782)
			notGit := wtRealPath(t, t.TempDir())
			const relative, forged = "relative/path", "/abs/new\nline"
			fx.tracker.addNamedWindow("901-implement", notGit)
			fx.tracker.addNamedWindow("902-implement", relative)
			fx.tracker.addNamedWindow("903-implement", forged)
			fx.tracker.addNamedWindow("904-implement", r.Root)
			logs := captureLog(t)

			fx.runPass()

			fx.requireRemoved(w)
			if got := fx.registry(); !slices.Equal(got, []string{r.Root}) {
				t.Errorf("persisted registry = %v, want only the valid [%s]", got, r.Root)
			}
			if strings.Contains(logs.String(), forged) {
				t.Errorf("the raw newline-bearing value forged log lines:\n%q", logs)
			}
			if logged := strings.Contains(logs.String(), notGit); logged != verbose {
				t.Errorf("rejected window dir mentioned in the log = %v with verbose=%v, want %v; log:\n%s", logged, verbose, verbose, logs)
			}
		})
	}
}

// Registry entries read back from disk are validated again before use: a plain
// directory, a relative path and a repo whose origin cannot be parsed are never
// processed (the latter skipped entirely, stale cleanup included), while an
// identical repo with a parseable origin is.
func TestWorktreeCleanup_RegistryEntriesAreRevalidatedBeforeUse(t *testing.T) {
	fx := newWTFixture(t)
	good := fx.addRepo("alpha", "")
	goodWT := fx.mergedWorktree(good, 782)
	staleControl := fx.addRepo("control", "")
	staleControlWT := staleControl.addWorktree("scratch", "scratch-branch")
	staleControlWT.age(30 * 24 * time.Hour)
	noOrigin := fx.addRepoWithOrigin("noorigin", "", "")
	noOriginWT := noOrigin.addWorktree("scratch", "scratch-branch")
	noOriginWT.age(30 * 24 * time.Hour)
	plain := wtRealPath(t, t.TempDir())
	fx.seedRegistry(good.Root, plain, "relative/repo", noOrigin.Root, staleControl.Root)

	fx.runPass()

	fx.requireRemoved(goodWT, staleControlWT)
	fx.requireKept(noOriginWT)
	for _, c := range fx.reader.recorded() {
		if c.Repo != good.Repo {
			t.Errorf("reader called for %v, want only the valid repo %v", c.Repo, good.Repo)
		}
	}
}

// Two entries that resolve to the same repo (the main checkout and a
// subdirectory of it) are processed once.
func TestWorktreeCleanup_EntriesOfOneRepoAreProcessedOnce(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	sub := filepath.Join(r.Root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	fx.seedRegistry(r.Root, sub)
	logs := captureLog(t)

	fx.runPass()

	fx.requireRemoved(w)
	if n := fx.reader.readsFor(r.Repo); n != 1 {
		t.Errorf("reader called %d times for one repo registered twice, want 1", n)
	}
	if n := strings.Count(logs.String(), "removed "+strconv.Quote(w.Path)); n != 1 {
		t.Errorf("removal logged %d times, want once; log:\n%s", n, logs)
	}
}

// A user config that cannot be read is logged and contributes nothing; the repos
// already registered are still processed.
func TestWorktreeCleanup_UnreadableDispatchConfigKeepsTheRegisteredRepos(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.seedRegistry(r.Root)
	wtWriteFile(t, fx.d.worktreeUserConfigPath, `{"dispatch":`)
	logs := captureLog(t)

	fx.runPass()

	fx.requireRemoved(w)
	if !hasLine(logs.String(), "worktree-cleanup:", fx.d.worktreeUserConfigPath) {
		t.Errorf("no log line names the unreadable config %s; log:\n%s", fx.d.worktreeUserConfigPath, logs)
	}
	if got := fx.registry(); !slices.Equal(got, []string{r.Root}) {
		t.Errorf("persisted registry = %v, want the existing entry kept: [%s]", got, r.Root)
	}
}

// A corrupt registry file is logged and rebuilt from this pass's candidates.
func TestWorktreeCleanup_CorruptRegistryIsLoggedAndRebuilt(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.registerViaDispatch(r)
	wtWriteFile(t, fx.d.worktreeRegistryPath, "{corrupt")
	logs := captureLog(t)

	fx.runPass()

	fx.requireRemoved(w)
	if !hasLine(logs.String(), "worktree-cleanup:", fx.d.worktreeRegistryPath) {
		t.Errorf("no log line names the corrupt registry %s; log:\n%s", fx.d.worktreeRegistryPath, logs)
	}
	if got := fx.registry(); !slices.Equal(got, []string{r.Root}) {
		t.Errorf("persisted registry = %v, want it rebuilt as [%s]", got, r.Root)
	}
}

// ---------------------------------------------------------------------------
// Failing closed: unreadable live state
// ---------------------------------------------------------------------------

// Any tmux error other than "no server running", and running inside the sandbox,
// leave every worktree in place with the reason logged: no enumeration, no GitHub
// read, no removal. The registry is still updated (the control that the pass ran).
func TestWorktreeCleanup_UnreadableTmuxRemovesNothingAndSaysWhy(t *testing.T) {
	tests := []struct {
		name          string
		tmuxErr       error
		sandbox       bool
		wantTmuxReads bool
	}{
		{"tmux error", errors.New("error connecting to /tmp/tmux-1000/default (No such file or directory)"), false, true},
		{"tmux binary missing", fmt.Errorf("list-panes: %w", os.ErrNotExist), false, true},
		{"inside the sandbox", nil, true, false},
		{"sandbox beats a benign no-server answer", tmux.ErrNoServer, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWTFixture(t)
			r := fx.addRepo("alpha", "")
			w := fx.mergedWorktree(r, 782)
			fx.registerViaDispatch(r)
			fx.panes.set(nil, tt.tmuxErr)
			fx.d.worktreeInSandbox = func() bool { return tt.sandbox }
			logs := captureLog(t)

			fx.runPass()

			fx.requireKept(w)
			if calls := fx.reader.recorded(); len(calls) != 0 {
				t.Errorf("reader calls = %+v, want none: nothing may be read from GitHub when in-use status is unknown", calls)
			}
			if !hasLine(logs.String(), "worktree-cleanup:", "in-use status unknown") {
				t.Errorf("no log line says in-use status is unknown; log:\n%s", logs)
			}
			if got := fx.registry(); !slices.Equal(got, []string{r.Root}) {
				t.Errorf("persisted registry = %v, want [%s]: the registry update still happens", got, r.Root)
			}
			if reads := fx.panes.calls.Load() > 0; reads != tt.wantTmuxReads {
				t.Errorf("tmux read = %v, want %v", reads, tt.wantTmuxReads)
			}
		})
	}
}

// "No server running" is an answer (no windows), not a failure: the narrow match
// must not turn it into "unreadable" (watch/AGENTS.md match-miss rule).
func TestWorktreeCleanup_NoTmuxServerIsNotAFailure(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.registerViaDispatch(r)
	fx.panes.set(nil, fmt.Errorf("list-panes: %w", tmux.ErrNoServer))
	logs := captureLog(t)

	fx.runPass()

	fx.requireRemoved(w)
	if strings.Contains(logs.String(), "in-use status unknown") {
		t.Errorf("log reports unknown in-use status for a benign no-server answer:\n%s", logs)
	}
}

// The tmux list is read again right before the removals: it turning unreadable
// after the evaluation removes nothing.
func TestWorktreeCleanup_TmuxTurningUnreadableBeforeRemovalKeepsEverything(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	first, second := fx.mergedWorktree(r, 782), fx.mergedWorktree(r, 783)
	fx.registerViaDispatch(r)
	res := fx.evaluate()
	if n := fx.reader.readsFor(r.Repo); n != 1 {
		t.Fatalf("fixture premise broken: the evaluate step made %d reads, want 1", n)
	}
	fx.panes.set(nil, errors.New("error connecting to /tmp/tmux-1000/default"))
	logs := captureLog(t)

	fx.finish(res)

	fx.requireKept(first, second)
	if !hasLine(logs.String(), "worktree-cleanup:", "in-use status unknown") {
		t.Errorf("no log line says in-use status is unknown; log:\n%s", logs)
	}
}

// The tmux list is read again before EACH removal, not once per step: a shell
// opened inside the second worktree while the first is being removed keeps the
// second. The pane appears only once the first worktree is gone from disk.
func TestWorktreeCleanup_ShellOpenedMidRemovalStepKeepsTheLaterWorktree(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	first, second := fx.mergedWorktree(r, 782), fx.mergedWorktree(r, 783)
	fx.registerViaDispatch(r)
	res := fx.evaluate()
	fx.d.worktreeListPanes = func() ([]tmux.LivePane, error) {
		if first.exists() {
			return nil, nil
		}
		return []tmux.LivePane{{PaneID: "%7", WindowName: "sh", CurrentPath: second.Path}}, nil
	}

	fx.finish(res)

	fx.requireRemoved(first)
	fx.requireKept(second)
}

// Likewise tmux turning unreadable between two removals stops the step: the
// second worktree is kept and the unknown in-use status is logged.
func TestWorktreeCleanup_TmuxTurningUnreadableMidRemovalStepKeepsTheLaterWorktree(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	first, second := fx.mergedWorktree(r, 782), fx.mergedWorktree(r, 783)
	fx.registerViaDispatch(r)
	res := fx.evaluate()
	fx.d.worktreeListPanes = func() ([]tmux.LivePane, error) {
		if first.exists() {
			return nil, nil
		}
		return nil, errors.New("error connecting to /tmp/tmux-1000/default")
	}
	logs := captureLog(t)

	fx.finish(res)

	fx.requireRemoved(first)
	fx.requireKept(second)
	if !hasLine(logs.String(), "worktree-cleanup:", "in-use status unknown") {
		t.Errorf("no log line says in-use status is unknown; log:\n%s", logs)
	}
}

// A shell opened inside a worktree after it was evaluated keeps it: the pane
// directories are read again in the remove step. The other worktree goes.
func TestWorktreeCleanup_ShellOpenedInAWorktreeAfterEvaluationKeepsIt(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	entered, control := fx.mergedWorktree(r, 782), fx.mergedWorktree(r, 783)
	fx.registerViaDispatch(r)
	res := fx.evaluate()
	fx.panes.set([]tmux.LivePane{{PaneID: "%7", WindowName: "sh", CurrentPath: entered.Path + "/pkg"}}, nil)

	fx.finish(res)

	fx.requireKept(entered)
	fx.requireRemoved(control)
}

// ---------------------------------------------------------------------------
// Re-checks between the steps
// ---------------------------------------------------------------------------

// A window or busy session for the ticket that appears after the evaluation, on
// the loop, keeps the worktree; the control worktree goes.
func TestWorktreeCleanup_WindowOrBusySessionAppearingAfterEvaluationKeepsTheWorktree(t *testing.T) {
	tests := []struct{ name, eventType string }{
		{"a done window", ""},
		{"a running session", "UserPromptSubmit"},
		{"a session needing input", "PermissionRequest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWTFixture(t)
			r := fx.addRepo("alpha", "")
			target, control := fx.mergedWorktree(r, 782), fx.mergedWorktree(r, 783)
			fx.registerViaDispatch(r)
			res := fx.evaluate()
			w := fx.tracker.addWindow(782, "")
			if tt.eventType != "" {
				fx.tracker.event(w, tt.eventType)
			}

			fx.finish(res)

			fx.requireKept(target)
			fx.requireRemoved(control)
		})
	}
}

// The babysit guard is consulted for the ticket in both the evaluate step and the
// loop re-check: a supervisor armed before the pass or after the evaluation keeps
// the worktree.
func TestWorktreeCleanup_BabysitSupervisorKeepsTheWorktreeWhenArmedBeforeOrAfterEvaluation(t *testing.T) {
	for _, armedBeforeEvaluation := range []bool{true, false} {
		t.Run(fmt.Sprintf("armed before evaluation=%v", armedBeforeEvaluation), func(t *testing.T) {
			fx := newWTFixture(t)
			r := fx.addRepo("alpha", "")
			target, control := fx.mergedWorktree(r, 782), fx.mergedWorktree(r, 783)
			fx.registerViaDispatch(r)
			var armed atomic.Bool
			var mu sync.Mutex
			var asked []string
			fx.d.closeGuard = func(ticket string) (bool, string) {
				mu.Lock()
				asked = append(asked, ticket)
				mu.Unlock()
				return ticket == "782" && armed.Load(), "babysit supervising PR #790, CI not green"
			}
			armed.Store(armedBeforeEvaluation)
			res := fx.evaluate()
			armed.Store(true)

			fx.finish(res)

			fx.requireKept(target)
			fx.requireRemoved(control)
			mu.Lock()
			defer mu.Unlock()
			if !slices.Contains(asked, "782") {
				t.Errorf("babysit guard asked about %v, want it consulted with ticket 782", asked)
			}
		})
	}
}

// The git checks are re-run in the remove step: a worktree that gained unsaved
// work after the evaluation is kept.
func TestWorktreeCleanup_WorktreeChangedAfterEvaluationIsKept(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	changed, control := fx.mergedWorktree(r, 782), fx.mergedWorktree(r, 783)
	fx.registerViaDispatch(r)
	res := fx.evaluate()
	wtWriteFile(t, filepath.Join(changed.Path, "notes.txt"), "unsaved agent work\n")

	fx.finish(res)

	fx.requireKept(changed)
	fx.requireRemoved(control)
	if _, err := os.Stat(filepath.Join(changed.Path, "notes.txt")); err != nil {
		t.Errorf("the unsaved file is gone: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Failures
// ---------------------------------------------------------------------------

// Fixture premise for the test below, against worktreeclean alone: the stuck
// worktree is classified finished and its removal really fails and leaves the
// directory behind. It passes before the pass exists, by design.
func TestWorktreeCleanupFixtures_AStuckWorktreeFailsToRemove(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	stuck := fx.stuckWorktree(r, 781)
	pass := func(context.Context, worktreeclean.Target) (bool, error) { return false, nil }
	o := worktreeclean.Options{StaleAfter: 7 * 24 * time.Hour, Outcomes: fx.reader.results[r.Repo], InUse: pass, Babysit: pass}
	main, entries, err := worktreeclean.Enumerate(context.Background(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	cs := worktreeclean.ClassifyAll(context.Background(), main, entries, o)
	if len(cs) != 1 || cs[0].Verdict != worktreeclean.VerdictFinished {
		t.Fatalf("classifications = %+v, want the stuck worktree finished", cs)
	}

	rem := worktreeclean.Remove(context.Background(), main, cs[0], o)

	if rem.Err == nil || !stuck.exists() {
		t.Errorf("Remove = %+v (worktree on disk: %v), want an error and the worktree left behind", rem, stuck.exists())
	}
}

// A failure while removing one worktree is logged, and the pass continues with the
// remaining worktrees of the repo and with the remaining repos. The failing
// worktree (a tracked directory that cannot be emptied) is in the first repo and
// sorts first.
func TestWorktreeCleanup_RemovalFailureIsLoggedAndThePassContinues(t *testing.T) {
	fx := newWTFixture(t)
	first := fx.addRepo("alpha", "")
	second := fx.addRepo("beta", "")
	stuck := fx.stuckWorktree(first, 781)
	sameRepo := fx.mergedWorktree(first, 783)
	otherRepo := fx.mergedWorktree(second, 790)
	fx.registerViaDispatch(first, second)
	logs := captureLog(t)

	fx.runPass()

	fx.requireRemoved(sameRepo, otherRepo)
	if !stuck.exists() {
		t.Fatal("fixture premise broken: the stuck worktree was removed, so no failure was exercised")
	}
	if !hasLine(logs.String(), "worktree-cleanup:", strconv.Quote(stuck.Path)) {
		t.Errorf("no log line names the failed worktree %s; log:\n%s", stuck.Path, logs)
	}
	if hasLine(logs.String(), strconv.Quote(stuck.Path), "freed") {
		t.Errorf("the failed removal is logged as freeing space; log:\n%s", logs)
	}
	if _, ok := removedLine(logs.String(), sameRepo.Path, "finished"); !ok {
		t.Errorf("the later worktree's removal is not logged; log:\n%s", logs)
	}
}

// Daemon logs never include remote URLs or the credentials in them, whether the
// repo's identity, a failed GitHub read or a repo that fails to resolve is being
// logged. A failed read is visible (owner/name) and leaves the worktree alone.
func TestWorktreeCleanup_LogsNeverContainRemoteURLs(t *testing.T) {
	const (
		secretURL = "https://user:SECRET@github.com/acme/widgets.git"
		brokenURL = "https://user:SECRET@github.com/broken"
	)
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%v", verbose), func(t *testing.T) {
			fx := newWTFixture(t)
			fx.d.cfg.Verbose = verbose
			widgets := fx.addRepoWithOrigin("widgets", "", secretURL)
			unread := widgets.addWorktree("782-work", "branch-782")
			unread.age(2 * time.Hour)
			fx.reader.script(widgets.Repo, 782, ticketstate.OutcomeResult{Err: errors.New("gh api graphql " + secretURL + ": HTTP 502")})
			broken := fx.addRepoWithOrigin("broken", "", brokenURL)
			brokenStale := broken.addWorktree("scratch", "scratch-branch")
			brokenStale.age(30 * 24 * time.Hour)
			fx.registerViaDispatch(widgets, broken)
			fx.tracker.addNamedWindow("905-implement", broken.Root)
			logs := captureLog(t)

			fx.runPass()

			fx.requireKept(unread, brokenStale)
			for _, forbidden := range []string{"SECRET", "user:", "://", "github.com/", secretURL, brokenURL} {
				if strings.Contains(logs.String(), forbidden) {
					t.Errorf("log contains %q:\n%s", forbidden, logs)
				}
			}
			if !hasLine(logs.String(), "worktree-cleanup:", "acme/widgets") {
				t.Errorf("the failed read is not logged with the repo's owner/name; log:\n%s", logs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrency: the loop, the workers, and what each may do
// ---------------------------------------------------------------------------

// A worker ctx that is already done (the step's timeout expired) skips the rest and
// is logged; the in-flight flag still clears, so the next pass runs.
func TestWorktreeCleanup_AnExpiredWorkerStepIsSkippedAndDoesNotBlockTheNextPass(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.registerViaDispatch(r)
	normal := fx.d.runOffLoop
	fx.d.runOffLoop = func(_ time.Duration, fn func(ctx context.Context)) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fn(ctx)
	}
	logs := captureLog(t)

	fx.runPass()

	fx.requireKept(w)
	if !strings.Contains(logs.String(), "worktree-cleanup:") {
		t.Errorf("the skipped step is not logged; log:\n%s", logs)
	}

	fx.d.runOffLoop = normal
	fx.runPass()
	fx.requireRemoved(w)
}

// Each worker step is bounded: the evaluate step and the remove step get 5
// minutes each.
func TestWorktreeCleanup_EachWorkerStepIsBoundedByFiveMinutes(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.registerViaDispatch(r)
	var timeouts []time.Duration
	inner := fx.d.runOffLoop
	fx.d.runOffLoop = func(timeout time.Duration, fn func(ctx context.Context)) {
		timeouts = append(timeouts, timeout)
		inner(timeout, fn)
	}

	fx.runPass()

	fx.requireRemoved(w)
	if want := []time.Duration{5 * time.Minute, 5 * time.Minute}; !slices.Equal(timeouts, want) {
		t.Errorf("worker timeouts = %v, want %v (evaluate, then remove)", timeouts, want)
	}
}

// No git, GitHub or tmux work runs on the loop, removals run in a worker, and at
// most one pass is in flight: with the runner deferred, starting a pass (twice)
// only queues the evaluate step.
func TestWorktreeCleanup_GitGithubTmuxAndRemovalsRunOffTheLoopAndOnePassAtATime(t *testing.T) {
	fx := newWTFixture(t)
	runner := &deferredRunner{}
	fx.d.runOffLoop = runner.run
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.registerViaDispatch(r)

	fx.d.startWorktreeCleanup()
	fx.d.startWorktreeCleanup() // the next tick, while the first pass is in flight

	if len(runner.fns) != 1 {
		t.Fatalf("%d worker steps queued after two starts, want exactly 1 (one pass in flight)", len(runner.fns))
	}
	if calls := fx.reader.recorded(); len(calls) != 0 {
		t.Errorf("reader called on the loop: %+v", calls)
	}
	if n := fx.panes.calls.Load(); n != 0 {
		t.Errorf("tmux read %d times on the loop, want 0", n)
	}
	if _, err := os.Stat(fx.d.worktreeRegistryPath); !os.IsNotExist(err) {
		t.Errorf("registry written on the loop (stat err %v)", err)
	}
	if !fx.d.worktreeInFlight {
		t.Error("worktreeInFlight = false while the evaluate step is queued")
	}

	runner.drain() // the evaluate worker runs
	var res worktreeEvalResult
	select {
	case res = <-fx.d.worktreeEvalResults:
	default:
		t.Fatal("the evaluate worker delivered no result")
	}
	if n := fx.reader.readsFor(r.Repo); n != 1 {
		t.Errorf("reader called %d times by the evaluate worker, want 1", n)
	}
	fx.requireKept(w) // evaluation removes nothing

	fx.d.applyWorktreeGuards(res)
	if len(runner.fns) != 1 {
		t.Fatalf("%d worker steps queued after the guard re-check, want the remove step", len(runner.fns))
	}
	fx.requireKept(w) // the remove step has not run: removal is not done on the loop

	runner.drain() // the remove worker runs
	fx.requireRemoved(w)
	select {
	case <-fx.d.worktreeRemoveDone:
		fx.d.finishWorktreeCleanup()
	default:
		t.Fatal("the remove worker did not report done")
	}

	// The next tick starts a new pass.
	fx.d.startWorktreeCleanup()
	if len(runner.fns) != 1 {
		t.Errorf("%d worker steps queued by the tick after a finished pass, want a new evaluate step", len(runner.fns))
	}
}

// A daemon without the reader (newDaemon's default) does nothing: no tmux read, no
// registry, no pass in flight. With the reader wired the same fixture does run.
func TestWorktreeCleanup_DoesNothingWithoutAReader(t *testing.T) {
	fx := newWTFixture(t)
	r := fx.addRepo("alpha", "")
	w := fx.mergedWorktree(r, 782)
	fx.registerViaDispatch(r)
	fx.d.worktreeReader = nil

	fx.d.startWorktreeCleanup()

	if fx.d.worktreeInFlight || fx.panes.calls.Load() != 0 {
		t.Errorf("in flight = %v, tmux reads = %d; want an inert pass", fx.d.worktreeInFlight, fx.panes.calls.Load())
	}
	if _, err := os.Stat(fx.d.worktreeRegistryPath); !os.IsNotExist(err) {
		t.Errorf("registry written by an inert pass (stat err %v)", err)
	}
	fx.requireKept(w)

	fx.d.worktreeReader = fx.reader
	fx.runPass()
	fx.requireRemoved(w)
}

// ---------------------------------------------------------------------------
// The loop
// ---------------------------------------------------------------------------

// The pass runs on its own interval only: a positive interval fires it (the
// control row), while a zero or negative interval, or a 1h interval under a 10ms
// sweep tick, never does -- no tmux read, no GitHub read, nothing removed. The
// loop runs until it has really completed many sweep ticks (bounded by a
// deadline, not a fixed sleep), which proves the negative rows ran.
func TestLoop_WorktreeCleanupRunsOnlyOnItsOwnPositiveInterval(t *testing.T) {
	const wantSweeps = 20
	for _, tc := range []struct {
		name     string
		interval time.Duration
		wantRun  bool
	}{
		{"a 20ms interval fires the pass", 20 * time.Millisecond, true},
		{"zero interval disables the pass", 0, false},
		{"negative interval disables the pass", -time.Second, false},
		{"sweep ticks alone do not run a 1h-interval pass", time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.WorktreeCleanupInterval = tc.interval
			cfg.WindowCleanupInterval = 0
			mc := &tmuxtest.MockClient{}
			counter := &listCounter{MockClient: mc}
			d := newDaemon(cfg, tmuxfe.New(cfg, counter), make(chan ipc.HookEvent, 16), &mockReaper{})
			fx := newWTFixtureFor(t, d, mc)
			r := fx.addRepo("alpha", "")
			w := fx.mergedWorktree(r, 782)
			fx.registerViaDispatch(r)
			// A tracked window makes every sweep tick list the panes, which is what
			// the sweep counter below observes.
			fx.tracker.addWindow(999, "")
			listsBefore := counter.lists.Load()

			until := func() bool { return counter.lists.Load()-listsBefore >= wantSweeps }
			if tc.wantRun {
				until = func() bool { return !w.exists() }
			}
			runLoop(t, d, until, 10*time.Second)

			if tc.wantRun {
				// The loop stops as soon as the worktree directory is gone, while the remove
				// step is still deleting the admin dir and the branch; let it report done
				// before asserting and before the test's TempDir is cleaned up under it. The
				// loop has exited, so worktreeInFlight can be read race-free.
				for deadline := time.Now().Add(5 * time.Second); d.worktreeInFlight && len(d.worktreeRemoveDone) == 0; {
					if time.Now().After(deadline) {
						t.Fatal("the remove step never reported done")
					}
					time.Sleep(5 * time.Millisecond)
				}
				fx.requireRemoved(w)
				return
			}
			if sweeps := counter.lists.Load() - listsBefore; sweeps < wantSweeps {
				t.Fatalf("only %d sweep ticks ran, want the loop to have run %d 10ms sweeps", sweeps, wantSweeps)
			}
			fx.requireKept(w)
			if calls := fx.reader.recorded(); len(calls) != 0 {
				t.Errorf("reader calls = %+v, want none", calls)
			}
			if n := fx.panes.calls.Load(); n != 0 {
				t.Errorf("tmux read %d times, want none", n)
			}
		})
	}
}

// At most one pass is in flight: with the production goroutine runner and a reader
// that blocks, further ticks make no second read and remove nothing. Releasing the
// reader lets the first pass complete (the worktree goes) and later ticks start new
// passes, proving the in-flight flag was cleared by the worker's result.
func TestLoop_WorktreeCleanupBlockedWorkerStartsNoSecondPassAndTheFirstLaterCompletes(t *testing.T) {
	const wait = 5 * time.Second
	cfg := testConfig()
	cfg.WorktreeCleanupInterval = 10 * time.Millisecond
	cfg.WindowCleanupInterval = 0
	mc := &tmuxtest.MockClient{}
	d := newDaemon(cfg, tmuxfe.New(cfg, mc), make(chan ipc.HookEvent, 16), &mockReaper{})
	fx := newWTFixtureFor(t, d, mc)
	r := fx.addRepo("alpha", "")
	merged := fx.mergedWorktree(r, 782)
	open := fx.openWorktree(r, 790) // keeps later passes reading ticket state
	fx.registerViaDispatch(r)
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	fx.reader.beforeRead = func(ctx context.Context) {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.loop(ctx, make(chan ipc.AttentionUpdate))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(wait)
	for len(fx.reader.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first pass never read the ticket state")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // ~20 more ticks while the read is blocked
	if calls := fx.reader.recorded(); len(calls) != 1 {
		t.Fatalf("reader calls while the first read is blocked = %+v, want exactly 1 (one pass in flight)", calls)
	}
	fx.requireKept(merged, open)

	release()
	deadline = time.Now().Add(wait)
	for merged.exists() || len(fx.reader.recorded()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("after release: merged worktree still on disk = %v, reader calls = %d; want it removed and a second pass started",
				merged.exists(), len(fx.reader.recorded()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	fx.requireKept(open)
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

// newDaemon leaves the pass inert -- no reader, so newDaemon-based tests never
// shell out to gh, git or tmux -- but owns the result channels, each with room for
// the worker's report so it can finish without the loop receiving.
func TestNewDaemon_LeavesTheWorktreeCleanupInertButOwnsItsResultChannels(t *testing.T) {
	d := newTestDaemon(&tmuxtest.MockClient{})

	if d.worktreeReader != nil {
		t.Errorf("worktreeReader = %T, want nil so tests never shell out to gh", d.worktreeReader)
	}
	if d.worktreeEvalResults == nil || cap(d.worktreeEvalResults) != 1 {
		t.Errorf("worktreeEvalResults = %v (cap %d), want a channel of capacity 1", d.worktreeEvalResults, cap(d.worktreeEvalResults))
	}
	if d.worktreeRemoveDone == nil || cap(d.worktreeRemoveDone) != 1 {
		t.Errorf("worktreeRemoveDone = %v (cap %d), want a channel of capacity 1", d.worktreeRemoveDone, cap(d.worktreeRemoveDone))
	}
}

// Run's installHostSeams must install the real GitHub reader, a fresh resolver per
// pass, the real tmux pane lister, the env-based sandbox gate and the default
// registry and user-config paths, or the production pass would silently do nothing
// (or read the wrong files).
func TestInstallHostSeams_InstallsTheRealWorktreeCleanupSeams(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d := newTestDaemon(&tmuxtest.MockClient{})

	d.installHostSeams(func(agent, workflow string) bool { return true })

	reader, ok := d.worktreeReader.(*ticketstate.Reader)
	if !ok || reader == nil {
		t.Fatalf("worktreeReader = %T, want a non-nil *ticketstate.Reader", d.worktreeReader)
	}
	if d.newWorktreeResolver == nil {
		t.Fatal("newWorktreeResolver = nil, want the real resolver factory installed")
	}
	first, ok := d.newWorktreeResolver().(*ticketstate.Resolver)
	if !ok || first == nil {
		t.Fatalf("newWorktreeResolver() = %T, want a non-nil *ticketstate.Resolver", d.newWorktreeResolver())
	}
	if second := d.newWorktreeResolver(); second == repoResolver(first) {
		t.Error("newWorktreeResolver returned the same Resolver twice, want a fresh one per pass (a long-lived one can cache a stale origin)")
	}
	if d.worktreeListPanes == nil || reflect.ValueOf(d.worktreeListPanes).Pointer() != reflect.ValueOf(tmux.ListLivePanes).Pointer() {
		t.Error("worktreeListPanes is not tmux.ListLivePanes")
	}
	if d.worktreeInSandbox == nil {
		t.Fatal("worktreeInSandbox = nil, want the CENCI_SANDBOX gate installed")
	}
	t.Setenv("CENCI_SANDBOX", "1")
	if !d.worktreeInSandbox() {
		t.Error("worktreeInSandbox() = false with CENCI_SANDBOX=1, want true")
	}
	t.Setenv("CENCI_SANDBOX", "")
	if d.worktreeInSandbox() {
		t.Error("worktreeInSandbox() = true with CENCI_SANDBOX unset, want false")
	}
	if want := worktreeclean.DefaultRegistryPath(); want == "" || d.worktreeRegistryPath != want {
		t.Errorf("worktreeRegistryPath = %q, want the default %q", d.worktreeRegistryPath, want)
	}
	if want := worktreeclean.DefaultUserConfigPath(); want == "" || d.worktreeUserConfigPath != want {
		t.Errorf("worktreeUserConfigPath = %q, want the default %q", d.worktreeUserConfigPath, want)
	}
}
