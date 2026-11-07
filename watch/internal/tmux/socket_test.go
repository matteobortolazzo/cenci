package tmux

// Ticket #1428: every socket-carrying tmux call goes through one hardened
// definition (shape check, allowlist env, bounded exec) and the trusted-socket
// primitives (directory check, resolver) live beside it.
//
// The tests drive the public entry points over a PATH shim. The shim is the
// test binary itself, symlinked as `tmux` and dispatched by TestMain on the
// basename of argv[0]. Re-executing the Go binary (not an sh script) gives an
// exact record of the child's argv and environment: an `sh` shim that dumped
// `env` would add PWD/SHLVL/_ of its own. The shim records every invocation
// under $HOME/calls (HOME is in the allowlist, so it survives the scrubbed
// env), and an empty calls directory is the "no tmux was exec'd" marker.
//
// The only seams overridden are windowCmdTimeout (shortened, like
// TestListLivePanes_MissingBinaryAndTimeoutFailClosed) and socketOwnerUID.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	ambientTMUX     = "/tmp/ambient/sock,4242,0"
	ambientPane     = "%9"
	ambientTmpdir   = "/tmp/ambient-tmpdir"
	sentinelVar     = "CENCI_TEST_SENTINEL_SECRET"
	sentinelValue   = "hunter2"
	ambientTerm     = "xterm-test-term"
	shortTimeout    = 300 * time.Millisecond
	boundSlack      = 1500 * time.Millisecond
	fakeModeFile    = "mode"
	fakeCallsDir    = "calls"
	fakeGrandchild  = "grandchild.pid"
	fakeModeHang    = "hang"
	fakeModeHangGrc = "hang-grandchild"
	fakeModeExitGrc = "exit-grandchild"
)

// TestMain turns the test binary into the fake `tmux` when it is exec'd under
// that name.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "tmux" {
		os.Exit(runAsFakeTmux())
	}
	os.Exit(m.Run())
}

type fakeCall struct {
	Args []string
	Env  []string
}

// runAsFakeTmux records its argv and environment under $HOME/calls, then acts
// out the mode in $HOME/mode: exit 0 by default, hang forever, or hang while a
// forked grandchild keeps the inherited stdout pipe open.
func runAsFakeTmux() int {
	home, ok := os.LookupEnv("HOME")
	if !ok || home == "" {
		fmt.Fprintln(os.Stderr, "fake tmux: HOME is not set")
		return 97
	}
	rec, err := json.Marshal(fakeCall{Args: os.Args[1:], Env: os.Environ()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake tmux:", err)
		return 98
	}
	name := fmt.Sprintf("%020d-%d.json", time.Now().UnixNano(), os.Getpid())
	if err := os.WriteFile(filepath.Join(home, fakeCallsDir, name), rec, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "fake tmux:", err)
		return 98
	}
	mode, _ := os.ReadFile(filepath.Join(home, fakeModeFile))
	switch strings.TrimSpace(string(mode)) {
	case fakeModeHang:
		time.Sleep(time.Hour)
	case fakeModeHangGrc:
		gc := exec.Command("sleep", "10")
		gc.Stdout = os.Stdout
		gc.Stderr = os.Stderr
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "fake tmux:", err)
			return 99
		}
		_ = os.WriteFile(filepath.Join(home, fakeGrandchild), []byte(strconv.Itoa(gc.Process.Pid)), 0o600)
		time.Sleep(time.Hour)
	case fakeModeExitGrc:
		gc := exec.Command("sleep", "10")
		gc.Stdout = os.Stdout
		gc.Stderr = os.Stderr
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "fake tmux:", err)
			return 99
		}
		_ = os.WriteFile(filepath.Join(home, fakeGrandchild), []byte(strconv.Itoa(gc.Process.Pid)), 0o600)
	}
	return 0
}

// installTestBinaryAsTmux puts a `tmux` symlink to the running test binary
// first on PATH, points HOME at a fresh directory the shim records into, and
// returns that directory. mode is the behavior written to $HOME/mode ("" =
// exit 0).
func installTestBinaryAsTmux(t *testing.T, mode string) (home string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(shimDir, "tmux")); err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	if err := os.Mkdir(filepath.Join(home, fakeCallsDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		if err := os.WriteFile(filepath.Join(home, fakeModeFile), []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", home)
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(home, fakeGrandchild))
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return home
}

// recordedCalls returns every invocation the shim recorded, oldest first.
func recordedCalls(t *testing.T, home string) []fakeCall {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, fakeCallsDir))
	if err != nil {
		t.Fatal(err)
	}
	var calls []fakeCall
	for _, e := range entries { // ReadDir sorts by name; names lead with a nanosecond stamp
		raw, err := os.ReadFile(filepath.Join(home, fakeCallsDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var c fakeCall
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("decoding recorded call %s: %v", e.Name(), err)
		}
		calls = append(calls, c)
	}
	return calls
}

// setAmbientTmuxEnv fills the ambient environment with everything a socket
// call must NOT forward (TMUX, TMUX_PANE, TMUX_TMPDIR, a secret-looking
// sentinel) next to a TERM, and unsets LANG so "only the ones that are set" is
// observable.
func setAmbientTmuxEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TMUX", ambientTMUX)
	t.Setenv("TMUX_PANE", ambientPane)
	t.Setenv("TMUX_TMPDIR", ambientTmpdir)
	t.Setenv(sentinelVar, sentinelValue)
	t.Setenv("TERM", ambientTerm)
	t.Setenv("LANG", "unset-below")
	if err := os.Unsetenv("LANG"); err != nil {
		t.Fatal(err)
	}
}

func setWindowCmdTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := windowCmdTimeout
	windowCmdTimeout = d
	t.Cleanup(func() { windowCmdTimeout = old })
}

// validSocket is a shape-valid absolute socket path (the primitive checks
// shape only; trust is a separate check).
func validSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "s.sock")
}

// socketCall is one public entry point that takes a leading socket, with the
// argv tmux must be given after any `-S <socket>` prefix.
type socketCall struct {
	name string
	run  func(socket string) error
	want []string
}

func socketCalls() []socketCall {
	c := &ExecClient{}
	return []socketCall{
		{"ListPanes", func(s string) error { _, err := c.ListPanes(s); return err },
			[]string{"list-panes", "-a", "-F", listFormat}},
		{"RenameWindow", func(s string) error { return c.RenameWindow(s, "=main:0", "name") },
			[]string{"rename-window", "-t", "=main:0", "name"}},
		{"SetWindowOption", func(s string) error { return c.SetWindowOption(s, "=main:0", "automatic-rename", "off") },
			[]string{"set-window-option", "-t", "=main:0", "automatic-rename", "off"}},
		{"GetWindowOption", func(s string) error { _, err := c.GetWindowOption(s, "=main:0", "automatic-rename"); return err },
			[]string{"show-window-option", "-t", "=main:0", "-v", "automatic-rename"}},
		{"SetOption", func(s string) error { return c.SetOption(s, "@cenci-headroom-claude", "73") },
			[]string{"set-option", "-g", "@cenci-headroom-claude", "73"}},
		{"CurrentSession", func(s string) error { _, err := c.CurrentSession(s); return err },
			[]string{"display-message", "-t", ambientPane, "-p", "#{session_name}"}},
		{"IsGroupedSession", func(s string) error { _, err := c.IsGroupedSession(s, "=work"); return err },
			[]string{"display-message", "-t", "=work", "-p", "#{session_grouped}"}},
		{"NewWindow", func(s string) error { return c.NewWindow(s, "=work", "40-implement", "agent command") },
			[]string{"new-window", "-d", "-t", "=work:", "-n", "40-implement", "agent command"}},
		{"HasSession", func(s string) error { _, err := c.HasSession(s, "work"); return err },
			[]string{"has-session", "-t", "=work"}},
		{"KillWindow", func(s string) error { return c.KillWindow(s, "%3") },
			[]string{"kill-window", "-t", "%3"}},
		{"ListWindowPaneProcs", func(s string) error { _, err := ListWindowPaneProcs(s, "=main:0"); return err },
			[]string{"list-panes", "-t", "=main:0", "-F", paneOwnedFormat}},
		{"SetPaneOption", func(s string) error { return SetPaneOption(s, "%1", "@cenci-launch-id", "abc") },
			[]string{"set-option", "-p", "-t", "%1", "--", "@cenci-launch-id", "abc"}},
	}
}

func callNamed(t *testing.T, name string) socketCall {
	t.Helper()
	for _, c := range socketCalls() {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no socket call named %q", name)
	return socketCall{}
}

// sorted returns a sorted copy so env sets compare independent of order.
func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// P20: a non-empty socket that fails the shape check errors and never execs.
func TestSocketCalls_MalformedSocketErrorsWithoutExec(t *testing.T) {
	malformed := []struct{ name, socket string }{
		{"relative path", "relative/s.sock"},
		{"bare name", "cenci"},
		{"tab", "/a\tb"},
		{"newline", "/a\nb"},
		{"over-long", "/" + strings.Repeat("a", 1001)},
	}
	for _, bad := range malformed {
		for _, call := range socketCalls() {
			t.Run(call.name+"/"+bad.name, func(t *testing.T) {
				home := installTestBinaryAsTmux(t, "")
				setAmbientTmuxEnv(t)

				err := call.run(bad.socket)

				if !errors.Is(err, ErrMalformedSocket) {
					t.Errorf("err = %v, want it to wrap ErrMalformedSocket", err)
				}
				if got := recordedCalls(t, home); len(got) != 0 {
					t.Errorf("tmux was exec'd %d time(s) for a malformed socket: %+v", len(got), got)
				}
			})
		}
	}
}

// P20 for the shared exec the reaper calls directly.
func TestRunSocket_MalformedSocketErrorsWithoutExec(t *testing.T) {
	for _, socket := range []string{"relative/s.sock", "/a\tb", "/a\nb"} {
		t.Run(strconv.Quote(socket), func(t *testing.T) {
			home := installTestBinaryAsTmux(t, "")
			var stdout, stderr bytes.Buffer

			err := RunSocket(context.Background(), socket, &stdout, &stderr, "list-panes", "-a")

			if !errors.Is(err, ErrMalformedSocket) {
				t.Errorf("err = %v, want it to wrap ErrMalformedSocket", err)
			}
			if got := recordedCalls(t, home); len(got) != 0 {
				t.Errorf("tmux was exec'd for a malformed socket: %+v", got)
			}
		})
	}
}

// A shape-valid socket execs `tmux -S <socket> <args...>`, the socket first.
func TestSocketCalls_ArgvCarriesDashSBeforeTheCommand(t *testing.T) {
	for _, call := range socketCalls() {
		t.Run(call.name, func(t *testing.T) {
			home := installTestBinaryAsTmux(t, "")
			setAmbientTmuxEnv(t)
			socket := validSocket(t)

			_ = call.run(socket) // the shim exits 0 with no output; some callers reject that after exec

			calls := recordedCalls(t, home)
			if len(calls) == 0 {
				t.Fatal("tmux was never exec'd for a shape-valid socket")
			}
			want := append([]string{"-S", socket}, call.want...)
			if !slices.Equal(calls[0].Args, want) {
				t.Errorf("argv = %q, want %q", calls[0].Args, want)
			}
		})
	}
}

// P21 defaults: the bound the AC names is 5s plus a 2s WaitDelay.
func TestSocketCallBounds_Defaults(t *testing.T) {
	waitDelay := windowCmdWaitDelay
	if windowCmdTimeout != 5*time.Second {
		t.Errorf("windowCmdTimeout = %v, want 5s", windowCmdTimeout)
	}
	if waitDelay != 2*time.Second {
		t.Errorf("windowCmdWaitDelay = %v, want 2s", waitDelay)
	}
}

// P21: a server that never replies yields a DeadlineExceeded error within the
// timeout plus the WaitDelay, for every socket-carrying entry point (HasSession
// included: a context kill surfaces as an ExitError and must not read as the
// benign "absent" (false, nil)).
func TestSocketCalls_HangingTmuxTimesOutWithDeadlineExceeded(t *testing.T) {
	for _, call := range socketCalls() {
		t.Run(call.name, func(t *testing.T) {
			installTestBinaryAsTmux(t, fakeModeHang)
			setAmbientTmuxEnv(t)
			setWindowCmdTimeout(t, shortTimeout)

			start := time.Now()
			err := call.run(validSocket(t))
			elapsed := time.Since(start)

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
			}
			if limit := shortTimeout + windowCmdWaitDelay + boundSlack; elapsed > limit {
				t.Errorf("call took %v, want at most %v (timeout + WaitDelay + slack)", elapsed, limit)
			}
		})
	}
}

// P21: a hung tmux that forked a grandchild still holding the stdout pipe must
// not stall the caller past the WaitDelay (watch/docs/go-gotchas.md). One call
// on each exec path (tmuxCmd-style and windowCmd-style).
func TestSocketCalls_GrandchildHoldingStdoutIsBoundedByWaitDelay(t *testing.T) {
	for _, name := range []string{"HasSession", "KillWindow"} {
		call := callNamed(t, name)
		t.Run(name, func(t *testing.T) {
			installTestBinaryAsTmux(t, fakeModeHangGrc)
			setAmbientTmuxEnv(t)
			setWindowCmdTimeout(t, shortTimeout)

			start := time.Now()
			err := call.run(validSocket(t))
			elapsed := time.Since(start)

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
			}
			if limit := shortTimeout + windowCmdWaitDelay + boundSlack; elapsed > limit {
				t.Errorf("call took %v, want at most %v (timeout + WaitDelay + slack)", elapsed, limit)
			}
		})
	}
}

// P21: a tmux that EXITS 0 while a forked grandchild still holds the output
// pipes is ended by the WaitDelay alone (exec.ErrWaitDelay), not by the call
// timeout, and RunSocket must still report it as a timeout. The timeout is set
// well above the WaitDelay (and below the grandchild's lifetime) so the context
// deadline cannot be what fires: a shortened timeout would expire during the
// WaitDelay and take the other branch.
func TestRunSocket_ExitedTmuxWithGrandchildHoldingPipeReportsDeadlineExceeded(t *testing.T) {
	installTestBinaryAsTmux(t, fakeModeExitGrc)
	timeout := 3 * windowCmdWaitDelay
	setWindowCmdTimeout(t, timeout)
	var stdout, stderr bytes.Buffer

	start := time.Now()
	err := RunSocket(context.Background(), validSocket(t), &stdout, &stderr, "list-panes", "-a")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Errorf("err = %v, want it to wrap exec.ErrWaitDelay (the WaitDelay, not the timeout, ended the call)", err)
	}
	if limit := windowCmdWaitDelay + boundSlack; elapsed > limit {
		t.Errorf("call took %v, want at most %v (WaitDelay + slack, well under the %v timeout)", elapsed, limit, timeout)
	}
}

// P21 for the shared exec: the caller's context bounds the call.
func TestRunSocket_ContextDeadlineBoundsAHangingTmux(t *testing.T) {
	installTestBinaryAsTmux(t, fakeModeHangGrc)
	ctx, cancel := context.WithTimeout(context.Background(), shortTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer

	start := time.Now()
	err := RunSocket(ctx, validSocket(t), &stdout, &stderr, "list-panes", "-a")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if limit := shortTimeout + windowCmdWaitDelay + boundSlack; elapsed > limit {
		t.Errorf("call took %v, want at most %v", elapsed, limit)
	}
}

// P21 against real tmux: a Unix listener that accepts the connection and never
// replies. Skipped without a tmux binary.
func TestSocketCall_RealTmuxAgainstNeverRepliedListener(t *testing.T) {
	requireTmux(t)
	dir, err := os.MkdirTemp("", "t") // short: a Unix socket path is length-bound
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { <-done; _ = conn.Close() }()
		}
	}()
	setWindowCmdTimeout(t, 500*time.Millisecond)

	start := time.Now()
	exists, err := (&ExecClient{}).HasSession(socket, "work")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("HasSession = (%v, %v), want an error wrapping context.DeadlineExceeded", exists, err)
	}
	if limit := 500*time.Millisecond + windowCmdWaitDelay + boundSlack; elapsed > limit {
		t.Errorf("call took %v, want at most %v", elapsed, limit)
	}
}

// P22: the child of a socket-carrying call sees exactly the allowlisted
// variables that are set, with their ambient values: never TMUX, TMUX_PANE,
// TMUX_TMPDIR, the sentinel or anything else. LANG is unset here and must stay
// absent rather than be fabricated. Every entry point is covered, SetPaneOption
// (which used to inherit the full environment) included.
func TestSocketCalls_ChildEnvIsOnlyTheSetAllowlist(t *testing.T) {
	for _, call := range socketCalls() {
		t.Run(call.name, func(t *testing.T) {
			home := installTestBinaryAsTmux(t, "")
			setAmbientTmuxEnv(t)

			_ = call.run(validSocket(t))

			calls := recordedCalls(t, home)
			if len(calls) == 0 {
				t.Fatal("tmux was never exec'd")
			}
			want := sorted([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=" + ambientTerm})
			for i, c := range calls {
				if got := sorted(c.Env); !slices.Equal(got, want) {
					t.Errorf("invocation %d child env = %q, want exactly %q", i, got, want)
				}
			}
		})
	}
}

// P22: LANG is part of the allowlist and is forwarded when it is set.
func TestSocketCalls_ChildEnvForwardsLANGWhenSet(t *testing.T) {
	for _, name := range []string{"HasSession", "KillWindow", "SetPaneOption"} {
		call := callNamed(t, name)
		t.Run(name, func(t *testing.T) {
			home := installTestBinaryAsTmux(t, "")
			setAmbientTmuxEnv(t)
			t.Setenv("LANG", "C.UTF-8")

			_ = call.run(validSocket(t))

			calls := recordedCalls(t, home)
			if len(calls) == 0 {
				t.Fatal("tmux was never exec'd")
			}
			want := sorted([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=" + ambientTerm, "LANG=C.UTF-8"})
			if got := sorted(calls[0].Env); !slices.Equal(got, want) {
				t.Errorf("child env = %q, want exactly %q", got, want)
			}
		})
	}
}

// P22 for the shared exec the reaper calls directly.
func TestRunSocket_ChildEnvIsOnlyTheSetAllowlist(t *testing.T) {
	home := installTestBinaryAsTmux(t, "")
	setAmbientTmuxEnv(t)
	var stdout, stderr bytes.Buffer

	if err := RunSocket(context.Background(), validSocket(t), &stdout, &stderr, "list-panes", "-a"); err != nil {
		t.Fatalf("RunSocket: %v", err)
	}

	calls := recordedCalls(t, home)
	if len(calls) != 1 {
		t.Fatalf("tmux exec'd %d times, want 1", len(calls))
	}
	want := sorted([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=" + ambientTerm})
	if got := sorted(calls[0].Env); !slices.Equal(got, want) {
		t.Errorf("child env = %q, want exactly %q", got, want)
	}
}

// P35: an empty socket keeps every call byte-identical to today's: no -S, the
// unchanged argv, and the full ambient environment (TMUX, TMUX_PANE,
// TMUX_TMPDIR and the sentinel included). SetPaneOption always names a socket,
// so it has no empty form.
func TestSocketCalls_EmptySocketKeepsArgvAndAmbientEnv(t *testing.T) {
	for _, call := range socketCalls() {
		if call.name == "SetPaneOption" {
			continue
		}
		t.Run(call.name, func(t *testing.T) {
			home := installTestBinaryAsTmux(t, "")
			setAmbientTmuxEnv(t)
			parentEnv := os.Environ()

			_ = call.run("")

			calls := recordedCalls(t, home)
			if len(calls) == 0 {
				t.Fatal("tmux was never exec'd")
			}
			if !slices.Equal(calls[0].Args, call.want) {
				t.Errorf("argv = %q, want %q (no -S)", calls[0].Args, call.want)
			}
			for _, e := range parentEnv {
				if !slices.Contains(calls[0].Env, e) {
					t.Errorf("ambient variable %q missing from the child env", strings.SplitN(e, "=", 2)[0])
				}
			}
			for _, e := range []string{"TMUX=" + ambientTMUX, "TMUX_PANE=" + ambientPane, "TMUX_TMPDIR=" + ambientTmpdir, sentinelVar + "=" + sentinelValue} {
				if !slices.Contains(calls[0].Env, e) {
					t.Errorf("child env lacks %q; an empty socket must pass the ambient env through", e)
				}
			}
		})
	}
}

// uidDirName is the per-user directory tmux creates under its tmpdir.
func uidDirName() string { return fmt.Sprintf("tmux-%d", os.Getuid()) }

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// trustedFixture points TMUX_TMPDIR at a fresh directory holding tmux-<uid>
// with the given permissions, and returns the directory TrustedSocketDir
// reports for it.
func trustedFixture(t *testing.T, perm os.FileMode) string {
	t.Helper()
	tmp := resolvedTempDir(t)
	t.Setenv("TMUX_TMPDIR", tmp)
	dir := filepath.Join(tmp, uidDirName())
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, perm); err != nil {
		t.Fatal(err)
	}
	got, err := TrustedSocketDir()
	if err != nil {
		t.Fatalf("TrustedSocketDir: %v", err)
	}
	if got != dir {
		t.Fatalf("TrustedSocketDir = %q, want %q", got, dir)
	}
	return dir
}

func setSocketOwnerUID(t *testing.T, uid int) {
	t.Helper()
	old := socketOwnerUID
	socketOwnerUID = func() int { return uid }
	t.Cleanup(func() { socketOwnerUID = old })
}

// P23 accept side.
func TestCheckTrustedSocket_Accepts(t *testing.T) {
	t.Run("cenci socket directly under tmux-uid", func(t *testing.T) {
		dir := trustedFixture(t, 0o700)
		if err := CheckTrustedSocket(dir, filepath.Join(dir, "cenci")); err != nil {
			t.Errorf("CheckTrustedSocket = %v, want nil", err)
		}
	})

	t.Run("another socket name directly under tmux-uid", func(t *testing.T) {
		dir := trustedFixture(t, 0o700)
		if err := CheckTrustedSocket(dir, filepath.Join(dir, "default")); err != nil {
			t.Errorf("CheckTrustedSocket = %v, want nil", err)
		}
	})

	t.Run("TMUX_TMPDIR is a symlink", func(t *testing.T) {
		realTmp := resolvedTempDir(t)
		link := filepath.Join(resolvedTempDir(t), "tmpdir-link")
		if err := os.Symlink(realTmp, link); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMUX_TMPDIR", link)
		wantDir := filepath.Join(realTmp, uidDirName())
		if err := os.Mkdir(wantDir, 0o700); err != nil {
			t.Fatal(err)
		}

		dir, err := TrustedSocketDir()
		if err != nil {
			t.Fatalf("TrustedSocketDir: %v", err)
		}
		if dir != wantDir {
			t.Fatalf("TrustedSocketDir = %q, want the symlink resolved: %q", dir, wantDir)
		}
		if err := CheckTrustedSocket(dir, filepath.Join(dir, "cenci")); err != nil {
			t.Errorf("CheckTrustedSocket = %v, want nil", err)
		}
	})
}

// P23 reject side: a socket path that is not exactly <dir>/<name>.
func TestCheckTrustedSocket_RejectsPathsOutsideTheTrustedDirectory(t *testing.T) {
	tests := []struct {
		name   string
		socket func(t *testing.T, dir string) string
	}{
		{"workspace path", func(t *testing.T, _ string) string { return filepath.Join(resolvedTempDir(t), "workspace", "cenci") }},
		{"path containing ..", func(_ *testing.T, dir string) string { return dir + "/../" + uidDirName() + "/cenci" }},
		{"dot-dot after a subdirectory", func(_ *testing.T, dir string) string { return dir + "/sub/../cenci" }},
		{"subdirectory of tmux-uid", func(_ *testing.T, dir string) string { return filepath.Join(dir, "sub", "cenci") }},
		{"the directory itself", func(_ *testing.T, dir string) string { return dir }},
		{"relative path", func(_ *testing.T, _ string) string { return "cenci" }},
		{"empty path", func(_ *testing.T, _ string) string { return "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := trustedFixture(t, 0o700)
			socket := tt.socket(t, dir)

			err := CheckTrustedSocket(dir, socket)

			if !errors.Is(err, ErrUntrustedSocket) {
				t.Errorf("CheckTrustedSocket(%q) = %v, want it to wrap ErrUntrustedSocket", socket, err)
			}
		})
	}
}

// P23 reject side: every socket is rejected when tmux-<uid> itself is not a
// real, private directory of the current uid.
func TestCheckTrustedSocket_RejectsEverySocketWhenTheDirectoryIsNotTrusted(t *testing.T) {
	t.Run("perm has group or other bits", func(t *testing.T) {
		for _, perm := range []os.FileMode{0o750, 0o705, 0o770, 0o777} {
			t.Run(fmt.Sprintf("%04o", perm), func(t *testing.T) {
				dir := trustedFixture(t, perm)

				err := CheckTrustedSocket(dir, filepath.Join(dir, "cenci"))

				if !errors.Is(err, ErrUntrustedSocket) {
					t.Errorf("CheckTrustedSocket = %v, want it to wrap ErrUntrustedSocket", err)
				}
			})
		}
	})

	t.Run("tmux-uid is a symlink to a private directory", func(t *testing.T) {
		tmp := resolvedTempDir(t)
		t.Setenv("TMUX_TMPDIR", tmp)
		target := filepath.Join(tmp, "elsewhere")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(tmp, uidDirName())
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}

		err := CheckTrustedSocket(dir, filepath.Join(dir, "cenci"))

		if !errors.Is(err, ErrUntrustedSocket) {
			t.Errorf("CheckTrustedSocket = %v, want it to wrap ErrUntrustedSocket", err)
		}
	})

	t.Run("tmux-uid is a regular file", func(t *testing.T) {
		tmp := resolvedTempDir(t)
		t.Setenv("TMUX_TMPDIR", tmp)
		dir := filepath.Join(tmp, uidDirName())
		if err := os.WriteFile(dir, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		err := CheckTrustedSocket(dir, filepath.Join(dir, "cenci"))

		if !errors.Is(err, ErrUntrustedSocket) {
			t.Errorf("CheckTrustedSocket = %v, want it to wrap ErrUntrustedSocket", err)
		}
	})

	t.Run("tmux-uid does not exist", func(t *testing.T) {
		tmp := resolvedTempDir(t)
		t.Setenv("TMUX_TMPDIR", tmp)
		dir := filepath.Join(tmp, uidDirName())

		err := CheckTrustedSocket(dir, filepath.Join(dir, "cenci"))

		if !errors.Is(err, ErrUntrustedSocket) {
			t.Errorf("CheckTrustedSocket = %v, want it to wrap ErrUntrustedSocket", err)
		}
	})

	t.Run("owned by another uid", func(t *testing.T) {
		tmp := resolvedTempDir(t)
		t.Setenv("TMUX_TMPDIR", tmp)
		setSocketOwnerUID(t, os.Getuid()+1)
		dir, err := TrustedSocketDir()
		if err != nil {
			t.Fatalf("TrustedSocketDir: %v", err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		err = CheckTrustedSocket(dir, filepath.Join(dir, "cenci"))

		if !errors.Is(err, ErrUntrustedSocket) {
			t.Errorf("CheckTrustedSocket = %v, want it to wrap ErrUntrustedSocket", err)
		}
	})
}

// Resolver: the first comma-separated field of $TMUX when it is set (and
// non-empty), else the cenci socket under the trusted directory.
func TestResolveSocket_UsesFirstFieldOfTMUXWhenSet(t *testing.T) {
	tests := []struct{ name, tmux, want string }{
		{"socket, server pid and session id", "/run/user/1000/tmux-1000/default,4242,3", "/run/user/1000/tmux-1000/default"},
		{"socket alone", "/run/x/sock", "/run/x/sock"},
		{"returned as-is, not shape-checked", "relative,1,0", "relative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX", tt.tmux)
			t.Setenv("TMUX_TMPDIR", resolvedTempDir(t))

			got, err := ResolveSocket()

			if err != nil {
				t.Fatalf("ResolveSocket: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveSocket = %q, want %q", got, tt.want)
			}
		})
	}
}

// A set, non-empty $TMUX whose first field is empty must never resolve to ""
// (which every socket-carrying call reads as "bare tmux, ambient env"): it is a
// malformed socket and errors.
func TestResolveSocket_EmptyFirstFieldOfTMUXIsMalformed(t *testing.T) {
	for _, value := range []string{",", ",1,2"} {
		t.Run(strconv.Quote(value), func(t *testing.T) {
			t.Setenv("TMUX", value)
			t.Setenv("TMUX_TMPDIR", resolvedTempDir(t))

			got, err := ResolveSocket()

			if !errors.Is(err, ErrMalformedSocket) {
				t.Errorf("ResolveSocket err = %v, want it to wrap ErrMalformedSocket", err)
			}
			if got != "" {
				t.Errorf("ResolveSocket = %q, want an empty result alongside the error", got)
			}
		})
	}
}

func TestResolveSocket_FallsBackToTheCenciSocketWhenTMUXIsEmptyOrUnset(t *testing.T) {
	for _, tmuxSet := range []bool{false, true} {
		name := "TMUX unset"
		if tmuxSet {
			name = "TMUX empty"
		}
		t.Run(name, func(t *testing.T) {
			realTmp := resolvedTempDir(t)
			link := filepath.Join(resolvedTempDir(t), "tmpdir-link")
			if err := os.Symlink(realTmp, link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMUX", "")
			if !tmuxSet {
				if err := os.Unsetenv("TMUX"); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMUX_TMPDIR", link)

			got, err := ResolveSocket()

			if err != nil {
				t.Fatalf("ResolveSocket: %v", err)
			}
			want := filepath.Join(realTmp, uidDirName(), "cenci")
			if got != want {
				t.Errorf("ResolveSocket = %q, want %q (TMUX_TMPDIR symlink resolved)", got, want)
			}
		})
	}
}

// ${TMUX_TMPDIR:-/tmp}: an unset or empty TMUX_TMPDIR means /tmp.
func TestResolveSocket_DefaultsTMUXTMPDIRToSlashTmp(t *testing.T) {
	slashTmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Skipf("/tmp unresolvable: %v", err)
	}
	want := filepath.Join(slashTmp, uidDirName(), "cenci")
	for _, tmpdirSet := range []bool{false, true} {
		name := "TMUX_TMPDIR unset"
		if tmpdirSet {
			name = "TMUX_TMPDIR empty"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("TMUX", "")
			if err := os.Unsetenv("TMUX"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMUX_TMPDIR", "")
			if !tmpdirSet {
				if err := os.Unsetenv("TMUX_TMPDIR"); err != nil {
					t.Fatal(err)
				}
			}

			got, err := ResolveSocket()

			if err != nil {
				t.Fatalf("ResolveSocket: %v", err)
			}
			if got != want {
				t.Errorf("ResolveSocket = %q, want %q", got, want)
			}
		})
	}
}
