package main_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

func TestOpenCodexCleansClosedPaneBeforeResume(t *testing.T) {
	for _, scanFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("scan failure=%t", scanFails), func(t *testing.T) {
			dir := t.TempDir()
			callLog := writeScriptedRuntimes(t, dir)
			env, _, socketDir := openTestEnv(t, dir, writeAssetFixture(t))
			released := filepath.Join(dir, "released")
			for _, name := range []string{"docker", "podman"} {
				original := filepath.Join(dir, name+"-original")
				if err := os.Rename(filepath.Join(dir, name), original); err != nil {
					t.Fatal(err)
				}
				exectest.WriteExecutable(t, filepath.Join(dir, name), fmt.Sprintf(`#!/bin/sh
case "$*" in
  *'exec -u dev codex-cenci-default sh -c'*'TMUX_PANE='*)
    if [ "%t" = true ]; then echo scan-unavailable >&2; exit 23; fi
    printf '4242\t%%%%9\t123\t/tmp/cenci-resume-test\taaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
    printf '4243\t%%%%8\t456\t/tmp/cenci-resume-test\tbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n'
    exit 0;;
  'exec -u root codex-cenci-default kill -TERM 4242') exit 0;;
  'exec -u root codex-cenci-default kill -KILL 4242') : > %s; exit 0;;
  'exec -u root codex-cenci-default sh -c'*'__GONE__'*) printf '123\n'; exit 0;;
  'exec -u root'*'kill '*) echo unexpected-signal >&2; exit 91;;
  'exec -it '*)
    if [ "%t" = false ] && [ ! -f %s ]; then echo conversation-still-locked >&2; exit 24; fi;;
esac
exec %s "$@"
`, scanFails, exectest.ShellQuote(released), scanFails, exectest.ShellQuote(released), exectest.ShellQuote(original)))
			}
			exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), `#!/bin/sh
case "$*" in *list-panes*) printf '%%8\tbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n';; esac
`)
			cmd := exec.Command(binaryPath, "open", "xa", "--", "resume", "--last")
			cmd.Dir = t.TempDir()
			cmd.Env = append(env,
				"CENCI_SANDBOX_REAP_GRACE_SECS=0",
				"FAKE_PS=codex-cenci-default\n",
				"FAKE_AGENT_MOUNTS=cenci-agent-cli-codex|/opt/cenci-agent|false\n",
				"FAKE_INSPECT_LABEL=detached",
				"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("open xa resume: %v\n%s", err, out)
			}
			if strings.Contains(string(out), "unexpected-signal") {
				t.Fatalf("cleanup signaled a live launch: %s", out)
			}
			if scanFails && (!strings.Contains(string(out), "Warning: could not clean up closed Codex sessions") || !strings.Contains(string(out), "scan-unavailable")) {
				t.Fatalf("cleanup failure must warn without blocking launch: %s", out)
			}
			if line := attachLine(t, callLogLines(t, callLog)); !strings.HasSuffix(line, "resume --last") {
				t.Fatalf("resume arguments lost: %s", line)
			}
		})
	}
}

// The attach-time sweep (#1127) is scoped per tmux socket: when one socket of the
// reopened container fails, the dead launch on the healthy socket is still reaped
// before Codex resumes, the row on the failed socket (even one carrying the same
// launch ID) is never signaled, the existing "Warning: could not clean up closed
// Codex sessions" line names the failed socket and its cause, and the launch
// carries on to the resume.
func TestOpenCodexSocketFailureWarnsReapsHealthyRowAndStillResumes(t *testing.T) {
	const (
		failingSocket = "/tmp/cenci-failing-test"
		healthySocket = "/tmp/cenci-resume-test"
		tmuxCause     = "protocol version mismatch"
		warning       = "Warning: could not clean up closed Codex sessions: "
	)
	dir := t.TempDir()
	callLog := writeScriptedRuntimes(t, dir)
	env, _, socketDir := openTestEnv(t, dir, writeAssetFixture(t))
	released := filepath.Join(dir, "released")
	for _, name := range []string{"docker", "podman"} {
		original := filepath.Join(dir, name+"-original")
		if err := os.Rename(filepath.Join(dir, name), original); err != nil {
			t.Fatal(err)
		}
		// 4242 is a dead launch on the healthy socket (only launch bbbb is live
		// there). 4243 sits on the failing socket and carries the same dead launch
		// ID: its liveness is unknown, so any signal to it is the failure.
		exectest.WriteExecutable(t, filepath.Join(dir, name), fmt.Sprintf(`#!/bin/sh
case "$*" in
  *'exec -u dev codex-cenci-default sh -c'*'TMUX_PANE='*)
    printf '4242\t%%%%9\t123\t%s\taaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
    printf '4243\t%%%%9\t456\t%s\taaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
    exit 0;;
  'exec -u root codex-cenci-default kill -TERM 4242') exit 0;;
  'exec -u root codex-cenci-default kill -KILL 4242') : > %s; exit 0;;
  'exec -u root codex-cenci-default sh -c'*'__GONE__'*) printf '123\n'; exit 0;;
  'exec -u root'*'kill '*) echo unexpected-signal >&2; exit 91;;
  'exec -it '*)
    if [ ! -f %s ]; then echo conversation-still-locked >&2; exit 24; fi;;
esac
exec %s "$@"
`, healthySocket, failingSocket, exectest.ShellQuote(released), exectest.ShellQuote(released), exectest.ShellQuote(original)))
	}
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), `#!/bin/sh
case "$*" in
  *'-S `+failingSocket+` list-panes'*) echo '`+tmuxCause+` (client 8, server 7)' >&2; exit 1;;
  *list-panes*) printf '%%8\tbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n';;
esac
`)
	cmd := exec.Command(binaryPath, "open", "xa", "--", "resume", "--last")
	cmd.Dir = t.TempDir()
	cmd.Env = append(env,
		"CENCI_SANDBOX_REAP_GRACE_SECS=0",
		"FAKE_PS=codex-cenci-default\n",
		"FAKE_AGENT_MOUNTS=cenci-agent-cli-codex|/opt/cenci-agent|false\n",
		"FAKE_INSPECT_LABEL=detached",
		"FAKE_INSPECT_MOUNTS=/workspace::/workspace\n/home/dev::/home/dev\n"+socketDir+"::/run/user/1000/cenci\n",
	)

	out, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf("open xa resume must carry on after a failed socket: %v\n%s", err, out)
	}
	text := string(out)
	if strings.Contains(text, "unexpected-signal") {
		t.Fatalf("the sweep signaled a row it must not (the failed socket's row): %s", text)
	}
	if strings.Contains(text, "conversation-still-locked") {
		t.Fatalf("Codex resumed before the dead launch on the healthy socket was reaped: %s", text)
	}
	if _, statErr := os.Stat(released); statErr != nil {
		t.Errorf("the dead launch on the healthy socket was never killed (no kill -KILL 4242): %v\n%s", statErr, text)
	}
	var warningLine string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, warning) {
			warningLine = line
		}
	}
	if warningLine == "" {
		t.Fatalf("missing the %q line:\n%s", warning, text)
	}
	if !strings.Contains(warningLine, fmt.Sprintf("%q", failingSocket)) || !strings.Contains(warningLine, tmuxCause) {
		t.Errorf("warning %q does not name the failed socket %q and its cause %q", warningLine, failingSocket, tmuxCause)
	}
	if want := fmt.Sprintf("Error: failed to query tmux panes on %q: ", failingSocket); !strings.Contains(text, want) {
		t.Errorf("output missing the socket's own %q line:\n%s", want, text)
	}
	if line := attachLine(t, callLogLines(t, callLog)); !strings.HasSuffix(line, "resume --last") {
		t.Fatalf("resume arguments lost: %s", line)
	}
}
