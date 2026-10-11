package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// "no server running" stderr, "error" fails with a genuine tmux error.
type orphanTmuxSocket struct {
	mode  string
	panes []string
}

// writeOrphanFakeTmux writes a fake tmux that parses the explicit `-S <path>`
// out of its argv, logs its invocation to callLog, and answers per socket from
// sockets. A socket that is not configured answers "no server running", so an
// unexpected query can never accidentally observe live panes.
func writeOrphanFakeTmux(t *testing.T, dir, callLog string, sockets map[string]orphanTmuxSocket) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`printf '%s\n' "$*" >> ` + exectest.ShellQuote(callLog) + "\n")
	b.WriteString(`socket=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-S" ]; then socket="$arg"; fi
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
  *)
    printf '%s\n' "$panes"
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
