package launcher

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// writeReapFakes writes fake docker + tmux for the two smoke cases below (the
// full 16-case contract lives in the retargeted bash suite,
// tests/reap-orphans.test.sh). The fake docker answers `ps` from FAKE_PS,
// serves the environ scan from FAKE_SCAN, reports gone on the liveness probe,
// and logs everything; the fake tmux prints FAKE_LIVE_PANES.
func writeReapFakes(t *testing.T, dir, callLog string) {
	t.Helper()
	docker := `#!/bin/sh
printf '%s\n' "$*" >> ` + shellQuote(callLog) + `
case "$1" in
ps) printf '%s' "${FAKE_PS:-}" ;;
exec)
  case "$*" in
  *TMUX_PANE=*) printf '%s' "${FAKE_SCAN:-}" ;;
  *__GONE__*) printf '__GONE__\n' ;;
  esac
  ;;
esac
exit 0
`
	writeExecutable(t, filepath.Join(dir, "docker"), docker)
	tmux := `#!/bin/sh
printf '%s\n' "${FAKE_LIVE_PANES:-}"
exit 0
`
	writeExecutable(t, filepath.Join(dir, "tmux"), tmux)
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
