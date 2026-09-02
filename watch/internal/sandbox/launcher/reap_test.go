package launcher

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// readFakeCfg is the fake-tmux-side counterpart of writeFakeTmuxValue: reads
// name from $HOME (falling back to def if the file is absent), the same
// pattern every fake-tmux shell snippet below uses in place of an inherited
// FAKE_* env var. Uses the `read` shell builtin rather than `cat`: production
// now runs the tmux child with a minimal, single-directory PATH (the test's
// fake-binary dir, via buildMinimalTmuxEnv), which has no external `cat` on
// it — only a builtin can be relied on to read the file back.
const readFakeCfg = `read_fake_cfg() { if [ -f "$HOME/$1" ]; then IFS= read -r rfc_val < "$HOME/$1"; printf '%s' "$rfc_val"; else printf '%s' "$2"; fi; }`

// writeFakeTmuxValue writes a fake-tmux configuration value (e.g.
// FAKE_LIVE_PANES) to a file under dir, read back via readFakeCfg (#1007
// review fix: production now runs the tmux child under a minimal, explicit
// allowlisted env — buildMinimalTmuxEnv, in reap.go — that never forwards
// arbitrary ambient vars, so a FAKE_* test knob set only via t.Setenv can no
// longer reach the fake tmux process; the file travels via $HOME instead,
// which IS in that allowlist).
func writeFakeTmuxValue(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
		t.Fatalf("write fake tmux config %s: %v", name, err)
	}
}

// writeReapFakes writes fake docker + tmux for the two smoke cases below (the
// full 16-case contract lives in the retargeted bash suite,
// tests/reap-orphans.test.sh). The fake docker answers `ps` from FAKE_PS,
// serves the environ scan from FAKE_SCAN, reports gone on the liveness probe,
// and logs everything; the fake tmux prints FAKE_LIVE_PANES (via
// writeFakeTmuxValue/readFakeCfg, not an inherited env var — see
// writeFakeTmuxValue's doc comment). Sets HOME to dir so the fake tmux
// process (run under production's minimal allowlisted env) can find those
// config files.
func writeReapFakes(t *testing.T, dir, callLog string) {
	t.Helper()
	t.Setenv("HOME", dir)
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
` + readFakeCfg + `
printf '%s\n' "$(read_fake_cfg FAKE_LIVE_PANES '')"
exit 0
`
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), tmux)
}

// writeSocketAwareReapFakes writes fake docker + tmux for the (socket, pane)
// pair-matching cases (#1007): the fake docker is identical to
// writeReapFakes' (it doesn't need to know about sockets — the socket field
// travels through the scan output, which the fake docker serves verbatim
// from FAKE_SCAN, same as before). The fake tmux parses an explicit `-S
// <path>` out of its argv (production is expected to always pass one,
// scrubbing its own ambient TMUX/TMUX_PANE — see watch/docs/tmux.md), logs
// its full invocation to callLog exactly like the fake docker (bare args, no
// program-name prefix), and answers per socket via two independently
// configurable slots: FAKE_SOCKET_1/FAKE_TMUX_MODE_1/FAKE_LIVE_PANES_1 and
// FAKE_SOCKET_2/FAKE_TMUX_MODE_2/FAKE_LIVE_PANES_2 (mirroring a "cenci"
// socket and a personal default socket), each read via writeFakeTmuxValue/
// readFakeCfg rather than an inherited env var (see writeFakeTmuxValue's doc
// comment). Any `-S` target that matches neither configured slot (including a
// missing `-S`, which production should never emit) answers "no server
// running" — an unscoped/unexpected query must never accidentally observe
// live panes. Sets HOME to dir so the fake tmux process (run under
// production's minimal allowlisted env) can find those config files.
func writeSocketAwareReapFakes(t *testing.T, dir, callLog string) {
	t.Helper()
	t.Setenv("HOME", dir)
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
` + readFakeCfg + `
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
socket=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-S" ]; then socket="$arg"; fi
  prev="$arg"
done
fake_socket_1="$(read_fake_cfg FAKE_SOCKET_1 '')"
fake_socket_2="$(read_fake_cfg FAKE_SOCKET_2 '')"
if [ -n "$socket" ] && [ "$socket" = "$fake_socket_1" ]; then
  mode="$(read_fake_cfg FAKE_TMUX_MODE_1 ok)"
  panes="$(read_fake_cfg FAKE_LIVE_PANES_1 '')"
elif [ -n "$socket" ] && [ "$socket" = "$fake_socket_2" ]; then
  mode="$(read_fake_cfg FAKE_TMUX_MODE_2 ok)"
  panes="$(read_fake_cfg FAKE_LIVE_PANES_2 '')"
else
  mode="noserver"
  panes=""
fi
case "$mode" in
  noserver)
    echo "no server running on $socket" >&2
    exit 1
    ;;
  *)
    printf '%s\n' "$panes"
    exit 0
    ;;
esac
`
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), tmux)
}

// legacyNoSocketNoteText builds the exact aggregated "Note:" diagnostic
// ReapOrphans is expected to print (#1007, mirroring the existing
// unreadableCount idiom) when a container's scan reports one or more
// processes whose row carries no CENCI_TMUX_SOCKET (or a malformed one) —
// the mandatory fail-open path, since a false kill here is irreversible loss
// of in-flight agent work.
func legacyNoSocketNoteText(count int, container string) string {
	return fmt.Sprintf("Note: %d process(es) in container %s carried no CENCI_TMUX_SOCKET; failing open and treating as live so no in-flight agent work is ever killed.", count, container)
}

// TestReapOrphans_SocketPaneMatchingKeepsLiveExcludesDead is AC #3 case (a):
// pair matching. Two processes share the same cenci-socket scan row: one
// whose pane is live on that socket must never be signaled, the other whose
// pane is dead on that same socket must be reaped.
func TestReapOrphans_SocketPaneMatchingKeepsLiveExcludesDead(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeSocketAwareReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-pairmatch\n")
	cenciSocket := "/tmp/tmux-1000/cenci"
	t.Setenv("FAKE_SCAN", fmt.Sprintf("3001\t%%3\t1000\t%s\n3002\t%%9\t2000\t%s\n", cenciSocket, cenciSocket))
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_1", cenciSocket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_1", "%3")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if strings.Contains(stdout.String(), "\t3001\t") {
		t.Errorf("pane live on its socket was signaled; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "reaped\tclaude-cenci-pairmatch\t3002\t%9") {
		t.Errorf("pane dead on its socket was not reaped; stdout:\n%s", stdout.String())
	}
	calls := readCallLog(t, callLog)
	if containsLine(calls, "exec -u root claude-cenci-pairmatch kill -TERM 3001") {
		t.Errorf("live pane was signaled; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !containsLine(calls, "exec -u root claude-cenci-pairmatch kill -TERM 3002") {
		t.Errorf("missing kill -TERM for the dead-pane orphan; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestReapOrphans_CrossServerNeverKillsLiveCenciPaneWithPersonalServerRunning
// is AC #3 case (b): cross-server no-kill. An agent pane live on the cenci
// socket must never be signaled while a personal default server is also
// running, and the reaper must have actually queried the cenci socket (via
// an explicit -S) rather than silently skipping liveness resolution.
func TestReapOrphans_CrossServerNeverKillsLiveCenciPaneWithPersonalServerRunning(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeSocketAwareReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-crossserver\n")
	cenciSocket := "/tmp/tmux-1000/cenci"
	personalSocket := "/tmp/tmux-1000/default"
	t.Setenv("FAKE_SCAN", fmt.Sprintf("4001\t%%3\t1000\t%s\n", cenciSocket))
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_1", cenciSocket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_1", "%3")
	// A personal default server is also running, with its own (unrelated)
	// live panes; this container's row never references it.
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_2", personalSocket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_2", "%99")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if strings.Contains(stdout.String(), "\t4001\t") {
		t.Errorf("live cenci-socket pane was signaled despite a personal server also running; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "No orphaned processes found.") {
		t.Errorf("missing no-op summary; stdout:\n%s", stdout.String())
	}
	calls := readCallLog(t, callLog)
	if !containsLine(calls, "-S "+cenciSocket+" list-panes -a -F #{pane_id}") {
		t.Errorf("expected the cenci socket to be queried via an explicit -S; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// TestReapOrphans_LegacyNoSocketRowFailsOpen is AC #3 case (c): a legacy
// 3-field scan row (no CENCI_TMUX_SOCKET, pre-#1007 launcher) must never be
// signaled — fail-open is mandatory since a false kill is irreversible loss
// of in-flight agent work — and the aggregated fail-open note must fire.
func TestReapOrphans_LegacyNoSocketRowFailsOpen(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeSocketAwareReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-legacy\n")
	// Legacy 3-field row: no socket field at all.
	t.Setenv("FAKE_SCAN", "5001\t%1\t1000\n")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if strings.Contains(stdout.String(), "\t5001\t") {
		t.Errorf("legacy no-socket row was signaled; must fail open; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "No orphaned processes found.") {
		t.Errorf("missing no-op summary; stdout:\n%s", stdout.String())
	}
	want := legacyNoSocketNoteText(1, "claude-cenci-legacy")
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("missing aggregated legacy fail-open note; want substring:\n%s\ngot stdout:\n%s", want, stdout.String())
	}
}

// TestReapOrphans_DeadOnOwnSocketButLiveIdOnOtherSocketStillReaped is AC #3's
// union-regression counterpart: both servers allocate pane ids from %0, so a
// union of live panes across sockets would make a pane genuinely dead on its
// own socket look live merely because an unrelated socket happens to reuse
// the same id. Pair matching must keep the genuinely-live pane alive and
// still reap the genuinely-dead one.
func TestReapOrphans_DeadOnOwnSocketButLiveIdOnOtherSocketStillReaped(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeSocketAwareReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-unionregress\n")
	cenciSocket := "/tmp/tmux-1000/cenci"
	personalSocket := "/tmp/tmux-1000/default"
	// 6001's pane %9 is genuinely live on its own (cenci) socket.
	// 6002's pane %8 is dead on its own (cenci) socket, but %8 happens to be
	// a LIVE pane id on the unrelated personal default socket.
	t.Setenv("FAKE_SCAN", fmt.Sprintf("6001\t%%9\t1000\t%s\n6002\t%%8\t2000\t%s\n", cenciSocket, cenciSocket))
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_1", cenciSocket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_1", "%9")
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_2", personalSocket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_2", "%8")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if strings.Contains(stdout.String(), "\t6001\t") {
		t.Errorf("pane live on its own socket was signaled; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "reaped\tclaude-cenci-unionregress\t6002\t%8") {
		t.Errorf("pane dead on its own socket must be reaped even though its pane id is live on a different (unrelated) socket -- a union match would wrongly skip it; stdout:\n%s", stdout.String())
	}
}

func TestReapOrphans_DeadPaneOrphanIsTermed(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-orphan\n")
	// A socket field is required for the row to reach (socket, pane) pair
	// resolution at all (#1007) rather than the no-socket fail-open path;
	// writeReapFakes' mock tmux ignores -S entirely, so any absolute path
	// works here -- this test is about TERM/KILL mechanics, not sockets.
	t.Setenv("FAKE_SCAN", "5001\t%1\t1000\t/fake/tmux-socket\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%99")

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

// The in-container /proc/*/environ scan must run as -u dev (same-uid reads
// need no CAP_SYS_PTRACE, so dev-owned agent processes become visible),
// while SIGTERM keeps running as -u root (see reap.go's scan comment and the
// sandbox CLAUDE.md's "docker run --user X persists" entrypoint pattern for
// why every exec call site needs its own explicit -u flag). writeReapFakes'
// mock docker doesn't guard on the -u value, so this only pins the exec
// call log shape.
func TestReapOrphans_ScanRunsAsDev(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-devscan\n")
	// See TestReapOrphans_DeadPaneOrphanIsTermed's comment: a socket field is
	// required to reach pair resolution (#1007) rather than fail-open.
	t.Setenv("FAKE_SCAN", "22001\t%26\t22000\t/fake/tmux-socket\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%99")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "reaped\tclaude-cenci-devscan\t22001\t%26") {
		t.Errorf("missing reaped line; stdout:\n%s", stdout.String())
	}
	calls := readCallLog(t, callLog)
	if !containsPrefix(calls, "exec -u dev claude-cenci-devscan sh -c") {
		t.Errorf("scan did not run as -u dev; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !containsLine(calls, "exec -u root claude-cenci-devscan kill -TERM 22001") {
		t.Errorf("SIGTERM did not stay -u root; calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestReapOrphans_LivePaneNeverSignaled(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-live\n")
	// See TestReapOrphans_DeadPaneOrphanIsTermed's comment: a socket field is
	// required to reach pair resolution (#1007) rather than fail-open, so
	// this test genuinely exercises the live-pane-match skip rather than the
	// no-socket fail-open skip.
	t.Setenv("FAKE_SCAN", "6001\t%7\t2000\t/fake/tmux-socket\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%7")

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
	// See TestReapOrphans_DeadPaneOrphanIsTermed's comment: 5001's row needs
	// a socket field to reach pair resolution (#1007) rather than fail-open;
	// pid 1's row is skipped before socket resolution regardless.
	t.Setenv("FAKE_SCAN", "1\t%1\t50\n5001\t%2\t1000\t/fake/tmux-socket\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%99")

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

// unreadableNoteText builds the exact "Note:" diagnostic ReapOrphans is
// expected to print (#361) when the -u dev scan's __UNREADABLE__\t<pid>
// marker count (excluding PID 1) is > 0 for a container.
func unreadableNoteText(count int, container string) string {
	return fmt.Sprintf("Note: %d process environ(s) in container %s were unreadable during the -u dev scan; if this persists it may mean the scan user's UID no longer matches the agent process UID, so orphans could go undetected.", count, container)
}

// A non-PID-1 __UNREADABLE__\t<pid> marker line from the scan (#361) must
// surface as an always-on diagnostic Note, since an environ read failure on
// any process other than the container's root-owned init is a possible sign
// that the scan user's UID has drifted from the agent process UID.
func TestReapOrphans_UnreadableEnvironNonPid1ProducesNote(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-unreadable\n")
	t.Setenv("FAKE_SCAN", "__UNREADABLE__\t5002\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%99")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	want := unreadableNoteText(1, "claude-cenci-unreadable")
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("missing unreadable-environ note; want substring:\n%s\ngot stdout:\n%s", want, stdout.String())
	}
}

// PID 1 is root-owned by construction (sudo init, see the container user
// model) and therefore always unreadable by the -u dev scan on a healthy
// container. Excluding it from the count is what keeps a healthy/idle
// container silent, so a __UNREADABLE__\t1 marker alone must NOT produce the
// Note.
func TestReapOrphans_UnreadableEnvironPid1ProducesNoNote(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-pid1unreadable\n")
	t.Setenv("FAKE_SCAN", "__UNREADABLE__\t1\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%99")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if strings.Contains(stdout.String(), "unreadable during the -u dev scan") {
		t.Errorf("PID-1-only unreadable marker must not produce a Note; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "No orphaned processes found.") {
		t.Errorf("missing no-op summary; stdout:\n%s", stdout.String())
	}
}

// A non-PID-1 __UNREADABLE__ marker and a genuine dead-pane orphan can appear
// in the same scan output; the marker must not interfere with reaping the
// real orphan, and the Note must still fire alongside the reap.
func TestReapOrphans_UnreadableEnvironCoexistsWithReapedOrphan(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", "claude-cenci-mixed\n")
	// See TestReapOrphans_DeadPaneOrphanIsTermed's comment: 5001's row needs
	// a socket field to reach pair resolution (#1007) rather than fail-open.
	t.Setenv("FAKE_SCAN", "__UNREADABLE__\t5003\n5001\t%1\t1000\t/fake/tmux-socket\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%99")

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "reaped\tclaude-cenci-mixed\t5001\t%1") {
		t.Errorf("missing reaped line for orphan alongside unreadable marker; stdout:\n%s", stdout.String())
	}
	want := unreadableNoteText(1, "claude-cenci-mixed")
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("missing unreadable-environ note alongside a real reap; want substring:\n%s\ngot stdout:\n%s", want, stdout.String())
	}
	if calls := readCallLog(t, callLog); !containsLine(calls, "exec -u root claude-cenci-mixed kill -TERM 5001") {
		t.Errorf("missing kill -TERM call for coexisting orphan; calls:\n%s", strings.Join(calls, "\n"))
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
	// See TestReapOrphans_DeadPaneOrphanIsTermed's comment: a socket field is
	// required to reach pair resolution (#1007) rather than fail-open.
	t.Setenv("FAKE_SCAN", "7001\t%3\t900\t/fake/tmux-socket\n")
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES", "%99")
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
