package main_test

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// Black-box tests for `cenci worktrees ls|prune` (#1266) against the binary
// built in main_test.go, over real git repositories. Nothing talks to a real
// tmux, GitHub or daemon: the child environment is built from scratch (see env),
// a fake tmux and gh sit first on PATH, and the daemon socket directory is a
// private temp dir (watch/docs/test-isolation.md).

const (
	// wtOrigin is the origin URL the repo ends up with, so the ticket-state
	// resolver finds an owner/name; the fake gh answers for it.
	wtOrigin  = "https://github.com/acme/widgets.git"
	wtOldDate = "@1577836800 +0000" // 2020-01-01, older than any staleAfter
	wtIdle    = 30 * 24 * time.Hour
	// wtNoServer is the one tmux failure that means "no windows".
	wtNoServer = "no server running on /tmp/tmux-1000/default"
	wtNoteUnk  = "(in-use status unknown: prune will keep it here)"
)

// wtSize matches a human size as the command prints it, e.g. "512 B", "12.3 KiB".
const wtSize = `\d+(?:\.\d+)? (?:B|KiB|MiB|GiB)`

// wtWorld is a main checkout with an origin, a fake tmux and gh, and an isolated
// environment for running the binary against it.
type wtWorld struct {
	t         *testing.T
	root      string // the main checkout (symlinks resolved)
	home      string
	binDir    string
	tmuxState string
	tmuxLog   string
	ghAnswer  string
	stateHome string
	cfgHome   string
	tmpDir    string
	sockDir   string
	gitEnv    []string
}

func newWTWorld(t *testing.T) *wtWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required: %v", err)
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &wtWorld{t: t, root: filepath.Join(tmp, "repo")}
	for _, d := range []*struct {
		dst  *string
		name string
	}{{&w.home, "home"}, {&w.binDir, "bin"}, {&w.tmuxState, "tmux-state"}, {&w.stateHome, "state"}, {&w.cfgHome, "config"}, {&w.tmpDir, "tmp"}} {
		*d.dst = filepath.Join(tmp, d.name)
		if err := os.MkdirAll(*d.dst, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.tmuxLog = filepath.Join(tmp, "tmux.log")
	w.ghAnswer = filepath.Join(tmp, "gh.json")
	// A unix socket path is length-limited, so the socket dir is short and 0700
	// (CENCI_SOCKET_DIR is used verbatim and hardened).
	if w.sockDir, err = os.MkdirTemp("", "wts"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(w.sockDir) })
	if err := os.Chmod(w.sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.gitEnv = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + w.home,
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_AUTHOR_DATE=" + wtOldDate, "GIT_COMMITTER_DATE=" + wtOldDate,
	}
	w.gitEnv = append(w.gitEnv, wtGitConfigEnv()...)

	if err := os.MkdirAll(w.root, 0o755); err != nil {
		t.Fatal(err)
	}
	w.git(w.root, "init", "-q", "-b", "main")
	w.write(filepath.Join(w.root, "tracked.txt"), "tracked\n")
	w.write(filepath.Join(w.root, ".gitignore"), "ignored/\n")
	w.git(w.root, "add", "-A")
	w.git(w.root, "commit", "-q", "-m", "init")
	origin := filepath.Join(tmp, "origin.git")
	w.git(tmp, "init", "-q", "--bare", "-b", "main", origin)
	w.git(w.root, "remote", "add", "origin", origin)
	w.git(w.root, "push", "-q", "origin", "main")
	w.git(w.root, "remote", "set-url", "origin", wtOrigin)

	q := exectest.ShellQuote
	w.script("tmux", "printf '%s\\n' \"$*\" >> "+q(w.tmuxLog)+"\n"+
		"if [ -f "+q(w.tmuxState+"/fail")+" ]; then cat "+q(w.tmuxState+"/fail")+" >&2; exit 1; fi\n"+
		"kind=w; case \"$*\" in *pane_current_path*) kind=p;; esac\n"+
		"n=$(cat "+q(w.tmuxState)+"/$kind.n 2>/dev/null || echo 0); n=$((n+1)); echo $n > "+q(w.tmuxState)+"/$kind.n\n"+
		"f="+q(w.tmuxState)+"/$kind.$n; [ -f \"$f\" ] || f="+q(w.tmuxState)+"/$kind.last\n"+
		"cat \"$f\"\n")
	w.script("gh", "if [ -f "+q(w.ghAnswer)+" ]; then cat "+q(w.ghAnswer)+"; else echo 'gh: not logged in' >&2; exit 1; fi\n")
	w.tmuxFails(wtNoServer)
	return w
}

func wtGitConfigEnv() []string {
	// Apply before even the initial init/commit/push, including the bare
	// origin and Git subprocesses. Background housekeeping can otherwise
	// remove maintenance.lock or change mtimes during a no-write assertion.
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0",
	}
}

func (w *wtWorld) write(path, content string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wtWorld) script(name, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.binDir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wtWorld) git(dir string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = w.gitEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

func TestWTWorld_AutomaticHousekeepingLeavesFixtureUntouched(t *testing.T) {
	t.Run("maintenance after commit", func(t *testing.T) {
		w := newWTWorld(t)
		// Make the next commit eligible for a visible maintenance task, and
		// keep it synchronous so the positive control cannot race the check.
		w.git(w.root, "config", "gc.autoDetach", "false")
		w.git(w.root, "config", "maintenance.gc.enabled", "false")
		w.git(w.root, "config", "maintenance.commit-graph.enabled", "true")
		w.git(w.root, "config", "maintenance.commit-graph.auto", "1")
		w.git(w.root, "commit", "-q", "--allow-empty", "-m", "maintenance pressure")
		if w.exists(".git", "objects", "info", "commit-graphs") {
			t.Error("fixture commit ran automatic maintenance")
		}
		before := w.fingerprint()
		out, code := w.run("worktrees", "ls")
		wantExit(t, code, 0, out)
		w.wantUnchanged(before)

		// Prove the same fixture really triggers maintenance when enabled.
		w.git(w.root, "-c", "maintenance.auto=true", "commit", "-q", "--allow-empty", "-m", "positive control")
		if !w.exists(".git", "objects", "info", "commit-graphs") {
			t.Fatal("positive control did not write a commit graph")
		}
	})

	t.Run("automatic garbage collection", func(t *testing.T) {
		w := newWTWorld(t)
		// Git estimates loose-object pressure by sampling the 17 directory.
		// Two objects there exceed gc.auto=1, without thousands of git calls.
		blob := filepath.Join(w.home, "loose-blob")
		for n, written := 0, 0; written < 2; n++ {
			content := fmt.Sprintf("housekeeping fixture %d\n", n)
			hash := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
			if hash[0] != 0x17 {
				continue
			}
			w.write(blob, content)
			w.git(w.root, "hash-object", "-w", blob)
			written++
		}
		w.git(w.root, "config", "gc.auto", "1")
		w.git(w.root, "config", "gc.autoDetach", "false")
		before := w.fingerprint()
		w.git(w.root, "gc", "--auto")
		w.wantUnchanged(before)

		// Explicit command options can still enable GC for the positive control.
		w.git(w.root, "-c", "gc.auto=1", "gc", "--auto")
		packs, err := filepath.Glob(filepath.Join(w.root, ".git", "objects", "pack", "*.pack"))
		if err != nil || len(packs) == 0 {
			t.Fatalf("positive control did not pack objects: %v, %v", packs, err)
		}
	})
}

// tmuxReset forgets the fake tmux's configured answers and its call counters.
func (w *wtWorld) tmuxReset() {
	w.t.Helper()
	if err := os.RemoveAll(w.tmuxState); err != nil {
		w.t.Fatal(err)
	}
	if err := os.MkdirAll(w.tmuxState, 0o755); err != nil {
		w.t.Fatal(err)
	}
}

// tmuxFails makes every tmux call fail with stderr text.
func (w *wtWorld) tmuxFails(stderr string) {
	w.t.Helper()
	w.tmuxReset()
	w.write(filepath.Join(w.tmuxState, "fail"), stderr+"\n")
}

// tmuxServes makes the n-th window-name listing answer windows[n-1] and the n-th
// path listing answer paths[n-1] (the last answer repeats), each as "%id<TAB>v"
// rows.
func (w *wtWorld) tmuxServes(windows, paths [][]string) {
	w.t.Helper()
	w.tmuxReset()
	for kind, answers := range map[string][][]string{"w": windows, "p": paths} {
		for i, vals := range answers {
			var rows strings.Builder
			for j, v := range vals {
				fmt.Fprintf(&rows, "%%%d\t%s\n", j+1, v)
			}
			w.write(filepath.Join(w.tmuxState, kind+"."+strconv.Itoa(i+1)), rows.String())
			if i == len(answers)-1 {
				w.write(filepath.Join(w.tmuxState, kind+".last"), rows.String())
			}
		}
	}
}

// tmuxWindows is tmuxServes with one fixed answer: one pane per window name, all
// sitting in a directory outside every worktree.
func (w *wtWorld) tmuxWindows(names ...string) {
	w.t.Helper()
	cwds := make([]string, len(names))
	for i := range cwds {
		cwds[i] = "/home/dev"
	}
	w.tmuxServes([][]string{names}, [][]string{cwds})
}

func (w *wtWorld) tmuxCalls() int {
	w.t.Helper()
	data, err := os.ReadFile(w.tmuxLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		w.t.Fatal(err)
	}
	return len(strings.Fields(string(data))) // any logged call has at least one word
}

// ghIssues makes the fake gh report each ticket as an issue in the given state.
func (w *wtWorld) ghIssues(open, closed []int) {
	w.t.Helper()
	var aliases []string
	for state, nums := range map[string][]int{"OPEN": open, "CLOSED": closed} {
		for _, n := range nums {
			aliases = append(aliases, fmt.Sprintf(`"n%d":{"__typename":"Issue","state":%q,"timelineItems":{"nodes":[]},`+
				`"closedByPullRequestsReferences":{"pageInfo":{"hasNextPage":false},"nodes":[]}}`, n, state))
		}
	}
	w.write(w.ghAnswer, `{"data":{"repository":{"defaultBranchRef":{"name":"main"},`+strings.Join(aliases, ",")+`}}}`)
}

// addWT creates .worktrees/<name> on a branch of the same name, at main's HEAD.
func (w *wtWorld) addWT(name string) string {
	w.t.Helper()
	path := filepath.Join(w.root, ".worktrees", name)
	w.git(w.root, "worktree", "add", "-q", "-b", name, path)
	return path
}

// asForeign re-registers the named worktrees as if created on the other side of
// the sandbox mount: git's admin gitdir and the worktree's .git file both point
// under a root that does not exist here, which is how a container-created
// worktree looks from the host. Call it before age, which resets mtimes.
func (w *wtWorld) asForeign(names ...string) string {
	w.t.Helper()
	foreign := filepath.Join(filepath.Dir(w.root), "container-repo")
	for _, name := range names {
		w.write(filepath.Join(w.root, ".git", "worktrees", name, "gitdir"), filepath.Join(foreign, ".worktrees", name, ".git")+"\n")
		w.write(filepath.Join(w.root, ".worktrees", name, ".git"), "gitdir: "+filepath.Join(foreign, ".git", "worktrees", name)+"\n")
	}
	return foreign
}

// babysitStateFile is where babysit keeps the state of a supervisor for a PR.
func (w *wtWorld) babysitStateFile() string {
	return filepath.Join(w.stateHome, "cenci", "babysit", "abc123-790.json")
}

// babysitState is the state a supervisor still arming leaves (PID 0, no process
// to check) for a PR with pending CI that closes the ticket.
func babysitState(ticket int) string {
	return fmt.Sprintf(`{"schemaVersion":2,"pr":"790","repo":"acme/widgets","agent":"claude","closingIssues":[%d],`+
		`"ciStatus":"pending","status":"arming","pid":0,"updatedAt":%q}`, ticket, time.Now().UTC().Format(time.RFC3339Nano))
}

// age makes the named worktrees (and their git admin dirs) look idle for wtIdle.
func (w *wtWorld) age(names ...string) {
	w.t.Helper()
	when := time.Now().Add(-wtIdle)
	for _, name := range names {
		for _, root := range []string{filepath.Join(w.root, ".worktrees", name), filepath.Join(w.root, ".git", "worktrees", name)} {
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.Type()&fs.ModeSymlink != 0 {
					return err
				}
				return os.Chtimes(p, when, when)
			})
			if err != nil {
				w.t.Fatal(err)
			}
		}
	}
}

// mixedRow is what `ls` shows for one worktree of the mixed world: the verdict,
// and for a keep the reason label.
type mixedRow struct{ name, verdict, reason string }

// mixedRows is the verdict table of newMixedWorld.
var mixedRows = []mixedRow{
	{"stale-clean", "stale", ""},
	{"21-ticketed", "stale", ""},
	{"52-done", "finished", ""},
	{"dirty-wt", "keep", "dirty"},
	{"untracked-wt", "keep", "untracked"},
	{"unpushed-wt", "keep", "unpushed"},
	{"active-wt", "keep", "active"},
	{"31-in-use", "keep", "in use"},
	{"41-midstage", "keep", "pipeline mid-stage"},
	{"53-fresh-done", "keep", "recently active"},
	{"gone-wt", "keep", "unmapped"},
}

// newMixedWorld has one worktree per row of mixedRows: two stale ones, a finished
// one (its issue is closed), and a keep for each reason. tmux reports a live
// window for ticket 31.
func newMixedWorld(t *testing.T) *wtWorld {
	t.Helper()
	w := newWTWorld(t)
	for _, name := range []string{"stale-clean", "21-ticketed", "52-done", "dirty-wt", "untracked-wt", "unpushed-wt",
		"active-wt", "31-in-use", "41-midstage", "53-fresh-done", "gone-wt"} {
		w.addWT(name)
	}
	wt := func(name string) string { return filepath.Join(w.root, ".worktrees", name) }
	w.write(filepath.Join(wt("dirty-wt"), "tracked.txt"), "edited\n")
	w.write(filepath.Join(wt("untracked-wt"), "new.txt"), "new\n")
	w.write(filepath.Join(wt("unpushed-wt"), "work.txt"), "unpushed work\n")
	w.git(wt("unpushed-wt"), "add", "work.txt")
	w.git(wt("unpushed-wt"), "commit", "-q", "-m", "unpushed")
	w.write(filepath.Join(w.root, ".cenci", "pipeline", "41.json"),
		fmt.Sprintf(`{"schemaVersion":1,"id":"41","stage":"execute","updatedAt":%q}`, time.Now().UTC().Format(time.RFC3339Nano)))
	if err := os.RemoveAll(wt("gone-wt")); err != nil {
		t.Fatal(err)
	}
	w.age("stale-clean", "21-ticketed", "52-done", "dirty-wt", "untracked-wt", "unpushed-wt", "31-in-use", "41-midstage")
	w.ghIssues([]int{21, 31, 41}, []int{52, 53})
	w.tmuxWindows("31-implement")
	return w
}

// env is the child environment, built from scratch: CENCI_SANDBOX is ambient in
// the dev container and must be cleared, TMUX/TMUX_PANE must not leak, and
// everything that locates state (sockets, XDG dirs, TMPDIR, gh credentials) is
// private to the world.
func (w *wtWorld) env(extra ...string) []string {
	env := []string{
		"PATH=" + w.binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + w.home, "XDG_STATE_HOME=" + w.stateHome, "XDG_CONFIG_HOME=" + w.cfgHome, "TMPDIR=" + w.tmpDir,
		"CENCI_SOCKET_DIR=" + w.sockDir, "CENCI_SANDBOX=", "TMUX=", "TMUX_PANE=",
		"GH_TOKEN=", "GITHUB_TOKEN=", "GH_ENTERPRISE_TOKEN=", "GITHUB_ENTERPRISE_TOKEN=", "GH_HOST=",
		"GH_CONFIG_DIR=" + filepath.Join(w.home, "gh"),
	}
	env = append(env, wtGitConfigEnv()...)
	return append(env, extra...)
}

// runIn runs the binary in dir and returns its combined output and exit code.
func (w *wtWorld) runIn(dir string, env []string, args ...string) (string, int) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(out), exitErr.ExitCode()
	}
	if err != nil {
		w.t.Fatalf("running cenci %v: %v\n%s", args, err, out)
	}
	return string(out), 0
}

func (w *wtWorld) run(args ...string) (string, int) {
	w.t.Helper()
	return w.runIn(w.root, w.env(), args...)
}

// fingerprint records every path under the repo (type, mode, size, mtime) plus
// git's own view of worktrees and refs.
func (w *wtWorld) fingerprint() map[string]string {
	w.t.Helper()
	fp := map[string]string{}
	err := filepath.WalkDir(w.root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		fp[p] = fmt.Sprintf("%v size=%d mtime=%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	fp["git worktree list"] = w.git(w.root, "worktree", "list", "--porcelain")
	fp["git for-each-ref"] = w.git(w.root, "for-each-ref")
	return fp
}

func (w *wtWorld) wantUnchanged(before map[string]string) {
	w.t.Helper()
	after := w.fingerprint()
	for k, b := range before {
		if a, ok := after[k]; !ok {
			w.t.Errorf("removed: %s", k)
		} else if a != b {
			w.t.Errorf("changed: %s\n  before: %s\n  after:  %s", k, b, a)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			w.t.Errorf("added: %s", k)
		}
	}
}

func (w *wtWorld) exists(rel ...string) bool {
	_, err := os.Lstat(filepath.Join(append([]string{w.root}, rel...)...))
	return err == nil
}

func (w *wtWorld) branchExists(name string) bool {
	cmd := exec.Command("git", "-C", w.root, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	cmd.Env = w.gitEnv
	return cmd.Run() == nil
}

// wantLine fails unless some line of out matches the regexp pattern; it returns
// the first matching line.
func wantLine(t *testing.T, out, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + pattern + `$`)
	if m := re.FindString(out); m != "" {
		return m
	}
	t.Errorf("no line matches /%s/ in:\n%s", pattern, out)
	return ""
}

func wantExit(t *testing.T, got, want int, out string) {
	t.Helper()
	if got != want {
		t.Fatalf("exit code = %d, want %d\n%s", got, want, out)
	}
}

func TestWorktreesLs_ListsEveryWorktreeWithVerdictReasonAndSize(t *testing.T) {
	w := newMixedWorld(t)

	out, code := w.run("worktrees", "ls")

	wantExit(t, code, 0, out)
	wantLine(t, out, `NAME\s+VERDICT\s+REASON\s+SIZE`)
	for _, r := range mixedRows {
		pattern := regexp.QuoteMeta(r.name) + `\s+` + r.verdict + `\s+.*`
		if r.reason != "" {
			pattern = regexp.QuoteMeta(r.name) + `\s+keep\s+` + regexp.QuoteMeta(r.reason) + `(?:\s{2,}.*)?`
		}
		if line := wantLine(t, out, pattern); line != "" && r.reason != "unmapped" {
			if !regexp.MustCompile(wtSize + `$`).MatchString(line) {
				t.Errorf("row %q does not end with a size: want /%s/", line, wtSize)
			}
		}
	}
	if strings.Contains(out, wtNoteUnk) {
		t.Errorf("in-use status is readable here, so no row may carry %q:\n%s", wtNoteUnk, out)
	}
	// The cause behind a row's label is printed below the table.
	wantLine(t, out, `note: gone-wt: worktreeclean: entry is unmapped: worktree-missing \(admin entry "gone-wt"\)`)
	if w.tmuxCalls() == 0 {
		t.Error("tmux was never read: the in-use verdicts above prove nothing")
	}
}

func TestWorktreesPruneDryRun_PrintsThePlanAndChangesNothing(t *testing.T) {
	w := newMixedWorld(t)
	before := w.fingerprint()

	out, code := w.run("worktrees", "prune", "--dry-run")

	wantExit(t, code, 0, out)
	wantLine(t, out, `would remove stale-clean: stale, `+wtSize)
	wantLine(t, out, `would remove 21-ticketed: stale, `+wtSize)
	wantLine(t, out, `would remove 52-done: finished, `+wtSize)
	for _, r := range mixedRows {
		if r.verdict == "keep" {
			wantLine(t, out, `keep `+regexp.QuoteMeta(r.name)+`: `+regexp.QuoteMeta(r.reason)+`\b.*`)
		}
	}
	if n := strings.Count(out, "would remove "); n != 3 {
		t.Errorf("%d `would remove` lines, want exactly 3:\n%s", n, out)
	}
	if strings.Contains(out, "removed ") {
		t.Errorf("a dry run must not report a removal:\n%s", out)
	}
	w.wantUnchanged(before)
}

func TestWorktreesPrune_RemovesOnlyTheRemovableAndReportsBytesFreed(t *testing.T) {
	w := newMixedWorld(t)

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	for _, name := range []string{"stale-clean", "21-ticketed", "52-done"} {
		verdict := "stale"
		if name == "52-done" {
			verdict = "finished"
		}
		line := wantLine(t, out, `removed `+regexp.QuoteMeta(name)+`: `+verdict+`, freed `+wtSize+` \(\d+ bytes\)`)
		if n, _ := strconv.Atoi(regexp.MustCompile(`\((\d+) bytes\)`).FindStringSubmatch(line)[1]); n <= 0 {
			t.Errorf("%s: freed %d bytes, want a positive size", name, n)
		}
		if w.exists(".worktrees", name) || w.exists(".git", "worktrees", name) {
			t.Errorf("%s was reported removed but is still on disk", name)
		}
	}
	if w.branchExists("52-done") {
		t.Error("the finished worktree's branch must go with it")
	}
	if !w.branchExists("stale-clean") {
		t.Error("a stale worktree's branch must be kept so it can be restored")
	}
	for _, r := range mixedRows {
		if r.verdict != "keep" {
			continue
		}
		wantLine(t, out, `kept `+regexp.QuoteMeta(r.name)+`: `+regexp.QuoteMeta(r.reason)+`\b.*`)
		kept := w.exists(".worktrees", r.name)
		if r.reason == "unmapped" {
			kept = w.exists(".git", "worktrees", r.name)
		}
		if !kept {
			t.Errorf("%s is kept (%s) but was deleted", r.name, r.reason)
		}
	}

	again, code := w.run("worktrees", "prune")
	wantExit(t, code, 0, again)
	if strings.Contains(again, "removed ") {
		t.Errorf("a second prune has nothing left to remove, got:\n%s", again)
	}
}

// With no tmux server there are no windows, so nothing is in use: prune goes on
// (and 31-in-use, whose only blocker was its window, is now removable).
func TestWorktreesPrune_NoTmuxServerMeansNothingIsInUse(t *testing.T) {
	w := newMixedWorld(t)
	w.tmuxFails(wtNoServer)

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	wantLine(t, out, `removed 31-in-use: stale, .*`)
	wantLine(t, out, `removed stale-clean: stale, .*`)
	if w.exists(".worktrees", "31-in-use") {
		t.Error("31-in-use should have been removed")
	}
	if w.tmuxCalls() == 0 {
		t.Error("tmux was never read: the no-server answer was not what allowed the removal")
	}
}

// When tmux cannot be read (a failure other than "no server running", or the
// sandbox, where host tmux is out of reach) the in-use state is unknown: prune
// removes nothing, while ls and --dry-run still classify and say so.
func TestWorktrees_FailClosedWhenInUseStatusIsUnreadable(t *testing.T) {
	removable := []string{"stale-clean", "21-ticketed", "52-done"}
	tests := []struct {
		name      string
		tmuxFail  string
		sandbox   bool
		wantTmux  bool // the fake tmux must have been consulted
		wantNoTmx bool // and, in the sandbox, must not have been
	}{
		{name: "tmux cannot connect", tmuxFail: "error connecting to /tmp/tmux-1000/default (No such file or directory)", wantTmux: true},
		{name: "tmux protocol mismatch", tmuxFail: "protocol version mismatch (client 8, server 7)", wantTmux: true},
		{name: "inside the sandbox", tmuxFail: wtNoServer, sandbox: true, wantNoTmx: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newMixedWorld(t)
			w.tmuxFails(tt.tmuxFail)
			env := w.env()
			if tt.sandbox {
				env = w.env("CENCI_SANDBOX=1")
			}
			before := w.fingerprint()

			ls, code := w.runIn(w.root, env, "worktrees", "ls")
			wantExit(t, code, 0, ls)
			dry, code := w.runIn(w.root, env, "worktrees", "prune", "--dry-run")
			wantExit(t, code, 0, dry)
			out, code := w.runIn(w.root, env, "worktrees", "prune")
			wantExit(t, code, 0, out)

			for _, name := range removable {
				verdict := "stale"
				if name == "52-done" {
					verdict = "finished"
				}
				row := wantLine(t, ls, regexp.QuoteMeta(name)+`\s+`+verdict+`\s+.*`)
				if !strings.Contains(row, wtNoteUnk) {
					t.Errorf("ls row %q lacks %q", row, wtNoteUnk)
				}
				plan := wantLine(t, dry, `would remove `+regexp.QuoteMeta(name)+`: `+verdict+`, .*`)
				if !strings.Contains(plan, wtNoteUnk) {
					t.Errorf("dry-run line %q lacks %q", plan, wtNoteUnk)
				}
				wantLine(t, out, `kept `+regexp.QuoteMeta(name)+`: in-use status unknown\b.*`)
			}
			if strings.Contains(out, "removed ") {
				t.Errorf("prune must remove nothing when in-use status is unknown:\n%s", out)
			}
			if row := wantLine(t, ls, `dirty-wt\s+keep\s+.*`); strings.Contains(row, wtNoteUnk) {
				t.Errorf("a worktree that is kept anyway carries no unknown note: %q", row)
			}
			w.wantUnchanged(before)
			switch {
			case tt.wantTmux && w.tmuxCalls() == 0:
				t.Error("tmux was never read: the failure under test was not exercised")
			case tt.wantNoTmx && w.tmuxCalls() != 0:
				t.Error("inside the sandbox host tmux must not be consulted at all")
			}
		})
	}
}

// The guards are re-checked right before each removal: a window that opens after
// the pass classified its worktrees keeps the one it belongs to.
func TestWorktreesPrune_RechecksInUseRightBeforeEachRemoval(t *testing.T) {
	w := newMixedWorld(t)
	w.tmuxServes(
		[][]string{{"scratch"}, {"21-implement"}},
		[][]string{{"/home/dev"}, {"/home/dev"}})

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	wantLine(t, out, `kept 21-ticketed: in use\b.*`)
	if !w.exists(".worktrees", "21-ticketed") {
		t.Error("21-ticketed got a window after classification and must survive")
	}
	wantLine(t, out, `removed stale-clean: stale, .*`)
	if w.exists(".worktrees", "stale-clean") {
		t.Error("stale-clean has no live window and should be removed")
	}
}

// A SIGTERM during the pass (here delivered by the fake tmux, whose parent is
// the CLI) stops it: nothing is removed and the exit is 1, not a clean 0.
func TestWorktreesPrune_InterruptedPassExits1AndRemovesNothing(t *testing.T) {
	w := newMixedWorld(t)
	w.script("tmux", "kill -TERM $PPID\nsleep 1\necho \""+wtNoServer+"\" >&2\nexit 1\n")
	before := w.fingerprint()

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 1, out)
	wantLine(t, out, `cenci worktrees prune: interrupted before the pass finished.*`)
	w.wantUnchanged(before)
}

// startDaemonSocket serves one snapshot on the socket the CLI will dial.
func startDaemonSocket(t *testing.T, w *wtWorld, windows ...watch.WindowState) {
	t.Helper()
	srv, err := ipc.NewServer(filepath.Join(w.sockDir, "cenci.sock"))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Accept(ctx)
	srv.Broadcast(watch.StateSnapshot{Windows: windows}) // cached, and replayed to every new client
}

func TestWorktreesLs_AppliesTheDaemonSnapshot(t *testing.T) {
	w := newWTWorld(t)
	names := []string{"61-busy", "62-tracked", "63-failed", "64-quiet"}
	for _, name := range names {
		w.addWT(name)
	}
	w.age(names...)
	w.ghIssues([]int{61, 62, 63, 64}, nil)
	startDaemonSocket(t, w,
		watch.WindowState{WindowName: "61-implement", Status: "running"},
		watch.WindowState{WindowName: "62-implement", Status: "done"},
		watch.WindowState{WindowName: "63-implement", Status: "failed"})

	out, code := w.run("worktrees", "ls")

	wantExit(t, code, 0, out)
	wantLine(t, out, `61-busy\s+keep\s+in use\b.*`)
	wantLine(t, out, `62-tracked\s+keep\s+in use\b.*`)
	wantLine(t, out, `63-failed\s+stale\s+.*`)
	wantLine(t, out, `64-quiet\s+stale\s+.*`)
	if strings.Contains(out, "daemon snapshot unavailable") {
		t.Errorf("the daemon was reachable, so no unavailable note is expected:\n%s", out)
	}
}

// serveMalformedSnapshot listens on the socket the CLI will dial and answers
// every connection with a line that is not a snapshot. The socket lives in
// w.sockDir, which is short enough for a unix socket path.
func serveMalformedSnapshot(t *testing.T, w *wtWorld) {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(w.sockDir, "cenci.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("not json\n"))
			_ = conn.Close()
		}
	}()
}

// A daemon that answers but cannot be read is not a daemon that is not running:
// the snapshot is the only source for renamed tracked windows and busy sessions,
// so prune keeps what it cannot rule out (#1300) while ls and --dry-run still
// show the real verdict and say why prune will not act on it.
func TestWorktreesPrune_MalformedDaemonSnapshotFailsClosed(t *testing.T) {
	w := newWTWorld(t)
	w.addWT("stale-clean")
	w.age("stale-clean")
	serveMalformedSnapshot(t, w)
	before := w.fingerprint()

	ls, code := w.run("worktrees", "ls")
	wantExit(t, code, 0, ls)
	dry, code := w.run("worktrees", "prune", "--dry-run")
	wantExit(t, code, 0, dry)

	if row := wantLine(t, ls, `stale-clean\s+stale\s+.*`); !strings.Contains(row, wtNoteUnk) {
		t.Errorf("ls row %q lacks %q", row, wtNoteUnk)
	}
	if plan := wantLine(t, dry, `would remove stale-clean: stale, .*`); !strings.Contains(plan, wtNoteUnk) {
		t.Errorf("dry-run line %q lacks %q", plan, wtNoteUnk)
	}
	for name, out := range map[string]string{"ls": ls, "--dry-run": dry} {
		wantLine(t, out, `note: in-use status unknown: read daemon snapshot: .*`)
		if strings.Contains(out, "daemon snapshot unavailable") {
			t.Errorf("%s: a malformed reply is not an absent daemon, so the benign note must not appear:\n%s", name, out)
		}
	}
	w.wantUnchanged(before)

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	wantLine(t, out, `kept stale-clean: in-use status unknown\b.*`)
	if strings.Contains(out, "removed ") {
		t.Errorf("prune must remove nothing when the daemon snapshot cannot be read:\n%s", out)
	}
	if !w.exists(".worktrees", "stale-clean") {
		t.Error("stale-clean must stay on disk")
	}
}

// A live tmux window that names the ticket is a definite "in use" even when the
// daemon's reply cannot be read: the row is a keep for its real reason, not a
// removable one carrying the unknown note (#1300).
func TestWorktreesLs_MalformedDaemonSnapshotStillShowsAWindowedTicketInUse(t *testing.T) {
	w := newWTWorld(t)
	w.addWT("31-in-use")
	w.age("31-in-use")
	w.ghIssues([]int{31}, nil)
	w.tmuxWindows("31-implement")
	serveMalformedSnapshot(t, w)

	out, code := w.run("worktrees", "ls")

	wantExit(t, code, 0, out)
	row := wantLine(t, out, `31-in-use\s+keep\s+in use\b.*`)
	if strings.Contains(row, wtNoteUnk) || strings.Contains(out, wtNoteUnk) {
		t.Errorf("a windowed ticket is in use, not unknown, so no row may carry %q:\n%s", wtNoteUnk, out)
	}
	if w.tmuxCalls() == 0 {
		t.Error("tmux was never read: the in-use verdict above proves nothing")
	}
}

// With no daemon running prune still runs, notes that only tmux windows and pane
// directories were checked, and removes a stale clean worktree: an absent daemon
// is benign, unlike a daemon whose reply cannot be read.
func TestWorktreesPrune_NoDaemonNotesTheLimitedCheckAndStillRemoves(t *testing.T) {
	w := newWTWorld(t)
	w.addWT("stale-clean")
	w.age("stale-clean")

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	if !strings.Contains(out, "daemon snapshot unavailable") ||
		!strings.Contains(out, "only tmux windows and pane directories are checked") {
		t.Errorf("prune output lacks the daemon-unavailable note:\n%s", out)
	}
	wantLine(t, out, `removed stale-clean: stale, freed `+wtSize+` \(\d+ bytes\)`)
	if w.exists(".worktrees", "stale-clean") {
		t.Error("stale-clean should have been removed: a daemon that is not running is benign")
	}
}

func TestWorktrees_NoteWhatCouldNotBeReadAndHonorConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    string // .cenci/config.json; "" = none
		noGh      bool
		args      []string
		wantExit  int
		wantLines []string
		wantNote  string
		wantGone  bool // stale-clean must have been removed
		wantKept  bool // both worktrees must still exist
	}{
		{name: "no daemon", args: []string{"worktrees", "ls"}, wantLines: []string{`stale-clean\s+stale\s+.*`}, wantNote: "daemon snapshot unavailable"},
		{name: "ticket state unreadable", noGh: true, args: []string{"worktrees", "ls"},
			wantLines: []string{`21-ticketed\s+stale\s+.*`}, wantNote: "ticket state unavailable"},
		{name: "cleanup disabled does not stop a manual prune", config: `{"worktreeCleanup":{"enabled":false}}`,
			args: []string{"worktrees", "prune"}, wantLines: []string{`removed stale-clean: stale, .*`}, wantNote: "worktreeCleanup.enabled is false", wantGone: true},
		{name: "staleAfter is honored", config: `{"worktreeCleanup":{"staleAfter":"90d"}}`,
			args: []string{"worktrees", "ls"}, wantLines: []string{`stale-clean\s+keep\s+active\b.*`}},
		{name: "malformed config stops ls", config: `{"worktreeCleanup":{"staleAfter":"soon"}}`,
			args: []string{"worktrees", "ls"}, wantExit: 1, wantNote: "config.json"},
		{name: "malformed config stops prune", config: `{"worktreeCleanup":{"staleAfter":"soon"}}`,
			args: []string{"worktrees", "prune"}, wantExit: 1, wantNote: "config.json", wantKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWTWorld(t)
			w.addWT("stale-clean")
			w.addWT("21-ticketed")
			w.age("stale-clean", "21-ticketed")
			if !tt.noGh {
				w.ghIssues([]int{21}, nil)
			}
			if tt.config != "" {
				w.write(filepath.Join(w.root, ".cenci", "config.json"), tt.config)
			}

			out, code := w.run(tt.args...)

			wantExit(t, code, tt.wantExit, out)
			for _, pattern := range tt.wantLines {
				wantLine(t, out, pattern)
			}
			if tt.wantNote != "" && !strings.Contains(out, tt.wantNote) {
				t.Errorf("output lacks the note %q:\n%s", tt.wantNote, out)
			}
			if tt.wantGone && w.exists(".worktrees", "stale-clean") {
				t.Error("stale-clean should have been removed")
			}
			if tt.wantKept && (!w.exists(".worktrees", "stale-clean") || !w.exists(".worktrees", "21-ticketed")) {
				t.Error("nothing may be removed when the config cannot be trusted")
			}
		})
	}
}

func TestWorktrees_UsageErrorsExit2(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"worktrees"}, "cenci worktrees: expected a subcommand"},
		{[]string{"worktrees", "--dry-run"}, "cenci worktrees: expected a subcommand"},
		{[]string{"worktrees", "bogus"}, `cenci worktrees: unknown subcommand "bogus"`},
		{[]string{"worktrees", "ls", "extra"}, `unexpected argument "extra"`},
		{[]string{"worktrees", "ls", "--dry-run"}, "dry-run"},
		{[]string{"worktrees", "prune", "--bogus"}, "bogus"},
		{[]string{"worktrees", "prune", "extra"}, `unexpected argument "extra"`},
		{[]string{"worktrees", "prune", "--dry-run", "extra"}, `unexpected argument "extra"`},
	}
	w := newMixedWorld(t)
	before := w.fingerprint()
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			out, code := w.run(tt.args...)

			wantExit(t, code, 2, out)
			if !strings.Contains(out, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, out)
			}
		})
	}
	w.wantUnchanged(before)
}

func TestWorktrees_OutsideARepoExits1(t *testing.T) {
	w := newWTWorld(t)

	for _, verb := range [][]string{{"worktrees", "ls"}, {"worktrees", "prune"}, {"worktrees", "prune", "--dry-run"}} {
		out, code := w.runIn(t.TempDir(), w.env(), verb...)

		wantExit(t, code, 1, out)
		if !strings.Contains(out, "cenci worktrees") {
			t.Errorf("%v: error output should name the command, got:\n%s", verb, out)
		}
	}
}

// newTicketedWorld has an idle ticketless worktree and an idle worktree of the
// open ticket 21, both removable until something guards them.
func newTicketedWorld(t *testing.T) *wtWorld {
	t.Helper()
	w := newWTWorld(t)
	w.addWT("stale-clean")
	w.addWT("21-ticketed")
	w.age("stale-clean", "21-ticketed")
	w.ghIssues([]int{21}, nil)
	return w
}

// An armed babysit supervisor for the ticket keeps its worktree, through the
// production babysit.BlocksCloseStrict, on ls, --dry-run and prune alike.
func TestWorktrees_ArmedBabysitKeepsTheTicketsWorktree(t *testing.T) {
	w := newTicketedWorld(t)
	w.write(w.babysitStateFile(), babysitState(21))
	before := w.fingerprint()

	ls, code := w.run("worktrees", "ls")
	wantExit(t, code, 0, ls)
	dry, code := w.run("worktrees", "prune", "--dry-run")
	wantExit(t, code, 0, dry)

	wantLine(t, ls, `21-ticketed\s+keep\s+babysit armed\b.*`)
	wantLine(t, ls, `stale-clean\s+stale\s+.*`)
	wantLine(t, dry, `keep 21-ticketed: babysit armed\b.*`)
	wantLine(t, dry, `would remove stale-clean: stale, .*`)
	w.wantUnchanged(before)

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	wantLine(t, out, `kept 21-ticketed: babysit armed\b.*`)
	wantLine(t, out, `removed stale-clean: stale, .*`)
	if !w.exists(".worktrees", "21-ticketed") {
		t.Error("21-ticketed has an armed babysit and must survive")
	}
}

// A babysit state file that cannot be decoded cannot be tied to any ticket, so
// every ticketed worktree is kept as unknown (#1300) on ls, --dry-run and prune
// alike; the ticketless one is never asked and is judged on its own facts.
func TestWorktrees_UnreadableBabysitStateKeepsTicketedWorktrees(t *testing.T) {
	w := newTicketedWorld(t)
	w.write(w.babysitStateFile(), "{not json")
	before := w.fingerprint()

	ls, code := w.run("worktrees", "ls")
	wantExit(t, code, 0, ls)
	dry, code := w.run("worktrees", "prune", "--dry-run")
	wantExit(t, code, 0, dry)

	wantLine(t, ls, `21-ticketed\s+keep\s+babysit status unknown\b.*`)
	wantLine(t, ls, `stale-clean\s+stale\s+.*`)
	wantLine(t, dry, `keep 21-ticketed: babysit status unknown\b.*`)
	wantLine(t, dry, `would remove stale-clean: stale, .*`)
	for name, out := range map[string]string{"ls": ls, "--dry-run": dry} {
		if note := wantLine(t, out, `note: 21-ticketed: babysit guard for 21-ticketed: .*`); !strings.Contains(note, w.babysitStateFile()) {
			t.Errorf("%s: cause line %q does not name the state file %s", name, note, w.babysitStateFile())
		}
	}
	w.wantUnchanged(before)

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	wantLine(t, out, `kept 21-ticketed: babysit status unknown\b.*`)
	wantLine(t, out, `note: 21-ticketed: babysit guard for 21-ticketed: .*`)
	wantLine(t, out, `removed stale-clean: stale, .*`)
	if !w.exists(".worktrees", "21-ticketed") {
		t.Error("21-ticketed has an unreadable babysit state and must survive")
	}
	if w.exists(".worktrees", "stale-clean") {
		t.Error("stale-clean is ticketless, never asks babysit, and should be removed")
	}
}

// A babysit that arms after the pass classified its worktrees (here: on the
// fake tmux's third call, the first read of the per-removal re-check) still keeps
// the ticket's worktree.
func TestWorktreesPrune_RechecksBabysitRightBeforeEachRemoval(t *testing.T) {
	w := newTicketedWorld(t)
	staged, counter := filepath.Join(w.home, "babysit-staged.json"), filepath.Join(w.home, "tmux-calls")
	w.write(staged, babysitState(21))
	if err := os.MkdirAll(filepath.Dir(w.babysitStateFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	q := exectest.ShellQuote
	w.script("tmux", "n=$(cat "+q(counter)+" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "+q(counter)+"\n"+
		"case \"$*\" in *pane_current_path*) printf '%%1\\t/home/dev\\n';; *) printf '%%1\\tscratch\\n';; esac\n"+
		"if [ \"$n\" -ge 3 ]; then cp "+q(staged)+" "+q(w.babysitStateFile())+"; fi\n")

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	wantLine(t, out, `kept 21-ticketed: babysit armed\b.*`)
	wantLine(t, out, `removed stale-clean: stale, .*`)
	if !w.exists(".worktrees", "21-ticketed") {
		t.Error("21-ticketed got an armed babysit after classification and must survive")
	}
	if data, err := os.ReadFile(counter); err != nil || len(data) == 0 {
		t.Errorf("the fake tmux never ran (%v): the babysit was armed before classification, so the re-check proved nothing", err)
	} else if n, _ := strconv.Atoi(strings.TrimSpace(string(data))); n < 3 {
		t.Errorf("tmux ran %d times, want the re-check's read (call 3) to have armed the babysit", n)
	}
}

// Worktrees registered on the other side of the sandbox mount (their git
// pointers name a root that is not here) are still classified, previewed and
// removed through their local path.
func TestWorktrees_TranslatedContainerPathWorktreesAreClassified(t *testing.T) {
	w := newWTWorld(t)
	w.addWT("stale-clean")
	w.addWT("52-done")
	foreign := w.asForeign("stale-clean", "52-done")
	w.age("stale-clean", "52-done")
	w.ghIssues(nil, []int{52})
	before := w.fingerprint()

	ls, code := w.run("worktrees", "ls")
	wantExit(t, code, 0, ls)
	dry, code := w.run("worktrees", "prune", "--dry-run")
	wantExit(t, code, 0, dry)

	wantLine(t, ls, `stale-clean\s+stale\s+-\s+`+wtSize)
	wantLine(t, ls, `52-done\s+finished\s+-\s+`+wtSize)
	wantLine(t, dry, `would remove stale-clean: stale, `+wtSize)
	wantLine(t, dry, `would remove 52-done: finished, `+wtSize)
	w.wantUnchanged(before)

	out, code := w.run("worktrees", "prune")

	wantExit(t, code, 0, out)
	wantLine(t, out, `removed stale-clean: stale, freed `+wtSize+` \(\d+ bytes\)`)
	wantLine(t, out, `removed 52-done: finished, freed `+wtSize+` \(\d+ bytes\)`)
	if w.exists(".worktrees", "stale-clean") || w.exists(".worktrees", "52-done") {
		t.Error("translated worktrees should have been removed through their local path")
	}
	if _, err := os.Lstat(foreign); !os.IsNotExist(err) {
		t.Errorf("the foreign root %s must never be created (Lstat err = %v)", foreign, err)
	}
}

// A host without a tmux binary cannot say what is in use: prune removes nothing,
// while ls and --dry-run still classify and say so.
func TestWorktrees_MissingTmuxBinaryFailsClosed(t *testing.T) {
	w := newWTWorld(t)
	w.addWT("stale-clean")
	w.age("stale-clean")
	if err := os.Remove(filepath.Join(w.binDir, "tmux")); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitOnly := filepath.Join(w.home, "git-only")
	if err := os.MkdirAll(gitOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(gitOnly, "git")); err != nil {
		t.Fatal(err)
	}
	env := w.env("PATH=" + w.binDir + string(os.PathListSeparator) + gitOnly)

	ls, code := w.runIn(w.root, env, "worktrees", "ls")
	wantExit(t, code, 0, ls)
	out, code := w.runIn(w.root, env, "worktrees", "prune")
	wantExit(t, code, 0, out)

	if row := wantLine(t, ls, `stale-clean\s+stale\s+.*`); !strings.Contains(row, wtNoteUnk) {
		t.Errorf("ls row %q lacks %q", row, wtNoteUnk)
	}
	wantLine(t, out, `kept stale-clean: in-use status unknown\b.*`)
	if !w.exists(".worktrees", "stale-clean") {
		t.Error("nothing may be removed when tmux cannot be run")
	}
}

func TestUsage_ListsTheWorktreesCommands(t *testing.T) {
	out, err := exec.Command(binaryPath, "help").CombinedOutput()
	if err != nil {
		t.Fatalf("help: %v\n%s", err, out)
	}
	for _, want := range []string{"worktrees ls", "worktrees prune"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("usage lacks %q:\n%s", want, out)
		}
	}
}
