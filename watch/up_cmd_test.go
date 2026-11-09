package main_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// -- harness -----------------------------------------------------------------
//
// These black-box tests run the real built `cenci up` against a fake `tmux` (a
// /bin/sh script using only shell builtins, because PATH holds nothing but the
// fake directory) that keeps a small state directory and logs every call:
//
//	server          exists once a tmux server has been started
//	session         exists once the cenci session has been created
//	server-starts   one line per call that had to start the server
//	calls           one "TMUX=<set|unset> TMUX_PANE=<set|unset> | <argv>" line per call
//
// The environment never reaches the real tmux or the real lazyboards: PATH is
// the fake directory only, TMUX_TMPDIR is a temp directory, and a live
// events-socket listener means no real daemon is ever spawned
// (watch/docs/test-isolation.md).

// cenciUpAllowedSubcommands is the whole set of tmux subcommands `cenci up` may
// issue: it never detaches a client and never spawns a terminal window (P37).
var cenciUpAllowedSubcommands = map[string]bool{
	"has-session":    true,
	"new-session":    true,
	"new-window":     true,
	"attach-session": true,
	"switch-client":  true,
}

type cenciUpCall struct {
	tmuxEnv string // "set" or "unset": whether $TMUX reached the call
	paneEnv string // "set" or "unset": whether $TMUX_PANE reached the call
	args    string // the argv after the program name, space-joined
	rest    string // args without the leading "-L cenci" and optional "-f <conf>"
	subcmd  string // the tmux subcommand
}

type cenciUpEnv struct {
	fakeDir     string
	stateDir    string
	confPath    string
	cenciSocket string
	lazyboards  string
	base        []string
}

const cenciUpFakeTmux = `#!/bin/sh
S=%s
t=unset; [ -n "${TMUX+x}" ] && t=set
p=unset; [ -n "${TMUX_PANE+x}" ] && p=set
printf 'TMUX=%%s TMUX_PANE=%%s | %%s\n' "$t" "$p" "$*" >> "$S/calls"
while [ $# -gt 0 ]; do
  case "$1" in
  -L|-f|-S) shift 2 ;;
  -*) shift ;;
  *) break ;;
  esac
done
case "$1" in
has-session)
  [ -e "$S/session" ] && exit 0
  echo "can't find session: cenci" >&2
  exit 1
  ;;
new-session)
  if [ -e "$S/session" ]; then
    echo "duplicate session: cenci" >&2
    exit 1
  fi
  [ -e "$S/server" ] || printf 'x\n' >> "$S/server-starts"
  : > "$S/server"
  : > "$S/session"
  exit 0
  ;;
new-window)
  if [ ! -e "$S/session" ]; then
    echo "can't find session: cenci" >&2
    exit 1
  fi
  exit 0
  ;;
esac
exit 0
`

// newCenciUpEnv builds the isolated environment for one `cenci up` test.
// withConfig writes cenci/tmux.conf under the pinned XDG_CONFIG_HOME;
// withLazyboards puts a fake lazyboards on PATH.
func newCenciUpEnv(t *testing.T, withConfig, withLazyboards bool) *cenciUpEnv {
	t.Helper()
	e := &cenciUpEnv{
		fakeDir:  t.TempDir(),
		stateDir: t.TempDir(),
	}
	exectest.WriteExecutable(t, filepath.Join(e.fakeDir, "tmux"),
		fmt.Sprintf(cenciUpFakeTmux, exectest.ShellQuote(e.stateDir)))
	if withLazyboards {
		e.lazyboards = filepath.Join(e.fakeDir, "lazyboards")
		exectest.WriteExecutable(t, e.lazyboards, "#!/bin/sh\nexit 0\n")
	}

	tmuxTmp := t.TempDir()
	resolvedTmp, err := filepath.EvalSymlinks(tmuxTmp)
	if err != nil {
		t.Fatalf("resolve TMUX_TMPDIR: %v", err)
	}
	e.cenciSocket = filepath.Join(resolvedTmp, fmt.Sprintf("tmux-%d", os.Getuid()), "cenci")

	xdg := t.TempDir()
	e.confPath = filepath.Join(xdg, "cenci", "tmux.conf")
	if withConfig {
		if err := os.MkdirAll(filepath.Dir(e.confPath), 0o755); err != nil {
			t.Fatalf("mkdir config dir: %v", err)
		}
		if err := os.WriteFile(e.confPath, []byte("# fake tmux config\n"), 0o644); err != nil {
			t.Fatalf("write tmux.conf: %v", err)
		}
	}

	// A live events socket under a private 0700 CENCI_SOCKET_DIR keeps the
	// daemon ensure a no-op, so no real daemon is ever spawned. MkdirTemp (not
	// t.TempDir) keeps the unix socket path short.
	socketDir, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	l, err := net.Listen("unix", filepath.Join(socketDir, "cenci-events.sock"))
	if err != nil {
		t.Fatalf("listen events socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	// TMUX and TMUX_PANE are dropped from the inherited environment so a
	// developer's own tmux can never leak in; each run sets them explicitly.
	// os/exec keeps the last duplicate key, so the appended values win.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			continue
		}
		e.base = append(e.base, kv)
	}
	e.base = append(e.base,
		"PATH="+e.fakeDir,
		"HOME="+t.TempDir(),
		"TMUX_TMPDIR="+tmuxTmp,
		"XDG_CONFIG_HOME="+xdg,
		"CENCI_SOCKET_DIR="+socketDir,
		"CENCI_SANDBOX=",
	)
	return e
}

// run executes `cenci up <args>`. tmuxVar is the $TMUX value; empty means
// outside tmux (neither $TMUX nor $TMUX_PANE is set), otherwise both are set.
func (e *cenciUpEnv) run(t *testing.T, tmuxVar string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var extra []string
	if tmuxVar != "" {
		extra = []string{"TMUX=" + tmuxVar, "TMUX_PANE=%3"}
	}
	return e.runWithEnv(t, extra, args...)
}

// runWithEnv executes `cenci up <args>` with extraEnv appended to the isolated
// base environment, for runs that need $TMUX or $TMUX_PANE set in a way run's
// tmuxVar cannot express (empty, or only one of the two).
func (e *cenciUpEnv) runWithEnv(t *testing.T, extraEnv []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, append([]string{"up"}, args...)...)
	cmd.Env = append(append([]string(nil), e.base...), extraEnv...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running cenci up %v: %v\nstderr: %s", args, err, errOut.String())
	}
	return out.String(), errOut.String(), code
}

func (e *cenciUpEnv) exists(name string) bool {
	_, err := os.Stat(filepath.Join(e.stateDir, name))
	return err == nil
}

func (e *cenciUpEnv) seedServer(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.stateDir, "server"), nil, 0o644); err != nil {
		t.Fatalf("seed server: %v", err)
	}
}

func (e *cenciUpEnv) seedSession(t *testing.T) {
	t.Helper()
	e.seedServer(t)
	if err := os.WriteFile(filepath.Join(e.stateDir, "session"), nil, 0o644); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func (e *cenciUpEnv) serverStarts(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.stateDir, "server-starts"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read server-starts: %v", err)
	}
	return strings.Count(string(data), "\n")
}

// calls returns every logged tmux call in order. It also enforces what holds
// for every `cenci up` run: each call targets the cenci server by name
// (`-L cenci` first, never `-S`, never bare) and its subcommand is on the
// allowlist, so `cenci up` can never detach a client or open a terminal (P37).
func (e *cenciUpEnv) calls(t *testing.T) []cenciUpCall {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.stateDir, "calls"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read tmux call log: %v", err)
	}
	var calls []cenciUpCall
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		prefix, args, ok := strings.Cut(line, " | ")
		if !ok {
			t.Fatalf("malformed tmux call log line %q", line)
		}
		var c cenciUpCall
		c.args = args
		for _, field := range strings.Fields(prefix) {
			switch {
			case strings.HasPrefix(field, "TMUX_PANE="):
				c.paneEnv = strings.TrimPrefix(field, "TMUX_PANE=")
			case strings.HasPrefix(field, "TMUX="):
				c.tmuxEnv = strings.TrimPrefix(field, "TMUX=")
			}
		}
		rest, ok := strings.CutPrefix(args, "-L cenci ")
		if !ok {
			t.Errorf("tmux call does not start with -L cenci: %q", args)
		}
		c.rest = strings.TrimPrefix(rest, "-f "+e.confPath+" ")
		c.subcmd, _, _ = strings.Cut(c.rest, " ")
		if !cenciUpAllowedSubcommands[c.subcmd] {
			t.Errorf("tmux subcommand %q is not on the cenci up allowlist: %q", c.subcmd, args)
		}
		if strings.Contains(" "+args+" ", " -S ") {
			t.Errorf("tmux call uses -S: %q", args)
		}
		calls = append(calls, c)
	}
	return calls
}

func cenciUpSubcommands(calls []cenciUpCall) []string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.subcmd)
	}
	return names
}

func cenciUpCount(calls []cenciUpCall, subcmd string) int {
	n := 0
	for _, c := range calls {
		if c.subcmd == subcmd {
			n++
		}
	}
	return n
}

func assertCenciUpSequence(t *testing.T, calls []cenciUpCall, want ...string) {
	t.Helper()
	got := cenciUpSubcommands(calls)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tmux subcommands = %v, want %v", got, want)
	}
}

func assertCenciUpEnvScrubbed(t *testing.T, calls []cenciUpCall) {
	t.Helper()
	for _, c := range calls {
		if c.tmuxEnv != "unset" || c.paneEnv != "unset" {
			t.Errorf("%s ran with TMUX=%s TMUX_PANE=%s, want both unset: %q", c.subcmd, c.tmuxEnv, c.paneEnv, c.args)
		}
	}
}

// -- tests -------------------------------------------------------------------

// P1, P3, P6: outside tmux, `cenci up` starts the server and the session on the
// cenci socket with the shipped config, creates the lazyboards window, and
// attaches.
func TestCenciUpLaunchesOutsideTmux(t *testing.T) {
	e := newCenciUpEnv(t, true, true)

	_, stderr, code := e.run(t, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
	}

	calls := e.calls(t)
	assertCenciUpSequence(t, calls, "has-session", "new-session", "new-window", "attach-session")
	if calls[0].rest != "has-session -t =cenci" {
		t.Errorf("probe = %q, want exact-match has-session -t =cenci", calls[0].rest)
	}
	if want := "-L cenci -f " + e.confPath + " new-session -d -s cenci"; !strings.HasPrefix(calls[1].args, want) {
		t.Errorf("session create = %q, want prefix %q", calls[1].args, want)
	}
	if want := "new-window -d -t =cenci: -n board '" + e.lazyboards + "'"; calls[2].rest != want {
		t.Errorf("board window = %q, want %q", calls[2].rest, want)
	}
	if calls[3].rest != "attach-session -t =cenci" {
		t.Errorf("attach = %q, want attach-session -t =cenci", calls[3].rest)
	}
	assertCenciUpEnvScrubbed(t, calls)
	if !e.exists("session") {
		t.Error("the cenci session was not created")
	}
	if n := e.serverStarts(t); n != 1 {
		t.Errorf("server starts = %d, want 1", n)
	}
}

// P2: a second `cenci up` attaches to the existing session and creates neither
// a session nor a board window.
func TestCenciUpSecondRunAttachesWithoutCreating(t *testing.T) {
	e := newCenciUpEnv(t, true, true)

	if _, stderr, code := e.run(t, ""); code != 0 {
		t.Fatalf("first run exit code = %d, want 0\nstderr: %s", code, stderr)
	}
	first := len(e.calls(t))

	if _, stderr, code := e.run(t, ""); code != 0 {
		t.Fatalf("second run exit code = %d, want 0\nstderr: %s", code, stderr)
	}
	all := e.calls(t)
	second := all[first:]

	assertCenciUpSequence(t, second, "has-session", "attach-session")
	if n := cenciUpCount(all, "new-session"); n != 1 {
		t.Errorf("new-session calls over both runs = %d, want 1", n)
	}
	if n := cenciUpCount(all, "new-window"); n != 1 {
		t.Errorf("new-window calls over both runs = %d, want 1", n)
	}
	if n := e.serverStarts(t); n != 1 {
		t.Errorf("server starts over both runs = %d, want 1", n)
	}
}

// P30: with the cenci server already running but no cenci session, the session
// is created on that server: one new-session, and the server is neither started
// again nor killed.
func TestCenciUpCreatesSessionOnRunningServer(t *testing.T) {
	e := newCenciUpEnv(t, true, true)
	e.seedServer(t)

	_, stderr, code := e.run(t, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
	}

	calls := e.calls(t)
	if n := cenciUpCount(calls, "new-session"); n != 1 {
		t.Errorf("new-session calls = %d, want exactly 1", n)
	}
	for _, c := range calls {
		if strings.Contains(c.rest, "start-server") || strings.Contains(c.rest, "kill-server") {
			t.Errorf("unexpected server lifecycle call: %q", c.args)
		}
	}
	if n := e.serverStarts(t); n != 0 {
		t.Errorf("server starts = %d, want 0 (the server was already running)", n)
	}
	if !e.exists("session") {
		t.Error("the cenci session was not created on the running server")
	}
	if cenciUpCount(calls, "attach-session") != 1 {
		t.Errorf("want one attach-session, got calls %v", cenciUpSubcommands(calls))
	}
}

// Q1: outside tmux (no non-empty $TMUX) a stray $TMUX or $TMUX_PANE must not
// reach any call, the exec'd attach-session included: the attach is the call
// that hands the terminal to the cenci server, so it must not resolve through
// the caller's own server.
func TestCenciUpOutsideTmuxAttachScrubsStrayTmuxVars(t *testing.T) {
	cases := []struct {
		name string
		env  []string
	}{
		{"empty TMUX and TMUX_PANE set", []string{"TMUX=", "TMUX_PANE=%3"}},
		{"TMUX_PANE set without TMUX", []string{"TMUX_PANE=%3"}},
		{"empty TMUX alone", []string{"TMUX="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCenciUpEnv(t, true, true)

			_, stderr, code := e.runWithEnv(t, tc.env)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
			}

			calls := e.calls(t)
			assertCenciUpSequence(t, calls, "has-session", "new-session", "new-window", "attach-session")
			assertCenciUpEnvScrubbed(t, calls)
		})
	}
}

// P4: with $TMUX on the cenci server, `cenci up` switches the client to the
// cenci session and never attaches. The switch keeps the user's environment
// (its $TMUX names the server being switched); the probe does not.
func TestCenciUpInsideCenciServerSwitchesClient(t *testing.T) {
	e := newCenciUpEnv(t, true, true)
	e.seedSession(t)

	_, stderr, code := e.run(t, e.cenciSocket+",1,0")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
	}

	calls := e.calls(t)
	assertCenciUpSequence(t, calls, "has-session", "switch-client")
	if calls[0].tmuxEnv != "unset" || calls[0].paneEnv != "unset" {
		t.Errorf("has-session ran with TMUX=%s TMUX_PANE=%s, want both unset", calls[0].tmuxEnv, calls[0].paneEnv)
	}
	if calls[1].rest != "switch-client -t =cenci" {
		t.Errorf("switch = %q, want switch-client -t =cenci", calls[1].rest)
	}
	if calls[1].tmuxEnv != "set" {
		t.Errorf("switch-client ran with TMUX=%s, want the unchanged environment (set)", calls[1].tmuxEnv)
	}
}

// P4 with a missing session: the session and board are created, then the client
// switches.
func TestCenciUpInsideCenciServerCreatesMissingSessionThenSwitches(t *testing.T) {
	e := newCenciUpEnv(t, true, true)
	e.seedServer(t)

	_, stderr, code := e.run(t, e.cenciSocket+",1,0")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
	}

	calls := e.calls(t)
	assertCenciUpSequence(t, calls, "has-session", "new-session", "new-window", "switch-client")
	assertCenciUpEnvScrubbed(t, calls[:3])
	if calls[3].tmuxEnv != "set" {
		t.Errorf("switch-client ran with TMUX=%s, want the unchanged environment (set)", calls[3].tmuxEnv)
	}
}

// P5: inside a personal tmux server `cenci up` never nests and never touches
// that server: every call is on -L cenci with TMUX scrubbed, the cenci session
// is still created, and the run exits 1 with the attach/detach guidance.
func TestCenciUpInsidePersonalServerRefuses(t *testing.T) {
	e := newCenciUpEnv(t, true, true)

	_, stderr, code := e.run(t, "/tmp/elsewhere/default,1,0")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr)
	}

	for _, want := range []string{"tmux -L cenci attach -t cenci", "detach"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	calls := e.calls(t)
	if cenciUpCount(calls, "new-session") != 1 {
		t.Errorf("the cenci session must still be created, got calls %v", cenciUpSubcommands(calls))
	}
	if cenciUpCount(calls, "attach-session") != 0 || cenciUpCount(calls, "switch-client") != 0 {
		t.Errorf("must neither attach nor switch from a personal server, got calls %v", cenciUpSubcommands(calls))
	}
	assertCenciUpEnvScrubbed(t, calls)
	if !e.exists("session") {
		t.Error("the cenci session was not created")
	}
}

// P7: without the shipped tmux config the run fails with the actionable hint
// before any tmux call, so no server or session is created.
func TestCenciUpMissingConfigFailsBeforeAnyTmuxCall(t *testing.T) {
	e := newCenciUpEnv(t, false, true)

	_, stderr, code := e.run(t, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "cenci-installer --tmux-defaults") {
		t.Errorf("stderr does not name the installer fix:\n%s", stderr)
	}
	if calls := e.calls(t); len(calls) != 0 {
		t.Errorf("tmux calls = %v, want none", cenciUpSubcommands(calls))
	}
	if e.exists("server") || e.exists("session") {
		t.Error("a server or session was created without a config")
	}
}

// P6: without lazyboards on PATH no board window is created, and nothing
// errors.
func TestCenciUpWithoutLazyboardsCreatesNoBoardWindow(t *testing.T) {
	e := newCenciUpEnv(t, true, false)

	_, stderr, code := e.run(t, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
	}

	assertCenciUpSequence(t, e.calls(t), "has-session", "new-session", "attach-session")
}

// P8: with a daemon already listening the ensure is a no-op and nothing warns.
func TestCenciUpRunningDaemonProducesNoWarning(t *testing.T) {
	e := newCenciUpEnv(t, true, true)

	_, stderr, code := e.run(t, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
	}
	if strings.Contains(strings.ToLower(stderr), "daemon") {
		t.Errorf("stderr mentions the daemon although it is running:\n%s", stderr)
	}
}

// An unknown flag or an extra argument is a usage error: exit 2, exactly one
// stderr line naming the verb, and no tmux call.
func TestCenciUpUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"extra argument", []string{"extra"}},
		{"unknown flag", []string{"--bogus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCenciUpEnv(t, true, true)

			stdout, stderr, code := e.run(t, "", tc.args...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2\nstderr: %s", code, stderr)
			}
			line := strings.TrimSuffix(stderr, "\n")
			if line == "" || strings.Contains(line, "\n") {
				t.Errorf("stderr must be exactly one line, got %q", stderr)
			}
			if !strings.HasPrefix(line, "cenci up") {
				t.Errorf("stderr line %q does not name the verb (want prefix %q)", line, "cenci up")
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if calls := e.calls(t); len(calls) != 0 {
				t.Errorf("tmux calls = %v, want none", cenciUpSubcommands(calls))
			}
		})
	}
}

// -h and --help print usage on stdout, exit 0 and make no tmux call.
func TestCenciUpHelp(t *testing.T) {
	for _, flagArg := range []string{"-h", "--help"} {
		t.Run(flagArg, func(t *testing.T) {
			e := newCenciUpEnv(t, true, true)

			stdout, stderr, code := e.run(t, "", flagArg)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", code, stderr)
			}
			if !strings.Contains(stdout, "cenci up") {
				t.Errorf("stdout lacks the usage text:\n%s", stdout)
			}
			if calls := e.calls(t); len(calls) != 0 {
				t.Errorf("tmux calls = %v, want none", cenciUpSubcommands(calls))
			}
		})
	}
}

// The top-level usage lists the new verb.
func TestCenciUpListedInTopLevelUsage(t *testing.T) {
	out, err := exec.Command(binaryPath, "help").Output()
	if err != nil {
		t.Fatalf("cenci help: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "up" {
			return
		}
	}
	t.Errorf("top-level usage has no `up` command line:\n%s", out)
}
