package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// readFakeCfg is the fake-tmux-side counterpart of writeFakeTmuxValue: reads
// name from $HOME (falling back to def if the file is absent), the same
// pattern every fake-tmux shell snippet below uses in place of an inherited
// FAKE_* env var. Uses the `read` shell builtin rather than `cat`: production
// now runs the tmux child with a minimal, single-directory PATH (the test's
// fake-binary dir, via tmux.RunSocket's minimalEnv), which has no external `cat` on
// it — only a builtin can be relied on to read the file back.
const readFakeCfg = `read_fake_cfg() { if [ -f "$HOME/$1" ]; then IFS= read -r rfc_val < "$HOME/$1"; printf '%s' "$rfc_val"; else printf '%s' "$2"; fi; }`

// writeFakeTmuxValue writes a fake-tmux configuration value (e.g.
// FAKE_LIVE_PANES) to a file under dir, read back via readFakeCfg (#1007
// review fix: production now runs the tmux child under a minimal, explicit
// allowlisted env — minimalEnv, in internal/tmux/socket.go — that never forwards
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
//
// #1127 additions, all opt-in so the parity tests above keep their behavior:
//   - a slot's FAKE_TMUX_MODE_<n> may also be "error" (a genuine tmux failure)
//     or "stall" (sleeps far past tmux.RunSocket's bound; needs the real sleep
//     binary on PATH behind dir, see newReapSweep);
//   - a container whose FAKE_SCAN_<container> file exists gets that file as its
//     scan output instead of the shared FAKE_SCAN (the fake docker runs under
//     the full ambient env, so it finds the file through HOME);
//   - writeSocketAwareReapPodman adds a second runtime with its own ps list
//     and call log.
func writeSocketAwareReapFakes(t *testing.T, dir, callLog string) {
	t.Helper()
	t.Setenv("HOME", dir)
	exectest.WriteExecutable(t, filepath.Join(dir, "docker"), socketAwareReapRuntimeScript(callLog, "FAKE_PS"))
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
  error)
    echo "protocol version mismatch (client 8, server 7)" >&2
    exit 1
    ;;
  stall)
    # exec so a kill on deadline reaches the sleeper itself, not a shell parent
    # holding the pipes open (watch/docs/go-gotchas.md, #886).
    exec sleep 30
    ;;
  *)
    printf '%s\n' "$panes"
    exit 0
    ;;
esac
`
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), tmux)
}

// socketAwareReapRuntimeScript is the fake docker/podman body: it logs every
// invocation (bare args) to callLog, answers `ps` from the psVar env var,
// serves the in-container environ scan from $HOME/FAKE_SCAN_<container> when
// that file exists (read with the `read` builtin; the container is the 4th argv
// slot of `exec -u <user> <container> sh -c <script>`) and from the shared
// FAKE_SCAN otherwise, fails that container's scan (stderr "scan unavailable",
// exit 23) when $HOME/FAKE_SCAN_FAIL_<container> exists, reports gone on the
// liveness probe, and logs everything
// else. FAKE_TERM_FAIL makes a `kill -TERM` fail.
func socketAwareReapRuntimeScript(callLog, psVar string) string {
	return `#!/bin/sh
printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + `
case "$1" in
ps) printf '%s' "${` + psVar + `:-}" ;;
exec)
  case "$*" in
  *TMUX_PANE=*)
    scanfile="$HOME/FAKE_SCAN_$4"
    if [ -f "$HOME/FAKE_SCAN_FAIL_$4" ]; then
      echo 'scan unavailable' >&2
      exit 23
    elif [ -f "$scanfile" ]; then
      while IFS= read -r scan_line || [ -n "$scan_line" ]; do printf '%s\n' "$scan_line"; done < "$scanfile"
    else
      printf '%s' "${FAKE_SCAN:-}"
    fi
    ;;
  *__GONE__*) printf '__GONE__\n' ;;
  *"kill -TERM"*) [ -n "${FAKE_TERM_FAIL:-}" ] && exit 1 ;;
  esac
  ;;
esac
exit 0
`
}

// writeSocketAwareReapPodman writes a fake podman beside the fake docker: the
// same behavior, but `ps` answers from FAKE_PS_PODMAN and every invocation goes
// to its own call log, so a test can tell which runtime was scanned or signaled.
func writeSocketAwareReapPodman(t *testing.T, dir, podmanCallLog string) {
	t.Helper()
	exectest.WriteExecutable(t, filepath.Join(dir, "podman"), socketAwareReapRuntimeScript(podmanCallLog, "FAKE_PS_PODMAN"))
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
	if !containsLine(calls, "-S "+cenciSocket+" list-panes -a -F #{pane_id}\t#{@cenci-launch-id}") {
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
// while SIGTERM keeps running as -u root (see ScanContainer's comment in internal/sandbox/orphans.go and the
// sandbox/AGENTS.md's routed docs/entrypoint.md "docker run --user X persists" entrypoint pattern for
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

// TestReapOrphans_ReapedPairsMatchSharedOrphanCount is the #1218 AC14
// evidence: the dispatch capacity gate's count (sandbox.CountOrphanedSessions)
// and the reaper classify through one shared scan, so on a single fixture the
// number of distinct (socket, pane) pairs ReapOrphans reports as `reaped` must
// equal the count. The fixture mixes every classification: duplicate pane
// ids across pids and across sockets, a pane that is dead on its own socket
// but live on another, a live pane, a legacy no-socket row, a malformed pane,
// PID 1, and an unreadable marker.
func TestReapOrphans_ReapedPairsMatchSharedOrphanCount(t *testing.T) {
	const (
		cenciSocket    = "/tmp/tmux-1000/cenci"
		personalSocket = "/tmp/tmux-1000/default"
		container      = "claude-cenci-parity"
	)
	type row struct{ pid, pane, start, socket string }
	rows := []row{
		{"3001", "%3", "1000", cenciSocket},    // dead
		{"3002", "%3", "2000", cenciSocket},    // same pair as 3001 on another pid
		{"3003", "%3", "3000", personalSocket}, // same pane id, other socket: dead
		{"3004", "%9", "4000", cenciSocket},    // live on its own socket
		{"3005", "%7", "5000", cenciSocket},    // dead here, live id on the other socket
		{"3006", "%2", "6000", ""},             // no socket: fail open
		{"3007", "not-a-pane", "7000", cenciSocket},
		{"1", "%1", "50", cenciSocket}, // container init
	}
	var scan strings.Builder
	socketByPid := map[string]string{}
	for _, r := range rows {
		scan.WriteString(r.pid + "\t" + r.pane + "\t" + r.start + "\t" + r.socket + "\n")
		socketByPid[r.pid] = r.socket
	}
	scan.WriteString("__UNREADABLE__\t3008\n")

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeSocketAwareReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", container+"\n")
	t.Setenv("FAKE_SCAN", scan.String())
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_1", cenciSocket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_1", "%9")
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_2", personalSocket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_2", "%7")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	counted, err := sandbox.CountOrphanedSessions(ctx, []string{"docker"}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("CountOrphanedSessions: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	reapedPairs := map[string]bool{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || fields[0] != "reaped" {
			continue
		}
		pid, pane := fields[2], fields[3]
		reapedPairs[socketByPid[pid]+"\x00"+pane] = true
	}

	// (cenci,%3), (personal,%3) and (cenci,%7) -- not the four reaped pids.
	const wantPairs = 3
	if len(reapedPairs) != wantPairs {
		t.Fatalf("fixture drift: ReapOrphans reaped %d distinct pairs, want %d; stdout:\n%s", len(reapedPairs), wantPairs, stdout.String())
	}
	if counted != len(reapedPairs) {
		t.Errorf("CountOrphanedSessions = %d, ReapOrphans reaped %d distinct (socket, pane) pairs; the gate and the reaper must agree", counted, len(reapedPairs))
	}
}

// TestReapOrphans_LaunchIDRowsMatchSharedOrphanCountAndMalformedIDNotes is the
// #1252 counterpart of the parity test above, with launch-ID rows: two dead
// launches that both used pane %0 are two orphaned sessions for the count and
// two reaped processes for the reaper, a live launch on the same pane is left
// alone by both, and the malformed launch ID is never signaled, never counted
// and reported in one aggregated Note.
func TestReapOrphans_LaunchIDRowsMatchSharedOrphanCountAndMalformedIDNotes(t *testing.T) {
	const (
		socket    = "/tmp/tmux-1000/cenci"
		container = "codex-cenci-launchids"
		liveID    = "0123456789abcdef0123456789abcdef"
		deadOne   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		deadTwo   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	scan := "4001\t%0\t1000\t" + socket + "\t" + deadOne + "\n" +
		"4002\t%0\t2000\t" + socket + "\t" + deadTwo + "\n" +
		"4003\t%0\t3000\t" + socket + "\t" + liveID + "\n" +
		"4004\t%0\t4000\t" + socket + "\t" + strings.ToUpper(liveID) + "\n"

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.txt")
	writeSocketAwareReapFakes(t, dir, callLog)
	t.Setenv("PATH", dir)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	t.Setenv("FAKE_PS", container+"\n")
	t.Setenv("FAKE_SCAN", scan)
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_1", socket)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_1", "%0\t"+liveID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	counted, err := sandbox.CountOrphanedSessions(ctx, []string{"docker"}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("CountOrphanedSessions: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := ReapOrphans(&stdout, &stderr); err != nil {
		t.Fatalf("ReapOrphans: %v\nstderr: %s", err, stderr.String())
	}

	out := stdout.String()
	for _, pid := range []string{"4001", "4002"} {
		if !strings.Contains(out, "reaped\t"+container+"\t"+pid+"\t%0\n") {
			t.Errorf("dead launch pid %s was not reaped; stdout:\n%s", pid, out)
		}
	}
	for _, pid := range []string{"4003", "4004"} {
		if strings.Contains(out, "\t"+pid+"\t") {
			t.Errorf("pid %s (live / malformed launch ID) was signaled; stdout:\n%s", pid, out)
		}
	}
	if counted != 2 {
		t.Errorf("CountOrphanedSessions = %d, want 2 (the two dead launches); the gate and the reaper must agree", counted)
	}
	wantNote := fmt.Sprintf("Note: 1 process(es) in container %s carried a malformed CENCI_LAUNCH_ID; failing open and treating as live so no in-flight agent work is ever killed.", container)
	if strings.Count(out, wantNote) != 1 {
		t.Errorf("want the aggregated malformed-launch-ID note exactly once:\n%s\ngot stdout:\n%s", wantNote, out)
	}
}

// -- ticket #1127: a failing tmux socket fails open per socket ---------------
//
// A genuine tmux failure on one socket (a connection error, a stall, output that
// breaks the list-panes format) must not stop the sweep: the rows that carry
// that socket are never signaled and its Error: line is printed, but every other
// socket, container and runtime is still swept, and the sweep still ends in an
// error so `cenci sandbox reap-orphans` exits non-zero. All tests drive
// reapOrphans directly (explicit runtimes, explicit context) through the
// socket-aware fakes.

const (
	failingSocket = "/tmp/tmux-1000/failing"
	healthySocket = "/tmp/tmux-1000/healthy"

	// tmuxErrorCause is the stderr the fake tmux's "error" mode writes.
	tmuxErrorCause = "protocol version mismatch"

	// sweepTestTimeout bounds every sweep so a regression that hangs fails the
	// test instead of the whole package.
	sweepTestTimeout = 60 * time.Second
)

// newReapSweep sets up the socket-aware fake docker + tmux with no grace wait.
// withSleep keeps the ambient PATH behind the fakes dir so a fake that runs
// `exec sleep 30` finds the real sleep binary (the fakes still win).
func newReapSweep(t *testing.T, withSleep bool) (dir, callLog string) {
	t.Helper()
	ambientPath := os.Getenv("PATH")
	dir = t.TempDir()
	callLog = filepath.Join(dir, "calls.txt")
	writeSocketAwareReapFakes(t, dir, callLog)
	path := dir
	if withSleep {
		if _, err := exec.LookPath("sleep"); err != nil {
			t.Fatalf("the stalled-socket fixture needs a sleep binary on PATH: %v", err)
		}
		path = dir + string(os.PathListSeparator) + ambientPath
	}
	t.Setenv("PATH", path)
	t.Setenv("CENCI_SANDBOX_REAP_GRACE_SECS", "0")
	return dir, callLog
}

// sweepRow is one in-container scan row "<pid>\t<pane>\t<start>\t<socket>" (plus
// "\t<launch id>" when one is given).
func sweepRow(pid, pane, socket string, launchID ...string) string {
	row := pid + "\t" + pane + "\t" + pid + "000\t" + socket
	if len(launchID) > 0 {
		row += "\t" + launchID[0]
	}
	return row + "\n"
}

// setSweepContainers makes the fake docker's `ps` list names.
func setSweepContainers(t *testing.T, names ...string) {
	t.Helper()
	t.Setenv("FAKE_PS", strings.Join(names, "\n")+"\n")
}

// setContainerScan serves scan rows as one container's in-container scan.
func setContainerScan(t *testing.T, dir, container string, rows ...string) {
	t.Helper()
	writeFakeTmuxValue(t, dir, "FAKE_SCAN_"+container, strings.Join(rows, ""))
}

// setContainerScanFails makes one container's in-container scan command fail
// (exit 23, stderr "scan unavailable").
func setContainerScanFails(t *testing.T, dir, container string) {
	t.Helper()
	writeFakeTmuxValue(t, dir, "FAKE_SCAN_FAIL_"+container, "1")
}

// setTmuxSocket configures fake tmux slot 1 or 2: the socket it answers for,
// its mode ("ok", "noserver", "error", "stall") and, in "ok" mode, the first
// line it prints (a pane id, or a malformed line).
func setTmuxSocket(t *testing.T, dir string, slot int, socket, mode, panes string) {
	t.Helper()
	n := strconv.Itoa(slot)
	writeFakeTmuxValue(t, dir, "FAKE_SOCKET_"+n, socket)
	writeFakeTmuxValue(t, dir, "FAKE_TMUX_MODE_"+n, mode)
	writeFakeTmuxValue(t, dir, "FAKE_LIVE_PANES_"+n, panes)
}

// runSweep runs reapOrphans over the given runtimes under ctx.
func runSweep(ctx context.Context, runtimes []string, onlyContainer string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	err = reapOrphans(ctx, runtimes, onlyContainer, &out, &errOut)
	return out.String(), errOut.String(), err
}

// runSweepWithTimeout runs reapOrphans under a generous deadline.
func runSweepWithTimeout(t *testing.T, runtimes []string, onlyContainer string) (stdout, stderr string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), sweepTestTimeout)
	defer cancel()
	return runSweep(ctx, runtimes, onlyContainer)
}

func reapedLine(container, pid, pane string) string {
	return "reaped\t" + container + "\t" + pid + "\t" + pane + "\n"
}

// signalsTo returns the kill calls in calls aimed at pid.
func signalsTo(calls []string, pid string) []string {
	var out []string
	for _, l := range calls {
		if strings.Contains(l, "kill -") && strings.HasSuffix(l, " "+pid) {
			out = append(out, l)
		}
	}
	return out
}

// requireNoSignals fails the test if any kill call targets one of pids.
func requireNoSignals(t *testing.T, calls []string, pids ...string) {
	t.Helper()
	for _, pid := range pids {
		if sent := signalsTo(calls, pid); len(sent) != 0 {
			t.Errorf("pid %s (a row on the failed socket) was signaled: %v", pid, sent)
		}
	}
}

// requireSocketError fails the test unless err is non-nil and names socket and
// carries wantCause.
func requireSocketError(t *testing.T, err error, socket, wantCause string) {
	t.Helper()
	if err == nil {
		t.Fatal("reapOrphans err = nil, want a non-nil error so the command exits non-zero after a failed socket")
	}
	if !strings.Contains(err.Error(), strconv.Quote(socket)) {
		t.Errorf("err = %q, want it to name the failed socket %s", err, strconv.Quote(socket))
	}
	if !strings.Contains(err.Error(), wantCause) {
		t.Errorf("err = %q, want it to carry the cause %q", err, wantCause)
	}
}

// AC1: one container references two sockets, A failing and B healthy with no live
// panes. The dead row on B is reaped, nothing carrying A is signaled (a
// well-formed launch-ID row included), A's Error: line is printed, and the sweep
// ends in an error after printing the reaped summary.
func TestReapOrphans_FailingSocketFailsOpenAndHealthySocketStillReaped(t *testing.T) {
	const container = "claude-cenci-mixedsockets"
	dir, callLog := newReapSweep(t, false)
	setSweepContainers(t, container)
	setContainerScan(t, dir, container,
		sweepRow("3001", "%1", failingSocket),
		sweepRow("3002", "%2", healthySocket),
		sweepRow("3003", "%3", failingSocket, "0123456789abcdef0123456789abcdef"),
	)
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

	stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if !strings.Contains(stdout, reapedLine(container, "3002", "%2")) {
		t.Errorf("dead row on the healthy socket was not reaped; stdout:\n%s", stdout)
	}
	calls := readCallLog(t, callLog)
	if !containsLine(calls, "exec -u root "+container+" kill -TERM 3002") {
		t.Errorf("missing kill -TERM for the dead row on the healthy socket; calls:\n%s", strings.Join(calls, "\n"))
	}
	requireNoSignals(t, calls, "3001", "3003")
	if strings.Contains(stdout, "\t3001\t") || strings.Contains(stdout, "\t3003\t") {
		t.Errorf("a row on the failed socket was reported; stdout:\n%s", stdout)
	}
	wantErrLine := fmt.Sprintf("Error: failed to query tmux panes on %q: ", failingSocket)
	if !strings.Contains(stderr, wantErrLine) || !strings.Contains(stderr, tmuxErrorCause) {
		t.Errorf("stderr missing %q with the tmux cause %q:\n%s", wantErrLine, tmuxErrorCause, stderr)
	}
}

// AC2: container C1's rows reference only the failing socket; C2, later on the
// same runtime, has a dead row on the healthy socket. The sweep must still scan
// C2 and reap it.
func TestReapOrphans_FailingSocketInOneContainerStillSweepsTheNextContainer(t *testing.T) {
	const (
		first  = "claude-cenci-onlyfailing"
		second = "codex-cenci-healthyone"
	)
	dir, callLog := newReapSweep(t, false)
	setSweepContainers(t, first, second)
	setContainerScan(t, dir, first, sweepRow("3001", "%1", failingSocket), sweepRow("3002", "%2", failingSocket))
	setContainerScan(t, dir, second, sweepRow("3101", "%1", healthySocket))
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

	stdout, _, err := runSweepWithTimeout(t, []string{"docker"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if !strings.Contains(stdout, reapedLine(second, "3101", "%1")) {
		t.Errorf("the second container's dead row was not reaped after the first container's socket failed; stdout:\n%s", stdout)
	}
	calls := readCallLog(t, callLog)
	if !containsPrefix(calls, "exec -u dev "+second+" sh -c") {
		t.Errorf("the second container was never scanned; calls:\n%s", strings.Join(calls, "\n"))
	}
	requireNoSignals(t, calls, "3001", "3002")
}

// AC2, across runtimes: C1 is on docker with a failing socket, C2 on podman has a
// dead row on a healthy socket. The podman runtime is still scanned and reaped.
func TestReapOrphans_FailingSocketOnOneRuntimeStillSweepsTheOtherRuntime(t *testing.T) {
	const (
		dockerContainer = "claude-cenci-dockerfailing"
		podmanContainer = "codex-cenci-podmanhealthy"
	)
	dir, dockerLog := newReapSweep(t, false)
	podmanLog := filepath.Join(dir, "podman-calls.txt")
	writeSocketAwareReapPodman(t, dir, podmanLog)
	setSweepContainers(t, dockerContainer)
	t.Setenv("FAKE_PS_PODMAN", podmanContainer+"\n")
	setContainerScan(t, dir, dockerContainer, sweepRow("3001", "%1", failingSocket))
	setContainerScan(t, dir, podmanContainer, sweepRow("3101", "%1", healthySocket))
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

	stdout, _, err := runSweepWithTimeout(t, []string{"docker", "podman"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if !strings.Contains(stdout, reapedLine(podmanContainer, "3101", "%1")) {
		t.Errorf("podman was not swept after docker's socket failed; stdout:\n%s", stdout)
	}
	if podmanCalls := readCallLog(t, podmanLog); !containsLine(podmanCalls, "exec -u root "+podmanContainer+" kill -TERM 3101") {
		t.Errorf("missing podman kill -TERM for the healthy row; podman calls:\n%s", strings.Join(podmanCalls, "\n"))
	}
	requireNoSignals(t, readCallLog(t, dockerLog), "3001")
}

// AC3: socket A's list-panes prints a line that breaks the format. Each variant
// prints the unexpected-output Error: line naming the line, signals nothing on A,
// still reaps the dead row on healthy socket B in the same container, and ends in
// an error.
func TestReapOrphans_UnexpectedListPanesOutputIsScopedToItsSocket(t *testing.T) {
	const container = "claude-cenci-badformat"
	cases := map[string]string{
		"garbage line":                      "garbage-line",
		"valid pane, malformed launch id":   "%0\tnot-a-launch-id",
		"valid pane, uppercase launch id":   "%0\t" + strings.ToUpper("0123456789abcdef0123456789abcdef"),
		"empty pane part with a launch id":  "\t0123456789abcdef0123456789abcdef",
		"escaped tab instead of a real tab": `%0\t0123456789abcdef0123456789abcdef`,
	}
	for name, badLine := range cases {
		t.Run(name, func(t *testing.T) {
			dir, callLog := newReapSweep(t, false)
			setSweepContainers(t, container)
			setContainerScan(t, dir, container,
				sweepRow("3001", "%0", failingSocket),
				sweepRow("3002", "%2", healthySocket),
			)
			setTmuxSocket(t, dir, 1, failingSocket, "ok", badLine)
			setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

			stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

			requireSocketError(t, err, failingSocket, "unexpected output line")
			wantErrLine := fmt.Sprintf("Error: unexpected tmux list-panes output on %q: line %q", failingSocket, badLine)
			if !strings.Contains(stderr, wantErrLine) {
				t.Errorf("stderr missing %q:\n%s", wantErrLine, stderr)
			}
			if !strings.Contains(stdout, reapedLine(container, "3002", "%2")) {
				t.Errorf("dead row on the healthy socket was not reaped; stdout:\n%s", stdout)
			}
			requireNoSignals(t, readCallLog(t, callLog), "3001")
		})
	}
}

// AC4: socket A stalls past tmux.RunSocket's bound. The sweep prints an Error: line
// for A, reaps the dead row on healthy socket B, ends in an error, and finishes in
// bounded time (an upper bound, not an exact figure: the stall costs about the 5s
// child timeout).
func TestReapOrphans_StalledSocketIsScopedToItsRows(t *testing.T) {
	const (
		container = "claude-cenci-stalled"
		promptly  = 15 * time.Second
	)
	dir, callLog := newReapSweep(t, true)
	setSweepContainers(t, container)
	setContainerScan(t, dir, container,
		sweepRow("3001", "%1", failingSocket),
		sweepRow("3002", "%2", healthySocket),
	)
	setTmuxSocket(t, dir, 1, failingSocket, "stall", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

	start := time.Now()
	stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")
	elapsed := time.Since(start)

	if elapsed >= promptly {
		t.Errorf("the sweep took %s, want it bounded under %s (the fake tmux sleeps 30s)", elapsed, promptly)
	}
	if err == nil {
		t.Fatal("reapOrphans err = nil, want an error after the stalled socket")
	}
	if !strings.Contains(err.Error(), strconv.Quote(failingSocket)) {
		t.Errorf("err = %q, want it to name the stalled socket %s", err, strconv.Quote(failingSocket))
	}
	if wantErrLine := fmt.Sprintf("Error: failed to query tmux panes on %q", failingSocket); !strings.Contains(stderr, wantErrLine) {
		t.Errorf("stderr missing %q:\n%s", wantErrLine, stderr)
	}
	if !strings.Contains(stdout, reapedLine(container, "3002", "%2")) {
		t.Errorf("dead row on the healthy socket was not reaped; stdout:\n%s", stdout)
	}
	requireNoSignals(t, readCallLog(t, callLog), "3001")
}

// AC5: a failed socket is dialed at most once per sweep. Two containers that both
// reference failing socket A produce exactly one `-S A list-panes` call, signal
// nothing carrying A, and still end in an error.
func TestReapOrphans_FailedSocketIsDialedOncePerSweep(t *testing.T) {
	const (
		first  = "claude-cenci-dialonce1"
		second = "codex-cenci-dialonce2"
	)
	dir, callLog := newReapSweep(t, false)
	setSweepContainers(t, first, second)
	setContainerScan(t, dir, first, sweepRow("3001", "%1", failingSocket))
	setContainerScan(t, dir, second, sweepRow("3101", "%1", failingSocket), sweepRow("3102", "%2", failingSocket))
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")

	_, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	calls := readCallLog(t, callLog)
	dials := 0
	for _, l := range calls {
		if strings.HasPrefix(l, "-S "+failingSocket+" list-panes") {
			dials++
		}
	}
	if dials != 1 {
		t.Errorf("failing socket was dialed %d times, want exactly 1; calls:\n%s", dials, strings.Join(calls, "\n"))
	}
	if !containsPrefix(calls, "exec -u dev "+second+" sh -c") {
		t.Errorf("the second container was never scanned; calls:\n%s", strings.Join(calls, "\n"))
	}
	requireNoSignals(t, calls, "3001", "3101", "3102")
	if got := strings.Count(stderr, "Error: failed to query tmux panes on"); got != 1 {
		t.Errorf("stderr has %d Error: lines for the one failed socket, want 1:\n%s", got, stderr)
	}
}

// AC6: rows on a failed socket are their own fail-open class, not "no socket": the
// aggregated carried-no-CENCI_TMUX_SOCKET Note counts only the one row that really
// has no socket (a regression folding the failed socket's two rows in would say 3).
func TestReapOrphans_FailedSocketRowsAreNotCountedAsNoSocket(t *testing.T) {
	const container = "claude-cenci-notsocketnote"
	dir, _ := newReapSweep(t, false)
	setSweepContainers(t, container)
	setContainerScan(t, dir, container,
		sweepRow("3001", "%1", failingSocket),
		sweepRow("3002", "%2", failingSocket),
		sweepRow("3003", "%3", ""),
		sweepRow("3004", "%4", healthySocket),
	)
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%4")

	stdout, _, err := runSweepWithTimeout(t, []string{"docker"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if want := legacyNoSocketNoteText(1, container); !strings.Contains(stdout, want) {
		t.Errorf("want the no-socket Note for exactly the one row with no socket:\n%s\ngot stdout:\n%s", want, stdout)
	}
	if strings.Contains(stdout, legacyNoSocketNoteText(3, container)) {
		t.Errorf("rows on the failed socket were counted into the no-socket Note; stdout:\n%s", stdout)
	}
}

// AC7: the summary after a sweep where a socket failed. Nothing reaped: no "No
// orphaned processes found." (the failed socket's rows are unknown, not absent).
// N > 0 reaped: "Reaped N orphaned process(es)." and the sweep still errors. The
// control with no failure keeps the old summary and a nil error.
func TestReapOrphans_SummaryAfterSocketFailure(t *testing.T) {
	const container = "claude-cenci-summary"
	t.Run("nothing reaped prints no summary line", func(t *testing.T) {
		dir, _ := newReapSweep(t, false)
		setSweepContainers(t, container)
		setContainerScan(t, dir, container, sweepRow("3001", "%1", failingSocket))
		setTmuxSocket(t, dir, 1, failingSocket, "error", "")

		stdout, _, err := runSweepWithTimeout(t, []string{"docker"}, "")

		requireSocketError(t, err, failingSocket, tmuxErrorCause)
		if strings.Contains(stdout, "No orphaned processes found.") {
			t.Errorf("printed the no-op summary although a socket failed; stdout:\n%s", stdout)
		}
		if strings.Contains(stdout, "Reaped ") {
			t.Errorf("printed a Reaped summary with nothing reaped; stdout:\n%s", stdout)
		}
	})
	t.Run("reaped rows print the count and the sweep still errors", func(t *testing.T) {
		dir, _ := newReapSweep(t, false)
		setSweepContainers(t, container)
		setContainerScan(t, dir, container,
			sweepRow("3001", "%1", failingSocket),
			sweepRow("3002", "%2", healthySocket),
			sweepRow("3003", "%3", healthySocket),
		)
		setTmuxSocket(t, dir, 1, failingSocket, "error", "")
		setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

		stdout, _, err := runSweepWithTimeout(t, []string{"docker"}, "")

		requireSocketError(t, err, failingSocket, tmuxErrorCause)
		if !strings.Contains(stdout, "Reaped 2 orphaned process(es).") {
			t.Errorf("missing the Reaped 2 summary; stdout:\n%s", stdout)
		}
		if strings.Contains(stdout, "No orphaned processes found.") {
			t.Errorf("printed the no-op summary after reaping; stdout:\n%s", stdout)
		}
	})
	t.Run("no failure keeps the no-op summary and a nil error", func(t *testing.T) {
		dir, _ := newReapSweep(t, false)
		setSweepContainers(t, container)
		setContainerScan(t, dir, container, sweepRow("3001", "%1", healthySocket))
		setTmuxSocket(t, dir, 1, healthySocket, "ok", "%1")

		stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

		if err != nil {
			t.Fatalf("reapOrphans: %v\nstderr: %s", err, stderr)
		}
		if !strings.Contains(stdout, "No orphaned processes found.") {
			t.Errorf("missing the no-op summary; stdout:\n%s", stdout)
		}
	})
}

// Edge case: every socket in every container fails. Nothing is signaled, one
// Error: line is printed per distinct socket, no summary line is printed, and the
// returned error names every failed socket.
func TestReapOrphans_AllSocketsFailPrintsOneErrorPerSocketAndNoSummary(t *testing.T) {
	const (
		first  = "claude-cenci-allfail1"
		second = "codex-cenci-allfail2"
	)
	dir, callLog := newReapSweep(t, false)
	setSweepContainers(t, first, second)
	setContainerScan(t, dir, first, sweepRow("3001", "%1", failingSocket), sweepRow("3002", "%2", healthySocket))
	setContainerScan(t, dir, second, sweepRow("3101", "%1", failingSocket))
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "error", "")

	stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if !strings.Contains(err.Error(), strconv.Quote(healthySocket)) {
		t.Errorf("err = %q, want it to name the second failed socket %s too", err, strconv.Quote(healthySocket))
	}
	for _, socket := range []string{failingSocket, healthySocket} {
		wantErrLine := fmt.Sprintf("Error: failed to query tmux panes on %q: ", socket)
		if got := strings.Count(stderr, wantErrLine); got != 1 {
			t.Errorf("stderr has %d %q lines, want exactly one per distinct socket:\n%s", got, wantErrLine, stderr)
		}
	}
	for _, l := range readCallLog(t, callLog) {
		if strings.Contains(l, "kill -") {
			t.Errorf("nothing may be signaled when every socket failed, got %q", l)
		}
	}
	if strings.Contains(stdout, "reaped\t") || strings.Contains(stdout, "orphaned process") {
		t.Errorf("want no reaped line and no summary line; stdout:\n%s", stdout)
	}
}

// Edge case: a hostile socket path with control bytes and an ANSI escape is shown
// %q-escaped in its Error: line and in the returned error; no raw escape byte
// reaches stdout, stderr or the error text, and the healthy socket is still swept.
func TestReapOrphans_HostileFailingSocketPathIsQuotedEverywhere(t *testing.T) {
	const (
		container      = "claude-cenci-hostile"
		hostileSocket  = "/x\x1b[31m\rforged"
		rawControlSet  = "\x1b\r"
		healthyPidLine = "3002"
	)
	dir, _ := newReapSweep(t, false)
	setSweepContainers(t, container)
	setContainerScan(t, dir, container,
		sweepRow("3001", "%1", hostileSocket),
		sweepRow(healthyPidLine, "%2", healthySocket),
	)
	setTmuxSocket(t, dir, 1, hostileSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

	stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

	requireSocketError(t, err, hostileSocket, tmuxErrorCause)
	if wantErrLine := fmt.Sprintf("Error: failed to query tmux panes on %q: ", hostileSocket); !strings.Contains(stderr, wantErrLine) {
		t.Errorf("stderr missing the %%q-escaped Error: line %q:\n%q", wantErrLine, stderr)
	}
	for name, text := range map[string]string{"stdout": stdout, "stderr": stderr, "error": err.Error()} {
		if strings.ContainsAny(text, rawControlSet) {
			t.Errorf("%s carries a raw control byte: %q", name, text)
		}
	}
	if !strings.Contains(stdout, reapedLine(container, healthyPidLine, "%2")) {
		t.Errorf("dead row on the healthy socket was not reaped; stdout:\n%s", stdout)
	}
}

// AC8: when the caller's context ends while a socket is being resolved (a stall
// under a short deadline, or an explicit cancel), reapOrphans stops: a non-nil
// error wrapping the context error, no kill for any row, and the later container
// is never scanned.
func TestReapOrphans_EndedContextStopsTheSweepWithoutSignaling(t *testing.T) {
	const (
		first  = "claude-cenci-ctxfirst"
		second = "codex-cenci-ctxsecond"
	)
	cases := []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "expired",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Second)
			},
			wantErr: context.DeadlineExceeded,
		},
		{
			name: "cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				timer := time.AfterFunc(time.Second, cancel)
				return ctx, func() { timer.Stop(); cancel() }
			},
			wantErr: context.Canceled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, callLog := newReapSweep(t, true)
			setSweepContainers(t, first, second)
			setContainerScan(t, dir, first, sweepRow("3001", "%1", failingSocket), sweepRow("3002", "%2", healthySocket))
			setContainerScan(t, dir, second, sweepRow("3101", "%1", healthySocket))
			setTmuxSocket(t, dir, 1, failingSocket, "stall", "")
			setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")
			ctx, cancel := tc.ctx()
			defer cancel()

			stdout, stderr, err := runSweep(ctx, []string{"docker"}, "")

			if err == nil || !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.wantErr)
			}
			calls := readCallLog(t, callLog)
			if !containsLine(calls, "-S "+failingSocket+" list-panes -a -F #{pane_id}\t#{@cenci-launch-id}") {
				t.Fatalf("the stalled socket was never queried, so the context end was not exercised; calls:\n%s", strings.Join(calls, "\n"))
			}
			for _, l := range calls {
				if strings.Contains(l, "kill -") {
					t.Errorf("a kill was issued after the context ended: %q", l)
				}
			}
			if containsPrefix(calls, "exec -u dev "+second+" sh -c") {
				t.Errorf("the later container was scanned after the context ended; calls:\n%s", strings.Join(calls, "\n"))
			}
			if strings.Contains(stdout, "reaped\t") || strings.Contains(stdout, "orphaned process") {
				t.Errorf("stdout reports a reap or summary after the context ended:\n%s", stdout)
			}
			if strings.Contains(stderr, "failed to query tmux panes") {
				t.Errorf("an ended caller context was reported as the socket's failure:\n%s", stderr)
			}
		})
	}
}

// The Codex attach-time sweep (onlyContainer set, as launch.go calls it) behaves
// the same way: the dead row on the healthy socket in that one container is
// reaped, nothing on the failed socket is signaled, the runtime's container list
// is never queried, and the returned error is non-nil so the launcher can print
// its warning and carry on.
func TestReapOrphans_AttachSweepReapsHealthyRowAndReturnsTheSocketError(t *testing.T) {
	const container = "codex-cenci-attach"
	dir, callLog := newReapSweep(t, false)
	setContainerScan(t, dir, container,
		sweepRow("3001", "%1", failingSocket),
		sweepRow("3002", "%2", healthySocket),
	)
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

	stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, container)

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if !strings.Contains(stdout, reapedLine(container, "3002", "%2")) {
		t.Errorf("dead row on the healthy socket was not reaped; stdout:\n%s", stdout)
	}
	calls := readCallLog(t, callLog)
	requireNoSignals(t, calls, "3001")
	if containsPrefix(calls, "ps ") {
		t.Errorf("an attach-time sweep must not list containers; calls:\n%s", strings.Join(calls, "\n"))
	}
	if !strings.Contains(stderr, fmt.Sprintf("Error: failed to query tmux panes on %q: ", failingSocket)) {
		t.Errorf("stderr missing the failed socket's Error: line:\n%s", stderr)
	}
}

// Edge case: a socket that fails tmux.ValidSocket is never dialed, is not a socket
// failure, and its rows still count toward the carried-no-CENCI_TMUX_SOCKET Note,
// unchanged by the per-socket failure scoping. The forms are a relative path, a
// value over the 1000-byte bound, and the marker the in-container scan writes in
// place of a socket holding any control character (a tab or newline in a value
// can never reach the host as such: the scan replaces it, and tmux's own
// RunSocket tests pin that a tab/newline socket is refused before any exec). The
// healthy-socket row is the control: it proves the scan and the sweep really ran.
func TestReapOrphans_MalformedSocketIsNeverDialedAndCountsTowardTheNoSocketNote(t *testing.T) {
	const container = "claude-cenci-malformedsock"
	cases := []struct{ name, socket string }{
		{"relative path", "relative/sock"},
		{"over 1000 bytes", "/" + strings.Repeat("a", 1001)},
		{"control-character marker from the scan", "__MALFORMED__"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, callLog := newReapSweep(t, false)
			setSweepContainers(t, container)
			setContainerScan(t, dir, container,
				sweepRow("3001", "%1", tc.socket),
				sweepRow("3002", "%2", healthySocket),
			)
			// A dial that did happen would answer "no server running", which would
			// make 3001 a dead row and kill it.
			setTmuxSocket(t, dir, 1, tc.socket, "noserver", "")
			setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

			stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

			if err != nil {
				t.Fatalf("reapOrphans: %v\nstderr: %s", err, stderr)
			}
			if want := legacyNoSocketNoteText(1, container); !strings.Contains(stdout, want) {
				t.Errorf("want the no-socket Note for the malformed-socket row:\n%s\ngot stdout:\n%s", want, stdout)
			}
			if strings.Contains(stderr, "Error:") {
				t.Errorf("a malformed socket was reported as a socket failure; stderr:\n%s", stderr)
			}
			if !strings.Contains(stdout, reapedLine(container, "3002", "%2")) {
				t.Errorf("control row on the healthy socket was not reaped; stdout:\n%s", stdout)
			}
			calls := readCallLog(t, callLog)
			for _, l := range calls {
				if strings.Contains(l, "list-panes") && !strings.HasPrefix(l, "-S "+healthySocket+" list-panes") {
					t.Errorf("tmux was asked about a socket other than the healthy one: %q", l)
				}
			}
			requireNoSignals(t, calls, "3001")
		})
	}
}

// Edge case: a row on a failed socket carries a well-formed launch ID that is also
// recorded on a dead row elsewhere (here on the healthy socket, where no live pane
// records it). The healthy-socket row is reaped; the failed-socket row, whose
// liveness is unknown, is never signaled whatever its launch ID says.
func TestReapOrphans_FailedSocketRowIsNotSignaledWhenItsLaunchIDIsDeadElsewhere(t *testing.T) {
	const (
		container = "claude-cenci-sharedlaunchid"
		launchID  = "0123456789abcdef0123456789abcdef"
	)
	dir, callLog := newReapSweep(t, false)
	setSweepContainers(t, container)
	setContainerScan(t, dir, container,
		sweepRow("3001", "%1", failingSocket, launchID),
		sweepRow("3002", "%2", healthySocket, launchID),
	)
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, healthySocket, "ok", "%99")

	stdout, _, err := runSweepWithTimeout(t, []string{"docker"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if !strings.Contains(stdout, reapedLine(container, "3002", "%2")) {
		t.Errorf("the row carrying the dead launch ID on the healthy socket was not reaped; stdout:\n%s", stdout)
	}
	requireNoSignals(t, readCallLog(t, callLog), "3001")
	if strings.Contains(stdout, "\t3001\t") {
		t.Errorf("the failed-socket row was reported as reaped; stdout:\n%s", stdout)
	}
}

// Q&A 8: a sweep that fails a socket and is then aborted by something that is not a
// socket failure (an in-container scan failure, or the caller's context ending
// while a later socket stalls) returns that error joined with the socket error
// collected earlier, so neither is lost. The abort ends the sweep at once: no
// summary line and nothing is signaled.
func TestReapOrphans_MidSweepAbortIsJoinedWithTheEarlierSocketError(t *testing.T) {
	const (
		first  = "claude-cenci-abortfirst"
		second = "codex-cenci-abortsecond"
	)

	t.Run("scan failure in a later container", func(t *testing.T) {
		dir, callLog := newReapSweep(t, false)
		setSweepContainers(t, first, second)
		setContainerScan(t, dir, first, sweepRow("3001", "%1", failingSocket))
		setContainerScanFails(t, dir, second)
		setTmuxSocket(t, dir, 1, failingSocket, "error", "")

		stdout, stderr, err := runSweepWithTimeout(t, []string{"docker"}, "")

		requireSocketError(t, err, failingSocket, tmuxErrorCause)
		var scanErr *sandbox.ScanError
		if !errors.As(err, &scanErr) {
			t.Fatalf("err = %v, want it to also carry the *sandbox.ScanError that aborted the sweep", err)
		}
		if scanErr.Container != second || scanErr.Stderr != "scan unavailable" {
			t.Errorf("ScanError = {Container: %q, Stderr: %q}, want {%q, %q}", scanErr.Container, scanErr.Stderr, second, "scan unavailable")
		}
		if want := "Error: failed to scan processes in container " + second + ": scan unavailable"; !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
		if strings.Contains(stdout, "orphaned process") {
			t.Errorf("an aborted sweep printed a summary line:\n%s", stdout)
		}
		requireNoSignals(t, readCallLog(t, callLog), "3001")
	})

	t.Run("caller context ends on a later stalled socket", func(t *testing.T) {
		const third = "claude-cenci-abortthird"
		dir, callLog := newReapSweep(t, true)
		setSweepContainers(t, first, second, third)
		setContainerScan(t, dir, first, sweepRow("3001", "%1", failingSocket))
		setContainerScan(t, dir, second, sweepRow("3101", "%1", healthySocket))
		setContainerScan(t, dir, third, sweepRow("3201", "%1", failingSocket))
		setTmuxSocket(t, dir, 1, failingSocket, "error", "")
		setTmuxSocket(t, dir, 2, healthySocket, "stall", "")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		stdout, stderr, err := runSweep(ctx, []string{"docker"}, "")

		requireSocketError(t, err, failingSocket, tmuxErrorCause)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want it to also wrap context.DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), strconv.Quote(healthySocket)) {
			t.Errorf("err = %q, want the aborting error to name the stalled socket %s", err, strconv.Quote(healthySocket))
		}
		calls := readCallLog(t, callLog)
		if !containsLine(calls, "-S "+healthySocket+" list-panes -a -F #{pane_id}\t#{@cenci-launch-id}") {
			t.Fatalf("the later socket was never queried, so the abort was not exercised; calls:\n%s", strings.Join(calls, "\n"))
		}
		for _, l := range calls {
			if strings.Contains(l, "kill -") {
				t.Errorf("a kill was issued in an aborted sweep: %q", l)
			}
		}
		if containsPrefix(calls, "exec -u dev "+third+" sh -c") {
			t.Errorf("the container after the aborting one was scanned; calls:\n%s", strings.Join(calls, "\n"))
		}
		if strings.Contains(stdout, "orphaned process") {
			t.Errorf("an aborted sweep printed a summary line:\n%s", stdout)
		}
		// The caller's context ending is the abort, not the stalled socket's own
		// failure: no per-socket Error: line for it (RunSocket would otherwise
		// have folded it into an ordinary timeout failure), while the earlier
		// socket's own Error: line stays.
		if want := fmt.Sprintf("Error: failed to query tmux panes on %q: ", failingSocket); !strings.Contains(stderr, want) {
			t.Errorf("stderr missing the earlier socket's %q line:\n%s", want, stderr)
		}
		if bad := fmt.Sprintf("Error: failed to query tmux panes on %q", healthySocket); strings.Contains(stderr, bad) {
			t.Errorf("the caller's context end was reported as the stalled socket's failure (%q):\n%s", bad, stderr)
		}
	})
}

// A caller context that ends on a stalled socket after an earlier socket of the
// SAME container already failed: the abort error still carries that earlier
// socket's failure (its Error: line was printed, so the returned error must not
// drop it), together with the stalled socket and the context error. Nothing is
// signaled and no summary is printed.
func TestReapOrphans_ContextEndInOneContainerKeepsThatContainersEarlierSocketFailure(t *testing.T) {
	const (
		container = "claude-cenci-samecontainerabort"
		stalled   = "/tmp/tmux-1000/stalled"
	)
	dir, callLog := newReapSweep(t, true)
	setSweepContainers(t, container)
	// The failing socket's row comes first, so it is resolved (and fails) before
	// the stalled one is dialed.
	setContainerScan(t, dir, container,
		sweepRow("3001", "%1", failingSocket),
		sweepRow("3002", "%2", stalled),
	)
	setTmuxSocket(t, dir, 1, failingSocket, "error", "")
	setTmuxSocket(t, dir, 2, stalled, "stall", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stdout, stderr, err := runSweep(ctx, []string{"docker"}, "")

	requireSocketError(t, err, failingSocket, tmuxErrorCause)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to also wrap context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), strconv.Quote(stalled)) {
		t.Errorf("err = %q, want the aborting error to name the stalled socket %s", err, strconv.Quote(stalled))
	}
	calls := readCallLog(t, callLog)
	if !containsLine(calls, "-S "+stalled+" list-panes -a -F #{pane_id}\t#{@cenci-launch-id}") {
		t.Fatalf("the stalled socket was never queried, so the abort was not exercised; calls:\n%s", strings.Join(calls, "\n"))
	}
	for _, l := range calls {
		if strings.Contains(l, "kill -") {
			t.Errorf("a kill was issued in an aborted sweep: %q", l)
		}
	}
	if strings.Contains(stdout, "orphaned process") {
		t.Errorf("an aborted sweep printed a summary line:\n%s", stdout)
	}
	if want := fmt.Sprintf("Error: failed to query tmux panes on %q: ", failingSocket); !strings.Contains(stderr, want) {
		t.Errorf("stderr missing the earlier socket's %q line:\n%s", want, stderr)
	}
	if bad := fmt.Sprintf("Error: failed to query tmux panes on %q", stalled); strings.Contains(stderr, bad) {
		t.Errorf("the caller's context end was reported as the stalled socket's failure (%q):\n%s", bad, stderr)
	}
}
