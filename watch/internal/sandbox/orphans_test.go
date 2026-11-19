package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

// Integration tests for CountOrphanedSessions (#1218): the read-only count of
// distinct (tmux socket, pane) pairs that `cenci sandbox reap-orphans` would
// classify as dead-pane reap targets. Everything runs against fake
// docker/podman/tmux executables placed alone on PATH (adapted from
// internal/sandbox/launcher/reap_test.go's writeSocketAwareReapFakes), so the
// real scan -> classify -> socket-liveness path is exercised end to end.

const (
	orphanSocketA = "/tmp/tmux-1000/dead-a"
	orphanSocketB = "/tmp/tmux-1000/dead-b"

	orphanDockerContainer = "claude-cenci-orphans"
	orphanDockerSecond    = "codex-cenci-orphans"
	orphanDockerThird     = "claude-cenci-third"
	orphanPodmanContainer = "claude-cenci-podman"

	// orphanTestTimeout bounds the happy-path counts so a regression that
	// hangs fails the test instead of the whole package.
	orphanTestTimeout = 30 * time.Second
)

// orphanFakeRuntime configures one fake docker/podman binary.
type orphanFakeRuntime struct {
	name     string            // "docker" or "podman"
	ps       []string          // running container names `ps` prints
	psExit   int               // non-zero makes `ps` fail
	scans    map[string]string // container -> raw in-container scan stdout
	scanFail map[string]bool   // container -> the scan exec exits 1
	scanHang map[string]bool   // container -> the scan exec sleeps far past any test deadline
}

// writeOrphanFakeRuntime writes a fake runtime binary named rt.name into dir.
// It logs every invocation (bare args) to callLog, answers `ps` from rt.ps,
// and serves the in-container environ scan per container (keyed on the
// container-name argv slot: `exec -u <user> <container> sh -c <script>`).
// Any other exec (kill, liveness probe) is logged and succeeds silently, so a
// regression that signals a process shows up in the call log.
func writeOrphanFakeRuntime(t *testing.T, dir, callLog string, rt orphanFakeRuntime) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + "\n")
	b.WriteString("case \"$1\" in\nps)\n")
	if rt.psExit != 0 {
		fmt.Fprintf(&b, "  echo 'ps failed' >&2\n  exit %d\n  ;;\n", rt.psExit)
	} else {
		b.WriteString("  printf '%s' " + exectest.ShellQuote(strings.Join(rt.ps, "\n")+"\n") + "\n  ;;\n")
	}
	b.WriteString("exec)\n  case \"$*\" in\n  *TMUX_PANE=*)\n    case \"$4\" in\n")
	for container, scan := range rt.scans {
		fmt.Fprintf(&b, "    %s) printf '%%s' %s ;;\n", exectest.ShellQuote(container), exectest.ShellQuote(scan))
	}
	for container := range rt.scanFail {
		fmt.Fprintf(&b, "    %s) echo 'scan failed' >&2; exit 1 ;;\n", exectest.ShellQuote(container))
	}
	for container := range rt.scanHang {
		// exec so the kill on deadline reaches the sleeper itself, not a
		// shell parent holding the pipes open (watch/docs/go-gotchas.md, #886).
		fmt.Fprintf(&b, "    %s) exec sleep 30 ;;\n", exectest.ShellQuote(container))
	}
	b.WriteString("    esac\n    ;;\n  esac\n  ;;\nesac\nexit 0\n")
	exectest.WriteExecutable(t, filepath.Join(dir, rt.name), b.String())
}

// orphanTmuxSocket configures how the fake tmux answers `-S <socket>
// list-panes`: mode "ok" (default) prints panes, "noserver" fails with tmux's
// "no server running" stderr, "error" fails with a genuine tmux error, "stall"
// sleeps far past any bound (needs the real sleep binary on PATH behind the
// fakes dir).
type orphanTmuxSocket struct {
	mode  string
	panes []string
}

// writeOrphanFakeTmux writes a fake tmux that parses the explicit `-S <path>`
// out of its argv, logs its invocation to callLog, and answers per socket from
// sockets. A socket that is not configured answers "no server running", so an
// unexpected query can never accidentally observe live panes.
//
// A configured pane line is "<pane id>" or "<pane id>\t<launch id>" (the
// recorded @cenci-launch-id, #1252). The fake honors the requested -F format
// like real tmux: asked for just "#{pane_id}" it prints only the pane id column,
// so a reaper that has not switched to the launch-ID format cannot observe any
// recorded ID.
func writeOrphanFakeTmux(t *testing.T, dir, callLog string, sockets map[string]orphanTmuxSocket) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + "\n")
	b.WriteString(`socket=""
fmt=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-S" ]; then socket="$arg"; fi
  if [ "$prev" = "-F" ]; then fmt="$arg"; fi
  prev="$arg"
done
mode=noserver
panes=""
case "$socket" in
`)
	for socket, cfg := range sockets {
		mode := cfg.mode
		if mode == "" {
			mode = "ok"
		}
		fmt.Fprintf(&b, "  %s) mode=%s; panes=%s ;;\n", exectest.ShellQuote(socket), mode, exectest.ShellQuote(strings.Join(cfg.panes, "\n")))
	}
	b.WriteString(`esac
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
    if [ "$fmt" = '#{pane_id}' ]; then
      tab=$(printf '\t')
      printf '%s\n' "$panes" | while IFS= read -r line; do printf '%s\n' "${line%%"$tab"*}"; done
    else
      printf '%s\n' "$panes"
    fi
    exit 0
    ;;
esac
`)
	exectest.WriteExecutable(t, filepath.Join(dir, "tmux"), b.String())
}

// orphanFixture creates the fake-binary directory (the only PATH entry) and
// the shared call log.
func orphanFixture(t *testing.T) (dir, callLog string) {
	t.Helper()
	dir = t.TempDir()
	callLog = filepath.Join(dir, "calls.txt")
	t.Setenv("PATH", dir)
	return dir, callLog
}

func readOrphanCallLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read call log: %v", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// scanLine is one "<pid>\t<pane>\t<start>\t<socket>" row of the in-container
// scan output.
func scanLine(pid, pane, start, socket string) string {
	return pid + "\t" + pane + "\t" + start + "\t" + socket + "\n"
}

// scanLineLaunch is the 5-field variant (#1252): the same row plus the
// process's CENCI_LAUNCH_ID (or the scan's malformed marker) as the 5th column.
func scanLineLaunch(pid, pane, start, socket, launchID string) string {
	return pid + "\t" + pane + "\t" + start + "\t" + socket + "\t" + launchID + "\n"
}

// panesWithLaunchIDs builds the fake tmux pane lines for a socket: each
// "<pane>=<launch id>" pair becomes "<pane>\t<launch id>" (an empty id is a pane
// with no recorded @cenci-launch-id).
func panesWithLaunchIDs(pairs ...string) []string {
	lines := make([]string, 0, len(pairs))
	for _, p := range pairs {
		pane, id, _ := strings.Cut(p, "=")
		lines = append(lines, pane+"\t"+id)
	}
	return lines
}

func anyLineContains(lines []string, sub string) bool {
	return slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, sub) })
}

// runCount runs CountOrphanedSessions with a generous deadline.
func runCount(runtimes []string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()
	return CountOrphanedSessions(ctx, runtimes, io.Discard, io.Discard)
}

// countOrphans fails the test on a count error.
func countOrphans(t *testing.T, runtimes ...string) int {
	t.Helper()
	got, err := runCount(runtimes)
	if err != nil {
		t.Fatalf("CountOrphanedSessions: %v", err)
	}
	return got
}

// requireCountError fails the test unless the count returns an error.
func requireCountError(t *testing.T, runtimes ...string) {
	t.Helper()
	got, err := runCount(runtimes)
	if err == nil {
		t.Fatalf("CountOrphanedSessions = (%d, nil), want an error (fail closed)", got)
	}
}

// The same (socket, pane) pair reported by two pids (an agent and its child
// both carry the pane env) is one orphaned session, not two.
func TestCountOrphanedSessions_DedupesSamePairAcrossPids(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA) + scanLine("3002", "%3", "2000", orphanSocketA),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (one distinct (socket, pane) pair)", got)
	}
}

// Both tmux servers allocate pane ids from %0, so the same pane id on two
// different dead sockets is two distinct sessions.
func TestCountOrphanedSessions_SamePaneIdOnTwoDeadSocketsCountsTwo(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA) + scanLine("3002", "%3", "2000", orphanSocketB),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: []string{"%99"}},
		orphanSocketB: {mode: "noserver"},
	})

	if got := countOrphans(t, "docker"); got != 2 {
		t.Errorf("count = %d, want 2 (same pane id, two distinct sockets)", got)
	}
}

func TestCountOrphanedSessions_LivePaneCountsZero(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "docker",
		ps:    []string{orphanDockerContainer},
		scans: map[string]string{orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA)},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%3"}}})

	if got := countOrphans(t, "docker"); got != 0 {
		t.Errorf("count = %d, want 0 (pane is live on its own socket)", got)
	}
	// Guard against a vacuous pass: the socket must actually have been queried.
	if calls := readOrphanCallLog(t, callLog); !anyLineContains(calls, "-S "+orphanSocketA+" list-panes") {
		t.Errorf("expected the socket to be queried via an explicit -S; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// Pair matching, never a union of live pane ids across sockets: %8 is dead on
// its own socket even though %8 is a live pane id on an unrelated socket.
func TestCountOrphanedSessions_PairMatchingNotUnionOfSockets(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("6001", "%9", "1000", orphanSocketA) + scanLine("6002", "%8", "2000", orphanSocketA),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: []string{"%9"}},
		orphanSocketB: {panes: []string{"%8"}},
	})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (only %%8, dead on its own socket)", got)
	}
}

// Each "fails open" case below ships next to one genuine dead-pane control row
// (controlRow, on orphanSocketA): the count must be exactly 1, proving the
// scan really ran and only the excluded rows were left out, and tmux may be
// asked about orphanSocketA alone.
var controlRow = scanLine("9001", "%9", "9000", orphanSocketA)

// requireOnlySocketQueried asserts tmux was asked about wantSocket at least
// once and never about any other socket.
func requireOnlySocketQueried(t *testing.T, callLog, wantSocket string) {
	t.Helper()
	calls := readOrphanCallLog(t, callLog)
	queried := false
	for _, l := range calls {
		if !strings.Contains(l, "list-panes") {
			continue
		}
		if !strings.HasPrefix(l, "-S "+wantSocket+" list-panes") {
			t.Errorf("tmux was queried for an excluded row's socket: %q", l)
			continue
		}
		queried = true
	}
	if !queried {
		t.Errorf("expected tmux to be asked about %s; calls:\n%s", wantSocket, strings.Join(calls, "\n"))
	}
}

// A row with no socket (pre-#1007 launcher), an empty socket, or a malformed
// one (relative path, over the length bound) fails open exactly like the
// reaper: never counted, and never passed to tmux.
func TestCountOrphanedSessions_NoSocketAndMalformedSocketAreNotCounted(t *testing.T) {
	dir, callLog := orphanFixture(t)
	tooLongSocket := "/" + strings.Repeat("a", 1001)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: "5001\t%1\t1000\n" + // legacy 3-field row
				scanLine("5002", "%2", "1000", "relative/sock") +
				scanLine("5003", "%3", "1000", "") +
				scanLine("5004", "%4", "1000", tooLongSocket) +
				controlRow,
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (only the control row; no or malformed socket fails open)", got)
	}
	requireOnlySocketQueried(t, callLog, orphanSocketA)
}

func TestCountOrphanedSessions_MalformedPaneIsNotCounted(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("5001", "not-a-pane", "1000", orphanSocketB) +
				scanLine("5002", "%", "1000", orphanSocketB) +
				scanLine("5003", "%1x", "1000", orphanSocketB) +
				controlRow,
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: []string{"%99"}},
		orphanSocketB: {mode: "noserver"},
	})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (only the control row; malformed pane values are never targets)", got)
	}
	requireOnlySocketQueried(t, callLog, orphanSocketA)
}

// PID 1 is the container init: never a reap target, whatever pane it carries.
func TestCountOrphanedSessions_Pid1IsNotCounted(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("1", "%1", "50", orphanSocketB) + controlRow,
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: []string{"%99"}},
		orphanSocketB: {mode: "noserver"},
	})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (only the control row; PID 1 is the container init)", got)
	}
	requireOnlySocketQueried(t, callLog, orphanSocketA)
}

// __UNREADABLE__ markers are a diagnostic about the scan, not sessions, and
// are not an error.
func TestCountOrphanedSessions_UnreadableMarkersAreNotCountedOrFatal(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: "__UNREADABLE__\t5002\n__UNREADABLE__\t1\n" + controlRow,
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (only the control row)", got)
	}
}

// Only claude-cenci-/codex-cenci-/opencode-cenci- containers are scanned: a
// foreign container's dead-looking rows are not ours to count.
func TestCountOrphanedSessions_NonSandboxContainersAreNeverScanned(t *testing.T) {
	dir, callLog := orphanFixture(t)
	const foreign = "postgres-db"
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{foreign, orphanDockerContainer},
		scans: map[string]string{
			foreign:               scanLine("7001", "%7", "1000", orphanSocketA),
			orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (the foreign container's row is excluded)", got)
	}
	if calls := readOrphanCallLog(t, callLog); anyLineContains(calls, foreign+" sh -c") {
		t.Errorf("a non-sandbox container was scanned; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// Containers on both runtimes are summed.
func TestCountOrphanedSessions_DockerAndPodmanContainersAreSummed(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "docker",
		ps:    []string{orphanDockerContainer},
		scans: map[string]string{orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA)},
	})
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "podman",
		ps:    []string{orphanPodmanContainer},
		scans: map[string]string{orphanPodmanContainer: scanLine("3101", "%5", "1000", orphanSocketA)},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	if got := countOrphans(t, "docker", "podman"); got != 2 {
		t.Errorf("count = %d, want 2 (one orphan per runtime)", got)
	}
}

// The (socket, pane) pair is the identity: the container and the runtime are
// not part of the key, so one pair reported from several containers across
// both runtimes is still one orphaned session (AC13's "distinct pairs").
func TestCountOrphanedSessions_DedupesSamePairAcrossContainersAndRuntimes(t *testing.T) {
	dir, callLog := orphanFixture(t)
	samePair := scanLine("3001", "%3", "1000", orphanSocketA)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer, orphanDockerSecond},
		scans: map[string]string{
			orphanDockerContainer: samePair,
			orphanDockerSecond:    samePair,
		},
	})
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "podman",
		ps:    []string{orphanPodmanContainer},
		scans: map[string]string{orphanPodmanContainer: samePair},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	if got := countOrphans(t, "docker", "podman"); got != 1 {
		t.Errorf("count = %d, want 1 (one distinct pair across 3 containers and 2 runtimes)", got)
	}
}

// "no server running" resolves to an empty live set: every pane on that
// socket is an orphan.
func TestCountOrphanedSessions_NoTmuxServerCountsPanesOnThatSocket(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%1", "1000", orphanSocketA) + scanLine("3002", "%2", "2000", orphanSocketA),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {mode: "noserver"}})

	if got := countOrphans(t, "docker"); got != 2 {
		t.Errorf("count = %d, want 2 (no server running => both panes orphaned)", got)
	}
}

// A genuine tmux failure is never folded into "no orphans": it fails closed.
func TestCountOrphanedSessions_GenuineTmuxErrorFailsClosed(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%1", "1000", orphanSocketA) + scanLine("3002", "%2", "2000", orphanSocketB),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: []string{"%99"}},
		orphanSocketB: {mode: "error"},
	})

	requireCountError(t, "docker")
}

// CENCI_TMUX_SOCKET is attacker-controlled and the returned error reaches the
// dispatch log, whose consumers classify lines by the ` dispatch ` / ` skip:`
// substrings. The error must carry the real tmux cause but neither raw control
// bytes nor those substrings, whatever the socket value holds.
func TestCountOrphanedSessions_TmuxErrorTextIsLogSafeForHostileSocket(t *testing.T) {
	const hostileSocket = "/x dispatch (a, b): y skip: z\x1b[31m\rforged"
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "docker",
		ps:    []string{orphanDockerContainer},
		scans: map[string]string{orphanDockerContainer: scanLine("3001", "%1", "1000", hostileSocket)},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{hostileSocket: {mode: "error"}})

	_, err := runCount([]string{"docker"})
	if err == nil {
		t.Fatal("CountOrphanedSessions err = nil, want the genuine tmux error")
	}
	text := err.Error()
	if strings.ContainsAny(text, "\x1b\r\n\t") {
		t.Errorf("error text contains raw control bytes: %q", text)
	}
	for _, forged := range []string{" dispatch ", " skip:"} {
		if strings.Contains(text, forged) {
			t.Errorf("error text contains the line-classifier substring %q: %q", forged, text)
		}
	}
	if !strings.Contains(text, "protocol version mismatch") {
		t.Errorf("error text %q does not carry the tmux stderr cause", text)
	}
}

// The runtime's own stderr is the real cause of a failed listing or scan; it
// must reach the returned error (quoted, so a hostile value stays inert).
func TestCountOrphanedSessions_RuntimeStderrIsInErrorText(t *testing.T) {
	t.Run("ps", func(t *testing.T) {
		dir, callLog := orphanFixture(t)
		writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{name: "docker", psExit: 1})
		_, err := runCount([]string{"docker"})
		if err == nil || !strings.Contains(err.Error(), "ps failed") {
			t.Errorf("err = %v, want the ps stderr cause %q", err, "ps failed")
		}
	})
	t.Run("scan", func(t *testing.T) {
		dir, callLog := orphanFixture(t)
		writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
			name:     "docker",
			ps:       []string{orphanDockerContainer},
			scanFail: map[string]bool{orphanDockerContainer: true},
		})
		writeOrphanFakeTmux(t, dir, callLog, nil)
		_, err := runCount([]string{"docker"})
		if err == nil || !strings.Contains(err.Error(), "scan failed") {
			t.Errorf("err = %v, want the scan stderr cause %q", err, "scan failed")
		}
	})
}

// A failing `ps` on any runtime is an error, not "that runtime has none".
func TestCountOrphanedSessions_PsFailureFailsClosed(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "docker",
		ps:    []string{orphanDockerContainer},
		scans: map[string]string{orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA)},
	})
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{name: "podman", psExit: 1})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	requireCountError(t, "docker", "podman")
}

// A scan exec failure in a later container is an error even though an
// earlier container scanned cleanly: no partial count is ever returned as if
// complete.
func TestCountOrphanedSessions_ScanExecFailureFailsClosed(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer, orphanDockerSecond},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA),
		},
		scanFail: map[string]bool{orphanDockerSecond: true},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

	requireCountError(t, "docker")
}

// AC16 timeout: a scan that hangs past the caller's deadline is an error, and
// it returns promptly instead of waiting for the hung process.
func TestCountOrphanedSessions_HungScanTimesOutAsError(t *testing.T) {
	// The fake's `exec sleep` needs the real sleep binary: keep the ambient
	// PATH behind the fakes dir, which stays first so the fake docker/tmux
	// still win.
	ambientPath := os.Getenv("PATH")
	dir, callLog := orphanFixture(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+ambientPath)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Fatalf("the hung-scan fixture needs a sleep binary on PATH: %v", err)
	}
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:     "docker",
		ps:       []string{orphanDockerContainer},
		scanHang: map[string]bool{orphanDockerContainer: true},
	})
	writeOrphanFakeTmux(t, dir, callLog, nil)

	const deadline = 300 * time.Millisecond
	const promptly = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	start := time.Now()
	got, err := CountOrphanedSessions(ctx, []string{"docker"}, io.Discard, io.Discard)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("CountOrphanedSessions = (%d, nil), want a timeout error", got)
	}
	if elapsed >= promptly {
		t.Errorf("returned after %s, want it bounded near the %s deadline (the scan sleeps 30s)", elapsed, deadline)
	}
	if calls := readOrphanCallLog(t, callLog); !anyLineContains(calls, "exec -u dev "+orphanDockerContainer) {
		t.Errorf("the hung scan was never started, so the timeout was not exercised; calls:\n%s", strings.Join(calls, "\n"))
	}
}

// AC15: counting is strictly read-only. With real orphans present, the call
// log must hold no kill and no root exec -- the only exec the count issues is
// the -u dev environ scan.
func TestCountOrphanedSessions_NeverSignalsOrExecsAsRoot(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%3", "1000", orphanSocketA) + scanLine("3002", "%4", "2000", orphanSocketA),
		},
	})
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "podman",
		ps:    []string{orphanPodmanContainer},
		scans: map[string]string{orphanPodmanContainer: scanLine("3101", "%5", "1000", orphanSocketB)},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: []string{"%99"}},
		orphanSocketB: {mode: "noserver"},
	})

	if got := countOrphans(t, "docker", "podman"); got != 3 {
		t.Fatalf("count = %d, want 3 (the fixture must contain real orphans for the no-signal check to mean anything)", got)
	}

	calls := readOrphanCallLog(t, callLog)
	if anyLineContains(calls, "kill") {
		t.Errorf("counting issued a kill; calls:\n%s", strings.Join(calls, "\n"))
	}
	if anyLineContains(calls, "-u root") {
		t.Errorf("counting issued a root exec; calls:\n%s", strings.Join(calls, "\n"))
	}
	for _, l := range calls {
		if strings.HasPrefix(l, "exec ") && !strings.HasPrefix(l, "exec -u dev ") {
			t.Errorf("every exec the count issues must be the -u dev scan, got %q", l)
		}
	}
}

// -- ticket #1252: launch-ID-keyed identity ---------------------------------
//
// A row that carries a CENCI_LAUNCH_ID (the scan's 5th field) is judged by
// whether that ID is recorded (@cenci-launch-id) on a live pane of its socket,
// and counted by (socket, launch ID); a row without one keeps the (socket, pane)
// rule. Every case runs for both a claude-cenci-* and a codex-cenci-*
// container (AC8).

const (
	launchIDOne   = "0123456789abcdef0123456789abcdef"
	launchIDTwo   = "fedcba9876543210fedcba9876543210"
	launchIDThree = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// launchIDAgentContainers are one container per agent prefix.
var launchIDAgentContainers = []string{"claude-cenci-orphans", "codex-cenci-orphans"}

// forEachAgentContainer runs fn once per agent-prefixed container.
func forEachAgentContainer(t *testing.T, fn func(t *testing.T, container string)) {
	t.Helper()
	for _, container := range launchIDAgentContainers {
		t.Run(container, func(t *testing.T) { fn(t, container) })
	}
}

// countLaunchScan serves scan as container's in-container scan output against
// the given fake tmux sockets and returns the orphan count plus the call log.
func countLaunchScan(t *testing.T, container, scan string, sockets map[string]orphanTmuxSocket) (int, string) {
	t.Helper()
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "docker",
		ps:    []string{container},
		scans: map[string]string{container: scan},
	})
	writeOrphanFakeTmux(t, dir, callLog, sockets)
	return countOrphans(t, "docker"), callLog
}

// AC7: after a tmux server restart both dead launches used pane %0 on the same
// socket. They are two orphaned sessions, not one (socket, pane) pair -- whether
// %0 is gone or has been reused by a new launch.
func TestCountOrphanedSessions_TwoDeadLaunchesOnSamePaneCountTwo(t *testing.T) {
	layouts := map[string][]string{
		"pane %0 reused by a new launch": panesWithLaunchIDs("%0=" + launchIDThree),
		"pane %0 gone":                   panesWithLaunchIDs("%99=" + launchIDThree),
	}
	for name, panes := range layouts {
		t.Run(name, func(t *testing.T) {
			forEachAgentContainer(t, func(t *testing.T, container string) {
				scan := scanLineLaunch("3001", "%0", "1000", orphanSocketA, launchIDOne) +
					scanLineLaunch("3002", "%0", "2000", orphanSocketA, launchIDTwo)

				got, _ := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{orphanSocketA: {panes: panes}})

				if got != 2 {
					t.Errorf("count = %d, want 2 (two dead launches that both used %%0 are two sessions)", got)
				}
			})
		})
	}
}

// AC7: a live launch on %0 does not hide a dead one that used the same pane id.
func TestCountOrphanedSessions_LiveLaunchOnSamePaneDoesNotHideDeadOne(t *testing.T) {
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := scanLineLaunch("3001", "%0", "1000", orphanSocketA, launchIDOne) +
			scanLineLaunch("3002", "%0", "2000", orphanSocketA, launchIDTwo)

		got, _ := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{
			orphanSocketA: {panes: panesWithLaunchIDs("%0=" + launchIDOne)},
		})

		if got != 1 {
			t.Errorf("count = %d, want 1 (launch one is recorded on a live pane; only launch two is dead)", got)
		}
	})
}

// The strict rule: a live pane that has no recorded launch ID does not keep
// alive a process that carries one.
func TestCountOrphanedSessions_LivePaneWithoutRecordedIDDoesNotKeepLaunchAlive(t *testing.T) {
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := scanLineLaunch("3001", "%0", "1000", orphanSocketA, launchIDOne)

		got, _ := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{
			orphanSocketA: {panes: panesWithLaunchIDs("%0=")},
		})

		if got != 1 {
			t.Errorf("count = %d, want 1 (pane %%0 is live but records no launch ID, so a row carrying one is dead)", got)
		}
	})
}

// A launch is live iff its ID is recorded on some live pane of its socket: the
// recorded ID, not the (possibly stale) TMUX_PANE value, is the identity.
func TestCountOrphanedSessions_LaunchRecordedOnAnotherLivePaneIsLive(t *testing.T) {
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := scanLineLaunch("3001", "%0", "1000", orphanSocketA, launchIDOne) +
			scanLineLaunch("3002", "%0", "2000", orphanSocketA, launchIDTwo)

		got, _ := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{
			orphanSocketA: {panes: panesWithLaunchIDs("%5=" + launchIDOne)},
		})

		if got != 1 {
			t.Errorf("count = %d, want 1 (launch one is recorded on live pane %%5 of the same socket; only launch two is dead)", got)
		}
	})
}

// A launch ID recorded on a live pane of a different socket proves nothing
// about this one: liveness is per socket, never a union. The scan carries a
// second row on socket B (where the ID is recorded live) so BOTH sockets are
// resolved: a union-across-sockets implementation would see the ID live for the
// first row too and count 0 -- only per-socket liveness yields exactly 1.
func TestCountOrphanedSessions_LaunchRecordedOnOtherSocketIsDead(t *testing.T) {
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := scanLineLaunch("3001", "%0", "1000", orphanSocketA, launchIDOne) +
			scanLineLaunch("3002", "%0", "2000", orphanSocketB, launchIDOne)

		got, callLog := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{
			orphanSocketA: {panes: panesWithLaunchIDs("%0=" + launchIDTwo)},
			orphanSocketB: {panes: panesWithLaunchIDs("%0=" + launchIDOne)},
		})

		calls := readOrphanCallLog(t, callLog)
		for _, socket := range []string{orphanSocketA, orphanSocketB} {
			if !anyLineContains(calls, "-S "+socket) {
				t.Errorf("socket %s was never queried, so the test cannot tell per-socket from union liveness; calls: %v", socket, calls)
			}
		}
		if got != 1 {
			t.Errorf("count = %d, want 1 (the first row's ID is only recorded on the unrelated socket; the second row is live on its own)", got)
		}
	})
}

// A non-empty list-panes line whose pane part is not a pane id (no tab and
// garbage, an escaped tab, an empty pane) means tmux did not honor the format.
// It must fail closed -- never be folded into a live pane with no recorded ID,
// which would make that pane's agent reapable.
func TestCountOrphanedSessions_UnexpectedListPanesLineFailsClosed(t *testing.T) {
	cases := map[string]string{
		"garbage without a tab": "garbage-line",
		"escaped tab":           `%0\t` + launchIDOne,
		"empty pane part":       "\t" + launchIDOne,
		"bad line after good":   "%0\t" + launchIDOne + "\nnot-a-pane",
		// A valid pane with a NON-EMPTY launch-ID column that fails
		// ValidLaunchID must not be treated as "no ID" (that would reap the
		// process carrying that pane's real ID as a dead pane).
		"valid pane, non-hex launch id":   "%0\tnot-a-launch-id",
		"valid pane, short launch id":     "%0\tabc123",
		"valid pane, uppercase launch id": "%0\t" + strings.ToUpper(launchIDOne),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			forEachAgentContainer(t, func(t *testing.T, container string) {
				dir, callLog := orphanFixture(t)
				writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
					name:  "docker",
					ps:    []string{container},
					scans: map[string]string{container: scanLineLaunch("3001", "%0", "1000", orphanSocketA, launchIDOne)},
				})
				writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{line}}})
				var stderr strings.Builder
				ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
				defer cancel()

				got, err := CountOrphanedSessions(ctx, []string{"docker"}, io.Discard, &stderr)

				if err == nil {
					t.Fatalf("CountOrphanedSessions = (%d, nil), want an error for the unexpected list-panes line %q", got, line)
				}
				bad := line[strings.LastIndex(line, "\n")+1:]
				if want := fmt.Sprintf("%q", bad); !strings.Contains(err.Error(), "unexpected") || !strings.Contains(stderr.String(), want) {
					t.Errorf("err = %v, stderr = %q; want an \"unexpected\" error and an Error: line naming the line as %s", err, stderr.String(), want)
				}
				if !strings.Contains(stderr.String(), "Error:") {
					t.Errorf("stderr = %q, want an Error: line", stderr.String())
				}
			})
		})
	}
}

// The identity kinds do not collide: a legacy row and a launch-ID row on the
// same dead pane are one (socket, pane) session plus one (socket, launch ID)
// session.
func TestCountOrphanedSessions_LegacyAndLaunchIDRowsOnSameDeadPaneCountSeparately(t *testing.T) {
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := scanLine("3001", "%0", "1000", orphanSocketA) +
			scanLineLaunch("3002", "%0", "2000", orphanSocketA, launchIDOne)

		got, _ := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{
			orphanSocketA: {panes: panesWithLaunchIDs("%99=" + launchIDThree)},
		})

		if got != 2 {
			t.Errorf("count = %d, want 2 (one legacy (socket, pane) orphan and one (socket, launch ID) orphan)", got)
		}
	})
}

// Regression guard (passes before the change too): the same launch seen from
// two pids, and from two containers, is one orphaned session.
func TestCountOrphanedSessions_SameLaunchIDAcrossPidsAndContainersCountsOnce(t *testing.T) {
	dir, callLog := orphanFixture(t)
	row := scanLineLaunch("3001", "%3", "1000", orphanSocketA, launchIDOne)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   launchIDAgentContainers,
		scans: map[string]string{
			launchIDAgentContainers[0]: row + scanLineLaunch("3002", "%3", "2000", orphanSocketA, launchIDOne),
			launchIDAgentContainers[1]: row,
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {panes: panesWithLaunchIDs("%99=" + launchIDThree)}})

	if got := countOrphans(t, "docker"); got != 1 {
		t.Errorf("count = %d, want 1 (one launch ID seen from 3 processes in 2 containers)", got)
	}
}

// A tmux server that is gone leaves every launch dead: each distinct ID on the
// socket is one orphaned session, even when they all report pane %0.
func TestCountOrphanedSessions_NoTmuxServerCountsLaunchIDRowsPerLaunch(t *testing.T) {
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := scanLineLaunch("3001", "%0", "1000", orphanSocketA, launchIDOne) +
			scanLineLaunch("3002", "%0", "2000", orphanSocketA, launchIDTwo)

		got, _ := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{orphanSocketA: {mode: "noserver"}})

		if got != 2 {
			t.Errorf("count = %d, want 2 (no server running => both launches dead, counted per launch ID)", got)
		}
	})
}

// A malformed launch ID (wrong length, uppercase, the scan's malformed marker)
// fails open: never counted, however dead its pane looks. The control row is
// the one genuine orphan.
func TestCountOrphanedSessions_MalformedLaunchIDIsNotCounted(t *testing.T) {
	malformed := []string{
		malformedFieldMarker,
		"abc123",
		strings.ToUpper(launchIDOne),
		launchIDOne + "0",
		strings.Repeat("g", 32),
	}
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := controlRow
		for i, id := range malformed {
			scan += scanLineLaunch(fmt.Sprintf("%d", 5001+i), fmt.Sprintf("%%%d", i+1), "1000", orphanSocketA, id)
		}

		got, _ := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

		if got != 1 {
			t.Errorf("count = %d, want 1 (only the control row; malformed launch IDs fail open)", got)
		}
	})
}

// AC5: a launch-ID row with no, or a malformed, socket fails open exactly like
// a legacy row: never counted, never passed to tmux.
func TestCountOrphanedSessions_LaunchIDRowWithMissingOrMalformedSocketIsNotCounted(t *testing.T) {
	tooLongSocket := "/" + strings.Repeat("a", 1001)
	forEachAgentContainer(t, func(t *testing.T, container string) {
		scan := scanLineLaunch("5001", "%1", "1000", "", launchIDOne) +
			scanLineLaunch("5002", "%2", "1000", "relative/sock", launchIDTwo) +
			scanLineLaunch("5003", "%3", "1000", tooLongSocket, launchIDThree) +
			scanLineLaunch("5004", "%4", "1000", malformedFieldMarker, launchIDOne) +
			controlRow

		got, callLog := countLaunchScan(t, container, scan, map[string]orphanTmuxSocket{orphanSocketA: {panes: []string{"%99"}}})

		if got != 1 {
			t.Errorf("count = %d, want 1 (only the control row; launch-ID rows without a usable socket fail open)", got)
		}
		requireOnlySocketQueried(t, callLog, orphanSocketA)
	})
}

// -- ticket #1127: a socket-resolution failure is scoped to its rows ----------
//
// A genuine tmux failure on one socket (a connection error, a stall, output that
// breaks the list-panes format) fails open for the rows that carry that socket
// and is recorded on the scan result; every other socket, container and runtime
// is still resolved and classified. The read-only count still fails closed.

const (
	orphanSocketC = "/tmp/tmux-1000/dead-c"

	// tmuxErrorCause is the stderr the fake tmux's "error" mode writes.
	tmuxErrorCause = "protocol version mismatch"
)

// scanRowByPID returns the classified row for pid.
func scanRowByPID(t *testing.T, scan ContainerOrphanScan, pid string) OrphanScanRow {
	t.Helper()
	for _, r := range scan.Rows {
		if r.PID == pid {
			return r
		}
	}
	t.Fatalf("scan has no row for pid %s; rows: %+v", pid, scan.Rows)
	return OrphanScanRow{}
}

// requireRowClass fails the test unless pid's row has the wanted class.
func requireRowClass(t *testing.T, scan ContainerOrphanScan, pid string, want OrphanRowClass, why string) {
	t.Helper()
	if got := scanRowByPID(t, scan, pid).Class; got != want {
		t.Errorf("pid %s class = %v, want %v (%s)", pid, got, want, why)
	}
}

// socketFailureSockets lists the failed sockets of a scan in recorded order.
func socketFailureSockets(scan ContainerOrphanScan) []string {
	sockets := make([]string, 0, len(scan.SocketFailures))
	for _, f := range scan.SocketFailures {
		sockets = append(sockets, f.Socket)
	}
	return sockets
}

// One container references a failing socket A and a healthy socket B. Rows on A
// are the new fail-open class (a well-formed launch-ID row included, and a
// malformed-launch-ID row too: the socket check comes first), the failure is
// recorded once on the scan, and the rows on B and the no-socket row keep their
// own classes. A's rows must not leak into NoSocketCount.
func TestScanContainer_FailingSocketScopesToItsRowsAndHealthySocketStillClassified(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%1", "1000", orphanSocketA) +
				scanLine("3002", "%2", "2000", orphanSocketB) +
				scanLineLaunch("3003", "%3", "3000", orphanSocketA, launchIDOne) +
				scanLineLaunch("3004", "%4", "4000", orphanSocketA, malformedFieldMarker) +
				scanLine("3005", "%5", "5000", "") +
				scanLineLaunch("3006", "%6", "6000", orphanSocketB, launchIDTwo) +
				scanLine("1", "%1", "50", orphanSocketA),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {mode: "error"},
		orphanSocketB: {panes: panesWithLaunchIDs("%99=" + launchIDOne)},
	})
	var stdout, stderr strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	scan, err := NewOrphanScanner(&stdout, &stderr).ScanContainer(ctx, "docker", orphanDockerContainer)

	if err != nil {
		t.Fatalf("ScanContainer err = %v, want nil: a failing socket is recorded on the scan, not returned", err)
	}
	requireRowClass(t, scan, "3001", OrphanUnresolvedSocket, "legacy row on the failing socket")
	requireRowClass(t, scan, "3003", OrphanUnresolvedSocket, "well-formed launch-ID row on the failing socket is not dead")
	requireRowClass(t, scan, "3004", OrphanUnresolvedSocket, "the socket check precedes the malformed-launch-ID check")
	requireRowClass(t, scan, "3002", OrphanDeadPane, "legacy row on the healthy socket whose pane is absent")
	requireRowClass(t, scan, "3006", OrphanDeadPane, "launch-ID row on the healthy socket whose ID is recorded on no live pane")
	requireRowClass(t, scan, "3005", OrphanNoSocket, "a row with no socket")
	requireRowClass(t, scan, "1", OrphanContainerInit, "PID 1 is never a target")

	if scan.NoSocketCount != 1 {
		t.Errorf("NoSocketCount = %d, want 1 (only the row with no socket; the failing socket's rows are their own class)", scan.NoSocketCount)
	}
	if scan.MalformedLaunchIDCount != 0 {
		t.Errorf("MalformedLaunchIDCount = %d, want 0 (the malformed-ID row sits on the failing socket)", scan.MalformedLaunchIDCount)
	}
	if got := socketFailureSockets(scan); !slices.Equal(got, []string{orphanSocketA}) {
		t.Fatalf("SocketFailures sockets = %v, want exactly [%s] (recorded once, healthy socket B absent)", got, orphanSocketA)
	}
	if !strings.Contains(scan.SocketFailures[0].Err.Error(), tmuxErrorCause) {
		t.Errorf("SocketFailures[0].Err = %v, want it to carry the tmux cause %q", scan.SocketFailures[0].Err, tmuxErrorCause)
	}

	wantLine := fmt.Sprintf("Error: failed to query tmux panes on %q: ", orphanSocketA)
	if got := strings.Count(stderr.String(), wantLine); got != 1 {
		t.Errorf("stderr has %d %q lines, want exactly 1; stderr:\n%s", got, wantLine, stderr.String())
	}
	if !strings.Contains(stderr.String(), tmuxErrorCause) {
		t.Errorf("stderr = %q, want the Error: line to carry the tmux cause %q", stderr.String(), tmuxErrorCause)
	}
}

// A row on a failed socket that carries a well-formed launch ID stays fail-open
// even when that same launch ID is recorded on a dead row elsewhere: the healthy
// socket's row is a dead launch, the failed socket's row is unresolved (its
// liveness is unknown, so it is neither dead nor counted).
func TestScanContainer_FailedSocketRowWithALaunchIDDeadElsewhereStaysUnresolved(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLineLaunch("3001", "%1", "1000", orphanSocketA, launchIDOne) +
				scanLineLaunch("3002", "%2", "2000", orphanSocketB, launchIDOne),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {mode: "error"},
		// A live pane here records a different launch ID, so launchIDOne is dead on B.
		orphanSocketB: {panes: panesWithLaunchIDs("%99=" + launchIDTwo)},
	})
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	scan, err := NewOrphanScanner(io.Discard, io.Discard).ScanContainer(ctx, "docker", orphanDockerContainer)

	if err != nil {
		t.Fatalf("ScanContainer err = %v, want nil", err)
	}
	requireRowClass(t, scan, "3002", OrphanDeadPane, "the launch ID is recorded on no live pane of the healthy socket")
	requireRowClass(t, scan, "3001", OrphanUnresolvedSocket, "the same launch ID on the failed socket must not be judged dead")
}

// A failed socket is dialed at most once per sweep: two containers that both
// reference failing socket A cost exactly one `-S A list-panes`, both scans
// still report the failure and class their A rows fail-open, and the Error: line
// is printed once, not once per container.
func TestScanContainer_FailedSocketIsDialedOnceAcrossContainers(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer, orphanDockerSecond},
		scans: map[string]string{
			// Two rows on A in the first container: one failure entry, not two.
			orphanDockerContainer: scanLine("3001", "%1", "1000", orphanSocketA) + scanLine("3002", "%2", "2000", orphanSocketA),
			orphanDockerSecond:    scanLine("3101", "%1", "1000", orphanSocketA) + scanLine("3102", "%2", "2000", orphanSocketB),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {mode: "error"},
		orphanSocketB: {panes: []string{"%99"}},
	})
	var stderr strings.Builder
	scanner := NewOrphanScanner(io.Discard, &stderr)
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	first, err := scanner.ScanContainer(ctx, "docker", orphanDockerContainer)
	if err != nil {
		t.Fatalf("ScanContainer(first) err = %v, want nil", err)
	}
	second, err := scanner.ScanContainer(ctx, "docker", orphanDockerSecond)
	if err != nil {
		t.Fatalf("ScanContainer(second) err = %v, want nil", err)
	}

	dials := 0
	for _, l := range readOrphanCallLog(t, callLog) {
		if strings.HasPrefix(l, "-S "+orphanSocketA+" list-panes") {
			dials++
		}
	}
	if dials != 1 {
		t.Errorf("failing socket A was dialed %d times, want exactly 1 per sweep", dials)
	}
	for name, scan := range map[string]ContainerOrphanScan{"first": first, "second": second} {
		if got := socketFailureSockets(scan); !slices.Equal(got, []string{orphanSocketA}) {
			t.Errorf("%s container SocketFailures sockets = %v, want [%s] (the memoized failure is still reported per container, once)", name, got, orphanSocketA)
		}
	}
	requireRowClass(t, first, "3001", OrphanUnresolvedSocket, "first container, row on the failed socket")
	requireRowClass(t, first, "3002", OrphanUnresolvedSocket, "first container, second row on the failed socket")
	requireRowClass(t, second, "3101", OrphanUnresolvedSocket, "second container, row on the memoized failed socket")
	requireRowClass(t, second, "3102", OrphanDeadPane, "second container, dead row on the healthy socket")
	if got := strings.Count(stderr.String(), "Error: failed to query tmux panes on"); got != 1 {
		t.Errorf("stderr has %d Error: lines, want 1 (the cache hit must not print again); stderr:\n%s", got, stderr.String())
	}
}

// fakeSweepClock is the injectable clock behind OrphanScanner.now: time is
// driven by the test, never by real sleeping. Every now() call returns a
// strictly later instant than the previous one (the scanner orders "scan
// finished" against "snapshot started" by the clock, so a frozen clock would
// make every snapshot look as fresh as the scan), and advance() adds a jump on
// top of that.
type fakeSweepClock struct{ at time.Time }

func (c *fakeSweepClock) now() time.Time {
	c.at = c.at.Add(time.Millisecond)
	return c.at
}

func (c *fakeSweepClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// newClockedScanner returns a scanner whose clock is a fake that ticks on every
// read.
func newClockedScanner(stderr io.Writer) (*OrphanScanner, *fakeSweepClock) {
	clock := &fakeSweepClock{at: time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC)}
	scanner := NewOrphanScanner(io.Discard, stderr)
	scanner.now = clock.now
	return scanner, clock
}

// countSocketDials counts the `-S <socket> list-panes` calls in the fake tmux
// call log.
func countSocketDials(t *testing.T, callLog, socket string) int {
	t.Helper()
	dials := 0
	for _, l := range readOrphanCallLog(t, callLog) {
		if strings.HasPrefix(l, "-S "+socket+" list-panes") {
			dials++
		}
	}
	return dials
}

// twoContainerSharedSocketFixture serves two containers that both reference
// socket A: the first holds an agent (launchIDOne on %1) and the second a
// later-launched agent (launchIDTwo on %2). Socket A initially lists only the
// first agent's pane, so launchIDTwo is recorded on no live pane yet.
func twoContainerSharedSocketFixture(t *testing.T) (dir, callLog string) {
	t.Helper()
	dir, callLog = orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer, orphanDockerSecond},
		scans: map[string]string{
			orphanDockerContainer: scanLineLaunch("3001", "%1", "1000", orphanSocketA, launchIDOne),
			orphanDockerSecond:    scanLineLaunch("3101", "%2", "2000", orphanSocketA, launchIDTwo),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: panesWithLaunchIDs("%1=" + launchIDOne)},
	})
	return dir, callLog
}

// A successful socket snapshot taken before the current container's scan
// finished must not judge that container's rows, however young it is: the
// launcher records the launch ID on the pane before it execs the agent, so an
// agent launched into the container after the first container's snapshot is
// listed by any list-panes started after that container's scan, yet absent from
// the earlier snapshot, which would class the new, live agent a dead pane and get
// it killed. The clock never jumps here: the rule is scan-relative, not an age.
func TestScanContainer_SnapshotTakenBeforeTheContainersScanIsReResolvedSoANewLaunchIsLive(t *testing.T) {
	dir, callLog := twoContainerSharedSocketFixture(t)
	scanner, _ := newClockedScanner(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	first, err := scanner.ScanContainer(ctx, "docker", orphanDockerContainer)
	if err != nil {
		t.Fatalf("ScanContainer(first) err = %v, want nil", err)
	}
	requireRowClass(t, first, "3001", OrphanLive, "its launch ID is recorded on a live pane of socket A")

	// A new agent is launched on socket A after the first container's snapshot.
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: panesWithLaunchIDs("%1="+launchIDOne, "%2="+launchIDTwo)},
	})

	second, err := scanner.ScanContainer(ctx, "docker", orphanDockerSecond)
	if err != nil {
		t.Fatalf("ScanContainer(second) err = %v, want nil", err)
	}
	requireRowClass(t, second, "3101", OrphanLive, "socket A was re-resolved after the second container's scan, so the launch recorded after the first snapshot is live, never a reap target")
	if got := countSocketDials(t, callLog, orphanSocketA); got != 2 {
		t.Errorf("socket A was dialed %d times, want 2 (the first container's snapshot, then a re-resolution for the second)", got)
	}
}

// Within one container a socket referenced by several rows is dialed once: the
// snapshot resolved after the scan stays reusable for every row of that
// container, so the scan-relative rule costs no extra dial per row.
func TestScanContainer_SocketReferencedByTwoRowsOfOneContainerIsDialedOnce(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			orphanDockerContainer: scanLineLaunch("3001", "%1", "1000", orphanSocketA, launchIDOne) +
				scanLineLaunch("3002", "%2", "2000", orphanSocketA, launchIDTwo),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: panesWithLaunchIDs("%1="+launchIDOne, "%9="+launchIDThree)},
	})
	scanner, _ := newClockedScanner(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	scan, err := scanner.ScanContainer(ctx, "docker", orphanDockerContainer)
	if err != nil {
		t.Fatalf("ScanContainer err = %v, want nil", err)
	}

	requireRowClass(t, scan, "3001", OrphanLive, "launchIDOne is recorded on a live pane of socket A")
	requireRowClass(t, scan, "3002", OrphanDeadPane, "launchIDTwo is recorded on no live pane of socket A")
	if got := countSocketDials(t, callLog, orphanSocketA); got != 1 {
		t.Errorf("socket A was dialed %d times, want exactly 1 for two rows of one container", got)
	}
}

// A failed socket stays memoized for the whole sweep whatever the clock says: the
// scan-relative rule only distrusts an earlier successful snapshot, it never
// re-dials a failure (a stalling socket would otherwise cost its 7s again per
// container).
func TestScanContainer_FailedSocketIsNotReDialedRegardlessOfClock(t *testing.T) {
	dir, callLog := orphanFixture(t)
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer, orphanDockerSecond},
		scans: map[string]string{
			orphanDockerContainer: scanLine("3001", "%1", "1000", orphanSocketA),
			orphanDockerSecond:    scanLine("3101", "%2", "2000", orphanSocketA),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {mode: "error"}})
	var stderr strings.Builder
	scanner, clock := newClockedScanner(&stderr)
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	if _, err := scanner.ScanContainer(ctx, "docker", orphanDockerContainer); err != nil {
		t.Fatalf("ScanContainer(first) err = %v, want nil", err)
	}
	clock.advance(time.Hour)
	second, err := scanner.ScanContainer(ctx, "docker", orphanDockerSecond)
	if err != nil {
		t.Fatalf("ScanContainer(second) err = %v, want nil", err)
	}

	if got := countSocketDials(t, callLog, orphanSocketA); got != 1 {
		t.Errorf("failed socket A was dialed %d times, want exactly 1 per sweep whatever the clock says", got)
	}
	requireRowClass(t, second, "3101", OrphanUnresolvedSocket, "the memoized failure still applies for a later container")
	if got := socketFailureSockets(second); !slices.Equal(got, []string{orphanSocketA}) {
		t.Errorf("second container SocketFailures sockets = %v, want [%s]", got, orphanSocketA)
	}
	if got := strings.Count(stderr.String(), "Error: failed to query tmux panes on"); got != 1 {
		t.Errorf("stderr has %d Error: lines, want 1; stderr:\n%s", got, stderr.String())
	}
}

// A re-resolution for a later container that now fails is that socket's failure
// like any other: its Error: line is printed once, the container records the
// failure and its row fails open instead of being judged against the earlier
// snapshot, and the failure is then memoized, so a third container neither
// re-dials the socket nor re-prints the line but still reports the failure.
func TestScanContainer_ReResolutionThatFailsFailsTheSocketOpenAndIsMemoized(t *testing.T) {
	dir, callLog := twoContainerSharedSocketFixture(t)
	var stderr strings.Builder
	scanner, _ := newClockedScanner(&stderr)
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	if _, err := scanner.ScanContainer(ctx, "docker", orphanDockerContainer); err != nil {
		t.Fatalf("ScanContainer(first) err = %v, want nil", err)
	}
	// Socket A starts failing, and a third container also references it.
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {mode: "error"}})
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer, orphanDockerSecond, orphanDockerThird},
		scans: map[string]string{
			orphanDockerContainer: scanLineLaunch("3001", "%1", "1000", orphanSocketA, launchIDOne),
			orphanDockerSecond:    scanLineLaunch("3101", "%2", "2000", orphanSocketA, launchIDTwo),
			orphanDockerThird:     scanLineLaunch("3201", "%3", "3000", orphanSocketA, launchIDThree),
		},
	})

	second, err := scanner.ScanContainer(ctx, "docker", orphanDockerSecond)
	if err != nil {
		t.Fatalf("ScanContainer(second) err = %v, want nil", err)
	}
	third, err := scanner.ScanContainer(ctx, "docker", orphanDockerThird)
	if err != nil {
		t.Fatalf("ScanContainer(third) err = %v, want nil", err)
	}

	requireRowClass(t, third, "3201", OrphanUnresolvedSocket, "the memoized failure applies to the third container")
	if got := socketFailureSockets(third); !slices.Equal(got, []string{orphanSocketA}) {
		t.Errorf("third container SocketFailures sockets = %v, want [%s] (the memoized failure is reported per container)", got, orphanSocketA)
	}
	requireRowClass(t, second, "3101", OrphanUnresolvedSocket, "the re-resolution failed, so the earlier snapshot must not classify the row")
	if got := socketFailureSockets(second); !slices.Equal(got, []string{orphanSocketA}) {
		t.Fatalf("second container SocketFailures sockets = %v, want [%s]", got, orphanSocketA)
	}
	if !strings.Contains(second.SocketFailures[0].Err.Error(), tmuxErrorCause) {
		t.Errorf("SocketFailures[0].Err = %v, want the tmux cause %q", second.SocketFailures[0].Err, tmuxErrorCause)
	}
	if got := strings.Count(stderr.String(), fmt.Sprintf("Error: failed to query tmux panes on %q: ", orphanSocketA)); got != 1 {
		t.Errorf("stderr has %d Error: lines for A, want 1; stderr:\n%s", got, stderr.String())
	}
	if got := countSocketDials(t, callLog, orphanSocketA); got != 2 {
		t.Errorf("socket A was dialed %d times, want 2 (original plus one refresh)", got)
	}
}

// The read-only count (the dispatch capacity gate) keeps failing closed when any
// socket fails, even though another socket in the same container resolved and
// holds a genuine dead row: an error and no count. With several failing sockets
// the error is the first one seen and stays on one line, because it reaches the
// line-classified dispatch log.
func TestCountOrphanedSessions_MixedSocketFailureFailsClosed(t *testing.T) {
	forEachAgentContainer(t, func(t *testing.T, container string) {
		dir, callLog := orphanFixture(t)
		writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
			name: "docker",
			ps:   []string{container},
			scans: map[string]string{
				container: scanLine("3001", "%1", "1000", orphanSocketB) + // healthy socket, a dead row
					scanLine("3002", "%2", "2000", orphanSocketA) + // first failing socket
					scanLine("3003", "%3", "3000", orphanSocketC), // second failing socket
			},
		})
		writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
			orphanSocketA: {mode: "error"},
			orphanSocketB: {panes: []string{"%99"}},
			orphanSocketC: {mode: "error"},
		})
		var stderr strings.Builder
		ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
		defer cancel()

		got, err := CountOrphanedSessions(ctx, []string{"docker"}, io.Discard, &stderr)

		if err == nil {
			t.Fatalf("CountOrphanedSessions = (%d, nil), want an error: any failing socket fails the count closed", got)
		}
		if got != 0 {
			t.Errorf("count = %d on error, want 0 (no partial count)", got)
		}
		text := err.Error()
		if !strings.Contains(text, tmuxErrorCause) {
			t.Errorf("error %q does not carry the tmux cause %q", text, tmuxErrorCause)
		}
		if !strings.Contains(text, strconv.Quote(orphanSocketA)) {
			t.Errorf("error %q does not name the first failing socket %s", text, orphanSocketA)
		}
		if strings.Contains(text, orphanSocketC) {
			t.Errorf("error %q names the second failing socket %s; want only the first failure", text, orphanSocketC)
		}
		if strings.ContainsAny(text, "\n\r") {
			t.Errorf("error text spans lines: %q", text)
		}
		if !strings.Contains(stderr.String(), fmt.Sprintf("Error: failed to query tmux panes on %q: ", orphanSocketA)) {
			t.Errorf("stderr = %q, want the Error: line for socket A", stderr.String())
		}
	})
}

// A socket that stalls past tmux.RunSocket's own 5s bound (the caller's context
// is still alive) is that socket's failure, and the count's error stays one line
// even though RunSocket joins context.DeadlineExceeded into its error with a
// newline: the dispatch log is line-classified. The error still names the socket
// and the timeout.
func TestCountOrphanedSessions_StalledSocketErrorIsSingleLine(t *testing.T) {
	// The fake's `exec sleep` needs the real sleep binary: keep the ambient PATH
	// behind the fakes dir, which stays first so the fake docker/tmux still win.
	ambientPath := os.Getenv("PATH")
	dir, callLog := orphanFixture(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+ambientPath)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Fatalf("the stalled-socket fixture needs a sleep binary on PATH: %v", err)
	}
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name:  "docker",
		ps:    []string{orphanDockerContainer},
		scans: map[string]string{orphanDockerContainer: scanLine("3001", "%1", "1000", orphanSocketA)},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{orphanSocketA: {mode: "stall"}})
	// The context outlives RunSocket's 5s child bound, so the bound is what ends
	// the stall; the elapsed-time cap only guards a regression that hangs.
	const promptly = 15 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer cancel()

	start := time.Now()
	got, err := CountOrphanedSessions(ctx, []string{"docker"}, io.Discard, io.Discard)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("CountOrphanedSessions = (%d, nil), want an error for the stalled socket", got)
	}
	if elapsed >= promptly {
		t.Errorf("returned after %s, want it bounded by the per-socket 5s limit (the tmux fake sleeps 30s)", elapsed)
	}
	text := err.Error()
	if strings.ContainsAny(text, "\n\r") {
		t.Errorf("error text spans lines: %q", text)
	}
	if !strings.Contains(text, strconv.Quote(orphanSocketA)) {
		t.Errorf("error %q does not name the stalled socket %s", text, orphanSocketA)
	}
	if !strings.Contains(text, context.DeadlineExceeded.Error()) {
		t.Errorf("error %q does not carry the timeout cause %q", text, context.DeadlineExceeded)
	}
}

// A caller context that expires while a socket is being resolved is not that
// socket's fault: the scan aborts with an error wrapping the context error, no
// Error: line is printed for that socket, it is not recorded as a socket failure
// and nothing about it is cached, so a later scan with a live context resolves it
// normally. Failures of sockets resolved earlier in the same container are kept:
// the abort returns them with the error so the caller can still report them.
func TestScanContainer_ExpiredContextAbortsWithoutCachingASocketFailure(t *testing.T) {
	// The fake's `exec sleep` needs the real sleep binary: keep the ambient PATH
	// behind the fakes dir, which stays first so the fake docker/tmux still win.
	ambientPath := os.Getenv("PATH")
	dir, callLog := orphanFixture(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+ambientPath)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Fatalf("the stalled-socket fixture needs a sleep binary on PATH: %v", err)
	}
	writeOrphanFakeRuntime(t, dir, callLog, orphanFakeRuntime{
		name: "docker",
		ps:   []string{orphanDockerContainer},
		scans: map[string]string{
			// B fails first (resolved before the stalled A), so its failure is the
			// partial result the aborted scan returns.
			orphanDockerContainer: scanLine("3002", "%2", "2000", orphanSocketB) + scanLine("3001", "%1", "1000", orphanSocketA),
		},
	})
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {mode: "stall"},
		orphanSocketB: {mode: "error"},
	})
	var stderr strings.Builder
	scanner := NewOrphanScanner(io.Discard, &stderr)

	const deadline = time.Second
	const promptly = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	start := time.Now()
	scan, err := scanner.ScanContainer(ctx, "docker", orphanDockerContainer)
	elapsed := time.Since(start)
	cancel()

	if err == nil {
		t.Fatal("ScanContainer err = nil, want the caller's expired context to abort the scan")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	var scanErr *ScanError
	if errors.As(err, &scanErr) {
		t.Errorf("err = %v is a *ScanError, want the abort to come from resolving the socket, not from the in-container scan", err)
	}
	if elapsed >= promptly {
		t.Errorf("returned after %s, want it bounded near the %s deadline (the tmux fake sleeps 30s)", elapsed, deadline)
	}
	if calls := readOrphanCallLog(t, callLog); !anyLineContains(calls, "-S "+orphanSocketA+" list-panes") {
		t.Errorf("the stalled socket was never queried, so the expiry was not exercised; calls:\n%s", strings.Join(calls, "\n"))
	}
	// The aborted scan returns exactly the failure recorded before the abort (B),
	// and not the expired socket A.
	if got := socketFailureSockets(scan); !slices.Equal(got, []string{orphanSocketB}) {
		t.Errorf("SocketFailures sockets = %v, want exactly [%s]: B failed before the abort, and an expired context is not A's failure", got, orphanSocketB)
	}
	if len(scan.Rows) != 0 {
		t.Errorf("Rows = %+v, want none: an aborted scan classifies nothing", scan.Rows)
	}
	if !strings.Contains(stderr.String(), fmt.Sprintf("Error: failed to query tmux panes on %q: ", orphanSocketB)) {
		t.Errorf("stderr = %q, want B's own Error: line", stderr.String())
	}
	if strings.Contains(stderr.String(), strconv.Quote(orphanSocketA)) {
		t.Errorf("stderr = %q, want no per-socket Error: line for the expired caller context on A", stderr.String())
	}

	// A was not cached: with the socket healthy and a live context the same
	// scanner resolves it and classifies the row from the real pane set. B's
	// failure is memoized and still reported.
	writeOrphanFakeTmux(t, dir, callLog, map[string]orphanTmuxSocket{
		orphanSocketA: {panes: []string{"%99"}},
		orphanSocketB: {mode: "error"},
	})
	retryCtx, retryCancel := context.WithTimeout(context.Background(), orphanTestTimeout)
	defer retryCancel()
	retry, err := scanner.ScanContainer(retryCtx, "docker", orphanDockerContainer)
	if err != nil {
		t.Fatalf("retry ScanContainer err = %v, want nil", err)
	}
	if got := socketFailureSockets(retry); !slices.Equal(got, []string{orphanSocketB}) {
		t.Errorf("retry SocketFailures sockets = %v, want exactly [%s] (B's failure is memoized, A's expired attempt must not be)", got, orphanSocketB)
	}
	requireRowClass(t, retry, "3001", OrphanDeadPane, "after the retry socket A resolves and %1 is absent from its live panes")
	requireRowClass(t, retry, "3002", OrphanUnresolvedSocket, "B's memoized failure still fails its row open")
}
