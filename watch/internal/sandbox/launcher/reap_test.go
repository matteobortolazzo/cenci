package launcher

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/internal/exectest"
)

// writeReapFakes writes fake docker + tmux for the two smoke cases below (the
// full 16-case contract lives in the retargeted bash suite,
// tests/reap-orphans.test.sh). The fake docker answers `ps` from FAKE_PS,
// serves the environ scan from FAKE_SCAN, reports gone on the liveness probe,
// and logs everything; the fake tmux prints FAKE_LIVE_PANES.
func writeReapFakes(t *testing.T, dir, callLog string) {
	t.Helper()
	docker := `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
case "$1" in
ps) printf '%s' "${FAKE_PS:-}" ;;
exec)
  case "$*" in
  *TMUX_PANE=*) printf '%s' "${FAKE_SCAN:-}" ;;
  *__GONE__*) printf '__GONE__\n' ;;
  *"kill -TERM"*) [ -n "${FAKE_TERM_FAIL:-}" ] && exit 1 ;;
  esac
  ;;
esac
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, "docker"), docker)
	tmux := `#!/bin/sh
printf '%s\n' "${FAKE_LIVE_PANES:-}"
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), tmux)
}

func TestReapOrphans_DeadPaneOrphanIsTermed(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-orphan\n")
	t.Setenv("FAKE_SCAN", "5001\t%1\t1000\n")
	t.Setenv("FAKE_LIVE_PANES", "%99")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "reaped\tclaude-cenci-orphan\t5001\t%1") {
		t.Errorf("missing reaped line; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Reaped 1 orphaned process(es).") {
		t.Errorf("missing summary; stdout:\n%s", stdout.String())
	}
	if calls := readCallLog(t, callLog); !containsLine(calls, "exec -u root claude-cenci-orphan kill -TERM 5001") {
		t.Errorf("missing kill -TERM call; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestReapOrphans_LivePaneNeverSignaled(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-live\n")
	t.Setenv("FAKE_SCAN", "6001\t%7\t2000\n")
	t.Setenv("FAKE_LIVE_PANES", "%7")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "No orphaned processes found.") {
		t.Errorf("missing no-op summary; stdout:\n%s", stdout.String())
	}
	if calls := readCallLog(t, callLog); containsPrefix(calls, "exec -u root claude-cenci-live kill") {
		t.Errorf("live-pane process signaled; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// PID 1 must never be signaled, even when its TMUX_PANE names a dead pane:
// containers created by pre-#356 launchers baked the creating pane's id into
// the container-lifetime env, so PID 1 (docker-init) matches the orphan
// predicate once that pane closes. Killing it destroys the whole shared
// container and every agent session exec'd inside it. Other orphans in the
// same container must still be reaped.
func TestReapOrphans_ContainerInitNeverSignaled(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-shared\n")
	t.Setenv("FAKE_SCAN", "1\t%1\t50\n5001\t%2\t1000\n")
	t.Setenv("FAKE_LIVE_PANES", "%99")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Note: process 1 in container claude-cenci-shared is the container init; skipping.") {
		t.Errorf("missing init-skip note; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "reaped\tclaude-cenci-shared\t5001\t%2") {
		t.Errorf("other orphan not reaped; stdout:\n%s", stdout.String())
	}
	calls := readCallLog(t, callLog)
	if containsLine(calls, "exec -u root claude-cenci-shared kill -TERM 1") ||
		containsLine(calls, "exec -u root claude-cenci-shared kill -KILL 1") {
		t.Errorf("PID 1 was signaled; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !containsLine(calls, "exec -u root claude-cenci-shared kill -TERM 5001") {
		t.Errorf("missing kill -TERM for real orphan; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// A pid that exits between the scan and the SIGTERM is a benign race, not a
// reap failure: the probe reports __GONE__ and the run continues. Guaranteed
// to happen on containers created by pre-#356 launchers, where the scan's own
// in-container sh inherits the stale creation-baked TMUX_PANE and reports
// itself — always exited by kill time.
func TestReapOrphans_GoneBeforeTermIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-race\n")
	t.Setenv("FAKE_SCAN", "7001\t%3\t900\n")
	t.Setenv("FAKE_LIVE_PANES", "%99")
	t.Setenv("FAKE_TERM_FAIL", "1")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Note: process 7001 in container claude-cenci-race exited before SIGTERM could be delivered; skipping.") {
		t.Errorf("missing gone-before-TERM note; stdout:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "reaped\t") {
		t.Errorf("nothing should be reported reaped; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "No orphaned processes found.") {
		t.Errorf("missing no-op summary; stdout:\n%s", stdout.String())
	}
	if calls := readCallLog(t, callLog); containsPrefix(calls, "exec -u root claude-cenci-race kill -KILL") {
		t.Errorf("SIGKILL must not follow a gone-before-TERM skip; calls:\n%s", strings.Join(calls, "\n"))
	}
}
