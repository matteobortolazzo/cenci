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

func TestDefaultMergeRepairActive(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantActive   bool
		wantErr      bool
	}{
		{"live exact worker", "42-merge-repair\t0\n", true, false},
		{"retained dead worker", "42-merge-repair\t1\n", false, false},
		{"missing worker", "other\t0\n", false, false},
		{"prefix sibling ignored", "42-merge-repair-old\t0\n", false, false},
		{"another PR ignored", "142-merge-repair\t0\n", false, false},
		{"any live pane means active", "42-merge-repair\t1\n42-merge-repair\t0\n", true, false},
		{"malformed pane status", "42-merge-repair\tunknown\n", false, true},
		{"missing pane status", "42-merge-repair\n", false, true},
		{"malformed row after live worker", "42-merge-repair\t0\nother\tunknown\n", false, true},
		{"empty output cannot prove absent", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active, err := workerActiveOver(t, tc.output, "merge-repair")
			if active != tc.wantActive || (err != nil) != tc.wantErr {
				t.Fatalf("active=%v err=%v, want active=%v err=%v", active, err, tc.wantActive, tc.wantErr)
			}
		})
	}
}

// workerActiveOver runs defaultWorkerActive("", "work", "42", workflow) against a
// fake tmux that answers the exact session-wide pane listing with output.
func workerActiveOver(t *testing.T, output, workflow string) (bool, error) {
	t.Helper()
	installFakeTmux(t, `
[ "$1" = list-panes ] && [ "$2" = -s ] && [ "$3" = -t ] && [ "$4" = =work ] || exit 1
[ "$5" = -F ] && [ "$6" = "$FAKE_FORMAT" ] || exit 1
printf '%s' "$FAKE_PANES"
`)
	t.Setenv("FAKE_PANES", output)
	t.Setenv("FAKE_FORMAT", "#{window_name}\t#{pane_dead}")
	return defaultWorkerActive("", "work", "42", workflow)
}

func TestDefaultMergeRepairActiveProbeFailure(t *testing.T) {
	installFakeTmux(t, "printf 'server unavailable' >&2\nexit 1\n")
	if active, err := defaultWorkerActive("", "work", "42", "merge-repair"); active || err == nil || !strings.Contains(err.Error(), "server unavailable") {
		t.Fatalf("probe failure active=%v err=%v", active, err)
	}
}

func TestDefaultMergeRepairActiveMissingBinaryIsProbeFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if active, err := defaultWorkerActive("", "work", "42", "merge-repair"); active || err == nil {
		t.Fatalf("missing binary active=%v err=%v", active, err)
	}
}

func TestDefaultMergeRepairActiveTruncationCannotProveWorkerAbsent(t *testing.T) {
	// A truncated inventory may omit the worker even if the buffered prefix
	// ends on a complete record. Reject it before interpreting any rows.
	output := strings.Repeat("other\t0\n", ghOutputCap/8+1)
	installFakeTmux(t, "printf '%s' '"+output+"'\n")
	if active, err := defaultWorkerActive("", "work", "42", "merge-repair"); active || err == nil || !strings.Contains(err.Error(), "bounded cap") {
		t.Fatalf("truncated inventory active=%v err=%v", active, err)
	}
}

// #1455: the ci-repair liveness probe matches only the exact live
// `<PR>-ci-repair` window, and fails closed on an inventory it cannot read.
func TestDefaultWorkerActiveCIRepair(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantActive   bool
		wantErr      bool
	}{
		{"live exact worker", "42-ci-repair\t0\n", true, false},
		{"retained dead worker", "42-ci-repair\t1\n", false, false},
		{"prefix sibling ignored", "42-ci-repair-old\t0\n", false, false},
		{"another PR ignored", "142-ci-repair\t0\n", false, false},
		{"merge repair is not a ci-repair worker", "42-merge-repair\t0\n", false, false},
		{"malformed pane status", "42-ci-repair\tunknown\n", false, true},
		{"empty output cannot prove absent", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active, err := workerActiveOver(t, tc.output, "ci-repair")
			if active != tc.wantActive || (err != nil) != tc.wantErr {
				t.Fatalf("active=%v err=%v, want active=%v err=%v", active, err, tc.wantActive, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "ci-repair") {
				t.Errorf("probe error %q does not name the ci-repair workflow", err)
			}
		})
	}
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

// The worker-liveness probe reads the pane inventory on the recorded socket.
func TestDefaultWorkerActive_ReadsTheRecordedSocket(t *testing.T) {
	sock := trustedTmuxSocket(t)
	logPath := installRecordingTmux(t, "printf '42-ci-repair\\t0\\n'\n")

	active, err := defaultWorkerActive(sock, "work", "42", "ci-repair")

	if err != nil || !active {
		t.Fatalf("defaultWorkerActive = %v, %v, want true, nil", active, err)
	}
	calls := recordedTmuxCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("tmux exec'd %d times, want 1", len(calls))
	}
	if want := "-S " + sock + " list-panes -s -t =work -F #{window_name}\t#{pane_dead}"; calls[0]["ARGS"] != want {
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
			if active, err := defaultWorkerActive(sock, "work", "42", "ci-repair"); active || err == nil {
				t.Errorf("defaultWorkerActive = %v, %v, want false and an error", active, err)
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
