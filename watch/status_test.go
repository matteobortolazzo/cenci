package main_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/pkg/watch"
)

// TestWidgetJSONAndWaybarSubcommandsBothRoute covers the widget-json split
// (#daemon-lifecycle PR): "widget-json" (the renamed hidden plumbing
// subcommand) and its pre-existing hidden alias "waybar" must both route to
// the same Waybar-JSON status frontend that plain `status` used to. With no
// daemon on the socket they exit 1 with no output — never the "unknown
// subcommand" error, and never route to the new human-readable `status`.
func TestWidgetJSONAndWaybarSubcommandsBothRoute(t *testing.T) {
	for _, sub := range []string{"widget-json", "waybar"} {
		cmd := exec.Command(binaryPath, sub, "-socket", filepath.Join(t.TempDir(), "nope.sock"))
		output, err := cmd.CombinedOutput()

		if strings.Contains(string(output), "unknown subcommand") {
			t.Errorf("%s: expected routing to the widget-json frontend, got:\n%s", sub, output)
		}
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: expected exit error (daemon not running), got %T: %v", sub, err, err)
		}
		if exitErr.ExitCode() != 1 {
			t.Errorf("%s: expected exit code 1 when daemon not running, got %d", sub, exitErr.ExitCode())
		}
	}
}

// TestWidgetJSONAndWaybarOutputByteIdentical drives a real broadcast snapshot
// through both "widget-json" and its "waybar" alias and asserts byte-for-byte
// identical output — the split (#daemon-lifecycle PR) must not change a
// single byte of what widget frontends parse as JSON.
func TestWidgetJSONAndWaybarOutputByteIdentical(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	snap := ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	}

	outputs := make(map[string]string, 2)
	for _, sub := range []string{"widget-json", "waybar"} {
		srv.Broadcast(snap)
		time.Sleep(20 * time.Millisecond)
		cmd := exec.Command(binaryPath, sub, "-socket", socket)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", sub, err, out)
		}
		outputs[sub] = string(out)
	}

	if outputs["widget-json"] != outputs["waybar"] {
		t.Errorf("widget-json and waybar output differ:\nwidget-json: %q\nwaybar:      %q", outputs["widget-json"], outputs["waybar"])
	}
	if !strings.Contains(outputs["widget-json"], `"text"`) || !strings.Contains(outputs["widget-json"], "77-implement") {
		t.Errorf("expected Waybar JSON with the session tooltip, got: %q", outputs["widget-json"])
	}
}

// TestWidgetJSON_EscalatedDefaultSymbolAndFlagOverride (#826) covers
// status_cmd.go's wiring of the new counter into widget-json's rendered
// text: the default "?" symbol renders for a broadcast snapshot carrying
// Summary.Escalated, and -symbol-escalated overrides it.
func TestWidgetJSON_EscalatedDefaultSymbolAndFlagOverride(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "esc.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	snap := ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{WindowName: "42-implement", Status: "escalated"},
		},
		Summary: ipc.StatusSummary{Total: 1, Escalated: 1},
	}

	srv.Broadcast(snap)
	time.Sleep(20 * time.Millisecond)
	defaultOut, err := exec.Command(binaryPath, "widget-json", "-socket", socket).CombinedOutput()
	if err != nil {
		t.Fatalf("widget-json (default symbol): %v\n%s", err, defaultOut)
	}
	if !strings.Contains(string(defaultOut), `"text":"? 1"`) {
		t.Errorf("widget-json default output = %q, want it to contain the default escalated symbol/count %q", defaultOut, `"text":"? 1"`)
	}

	srv.Broadcast(snap)
	time.Sleep(20 * time.Millisecond)
	overrideOut, err := exec.Command(binaryPath, "widget-json", "-socket", socket, "-symbol-escalated", "@").CombinedOutput()
	if err != nil {
		t.Fatalf("widget-json (-symbol-escalated override): %v\n%s", err, overrideOut)
	}
	if !strings.Contains(string(overrideOut), `"text":"@ 1"`) {
		t.Errorf("widget-json overridden output = %q, want it to contain the overridden escalated symbol/count %q", overrideOut, `"text":"@ 1"`)
	}
}

// TestStatusSubcommand_HumanReadable_DegradesGracefullyWithNoDaemon covers
// the new human-readable `cenci status` (distinct from `widget-json`):
// with nothing listening on either socket it must still print a report and
// exit 0 — never the "unknown subcommand" error, and never a non-zero exit
// (unlike `daemon status`, which exits 1 when not running).
func TestStatusSubcommand_HumanReadable_DegradesGracefullyWithNoDaemon(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(binaryPath, "status",
		"-socket", filepath.Join(dir, "nope.sock"),
		"-event-socket", filepath.Join(dir, "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: expected exit 0 even with no daemon, got %v\n%s", err, output)
	}
	if strings.Contains(string(output), "unknown subcommand") {
		t.Errorf("expected routing to the human status overview, got:\n%s", output)
	}
	if !strings.Contains(string(output), "daemon: not running") {
		t.Errorf("expected 'daemon: not running', got:\n%s", output)
	}
	if !strings.Contains(string(output), "sessions: unavailable") {
		t.Errorf("expected sessions to be reported unavailable, got:\n%s", output)
	}
}

// TestStatusSubcommand_HumanReadable_CorruptSnapshotShowsRealError covers
// #412: a daemon that accepts the connection (Dial succeeds — the daemon IS
// reachable) but then sends a malformed/truncated NDJSON line must not be
// reported as "daemon not reachable". The real decode error must surface via
// "sessions: unavailable (error reading snapshot: <err>)" so a corrupt
// snapshot is distinguishable from the daemon simply not running.
func TestStatusSubcommand_HumanReadable_CorruptSnapshotShowsRealError(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "corrupt.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// renderHumanStatus dials the socket twice (once for the session snapshot,
	// once more inside ResolveDispatchState for the embedded dispatch-loop
	// state), so the fake daemon must keep accepting connections until the
	// listener is closed, not just serve one.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				// Malformed NDJSON: not valid JSON, and never newline-terminated
				// before the connection closes — a corrupt/truncated line.
				_, _ = conn.Write([]byte("{this is not valid json"))
			}()
		}
	}()

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(dir, "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: expected exit 0 even with a corrupt snapshot, got %v\n%s", err, output)
	}
	if strings.Contains(string(output), "daemon not reachable") {
		t.Errorf("a corrupt snapshot after a successful dial must not say 'daemon not reachable', got:\n%s", output)
	}
	if !strings.Contains(string(output), "sessions: unavailable (error reading snapshot:") {
		t.Errorf("expected 'sessions: unavailable (error reading snapshot: ...)' with the real decode error, got:\n%s", output)
	}
}

// TestStatusSubcommand_HumanReadable_PermissionDeniedSocketShowsRealError
// covers #412's permission-denied case: a socket file that exists (so the
// daemon may well be running) but is unreadable/unwritable by the current
// user must not be reported identically to "daemon not reachable" — the real
// permission error must surface via "sessions: unavailable (error reading
// snapshot: <err>)". Simulated via Unix socket-file permission bits (0000),
// which reliably yields EACCES on connect for a non-root caller; skipped
// when running as root since root bypasses Unix file permission checks
// entirely, making the simulation unreliable in that environment.
func TestStatusSubcommand_HumanReadable_PermissionDeniedSocketShowsRealError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses Unix socket permission checks; cannot simulate permission-denied")
	}

	dir := t.TempDir()
	socket := filepath.Join(dir, "denied.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if err := os.Chmod(socket, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(dir, "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: expected exit 0 even with a permission-denied socket, got %v\n%s", err, output)
	}
	if strings.Contains(string(output), "daemon not reachable") {
		t.Errorf("a permission-denied socket must not say 'daemon not reachable', got:\n%s", output)
	}
	if !strings.Contains(string(output), "sessions: unavailable (error reading snapshot:") {
		t.Errorf("expected 'sessions: unavailable (error reading snapshot: ...)' with the real permission error, got:\n%s", output)
	}
}

// TestStatusSubcommand_HumanReadable_DispatchSectionShowsResolveErrorOnCorruptSnapshot
// covers #446: the same corrupt-snapshot scenario as
// TestStatusSubcommand_HumanReadable_CorruptSnapshotShowsRealError above, but
// for the embedded dispatch-loop section of `cenci status` (rendered via
// renderDispatchState, the same renderer `dispatch loop status` uses). A
// daemon that accepts the connection (Dial succeeds -- the daemon IS
// reachable) but then sends a malformed/truncated NDJSON line must not leave
// the dispatch section looking identical to "no daemon at all" -- the real
// ReadSnapshot error must surface as a "resolve_error:" line.
func TestStatusSubcommand_HumanReadable_DispatchSectionShowsResolveErrorOnCorruptSnapshot(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "corrupt.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// renderHumanStatus dials the socket twice (once for the session
	// snapshot, once more inside ResolveDispatchState for the embedded
	// dispatch-loop state), so the fake daemon must keep accepting
	// connections until the listener is closed, not just serve one.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				// Malformed NDJSON: not valid JSON, and never newline-terminated
				// before the connection closes -- a corrupt/truncated line.
				_, _ = conn.Write([]byte("{this is not valid json"))
			}()
		}
	}()

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(dir, "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: expected exit 0 even with a corrupt snapshot, got %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "resolve_error:") {
		t.Errorf("expected the dispatch section to show a %q line with the real decode error, got:\n%s", "resolve_error:", output)
	}
}

// TestStatusSubcommand_HumanReadable_ListsLiveSessions drives a real
// broadcast snapshot (same pattern as TestClose_DryRun_PrintsCloseAndSkipDecisions)
// through the human `status` overview and asserts the session line renders.
func TestStatusSubcommand_HumanReadable_ListsLiveSessions(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", TaskName: "implement thing", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "sess-a:0 - implement thing (unknown) (running)") {
		t.Errorf("expected session line, got:\n%s", output)
	}
	if !strings.Contains(string(output), "daemon: not running") {
		t.Errorf("expected daemon not running (only the broadcast socket was live), got:\n%s", output)
	}
}

// --- Unified session line format (#405) ------------------------------------

// TestStatusSubcommand_HumanReadable_SessionlessBlankShowsPlaceholders covers
// the exact bug that motivated #405: a sessionless (paneless) instance with
// no known task name and no known agent yet must never render a blank
// title — it must show the unified "(no session) - (untitled) (unknown)
// (status)" shape via renderHumanStatus(), the same as status.Format()'s
// tooltip.
func TestStatusSubcommand_HumanReadable_SessionlessBlankShowsPlaceholders(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "(no session) - (untitled) (unknown) (running)") {
		t.Errorf("expected placeholder session line, got:\n%s", output)
	}
}

// TestStatusSubcommand_HumanReadable_SessionlessKnownAgentNoTaskName covers a
// sessionless instance with a known agent but no task name yet: the name
// placeholder must still show, alongside the real agent.
func TestStatusSubcommand_HumanReadable_SessionlessKnownAgentNoTaskName(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Status: "idle", Agent: "claude"},
		},
		Summary: ipc.StatusSummary{Total: 1, Idle: 1},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "(no session) - (untitled) (claude) (idle)") {
		t.Errorf("expected session line with known agent and untitled name, got:\n%s", output)
	}
}

// TestStatusSubcommand_HumanReadable_TmuxAllFieldsKnown covers a normal,
// fully-known tmux-backed session: session:index prefix, name, agent, and
// status must all be present in the unified order.
func TestStatusSubcommand_HumanReadable_TmuxAllFieldsKnown(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "feature-x", WindowIndex: "1", TaskName: "fix-login", Agent: "claude", Status: "running"},
		},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "feature-x:1 - fix-login (claude) (running)") {
		t.Errorf("expected fully-known tmux session line, got:\n%s", output)
	}
}

// TestFormatEquivalence_WidgetJSONTooltipMatchesHumanStatusLine drives the
// same WindowState through both "widget-json" (status.Format's tooltip) and
// "status" (renderHumanStatus's human output) and asserts they render the
// same unified per-session line shape: "session:index - name (agent)
// (status)". #405's fix must land in both render sites identically, not
// just one, so this line shape can't silently drift out of sync again.
func TestFormatEquivalence_WidgetJSONTooltipMatchesHumanStatusLine(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "aw.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	snap := ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "sess-b", WindowIndex: "2", TaskName: "launch feature", Agent: "codex", Status: "need-input"},
		},
		Summary: ipc.StatusSummary{Total: 1, NeedInput: 1},
	}
	wantLine := "sess-b:2 - launch feature (codex) (need-input)"

	srv.Broadcast(snap)
	time.Sleep(20 * time.Millisecond)
	widgetOut, err := exec.Command(binaryPath, "widget-json", "-socket", socket).CombinedOutput()
	if err != nil {
		t.Fatalf("widget-json: %v\n%s", err, widgetOut)
	}
	var decoded struct {
		Tooltip string `json:"tooltip"`
	}
	if err := json.Unmarshal(widgetOut, &decoded); err != nil {
		t.Fatalf("json.Unmarshal widget-json output: %v\nraw: %s", err, widgetOut)
	}
	if !strings.Contains(decoded.Tooltip, wantLine) {
		t.Errorf("widget-json tooltip: expected %q in %q", wantLine, decoded.Tooltip)
	}

	srv.Broadcast(snap)
	time.Sleep(20 * time.Millisecond)
	statusOut, err := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock")).CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, statusOut)
	}
	if !strings.Contains(string(statusOut), wantLine) {
		t.Errorf("status: expected %q in %q", wantLine, statusOut)
	}
}

// --- Model switches (#1179) -------------------------------------------------

// broadcastModelSwitchSnapshot serves one snapshot whose first window carries
// two model switches and an overflow count, and returns the socket path.
func broadcastModelSwitchSnapshot(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "ms.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{
				Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", TaskName: "implement thing", Agent: "claude", Status: "running",
				ModelSwitches: []watch.ModelSwitch{
					{FromModel: "claude-sonnet-5-5", ToModel: "claude-opus-5-5", Source: "auto", Fallback: true},
					{FromModel: "claude-opus-5-5", ToModel: "claude-sonnet-5-5", Source: "sdk", Subagent: true, AgentType: "cenci:reviewer"},
				},
				ModelSwitchesMore: 4,
			},
			{Session: "sess-a", WindowIndex: "1", WindowName: "78-plan", TaskName: "plan thing", Agent: "claude", Status: "idle"},
		},
		Summary: ipc.StatusSummary{Total: 2, Running: 1, Idle: 1},
	})
	time.Sleep(20 * time.Millisecond)
	return socket
}

// TestStatusSubcommand_HumanReadable_ShowsModelSwitchesUnderTheSession asserts
// AC6's human half: the `<previous> → <new>` lines (with attribution, the
// fallback label only on the automatic switch, and the `+<n> more` overflow)
// are printed indented under the session that has them, and only there.
func TestStatusSubcommand_HumanReadable_ShowsModelSwitchesUnderTheSession(t *testing.T) {
	socket := broadcastModelSwitchSnapshot(t)

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}

	want := []string{
		"  sess-a:0 - implement thing (claude) (running)",
		"    Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session) — fallback",
		"    Model switch: claude-opus-5-5 → claude-sonnet-5-5 (subagent cenci:reviewer)",
		"    Model switch: +4 more",
		"  sess-a:1 - plan thing (claude) (idle)",
	}
	lines := strings.Split(string(output), "\n")
	next := 0
	for _, line := range lines {
		if next < len(want) && line == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Errorf("expected these lines in order, matched %d of %d (first missing: %q); got:\n%s", next, len(want), want[next], output)
	}
	if strings.Count(string(output), "Model switch:") != 3 {
		t.Errorf("expected exactly 3 model switch lines (the idle session has none), got:\n%s", output)
	}
}

// TestStatusSubcommand_HumanReadable_ResanitizesModelSwitchesAtRender pins that
// the human renderer does not trust the snapshot: a switch whose model or agent
// type carries an escape sequence renders the placeholder, never the raw bytes.
func TestStatusSubcommand_HumanReadable_ResanitizesModelSwitchesAtRender(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "ms.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	srv.Broadcast(ipc.StateSnapshot{
		Windows: []ipc.WindowState{{
			Session: "sess-a", WindowIndex: "0", WindowName: "77-implement", TaskName: "implement thing", Agent: "claude", Status: "running",
			ModelSwitches: []watch.ModelSwitch{
				{FromModel: "opus\x1b[31mevil", ToModel: "claude-opus-5-5", Source: "auto", Fallback: true},
				{FromModel: "claude-opus-5-5", ToModel: "claude-sonnet-5-5", Subagent: true, AgentType: "bad\x1b]0;title\x07type"},
			},
		}},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})
	time.Sleep(20 * time.Millisecond)

	output, err := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(t.TempDir(), "nope-events.sock")).CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, output)
	}
	if strings.ContainsAny(string(output), "\x1b\x07") || strings.Contains(string(output), "evil") || strings.Contains(string(output), "title") {
		t.Errorf("raw snapshot bytes reached the terminal: %q", output)
	}
	for _, want := range []string{
		"    Model switch: (invalid model id) → claude-opus-5-5 (main session) — fallback",
		"    Model switch: claude-opus-5-5 → claude-sonnet-5-5 (subagent unknown)",
	} {
		if !strings.Contains(string(output), want+"\n") {
			t.Errorf("missing line %q in:\n%s", want, output)
		}
	}
}

// TestWidgetJSON_ExposesModelSwitchesField asserts AC6's widget half through
// the real binary: widget-json carries the top-level model_switches array.
func TestWidgetJSON_ExposesModelSwitchesField(t *testing.T) {
	socket := broadcastModelSwitchSnapshot(t)

	out, err := exec.Command(binaryPath, "widget-json", "-socket", socket).CombinedOutput()
	if err != nil {
		t.Fatalf("widget-json: %v\n%s", err, out)
	}
	var decoded struct {
		ModelSwitches []struct {
			Session     string `json:"session"`
			WindowIndex string `json:"window_index"`
			WindowName  string `json:"window_name"`
			More        int    `json:"more"`
			Switches    []struct {
				FromModel string `json:"from_model"`
				ToModel   string `json:"to_model"`
				Source    string `json:"source"`
				Subagent  bool   `json:"subagent"`
				AgentType string `json:"agent_type"`
				Fallback  bool   `json:"fallback"`
			} `json:"switches"`
		} `json:"model_switches"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode widget-json output: %v\n%s", err, out)
	}
	if len(decoded.ModelSwitches) != 1 {
		t.Fatalf("expected 1 model_switches entry, got %d: %s", len(decoded.ModelSwitches), out)
	}
	entry := decoded.ModelSwitches[0]
	if entry.Session != "sess-a" || entry.WindowIndex != "0" || entry.WindowName != "77-implement" || entry.More != 4 || len(entry.Switches) != 2 {
		t.Errorf("entry = %+v", entry)
	}
	if first := entry.Switches[0]; first.FromModel != "claude-sonnet-5-5" || first.ToModel != "claude-opus-5-5" || !first.Fallback {
		t.Errorf("first switch = %+v", first)
	}
	if second := entry.Switches[1]; !second.Subagent || second.AgentType != "cenci:reviewer" {
		t.Errorf("second switch = %+v", second)
	}
}
