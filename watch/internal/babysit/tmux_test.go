package babysit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
)

// These tests exercise defaultCurrentTmuxSession/defaultTmuxHasSession
// directly (not the currentTmuxSession/tmuxHasSession seams every other
// package test stubs) via a PATH-shimmed fake `tmux`, mirroring
// gh_test.go/installFakeGH's technique -- the seam-stubbed tests elsewhere in
// this package never invoke the real bounded subprocess/exit-code parsing
// these defaults implement, so this is the only coverage for that behavior.

// installFakeTmux writes a fake `tmux` on a fresh PATH (replacing it
// wholly, matching gh_test.go's installFakeGH -- no shell-pipeline
// dependency exists in these scripts).
func installFakeTmux(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestDefaultTmuxHasSession_ExistingSessionReturnsTrue(t *testing.T) {
	installFakeTmux(t, `
case "$1" in
  has-session) [ "$3" = "=work" ] && exit 0 || exit 1 ;;
esac
`)
	ok, err := defaultTmuxHasSession("", "work")
	if err != nil {
		t.Fatalf("defaultTmuxHasSession: %v", err)
	}
	if !ok {
		t.Fatal("defaultTmuxHasSession = false, want true for an existing session")
	}
}

// TestDefaultTmuxHasSession_UsesExactMatchForm pins docs/tmux.md's rule: the
// probe must pass the leading `=` exact-match marker, not a bare session
// name (which would prefix-match a same-prefixed sibling).
func TestDefaultTmuxHasSession_UsesExactMatchForm(t *testing.T) {
	installFakeTmux(t, `
case "$1" in
  has-session) [ "$3" = "=work" ] && exit 0 || exit 1 ;;
esac
`)
	// A bare "work-2" target must NOT satisfy the "=work" exact-match check
	// the fake enforces above -- proving defaultTmuxHasSession("", "work-2")
	// asks for exactly "=work-2", not a prefix match against "work".
	ok, err := defaultTmuxHasSession("", "work-2")
	if err != nil {
		t.Fatalf("defaultTmuxHasSession: %v", err)
	}
	if ok {
		t.Fatal("defaultTmuxHasSession(\"work-2\") = true, want false: the fake only accepts an exact =work target")
	}
}

func TestDefaultTmuxHasSession_AbsentSessionReturnsFalseNilError(t *testing.T) {
	installFakeTmux(t, "exit 1\n")
	ok, err := defaultTmuxHasSession("", "gone")
	if err != nil {
		t.Fatalf("defaultTmuxHasSession: err = %v, want nil for an ordinary nonzero exit (no such session)", err)
	}
	if ok {
		t.Fatal("defaultTmuxHasSession = true, want false")
	}
}

// TestDefaultTmuxHasSession_StartFailureReturnsDistinctError pins
// watch/docs/error-handling.md's rule against collapsing "probe errored"
// into "condition false": a failure to even run tmux (binary unresolvable)
// must be a non-nil error, not silently folded into the negative-boolean
// case a nonzero exit produces.
func TestDefaultTmuxHasSession_StartFailureReturnsDistinctError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux binary anywhere on PATH
	ok, err := defaultTmuxHasSession("", "work")
	if err == nil {
		t.Fatal("defaultTmuxHasSession: err = nil, want a non-nil error when tmux itself cannot be started")
	}
	if ok {
		t.Fatal("defaultTmuxHasSession = true, want false alongside the start-failure error")
	}
}

func TestDefaultCurrentTmuxSession_NoTmuxPaneReturnsErrorWithoutSpawning(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	// No tmux binary resolvable at all: if defaultCurrentTmuxSession spawned
	// anything despite TMUX_PANE being unset, this would surface as an
	// "executable file not found" error rather than the TMUX_PANE-unset
	// message asserted below.
	t.Setenv("PATH", t.TempDir())
	_, err := defaultCurrentTmuxSession()
	if err == nil {
		t.Fatal("defaultCurrentTmuxSession: err = nil, want an error when TMUX_PANE is unset")
	}
	if !strings.Contains(err.Error(), "TMUX_PANE") {
		t.Fatalf("defaultCurrentTmuxSession err = %q, want it to name TMUX_PANE", err.Error())
	}
}

func TestDefaultCurrentTmuxSession_ResolvesSessionName(t *testing.T) {
	t.Setenv("TMUX_PANE", "%5")
	installFakeTmux(t, `
case "$1" in
  display-message) [ "$3" = "%5" ] && printf 'work' || exit 1 ;;
esac
`)
	session, err := defaultCurrentTmuxSession()
	if err != nil {
		t.Fatalf("defaultCurrentTmuxSession: %v", err)
	}
	if session != "work" {
		t.Fatalf("defaultCurrentTmuxSession = %q, want %q", session, "work")
	}
}

// TestDefaultCurrentTmuxSession_EmptyOutputIsError pins docs/tmux.md's
// documented trap: `tmux display-message -p` exits 0 with empty stdout when
// -t names a pane that no longer exists, rather than erroring like most
// other tmux subcommands do.
func TestDefaultCurrentTmuxSession_EmptyOutputIsError(t *testing.T) {
	t.Setenv("TMUX_PANE", "%5")
	installFakeTmux(t, "exit 0\n") // prints nothing, exits 0
	_, err := defaultCurrentTmuxSession()
	if err == nil {
		t.Fatal("defaultCurrentTmuxSession: err = nil, want an explicit error for empty display-message output")
	}
}

// TestDefaultCurrentTmuxSession_StartFailureReturnsError pins the other
// error path: tmux itself cannot be resolved/run.
func TestDefaultCurrentTmuxSession_StartFailureReturnsError(t *testing.T) {
	t.Setenv("TMUX_PANE", "%5")
	t.Setenv("PATH", t.TempDir())
	_, err := defaultCurrentTmuxSession()
	if err == nil {
		t.Fatal("defaultCurrentTmuxSession: err = nil, want a non-nil error when tmux cannot be started")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("defaultCurrentTmuxSession err = %v, want a start failure, not an *exec.ExitError", err)
	}
}

// -- #1457: one 3-column pane inventory, exact ownership ---------------------

// ownedState is the supervisor the inventory tests classify panes for: PR 42,
// launched from /repo/wt with its checkout root at /repo/root.
func ownedState() State {
	return State{PR: "42", LaunchSession: "work", LaunchDir: "/repo/wt", RepoRoot: "/repo/root"}
}

// observeOver runs observeWorkers(s) through the real defaultTmuxPaneInventory
// against a fake tmux that answers only the exact session-wide 3-column pane
// listing, printing output verbatim.
func observeOver(t *testing.T, s State, output string) (workerLiveness, error) {
	t.Helper()
	installFakeTmux(t, `
[ "$1" = list-panes ] && [ "$2" = -s ] && [ "$3" = -t ] && [ "$4" = =work ] || exit 1
[ "$5" = -F ] && [ "$6" = "$FAKE_FORMAT" ] || exit 1
printf '%s' "$FAKE_PANES"
`)
	t.Setenv("FAKE_PANES", output)
	t.Setenv("FAKE_FORMAT", "#{window_name}\t#{pane_dead}\t#{@cenci-repo-dir}")
	original := tmuxPaneInventory
	tmuxPaneInventory = defaultTmuxPaneInventory
	t.Cleanup(func() { tmuxPaneInventory = original })
	return observeWorkers(&s)
}

// stubGitCommonDir answers the gitCommonDir seam from dirs (a missing dir is a
// lookup error) and returns the lookups it served.
func stubGitCommonDir(t *testing.T, dirs map[string]string) *[]string {
	t.Helper()
	var asked []string
	original := gitCommonDir
	gitCommonDir = func(dir string) (string, error) {
		asked = append(asked, dir)
		if common, ok := dirs[dir]; ok {
			return common, nil
		}
		return "", errors.New("fatal: not a git repository")
	}
	t.Cleanup(func() { gitCommonDir = original })
	return &asked
}

// The parser keeps every column of a row (only the trailing newline is
// trimmed), and any row that is not exactly <name>\t<0|1>\t<repo-dir> fails the
// whole read: a malformed inventory cannot prove a worker absent.
func TestParsePaneInventory(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         []paneRow
		wantErr      bool
	}{
		{"live row", "42-ci-repair\t0\t/repo/wt\n", []paneRow{{window: "42-ci-repair", repoDir: "/repo/wt"}}, false},
		{"dead row", "42-ci-repair\t1\t/repo/wt\n", []paneRow{{window: "42-ci-repair", dead: true, repoDir: "/repo/wt"}}, false},
		{"empty repo-dir keeps three columns", "42-ci-repair\t0\t\n", []paneRow{{window: "42-ci-repair"}}, false},
		{"no trailing newline", "1-implement\t0\t", []paneRow{{window: "1-implement"}}, false},
		{"several rows", "a\t0\t/x\nb\t1\t\n", []paneRow{{window: "a", repoDir: "/x"}, {window: "b", dead: true}}, false},
		{"empty output cannot prove absent", "", nil, true},
		{"a lone newline", "\n", nil, true},
		{"two columns (pre-#1457 format)", "42-ci-repair\t0\n", nil, true},
		{"four columns", "42-ci-repair\t0\t/repo\textra\n", nil, true},
		{"pane_dead not 0 or 1", "42-ci-repair\tunknown\t/repo/wt\n", nil, true},
		{"empty window name", "\t0\t/repo/wt\n", nil, true},
		{"malformed row after a good one", "42-ci-repair\t0\t/repo/wt\nother\t2\t\n", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePaneInventory(tc.output)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parsePaneInventory(%q) err = %v, want error %v", tc.output, err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parsePaneInventory(%q) = %+v, want %+v", tc.output, got, tc.want)
			}
		})
	}
}

// Ownership matrix: only an exact, live `42-ci-repair`/`42-merge-repair` pane
// counts; it is owned when its repo-dir is Clean-equal to LaunchDir/RepoRoot or
// shares their git common dir, foreign (ignored) when its common dir provably
// differs, and unknown (still live, with a recovery note) otherwise.
func TestObserveWorkersOwnership(t *testing.T) {
	commonDirs := map[string]string{
		"/repo/wt":             "/repo/root/.git",
		"/repo/other-checkout": "/repo/root/.git",
		"/elsewhere/repo":      "/elsewhere/repo/.git",
	}
	for _, tc := range []struct {
		name, output     string
		wantCI, wantMerg bool
		wantNote         bool
	}{
		{"no worker windows", "1-implement\t0\t/repo/wt\n", false, false, false},
		{"owned via LaunchDir", "42-ci-repair\t0\t/repo/wt\n", true, false, false},
		{"owned via RepoRoot after Clean", "42-merge-repair\t0\t/repo/root/./\n", false, true, false},
		{"owned via the same git common dir", "42-ci-repair\t0\t/repo/other-checkout\n", true, false, false},
		{"retained dead pane is ended", "42-ci-repair\t1\t/repo/wt\n", false, false, false},
		{"prefix sibling ignored", "42-ci-repair-old\t0\t/repo/wt\n", false, false, false},
		{"another PR ignored", "142-ci-repair\t0\t/repo/wt\n", false, false, false},
		{"foreign ci-repair ignored", "42-ci-repair\t0\t/elsewhere/repo\n", false, false, false},
		{"foreign merge-repair ignored", "42-merge-repair\t0\t/elsewhere/repo\n", false, false, false},
		{"empty repo-dir is unknown", "42-ci-repair\t0\t\n", true, false, true},
		{"relative repo-dir is unknown", "42-merge-repair\t0\trepo/wt\n", false, true, true},
		{"lookup failure is unknown, never foreign", "42-ci-repair\t0\t/no/such/repo\n", true, false, true},
		{"any live pane of the window counts", "42-ci-repair\t1\t/repo/wt\n42-ci-repair\t0\t/repo/wt\n", true, false, false},
		{"both workers", "42-ci-repair\t0\t/repo/wt\n42-merge-repair\t0\t/repo/root\n", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubGitCommonDir(t, commonDirs)
			got, err := observeOver(t, ownedState(), tc.output)
			if err != nil {
				t.Fatalf("observeWorkers: %v", err)
			}
			if got.ciLive != tc.wantCI || got.mergeLive != tc.wantMerg || (len(got.notes) > 0) != tc.wantNote {
				t.Fatalf("observeWorkers(%q) = ci %v merge %v notes %q, want ci %v merge %v note %v",
					tc.output, got.ciLive, got.mergeLive, got.notes, tc.wantCI, tc.wantMerg, tc.wantNote)
			}
			for _, note := range got.notes {
				if !strings.Contains(note, "work:42-") || !strings.Contains(note, "cenci babysit reset 42") {
					t.Errorf("note %q, want the session:window and the reset instruction", note)
				}
				if strings.Contains(note, "/no/such/repo") || strings.Contains(note, "repo/wt") {
					t.Errorf("note %q echoes the raw repo-dir", note)
				}
			}
		})
	}
}

// The repo-dir matching LaunchDir/RepoRoot is decided without running git; a
// differing one costs one lookup for it and one, memoized, for our own dir.
func TestObserveWorkersCommonDirLookupsAreBoundedAndMemoized(t *testing.T) {
	asked := stubGitCommonDir(t, map[string]string{"/repo/wt": "/repo/root/.git", "/a": "/x/.git", "/b": "/y/.git"})
	if _, err := observeOver(t, ownedState(), "42-ci-repair\t0\t/repo/wt\n"); err != nil || len(*asked) != 0 {
		t.Fatalf("an exact LaunchDir match ran git %q (err %v), want no lookup", *asked, err)
	}
	if _, err := observeOver(t, ownedState(), "42-ci-repair\t0\t/a\n42-merge-repair\t0\t/b\n"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/repo/wt", "/a", "/b"}; !reflect.DeepEqual(*asked, want) {
		t.Errorf("git common-dir lookups = %q, want %q (ours once, then each foreign candidate)", *asked, want)
	}
}

// When our own common dir cannot be resolved, no pane can be proved foreign.
func TestObserveWorkersOwnLookupFailureMakesEveryCandidateUnknown(t *testing.T) {
	stubGitCommonDir(t, map[string]string{"/elsewhere/repo": "/elsewhere/repo/.git"})
	got, err := observeOver(t, ownedState(), "42-ci-repair\t0\t/elsewhere/repo\n")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ciLive || len(got.notes) != 1 {
		t.Fatalf("observeWorkers = ci %v notes %q, want a live unknown worker", got.ciLive, got.notes)
	}
}

func TestObserveWorkersInventoryFailures(t *testing.T) {
	original := tmuxPaneInventory
	tmuxPaneInventory = defaultTmuxPaneInventory
	t.Cleanup(func() { tmuxPaneInventory = original })
	for _, tc := range []struct {
		name, script, want string
	}{
		{"tmux error", "printf 'server unavailable' >&2\nexit 1\n", "server unavailable"},
		{"empty inventory", "exit 0\n", "empty pane inventory"},
		{"malformed inventory", "printf '42-ci-repair\\t0\\n'\n", "malformed pane inventory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeTmux(t, tc.script)
			s := ownedState()
			if got, err := observeWorkers(&s); err == nil || got.any() || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("observeWorkers = %+v, %v, want an error containing %q", got, err, tc.want)
			}
		})
	}
	t.Run("missing binary", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		s := ownedState()
		if _, err := defaultTmuxPaneInventory(s.LaunchSocket, s.LaunchSession); err == nil {
			t.Fatal("a missing tmux binary read as an inventory")
		}
	})
	t.Run("truncated", func(t *testing.T) {
		// A truncated inventory may omit the worker even if the buffered prefix
		// ends on a complete record. Reject it before interpreting any rows.
		installFakeTmux(t, "printf '%s' '"+strings.Repeat("other\t0\t\n", ghOutputCap/9+1)+"'\n")
		if _, err := defaultTmuxPaneInventory("", "work"); err == nil || !strings.Contains(err.Error(), "bounded cap") {
			t.Fatalf("truncated inventory err = %v, want the bounded-cap error", err)
		}
	})
}

// installFakeGit puts a fake `git` first on PATH that records its argv and the
// prompt-hardening env into a log, then runs body.
func installFakeGit(t *testing.T, body string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "git.log")
	script := "#!/bin/sh\n" +
		`printf 'ARGS=%s\nPROMPT=%s\n' "$*" "${GIT_TERMINAL_PROMPT-unset}" >> '` + logPath + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return logPath
}

func TestDefaultGitCommonDir(t *testing.T) {
	t.Run("absolute output is cleaned", func(t *testing.T) {
		logPath := installFakeGit(t, "printf '/repo/root/.git/\\n'\n")
		got, err := defaultGitCommonDir("/repo/wt")
		if err != nil || got != "/repo/root/.git" {
			t.Fatalf("defaultGitCommonDir = %q, %v, want /repo/root/.git", got, err)
		}
		raw, _ := os.ReadFile(logPath)
		if want := "ARGS=-C /repo/wt rev-parse --path-format=absolute --git-common-dir\nPROMPT=0\n"; string(raw) != want {
			t.Errorf("git call log = %q, want %q", raw, want)
		}
	})
	for _, tc := range []struct{ name, body string }{
		{"git error", "echo 'fatal: not a git repository' >&2\nexit 128\n"},
		{"relative output", "printf '.git\\n'\n"},
		{"empty output", "exit 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeGit(t, tc.body)
			if got, err := defaultGitCommonDir("/repo/wt"); err == nil {
				t.Fatalf("defaultGitCommonDir = %q, nil, want an error", got)
			}
		})
	}
	t.Run("missing binary", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := defaultGitCommonDir("/repo/wt"); err == nil {
			t.Fatal("a missing git binary resolved a common dir")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		installFakeGit(t, "exec sleep 30\n")
		original := gitCommonDirTimeout
		gitCommonDirTimeout = 100 * time.Millisecond
		t.Cleanup(func() { gitCommonDirTimeout = original })
		if _, err := defaultGitCommonDir("/repo/wt"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("defaultGitCommonDir err = %v, want context.DeadlineExceeded", err)
		}
	})
}

// -- #1431: socket-carrying calls are hardened and logged exactly --------------

// trustedTmuxSocket creates a trusted tmux socket directory under a fresh
// $TMUX_TMPDIR (<tmpdir>/tmux-<uid>, mode 0700) and returns a socket path
// directly under it. The socket file need not exist: the fake tmux never
// connects.
func trustedTmuxSocket(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", base)
	return filepath.Join(dir, "sock")
}

// installRecordingTmux puts a fake `tmux` first on PATH that appends one record
// per call to the returned log -- its argv plus the value (or "unset") of each
// variable a hardened call must not leak, and of the allowlisted ones -- and then
// runs body. The log path is baked into the script because the child env of a
// socket call is only PATH/HOME/TERM/LANG, so no custom variable could carry it
// (watch/docs/tmux.md).
func installRecordingTmux(t *testing.T, body string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "calls.log")
	script := "#!/bin/sh\n{\n" +
		`printf 'ARGS=%s\n' "$*"` + "\n" +
		`printf 'SECRET=%s\n' "${CENCI_TEST_SECRET-unset}"` + "\n" +
		`printf 'TMUX=%s\n' "${TMUX-unset}"` + "\n" +
		`printf 'TMUX_PANE=%s\n' "${TMUX_PANE-unset}"` + "\n" +
		`printf 'TMUX_TMPDIR=%s\n' "${TMUX_TMPDIR-unset}"` + "\n" +
		`printf 'TERM=%s\n' "${TERM-unset}"` + "\n" +
		`printf 'LANG=%s\n' "${LANG-unset}"` + "\n" +
		`printf 'HOME=%s\n' "${HOME-unset}"` + "\n" +
		"} >> '" + logPath + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The system dirs stay on PATH for the scripts that call sleep.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return logPath
}

// recordedTmuxCalls decodes installRecordingTmux's log into one map per call
// (a missing log is no calls).
func recordedTmuxCalls(t *testing.T, logPath string) []map[string]string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []map[string]string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if key == "ARGS" {
			calls = append(calls, map[string]string{})
		}
		calls[len(calls)-1][key] = value
	}
	return calls
}

// With a socket the probe is `tmux -S <socket> has-session -t =<name>`; with none
// it is today's bare call, with no -S.
func TestDefaultTmuxHasSession_PassesTheSocketAsDashS(t *testing.T) {
	sock := trustedTmuxSocket(t)
	logPath := installRecordingTmux(t, "exit 0\n")

	for _, tc := range []struct{ socket, wantArgs string }{
		{sock, "-S " + sock + " has-session -t =work"},
		{"", "has-session -t =work"},
	} {
		ok, err := defaultTmuxHasSession(tc.socket, "work")
		if err != nil || !ok {
			t.Fatalf("defaultTmuxHasSession(%q) = %v, %v, want true, nil", tc.socket, ok, err)
		}
		calls := recordedTmuxCalls(t, logPath)
		if got := calls[len(calls)-1]["ARGS"]; got != tc.wantArgs {
			t.Errorf("socket %q: tmux argv = %q, want %q", tc.socket, got, tc.wantArgs)
		}
	}
}

// The pane inventory is read once, on the recorded socket, in the 3-column form.
func TestDefaultTmuxPaneInventory_ReadsTheRecordedSocket(t *testing.T) {
	sock := trustedTmuxSocket(t)
	logPath := installRecordingTmux(t, "printf '42-ci-repair\\t0\\t\\n'\n")

	out, err := defaultTmuxPaneInventory(sock, "work")

	if err != nil || out != "42-ci-repair\t0\t\n" {
		t.Fatalf("defaultTmuxPaneInventory = %q, %v, want the raw row, nil", out, err)
	}
	calls := recordedTmuxCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("tmux exec'd %d times, want 1", len(calls))
	}
	if want := "-S " + sock + " list-panes -s -t =work -F #{window_name}\t#{pane_dead}\t#{@cenci-repo-dir}"; calls[0]["ARGS"] != want {
		t.Errorf("tmux argv = %q, want %q", calls[0]["ARGS"], want)
	}
}

// Whenever a socket is set the child env is only the allowlist: a secret, $TMUX,
// $TMUX_PANE and $TMUX_TMPDIR (all set in the supervisor, which inherits the
// daemon's full env) never reach a possibly hostile server; TERM and HOME are
// forwarded because they are set, LANG is not fabricated. An empty socket keeps
// the full ambient env and today's argv.
func TestExecTmux_SocketChildEnvIsOnlyTheSetAllowlist(t *testing.T) {
	sock := trustedTmuxSocket(t) // sets $TMUX_TMPDIR, which the child must not see
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERM", "xterm-test-term")
	t.Setenv("TMUX", "/ambient/server,4242,0")
	t.Setenv("TMUX_PANE", "%9")
	t.Setenv("CENCI_TEST_SECRET", "hunter2")
	t.Setenv("LANG", "unset-below")
	if err := os.Unsetenv("LANG"); err != nil {
		t.Fatal(err)
	}
	logPath := installRecordingTmux(t, "exit 0\n")

	if _, _, err := execTmux(sock, "list-panes"); err != nil {
		t.Fatalf("execTmux with a trusted socket: %v", err)
	}
	if _, _, err := execTmux("", "list-panes"); err != nil {
		t.Fatalf("execTmux with no socket: %v", err)
	}

	calls := recordedTmuxCalls(t, logPath)
	if len(calls) != 2 {
		t.Fatalf("tmux exec'd %d times, want 2", len(calls))
	}
	wantSocketCall := map[string]string{
		"ARGS": "-S " + sock + " list-panes", "SECRET": "unset", "TMUX": "unset", "TMUX_PANE": "unset",
		"TMUX_TMPDIR": "unset", "TERM": "xterm-test-term", "LANG": "unset", "HOME": home,
	}
	if !reflect.DeepEqual(calls[0], wantSocketCall) {
		t.Errorf("socket call saw %v, want exactly %v", calls[0], wantSocketCall)
	}
	if calls[1]["ARGS"] != "list-panes" || calls[1]["SECRET"] != "hunter2" || calls[1]["TMUX"] != "/ambient/server,4242,0" {
		t.Errorf("empty-socket call saw %v, want today's bare argv and the full ambient env", calls[1])
	}
}

// A socket that is not exactly one name directly under a private, user-owned
// tmux directory is refused before anything is exec'd, and every probe built on
// it reports an error -- never "session absent" or "no live worker".
func TestExecTmux_UntrustedSocketIsRefusedWithoutExec(t *testing.T) {
	for _, tc := range []struct {
		name   string
		socket func(t *testing.T) string
		want   error
	}{
		{"outside the trusted directory", func(t *testing.T) string {
			trustedTmuxSocket(t)
			return filepath.Join(t.TempDir(), "sock")
		}, tmux.ErrUntrustedSocket},
		{"nested below the trusted directory", func(t *testing.T) string {
			return filepath.Join(filepath.Dir(trustedTmuxSocket(t)), "sub", "sock")
		}, tmux.ErrUntrustedSocket},
		{"trusted directory open to group and others", func(t *testing.T) string {
			sock := trustedTmuxSocket(t)
			if err := os.Chmod(filepath.Dir(sock), 0o755); err != nil {
				t.Fatal(err)
			}
			return sock
		}, tmux.ErrUntrustedSocket},
		{"relative path", func(t *testing.T) string { return "tmux-1000/sock" }, tmux.ErrMalformedSocket},
		{"contains a tab", func(t *testing.T) string { return filepath.Join(filepath.Dir(trustedTmuxSocket(t)), "a\tb") }, tmux.ErrMalformedSocket},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock := tc.socket(t)
			logPath := installRecordingTmux(t, "exit 0\n")

			if _, _, err := execTmux(sock, "list-panes"); !errors.Is(err, tc.want) {
				t.Errorf("execTmux err = %v, want errors.Is %v", err, tc.want)
			}
			if ok, err := defaultTmuxHasSession(sock, "work"); ok || err == nil {
				t.Errorf("defaultTmuxHasSession = %v, %v, want false and an error (not \"absent\")", ok, err)
			}
			if out, err := defaultTmuxPaneInventory(sock, "work"); out != "" || err == nil {
				t.Errorf("defaultTmuxPaneInventory = %q, %v, want no inventory and an error", out, err)
			}
			if calls := recordedTmuxCalls(t, logPath); len(calls) != 0 {
				t.Errorf("tmux was exec'd %d times for a socket that must be refused first", len(calls))
			}
		})
	}
}

// A socket call that outlives its bound is a probe failure carrying
// context.DeadlineExceeded -- tmux's kill surfaces as an *exec.ExitError, which
// would otherwise read as "session absent" and fail a launch for the wrong reason.
func TestDefaultTmuxHasSession_TimeoutOnASocketIsAnErrorNotAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the real 5s socket-call bound")
	}
	sock := trustedTmuxSocket(t)
	installRecordingTmux(t, "case \"$1\" in -S) exec sleep 30 ;; esac\nexit 1\n")

	ok, err := defaultTmuxHasSession(sock, "work")

	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("defaultTmuxHasSession = %v, %v, want false and an error wrapping context.DeadlineExceeded", ok, err)
	}
}
