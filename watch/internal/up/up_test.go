package up_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/tmux"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/up"
)

// These tests inject the failures the black-box tests in watch/up_cmd_test.go
// cannot reach without a real daemon or two racing processes: a daemon that
// cannot start, a lost create race, a failing board window, a lookup that
// reports ErrDot, a malformed $TMUX and a PATH without tmux. tmux itself is a
// fake /bin/sh script on PATH (shell builtins only: PATH holds nothing else)
// that keeps a state directory:
//
//	session            exists once the cenci session has been created
//	race               new-session creates the session, then fails (a rival won)
//	fail-new-session   new-session fails and creates nothing
//	fail-new-window    new-window fails
//	calls              one "TMUX=<set|unset> | <argv>" line per call
const fakeTmuxScript = `#!/bin/sh
S=%s
t=unset; [ -n "${TMUX+x}" ] && t=set
printf 'TMUX=%%s | %%s\n' "$t" "$*" >> "$S/calls"
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
  if [ -e "$S/race" ]; then
    : > "$S/session"
    echo "duplicate session: cenci" >&2
    exit 1
  fi
  if [ -e "$S/fail-new-session" ]; then
    echo "boom: server exploded" >&2
    exit 1
  fi
  : > "$S/session"
  exit 0
  ;;
new-window)
  if [ -e "$S/fail-new-window" ]; then
    echo "no space for new window" >&2
    exit 1
  fi
  exit 0
  ;;
esac
exit 0
`

type execCall struct {
	argv0 string
	argv  []string
	env   []string
}

type call struct {
	tmuxEnv string
	args    string
	subcmd  string
}

type harness struct {
	t          *testing.T
	fakeDir    string
	stateDir   string
	tmuxPath   string
	lazyboards string
	conf       string
	execs      []execCall
	daemonRuns int
	daemonErr  error
	stderr     bytes.Buffer
}

// newHarness installs the fake tmux and lazyboards and pins PATH and
// TMUX_TMPDIR to temp directories, so the real tmux can never be reached.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, fakeDir: t.TempDir(), stateDir: t.TempDir()}
	h.tmuxPath = filepath.Join(h.fakeDir, "tmux")
	exectest.WriteExecutable(t, h.tmuxPath, fmt.Sprintf(fakeTmuxScript, exectest.ShellQuote(h.stateDir)))
	h.lazyboards = filepath.Join(h.fakeDir, "lazyboards")
	exectest.WriteExecutable(t, h.lazyboards, "#!/bin/sh\nexit 0\n")

	h.conf = filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(h.conf, []byte("# fake tmux config\n"), 0o644); err != nil {
		t.Fatalf("write tmux.conf: %v", err)
	}

	t.Setenv("PATH", h.fakeDir)
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	return h
}

func (h *harness) mark(name string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.stateDir, name), nil, 0o644); err != nil {
		h.t.Fatalf("mark %s: %v", name, err)
	}
}

func (h *harness) lookPath(file string) (string, error) {
	switch file {
	case "tmux":
		return h.tmuxPath, nil
	case "lazyboards":
		return h.lazyboards, nil
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

// env is the ambient environment handed to Run: the process environment minus
// any real $TMUX/$TMUX_PANE, plus tmuxVar as $TMUX when it is non-empty.
func (h *harness) env(tmuxVar string) func() []string {
	return func() []string {
		var env []string
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
				continue
			}
			env = append(env, kv)
		}
		if tmuxVar != "" {
			env = append(env, "TMUX="+tmuxVar)
		}
		return env
	}
}

func (h *harness) opts(tmuxVar string) up.Opts {
	return up.Opts{
		ConfigPath: h.conf,
		EnsureDaemon: func() error {
			h.daemonRuns++
			return h.daemonErr
		},
		LookPath: h.lookPath,
		Exec: func(argv0 string, argv, env []string) error {
			h.execs = append(h.execs, execCall{argv0: argv0, argv: argv, env: env})
			return nil
		},
		Env:    h.env(tmuxVar),
		Stderr: &h.stderr,
	}
}

func (h *harness) calls() []call {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.stateDir, "calls"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatalf("read tmux call log: %v", err)
	}
	var calls []call
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		prefix, args, ok := strings.Cut(line, " | ")
		if !ok {
			h.t.Fatalf("malformed tmux call log line %q", line)
		}
		rest, ok := strings.CutPrefix(args, "-L cenci ")
		if !ok {
			h.t.Errorf("tmux call does not start with -L cenci: %q", args)
		}
		if strings.HasPrefix(rest, "-f ") {
			_, rest, _ = strings.Cut(strings.TrimPrefix(rest, "-f "), " ")
		}
		subcmd, _, _ := strings.Cut(rest, " ")
		calls = append(calls, call{tmuxEnv: strings.TrimPrefix(prefix, "TMUX="), args: args, subcmd: subcmd})
	}
	return calls
}

func count(calls []call, subcmd string) int {
	n := 0
	for _, c := range calls {
		if c.subcmd == subcmd {
			n++
		}
	}
	return n
}

func subcommands(calls []call) []string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.subcmd)
	}
	return names
}

func containsSeq(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if strings.Join(args[i:i+len(seq)], "\x00") == strings.Join(seq, "\x00") {
			return true
		}
	}
	return false
}

// assertAttached checks that Run handed the terminal to `tmux -L cenci
// attach-session -t =cenci` exactly once, through the resolved tmux binary and
// without $TMUX or $TMUX_PANE in its environment.
func (h *harness) assertAttached() {
	h.t.Helper()
	if len(h.execs) != 1 {
		h.t.Fatalf("Exec calls = %d, want exactly 1 attach", len(h.execs))
	}
	e := h.execs[0]
	if e.argv0 != h.tmuxPath {
		h.t.Errorf("Exec argv0 = %q, want the resolved tmux %q", e.argv0, h.tmuxPath)
	}
	if !containsSeq(e.argv, "-L", "cenci") || !containsSeq(e.argv, "attach-session", "-t", "=cenci") {
		h.t.Errorf("Exec argv = %v, want -L cenci ... attach-session -t =cenci", e.argv)
	}
	for _, kv := range e.env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			h.t.Errorf("attach environment carries %q, want TMUX and TMUX_PANE scrubbed", kv)
		}
	}
}

func TestRunWarnsOnceWhenDaemonCannotStartAndStillAttaches(t *testing.T) {
	h := newHarness(t)
	h.daemonErr = errors.New("daemon did not become reachable")

	if err := up.Run(context.Background(), h.opts("")); err != nil {
		t.Fatalf("Run: %v, want nil (a daemon failure only warns)", err)
	}

	if h.daemonRuns != 1 {
		t.Errorf("EnsureDaemon calls = %d, want exactly 1", h.daemonRuns)
	}
	var lines []string
	for _, l := range strings.Split(h.stderr.String(), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "daemon did not become reachable") {
		t.Errorf("stderr = %q, want exactly one warning line carrying the cause", h.stderr.String())
	}
	if count(h.calls(), "new-session") != 1 {
		t.Errorf("the session must still be created, got calls %v", subcommands(h.calls()))
	}
	h.assertAttached()
}

func TestRunLostCreateRaceAttachesWithoutBoard(t *testing.T) {
	h := newHarness(t)
	h.mark("race")

	if err := up.Run(context.Background(), h.opts("")); err != nil {
		t.Fatalf("Run: %v, want nil (the rival run created the session)", err)
	}

	calls := h.calls()
	if n := count(calls, "new-session"); n != 1 {
		t.Errorf("new-session calls = %d, want 1", n)
	}
	if n := count(calls, "has-session"); n != 2 {
		t.Errorf("has-session probes = %d, want 2 (one before and one after the failed create)", n)
	}
	if n := count(calls, "new-window"); n != 0 {
		t.Errorf("new-window calls = %d, want 0: a session this run did not create gets no board", n)
	}
	h.assertAttached()
}

func TestRunFailedCreateWithoutSessionReturnsTmuxStderr(t *testing.T) {
	h := newHarness(t)
	h.mark("fail-new-session")

	err := up.Run(context.Background(), h.opts(""))
	if err == nil {
		t.Fatal("Run returned nil, want the failed create reported")
	}
	if !strings.Contains(err.Error(), "boom: server exploded") {
		t.Errorf("error %q does not carry tmux's stderr", err)
	}
	if len(h.execs) != 0 {
		t.Errorf("Exec calls = %d, want none after a failed create", len(h.execs))
	}
}

func TestRunBoardWindowFailureWarnsAndStillAttaches(t *testing.T) {
	h := newHarness(t)
	h.mark("fail-new-window")

	if err := up.Run(context.Background(), h.opts("")); err != nil {
		t.Fatalf("Run: %v, want nil (a failed board only warns)", err)
	}

	if count(h.calls(), "new-window") != 1 {
		t.Errorf("want the board window attempted once, got calls %v", subcommands(h.calls()))
	}
	if !strings.Contains(strings.ToLower(h.stderr.String()), "board") {
		t.Errorf("stderr = %q, want a warning about the board window", h.stderr.String())
	}
	h.assertAttached()
}

func TestRunLookPathErrDotMeansNoBoard(t *testing.T) {
	h := newHarness(t)
	opts := h.opts("")
	opts.LookPath = func(file string) (string, error) {
		if file == "lazyboards" {
			return "", &exec.Error{Name: file, Err: exec.ErrDot}
		}
		return h.lookPath(file)
	}

	if err := up.Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v, want nil (a lookup error means lazyboards is absent)", err)
	}

	if n := count(h.calls(), "new-window"); n != 0 {
		t.Errorf("new-window calls = %d, want 0", n)
	}
	h.assertAttached()
}

// Outside tmux a stray $TMUX (set but empty) or $TMUX_PANE in the ambient
// environment must be dropped from the attach-session environment too, not just
// from the probe, create and board calls.
func TestRunOutsideTmuxAttachDropsStrayTmuxVars(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"empty TMUX and TMUX_PANE set", map[string]string{"TMUX": "", "TMUX_PANE": "%3"}},
		{"TMUX_PANE set without TMUX", map[string]string{"TMUX_PANE": "%3"}},
		{"empty TMUX alone", map[string]string{"TMUX": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			// os.Unsetenv is restored by t.Setenv's cleanup; the ambient process
			// environment is what Run reads here.
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")
			if err := os.Unsetenv("TMUX"); err != nil {
				t.Fatalf("unset TMUX: %v", err)
			}
			if err := os.Unsetenv("TMUX_PANE"); err != nil {
				t.Fatalf("unset TMUX_PANE: %v", err)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			opts := h.opts("")
			opts.Env = os.Environ

			if err := up.Run(context.Background(), opts); err != nil {
				t.Fatalf("Run: %v, want nil", err)
			}

			for _, c := range h.calls() {
				if c.tmuxEnv != "unset" {
					t.Errorf("%s ran with $TMUX set: %q", c.subcmd, c.args)
				}
			}
			h.assertAttached()
		})
	}
}

func TestRunMalformedTmuxEnvIsPersonalServerRefusal(t *testing.T) {
	h := newHarness(t)

	err := up.Run(context.Background(), h.opts(",1,2"))
	if !errors.Is(err, up.ErrPersonalServer) {
		t.Fatalf("Run error = %v, want ErrPersonalServer", err)
	}

	calls := h.calls()
	if count(calls, "new-session") != 1 {
		t.Errorf("the cenci session must still be created, got calls %v", subcommands(calls))
	}
	if count(calls, "attach-session") != 0 || count(calls, "switch-client") != 0 {
		t.Errorf("must neither attach nor switch, got calls %v", subcommands(calls))
	}
	for _, c := range calls {
		if c.tmuxEnv != "unset" {
			t.Errorf("%s ran with $TMUX set: %q", c.subcmd, c.args)
		}
	}
	if len(h.execs) != 0 {
		t.Errorf("Exec calls = %d, want none", len(h.execs))
	}
}

func TestRunInsideCenciServerSwitchesClient(t *testing.T) {
	h := newHarness(t)
	h.mark("session")
	cenciSocket, err := tmux.CenciSocketPath()
	if err != nil || filepath.Base(cenciSocket) != "cenci" {
		t.Fatalf("CenciSocketPath() = %q, %v, want a path ending in /cenci", cenciSocket, err)
	}

	if err := up.Run(context.Background(), h.opts(cenciSocket+",1,0")); err != nil {
		t.Fatalf("Run: %v, want nil", err)
	}

	calls := h.calls()
	if got, want := strings.Join(subcommands(calls), ","), "has-session,switch-client"; got != want {
		t.Errorf("tmux subcommands = %s, want %s", got, want)
	}
	if len(h.execs) != 0 {
		t.Errorf("Exec calls = %d, want none: a switch is not an attach", len(h.execs))
	}
}

func TestRunMissingConfigFailsBeforeAnyWork(t *testing.T) {
	h := newHarness(t)
	opts := h.opts("")
	opts.ConfigPath = filepath.Join(t.TempDir(), "absent", "tmux.conf")

	err := up.Run(context.Background(), opts)
	if !errors.Is(err, up.ErrConfigMissing) {
		t.Fatalf("Run error = %v, want ErrConfigMissing", err)
	}
	if !strings.Contains(err.Error(), "cenci-installer --tmux-defaults") {
		t.Errorf("error %q does not name the installer fix", err)
	}
	if h.daemonRuns != 0 {
		t.Errorf("EnsureDaemon calls = %d, want 0", h.daemonRuns)
	}
	if calls := h.calls(); len(calls) != 0 {
		t.Errorf("tmux calls = %v, want none", subcommands(calls))
	}
}

func TestRunTmuxMissingFromPathNamesTmux(t *testing.T) {
	h := newHarness(t)
	t.Setenv("PATH", t.TempDir())
	opts := h.opts("")
	opts.LookPath = exec.LookPath

	err := up.Run(context.Background(), opts)
	if err == nil {
		t.Fatal("Run returned nil, want an error when tmux is not on PATH")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error %q does not name tmux", err)
	}
	if len(h.execs) != 0 {
		t.Errorf("Exec calls = %d, want none", len(h.execs))
	}
}
