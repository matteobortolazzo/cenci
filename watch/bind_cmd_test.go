package main_test

// Ticket #1529: `cenci bind <session-id>` through the real binary against an
// in-process EventReceiver stub on the isolated default event socket
// (CENCI_SOCKET_DIR, watch/docs/test-isolation.md).

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

const (
	bindTestTMUX    = "/tmp/tmux-1000/default,4242,0"
	bindTestPane    = "%20"
	bindTestSession = "019a2b3c-thread"
	bindUsageLine   = "usage: cenci bind <session-id>"
)

// bindStub is the daemon side of a `cenci bind` run: a real EventReceiver on the
// isolated default event socket whose bind handler records each request and
// answers with resp.
type bindStub struct {
	base, socketDir string
	recv            *ipc.EventReceiver

	mu   sync.Mutex
	reqs []ipc.BindRequest
	resp ipc.BindResponse
}

// newBindStub prepares an isolated socket directory; with listen it serves the
// receiver there, otherwise nothing listens (daemon unreachable).
func newBindStub(t *testing.T, resp ipc.BindResponse, listen bool) *bindStub {
	t.Helper()
	base, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	s := &bindStub{base: base, socketDir: filepath.Join(base, "run"), resp: resp}
	if err := os.Mkdir(s.socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if !listen {
		return s
	}
	recv, err := ipc.NewEventReceiver(filepath.Join(s.socketDir, watch.EventSocketBasename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	recv.SetBindHandler(func(req ipc.BindRequest) ipc.BindResponse {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reqs = append(s.reqs, req)
		return s.resp
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go recv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)
	s.recv = recv
	return s
}

// received reports the bind requests and hook events the stub got.
func (s *bindStub) received() (reqs []ipc.BindRequest, events int) {
	if s.recv != nil {
		for {
			select {
			case <-s.recv.Events():
				events++
				continue
			default:
			}
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ipc.BindRequest(nil), s.reqs...), events
}

func (s *bindStub) assertNothingSent(t *testing.T) {
	t.Helper()
	if reqs, events := s.received(); len(reqs) != 0 || events != 0 {
		t.Errorf("the daemon received %d binds %+v and %d events, want nothing", len(reqs), reqs, events)
	}
}

// runBind runs `cenci bind args...` with $TMUX_PANE and $TMUX set as given
// (nil = unset) and returns the exit code and both streams.
func runBind(t *testing.T, s *bindStub, pane, tmux *string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(binaryPath, append([]string{"bind"}, args...)...)
	env := []string{
		"CENCI_SOCKET_DIR=" + s.socketDir,
		"XDG_STATE_HOME=" + filepath.Join(s.base, "state"),
		"XDG_CONFIG_HOME=" + filepath.Join(s.base, "config"),
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") && !strings.HasPrefix(kv, "CENCI_SOCKET_DIR=") {
			env = append(env, kv)
		}
	}
	if pane != nil {
		env = append(env, "TMUX_PANE="+*pane)
	}
	if tmux != nil {
		env = append(env, "TMUX="+*tmux)
	}
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("run cenci bind: %v", err)
	}
	return code, out.String(), errOut.String()
}

func inTmux() (pane, tmux *string) {
	p, x := bindTestPane, bindTestTMUX
	return &p, &x
}

// P3: a missing or extra argument and an unknown flag are usage errors: exit 2,
// one stderr line, nothing sent.
func TestBindCmd_UsageErrorsExit2WithOneLine(t *testing.T) {
	for name, args := range map[string][]string{
		"missing argument":          nil,
		"extra argument":            {bindTestSession, "extra"},
		"unknown flag":              {"--bogus", bindTestSession},
		"unknown flag after the id": {bindTestSession, "--bogus"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newBindStub(t, ipc.BindResponse{OK: true, Pane: bindTestPane}, true)
			pane, tmux := inTmux()
			code, stdout, stderr := runBind(t, s, pane, tmux, args...)
			if code != 2 || stdout != "" {
				t.Errorf("exit %d stdout %q, want 2 and nothing on stdout", code, stdout)
			}
			if strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, bindUsageLine) {
				t.Errorf("stderr = %q, want one line with %q", stderr, bindUsageLine)
			}
			s.assertNothingSent(t)
		})
	}
}

// P3: -h and --help print the usage to stdout and exit 0.
func TestBindCmd_HelpPrintsUsageToStdout(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		t.Run(flag, func(t *testing.T) {
			s := newBindStub(t, ipc.BindResponse{}, true)
			pane, tmux := inTmux()
			code, stdout, stderr := runBind(t, s, pane, tmux, flag)
			if code != 0 || !strings.Contains(stdout, bindUsageLine) || stderr != "" {
				t.Errorf("exit %d stdout %q stderr %q, want 0 with the usage on stdout", code, stdout, stderr)
			}
			s.assertNothingSent(t)
		})
	}
}

// P11: outside tmux (an empty $TMUX_PANE or $TMUX) the command exits 1 with
// `not inside a tmux pane` and never contacts the daemon.
func TestBindCmd_OutsideTmuxExits1AndSendsNothing(t *testing.T) {
	empty := ""
	pane, tmux := inTmux()
	for name, env := range map[string][2]*string{
		"empty TMUX_PANE": {&empty, tmux},
		"empty TMUX":      {pane, &empty},
		"both unset":      {nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			s := newBindStub(t, ipc.BindResponse{OK: true, Pane: bindTestPane}, true)
			code, stdout, stderr := runBind(t, s, env[0], env[1], bindTestSession)
			if code != 1 || stdout != "" || stderr != "cenci bind: not inside a tmux pane\n" {
				t.Errorf("exit %d stdout %q stderr %q, want 1 and `not inside a tmux pane`", code, stdout, stderr)
			}
			s.assertNothingSent(t)
		})
	}
}

// P3/P12/P13: each daemon refusal exits 1 with the reason on one stderr line.
func TestBindCmd_RelaysEachDaemonReason(t *testing.T) {
	for _, reason := range []string{
		daemon.ReasonBindUnknownSession,
		daemon.ReasonBindNotCodex,
		daemon.ReasonBindUntrustedSocket,
		daemon.ReasonBindPaneNotListed,
		daemon.ReasonBindServerPIDMismatch,
		daemon.ReasonBindListingFailed,
		daemon.ReasonBindNonCodexPaneHolder,
		daemon.ReasonBindInvalidRequest,
		daemon.ReasonBindRateLimited,
		daemon.ReasonBindTimedOut,
	} {
		t.Run(reason, func(t *testing.T) {
			s := newBindStub(t, ipc.BindResponse{OK: false, Reason: reason}, true)
			pane, tmux := inTmux()
			code, stdout, stderr := runBind(t, s, pane, tmux, bindTestSession)
			if code != 1 || stdout != "" || stderr != "cenci bind: "+reason+"\n" {
				t.Errorf("exit %d stdout %q stderr %q, want 1 and the reason on one line", code, stdout, stderr)
			}
		})
	}
}

// P3: an unreachable daemon exits 1 with one stderr line.
func TestBindCmd_DaemonUnreachableExits1(t *testing.T) {
	s := newBindStub(t, ipc.BindResponse{}, false)
	pane, tmux := inTmux()
	code, stdout, stderr := runBind(t, s, pane, tmux, bindTestSession)
	if code != 1 || stdout != "" || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "not reachable") {
		t.Errorf("exit %d stdout %q stderr %q, want 1 and one `not reachable` line", code, stdout, stderr)
	}
}

// P3: a recorded binding exits 0 with one line naming the session and the pane,
// and the request carries the caller's own pane, socket and server pid.
func TestBindCmd_SuccessPrintsOneLineNamingSessionAndPane(t *testing.T) {
	s := newBindStub(t, ipc.BindResponse{OK: true, Pane: bindTestPane}, true)
	pane, tmux := inTmux()
	code, stdout, stderr := runBind(t, s, pane, tmux, bindTestSession)
	if want := "bound Codex session " + bindTestSession + " to tmux pane " + bindTestPane + "\n"; code != 0 || stdout != want || stderr != "" {
		t.Errorf("exit %d stdout %q stderr %q, want 0 and %q", code, stdout, stderr, want)
	}
	reqs, events := s.received()
	want := ipc.BindRequest{SessionID: bindTestSession, TmuxPane: bindTestPane, TmuxSocket: "/tmp/tmux-1000/default", TmuxServerPID: "4242"}
	if len(reqs) != 1 || reqs[0] != want || events != 0 {
		t.Errorf("daemon received %+v and %d events, want exactly %+v", reqs, events, want)
	}
}
