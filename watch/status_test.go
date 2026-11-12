package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/measurement"
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

// --- Native measurements and budget headroom (#1452) ------------------------

const (
	nativeSuffixFresh = "source: claude session.measure, measured 3m ago"
	nativeQuotaLabel  = "    plan quota (native, session-scoped): "
	nativeContextLead = "    context: "

	// nativeDispatchText is what the Dispatch section prints for
	// nativeDispatchState, so a golden output never reads the ambient config.
	nativeDispatchText = "Dispatch loop: enabled\n  daemon:   running\n  interval: 5m\n  pass_running: false\n  last_dispatched: 0\n  last_skipped: 0\n"

	nativeHeadroomLine = "budget headroom (configured estimate): claude 73%  codex 15%"
)

func ptrTo[T any](v T) *T { return &v }

// runNativeStatus serves snap on a throwaway broadcast socket and returns the
// stdout of `cenci status` against it. The run is isolated from the developer's
// real config, state and daemon socket (watch/docs/test-isolation.md): its own
// XDG_CONFIG_HOME, XDG_STATE_HOME and CENCI_SOCKET_DIR, and an event socket
// nobody listens on, so "daemon: not running" is deterministic. The directory is
// shallow (os.MkdirTemp) to stay under the unix socket path limit.
func runNativeStatus(t *testing.T, snap ipc.StateSnapshot) string {
	t.Helper()
	base, err := os.MkdirTemp("", "cenci")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	socketDir := filepath.Join(base, "run")
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}

	socket := filepath.Join(base, "ms.sock")
	srv, err := ipc.NewServer(socket)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Accept(ctx)
	time.Sleep(20 * time.Millisecond)

	snap.Dispatch = &watch.DispatchState{Enabled: true, DaemonRunning: true, Interval: "5m"}
	srv.Broadcast(snap)
	time.Sleep(20 * time.Millisecond)

	cmd := exec.Command(binaryPath, "status",
		"-socket", socket,
		"-event-socket", filepath.Join(base, "nope-events.sock"))
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "config"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"CENCI_SOCKET_DIR="+socketDir,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("status: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// nativeBody is the body shape `cenci notify -measurement` sends: the session,
// the sample time, the constant source, plus the fields a test adds.
func nativeBody(measuredAt time.Time, fields map[string]any) map[string]any {
	body := map[string]any{
		"session_id":  "sess-a",
		"measured_at": isoTime(measuredAt),
		"source":      "session.measure",
	}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

// parseNative runs the real validator over body, as the daemon does, and
// returns the stored snapshot a status reader would see.
func parseNative(t *testing.T, now time.Time, body map[string]any) *watch.NativeMeasurement {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	sample, err := measurement.Parse(raw, now)
	if err != nil {
		t.Fatalf("measurement.Parse: %v", err)
	}
	return &sample.Snapshot
}

func nativeWindow(index, task string, m *watch.NativeMeasurement) watch.WindowState {
	return watch.WindowState{
		Session: "sess-a", WindowIndex: index, WindowName: index + "-implement", TaskName: task,
		Agent: "claude", Status: "running", NativeMeasurement: m,
	}
}

func isoTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// assertContiguousLines requires want to appear as consecutive whole lines.
func assertContiguousLines(t *testing.T, output string, want ...string) {
	t.Helper()
	if !strings.Contains("\n"+output, "\n"+strings.Join(want, "\n")+"\n") {
		t.Errorf("expected these consecutive lines:\n%s\ngot:\n%s", strings.Join(want, "\n"), output)
	}
}

// nativeLines returns every native context and quota line in output.
func nativeLines(output string) []string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, nativeContextLead) || strings.HasPrefix(line, nativeQuotaLabel) {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestStatusSubcommand_NativeMeasurement_RendersContextAndQuotaUnderSession
// covers P1 and P3's "no native line carries headroom": a sample that went
// through measurement.Parse (the body `notify -measurement` sends) renders a
// context line and a plan-quota line directly after the session's model-switch
// lines, each ending with the source and the sample age. The idle window has no
// native sample, so it gets no native lines.
func TestStatusSubcommand_NativeMeasurement_RendersContextAndQuotaUnderSession(t *testing.T) {
	now := time.Now()
	sample := parseNative(t, now, nativeBody(now.Add(-3*time.Minute), map[string]any{
		"context": map[string]any{"tokens": 47000, "window": 200000, "percent": 23.5},
		"rate_limits": []any{
			map[string]any{"kind": "five_hour", "percent_used": 41, "resets_at": isoTime(now.Add(150 * time.Minute))},
			map[string]any{"kind": "seven_day", "percent_used": 12.5, "resets_at": isoTime(now.Add(80 * time.Hour))},
		},
	}))

	withNative := nativeWindow("0", "implement thing", sample)
	withNative.ModelSwitches = []watch.ModelSwitch{
		{FromModel: "claude-sonnet-5-5", ToModel: "claude-opus-5-5", Source: "auto", Fallback: true},
	}
	idle := nativeWindow("1", "plan thing", nil)
	idle.Status = "idle"

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows:  []ipc.WindowState{withNative, idle},
		Summary:  ipc.StatusSummary{Total: 2, Running: 1, Idle: 1},
		Headroom: map[string]float64{"claude": 0.73, "codex": 0.15},
	})

	assertContiguousLines(t, output,
		"  sess-a:0 - implement thing (claude) (running)",
		"    Model switch: claude-sonnet-5-5 → claude-opus-5-5 (main session) — fallback",
		"    context: percent 23.5%, tokens 47000, window 200000 — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): five_hour 41% used, resets in 2h, weekly 12.5% used, resets in 3d — "+nativeSuffixFresh,
		"  sess-a:1 - plan thing (claude) (idle)",
	)
	if got := len(nativeLines(output)); got != 2 {
		t.Errorf("expected exactly 2 native lines (the idle window has no sample), got %d:\n%s", got, output)
	}
	for _, line := range nativeLines(output) {
		if strings.Contains(strings.ToLower(line), "headroom") {
			t.Errorf("a native line carries headroom: %q", line)
		}
	}
	if n := strings.Count(output, "quota"); n != 1 {
		t.Errorf("expected the word quota on the plan-quota line only, found %d occurrences in:\n%s", n, output)
	}
	if !strings.Contains(output, "\n"+nativeHeadroomLine+"\n") {
		t.Errorf("expected the configured-estimate line %q in:\n%s", nativeHeadroomLine, output)
	}
}

// TestStatusSubcommand_BudgetHeadroomLine covers P3's line and its placement:
// one line after the sessions block (also after `sessions: none`) and before the
// Dispatch loop section.
func TestStatusSubcommand_BudgetHeadroomLine(t *testing.T) {
	headroom := map[string]float64{"claude": 0.73, "codex": 0.15}

	t.Run("after the session lines", func(t *testing.T) {
		output := runNativeStatus(t, ipc.StateSnapshot{
			Windows:  []ipc.WindowState{nativeWindow("0", "implement thing", nil)},
			Summary:  ipc.StatusSummary{Total: 1, Running: 1},
			Headroom: headroom,
		})
		want := "daemon: not running\n" +
			"sessions (1):\n" +
			"  sess-a:0 - implement thing (claude) (running)\n" +
			nativeHeadroomLine + "\n" +
			nativeDispatchText
		if output != want {
			t.Errorf("output mismatch.\nwant:\n%s\ngot:\n%s", want, output)
		}
	})

	t.Run("after sessions none", func(t *testing.T) {
		output := runNativeStatus(t, ipc.StateSnapshot{Headroom: headroom})
		want := "daemon: not running\n" +
			"sessions: none\n" +
			nativeHeadroomLine + "\n" +
			nativeDispatchText
		if output != want {
			t.Errorf("output mismatch.\nwant:\n%s\ngot:\n%s", want, output)
		}
	})
}

// TestStatusSubcommand_NoNativeData_OutputIsUnchangedExceptHeadroomLine covers
// P10: with no native sample the output is byte-identical to the pre-change
// rendering, and a snapshot that also carries headroom differs from it by the
// one inserted headroom line and nothing else.
func TestStatusSubcommand_NoNativeData_OutputIsUnchangedExceptHeadroomLine(t *testing.T) {
	windows := []ipc.WindowState{nativeWindow("0", "implement thing", nil)}
	summary := ipc.StatusSummary{Total: 1, Running: 1}

	const golden = "daemon: not running\n" +
		"sessions (1):\n" +
		"  sess-a:0 - implement thing (claude) (running)\n" +
		nativeDispatchText

	without := runNativeStatus(t, ipc.StateSnapshot{Windows: windows, Summary: summary})
	if without != golden {
		t.Errorf("no-native, no-headroom output changed.\nwant:\n%s\ngot:\n%s", golden, without)
	}

	// An empty (non-nil) headroom map is "no headroom" too.
	empty := runNativeStatus(t, ipc.StateSnapshot{Windows: windows, Summary: summary, Headroom: map[string]float64{}})
	if empty != golden {
		t.Errorf("an empty headroom map must not add a line.\nwant:\n%s\ngot:\n%s", golden, empty)
	}

	with := runNativeStatus(t, ipc.StateSnapshot{
		Windows: windows, Summary: summary, Headroom: map[string]float64{"claude": 0.73, "codex": 0.15},
	})
	wantWith := strings.Replace(golden, "Dispatch loop:", nativeHeadroomLine+"\nDispatch loop:", 1)
	if with != wantWith {
		t.Errorf("headroom output differs from the golden by more than the inserted line.\nwant:\n%s\ngot:\n%s", wantWith, with)
	}

	// Positive control: the comparison can fail, because the golden and the
	// headroom output are different strings and a native sample changes the output.
	if with == without {
		t.Errorf("positive control: headroom output equals the no-headroom output")
	}
	now := time.Now()
	sample := parseNative(t, now, nativeBody(now.Add(-3*time.Minute), map[string]any{"rate_limits": []any{}}))
	withSample := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{nativeWindow("0", "implement thing", sample)}, Summary: summary,
	})
	if withSample == golden {
		t.Errorf("positive control: a native sample did not change the output:\n%s", withSample)
	}
}

// TestStatusSubcommand_NativeMeasurement_UnknownStatesNeverRenderAsZero covers
// P5: a missing field reads `unknown (not reported)`, a rejected one reads
// `unknown (invalid)`, in the right place, and no native line shows 0% or 100%.
// The third window has a non-object rate_limits entry ahead of the valid ones:
// the stored Quota skips it, so the path index (rate_limits[1]) is the input
// position and must be mapped back, or the rejected percentage would read as
// not reported.
func TestStatusSubcommand_NativeMeasurement_UnknownStatesNeverRenderAsZero(t *testing.T) {
	now := time.Now()
	measured := now.Add(-3 * time.Minute)
	resets := isoTime(now.Add(150 * time.Minute))

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			// context.percent rejected, tokens absent; rate_limits rejected as a whole.
			nativeWindow("0", "t0", parseNative(t, now, nativeBody(measured, map[string]any{
				"context":     map[string]any{"window": 200000, "percent": -1},
				"rate_limits": "nope",
			}))),
			// a real zero token count; a window whose percent_used is absent.
			nativeWindow("1", "t1", parseNative(t, now, nativeBody(measured, map[string]any{
				"context":     map[string]any{"tokens": 0, "window": 200000},
				"rate_limits": []any{map[string]any{"kind": "five_hour", "resets_at": resets}},
			}))),
			// context rejected as a whole; index shift behind a non-object entry.
			nativeWindow("2", "t2", parseNative(t, now, nativeBody(measured, map[string]any{
				"context": "bad",
				"rate_limits": []any{
					"junk",
					map[string]any{"kind": "five_hour", "percent_used": 150, "resets_at": resets},
					map[string]any{"kind": "seven_day", "percent_used": 35},
				},
			}))),
			// neither context nor rate_limits reported.
			nativeWindow("3", "t3", parseNative(t, now, nativeBody(measured, nil))),
		},
		Summary: ipc.StatusSummary{Total: 4, Running: 4},
	})

	assertContiguousLines(t, output,
		"  sess-a:0 - t0 (claude) (running)",
		"    context: percent unknown (invalid), tokens unknown (not reported), window 200000 — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): unknown (invalid) — "+nativeSuffixFresh,
		"  sess-a:1 - t1 (claude) (running)",
		"    context: percent unknown (not reported), tokens 0, window 200000 — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): five_hour used unknown (not reported), resets in 2h — "+nativeSuffixFresh,
		"  sess-a:2 - t2 (claude) (running)",
		"    context: unknown (invalid) — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): five_hour used unknown (invalid), resets in 2h, weekly 35% used, resets unknown (not reported) — "+nativeSuffixFresh,
		"  sess-a:3 - t3 (claude) (running)",
		"    context: unknown (not reported) — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): unknown (not reported) — "+nativeSuffixFresh,
	)
	lines := nativeLines(output)
	if len(lines) != 8 {
		t.Fatalf("expected 8 native lines, got %d:\n%s", len(lines), output)
	}
	for _, line := range lines {
		if strings.Contains(line, "0%") || strings.Contains(line, "100%") {
			t.Errorf("a missing or invalid field rendered as a percentage: %q", line)
		}
	}
}

// TestStatusSubcommand_NativeMeasurement_EmptyRateLimitsRendersNoneReported
// covers P15: rate_limits [] stores an empty quota and renders `none reported`
// (with the source suffix), while an absent rate_limits stays `unknown (not
// reported)`, so the two are told apart.
func TestStatusSubcommand_NativeMeasurement_EmptyRateLimitsRendersNoneReported(t *testing.T) {
	now := time.Now()
	measured := now.Add(-3 * time.Minute)
	ctxFields := map[string]any{"tokens": 47000, "window": 200000, "percent": 23.5}

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			nativeWindow("0", "empty", parseNative(t, now, nativeBody(measured, map[string]any{
				"context": ctxFields, "rate_limits": []any{},
			}))),
			nativeWindow("1", "absent", parseNative(t, now, nativeBody(measured, map[string]any{
				"context": ctxFields,
			}))),
		},
		Summary: ipc.StatusSummary{Total: 2, Running: 2},
	})

	assertContiguousLines(t, output,
		"  sess-a:0 - empty (claude) (running)",
		"    context: percent 23.5%, tokens 47000, window 200000 — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): none reported — "+nativeSuffixFresh,
		"  sess-a:1 - absent (claude) (running)",
		"    context: percent 23.5%, tokens 47000, window 200000 — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): unknown (not reported) — "+nativeSuffixFresh,
	)
	if n := strings.Count(output, "none reported"); n != 1 {
		t.Errorf("expected `none reported` for the empty list only, found %d in:\n%s", n, output)
	}
}

// TestStatusSubcommand_NativeMeasurement_StaleAndExpiredLabels covers P21: a
// sample older than 15m carries `(stale)` after its age, an expired quota window
// reads `expired (reset passed)` without its percentage, and a fresh sample with
// a current window carries neither.
func TestStatusSubcommand_NativeMeasurement_StaleAndExpiredLabels(t *testing.T) {
	now := time.Now()
	ctxFields := map[string]any{"tokens": 47000, "window": 200000, "percent": 23.5}

	stale := parseNative(t, now, nativeBody(now.Add(-20*time.Minute), map[string]any{
		"context": ctxFields,
		"rate_limits": []any{
			map[string]any{"kind": "five_hour", "percent_used": 77, "resets_at": isoTime(now.Add(-10 * time.Minute))},
			map[string]any{"kind": "seven_day", "percent_used": 12, "resets_at": isoTime(now.Add(80 * time.Hour))},
		},
	}))
	fresh := parseNative(t, now, nativeBody(now.Add(-3*time.Minute), map[string]any{
		"context": ctxFields,
		"rate_limits": []any{
			map[string]any{"kind": "five_hour", "percent_used": 41, "resets_at": isoTime(now.Add(150 * time.Minute))},
		},
	}))

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{nativeWindow("0", "old", stale), nativeWindow("1", "new", fresh)},
		Summary: ipc.StatusSummary{Total: 2, Running: 2},
	})

	const staleSuffix = "source: claude session.measure, measured 20m ago (stale)"
	assertContiguousLines(t, output,
		"  sess-a:0 - old (claude) (running)",
		"    context: percent 23.5%, tokens 47000, window 200000 — "+staleSuffix,
		"    plan quota (native, session-scoped): five_hour expired (reset passed), weekly 12% used, resets in 3d — "+staleSuffix,
		"  sess-a:1 - new (claude) (running)",
		"    context: percent 23.5%, tokens 47000, window 200000 — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): five_hour 41% used, resets in 2h — "+nativeSuffixFresh,
	)
	if n := strings.Count(output, "(stale)"); n != 2 {
		t.Errorf("expected (stale) on the two lines of the old sample only, found %d in:\n%s", n, output)
	}
	for _, line := range nativeLines(output) {
		if strings.Contains(line, "expired (reset passed)") && strings.Contains(line, "77") {
			t.Errorf("an expired window shows its percentage: %q", line)
		}
	}
}

// TestStatusSubcommand_NativeMeasurement_AgeUnderAMinuteRendersSeconds covers
// the `s` unit of the compact age: a sample measured 30s ago reads `measured
// <n>s ago`, rounded down, with no minute unit and no (stale) label. The test
// binary runs after the sample time is fixed, so the rendered seconds are 30
// plus the process start-up time; the pattern bounds that drift.
func TestStatusSubcommand_NativeMeasurement_AgeUnderAMinuteRendersSeconds(t *testing.T) {
	now := time.Now()
	sample := parseNative(t, now, nativeBody(now.Add(-30*time.Second), map[string]any{
		"context": map[string]any{"tokens": 47000, "window": 200000, "percent": 23.5},
	}))

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{nativeWindow("0", "secs", sample)},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})

	lines := nativeLines(output)
	if len(lines) != 2 {
		t.Fatalf("expected 2 native lines, got %d:\n%s", len(lines), output)
	}
	want := regexp.MustCompile(` — source: claude session\.measure, measured 3[0-5]s ago$`)
	for _, line := range lines {
		if !want.MatchString(line) {
			t.Errorf("native line does not end with a seconds age of about 30s: %q", line)
		}
	}
}

// TestStatusSubcommand_NativeMeasurement_FutureMeasuredAtClampsToZeroSeconds
// covers the clamp: the parser accepts a measured_at up to 5m ahead of the
// daemon clock, and the renderer shows such a sample as `measured 0s ago`, never
// a negative age, never (stale).
func TestStatusSubcommand_NativeMeasurement_FutureMeasuredAtClampsToZeroSeconds(t *testing.T) {
	now := time.Now()
	sample := parseNative(t, now, nativeBody(now.Add(2*time.Minute), map[string]any{
		"context": map[string]any{"tokens": 47000, "window": 200000, "percent": 23.5},
	}))

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{nativeWindow("0", "future", sample)},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})

	const futureSuffix = "source: claude session.measure, measured 0s ago"
	assertContiguousLines(t, output,
		"  sess-a:0 - future (claude) (running)",
		"    context: percent 23.5%, tokens 47000, window 200000 — "+futureSuffix,
		"    plan quota (native, session-scoped): unknown (not reported) — "+futureSuffix,
	)
}

// TestStatusSubcommand_NativeMeasurement_FarFutureMeasuredAtIsInvalid pins that
// the renderer re-applies the parser's future cap: a socket writer that skipped
// daemon validation and sent a measured_at past the 5m tolerance is shown as
// `measured unknown (invalid)`, not as a fresh `0s ago` sample, and carries no
// (stale) label. The snapshot is hand-built because the parser rejects it.
func TestStatusSubcommand_NativeMeasurement_FarFutureMeasuredAtIsInvalid(t *testing.T) {
	farFuture := &watch.NativeMeasurement{
		MeasuredAt: isoTime(time.Now().Add(time.Hour)), Source: "session.measure",
		Context: &watch.NativeContext{
			Tokens: ptrTo(int64(100)), Window: ptrTo(int64(200)), Percent: ptrTo(50.0),
		},
		Quota:   []watch.NativeQuotaWindow{},
		Missing: []string{}, Invalid: []string{},
	}

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{nativeWindow("0", "farfuture", farFuture)},
		Summary: ipc.StatusSummary{Total: 1, Running: 1},
	})

	lines := nativeLines(output)
	if len(lines) != 2 {
		t.Fatalf("expected 2 native lines, got %d:\n%s", len(lines), output)
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, " — source: claude session.measure, measured unknown (invalid)") {
			t.Errorf("native line does not end with the invalid-age placeholder: %q", line)
		}
		if strings.Contains(line, "0s ago") || strings.Contains(line, "(stale)") {
			t.Errorf("far-future measured_at rendered as a real age: %q", line)
		}
	}
}

// TestStatusSubcommand_NativeMeasurement_ResanitizesSocketValues pins that the
// renderer does not trust the snapshot: a socket writer that skips the daemon's
// validation cannot put an escape sequence, an out-of-range percentage or a
// forged headroom key on the terminal. Values re-checked here are the kind,
// the source, the percentages, the token count, the reset time, the sample time
// and the headroom keys and values; a stored OverWindow flag is not trusted.
func TestStatusSubcommand_NativeMeasurement_ResanitizesSocketValues(t *testing.T) {
	now := time.Now()
	measured := isoTime(now.Add(-3 * time.Minute))

	hostile := &watch.NativeMeasurement{
		MeasuredAt: measured,
		Source:     "sess\x1b[31mion.evil\x07",
		Context: &watch.NativeContext{
			Tokens: ptrTo(int64(-5)), Window: ptrTo(int64(200000)), Percent: ptrTo(120.0),
		},
		Quota: []watch.NativeQuotaWindow{
			{Kind: "unknown:\x1b[31mevil", PercentUsed: ptrTo(250.0), ResetsAt: isoTime(now.Add(150 * time.Minute))},
			{Kind: "bogus\x1b[1m", PercentUsed: ptrTo(35.0), ResetsAt: "tomorrow\x1b[0m"},
		},
		Missing: []string{}, Invalid: []string{},
	}
	badTime := &watch.NativeMeasurement{
		MeasuredAt: "garbage\x07", Source: "session.measure", Missing: []string{}, Invalid: []string{},
	}
	staleFlag := &watch.NativeMeasurement{
		MeasuredAt: measured, Source: "session.measure",
		Context: &watch.NativeContext{
			Tokens: ptrTo(int64(100)), Window: ptrTo(int64(200)), Percent: ptrTo(50.0), OverWindow: true,
		},
		Quota:   []watch.NativeQuotaWindow{},
		Missing: []string{}, Invalid: []string{},
	}

	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			nativeWindow("0", "hostile", hostile),
			nativeWindow("1", "badtime", badTime),
			nativeWindow("2", "flag", staleFlag),
		},
		Summary:  ipc.StatusSummary{Total: 3, Running: 3},
		Headroom: map[string]float64{"claude": 0.5, "codex": 1.7, "evil\x1b[31m": 0.1},
	})

	if strings.ContainsAny(output, "\x1b\x07") {
		t.Errorf("raw snapshot bytes reached the terminal: %q", output)
	}
	const hostileSuffix = "source: claude session.evil, measured 3m ago"
	assertContiguousLines(t, output,
		"  sess-a:0 - hostile (claude) (running)",
		"    context: percent 120% (over window), tokens unknown (invalid), window 200000 — "+hostileSuffix,
		"    plan quota (native, session-scoped): unknown:evil used unknown (invalid), resets in 2h, unknown:bogus 35% used, resets unknown (invalid) — "+hostileSuffix,
		"  sess-a:1 - badtime (claude) (running)",
		"    context: unknown (not reported) — source: claude session.measure, measured unknown (invalid)",
		"    plan quota (native, session-scoped): unknown (not reported) — source: claude session.measure, measured unknown (invalid)",
		"  sess-a:2 - flag (claude) (running)",
		"    context: percent 50%, tokens 100, window 200 — "+nativeSuffixFresh,
		"    plan quota (native, session-scoped): none reported — "+nativeSuffixFresh,
	)
	if want := "\nbudget headroom (configured estimate): claude 50%  codex unknown (invalid)  evil 10%\n"; !strings.Contains(output, want) {
		t.Errorf("expected the sanitized headroom line %q in:\n%s", want, output)
	}
}

// --- cenci bind hint (#1529) -------------------------------------------------

// bindHintFor is the hint line `cenci status` prints under an unproven Codex
// session.
func bindHintFor(id string) string {
	return "    hint: hooks were delivered but no tmux association is proven; run 'cenci bind " + id + "' in the session's tmux pane"
}

// lineAfter returns the line that follows the first line containing marker.
func lineAfter(t *testing.T, output, marker string) string {
	t.Helper()
	lines := strings.Split(output, "\n")
	for i, l := range lines {
		if strings.Contains(l, marker) && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	t.Fatalf("no line containing %q in:\n%s", marker, output)
	return ""
}

// P6: an unproven Codex session gets one hint line naming its id and the bind
// command, directly under its session line. Bound and Claude sessions get none,
// and a session id that fails the id pattern is never printed.
func TestStatusSubcommand_HumanReadable_UnprovenCodexShowsBindHint(t *testing.T) {
	output := runNativeStatus(t, ipc.StateSnapshot{
		Windows: []ipc.WindowState{
			{Session: "main", WindowIndex: "2", PaneID: "%20", TaskName: "bound work", Status: "running", Agent: "codex", Association: watch.AssociationBound},
			{TaskName: "claude work", WindowName: "claude work", Status: "running", Agent: "claude"},
			{TaskName: "stale thread", WindowName: "stale thread", Status: "running", Agent: "codex", Association: watch.AssociationUnproven, SessionID: "019a2b3c-thread"},
			{TaskName: "forged id", WindowName: "forged id", Status: "done", Agent: "codex", Association: watch.AssociationUnproven, SessionID: "x\x1b[2Jy"},
		},
		Summary: ipc.StatusSummary{Total: 4, Running: 3, Done: 1},
	})

	if got := lineAfter(t, output, "stale thread"); got != bindHintFor("019a2b3c-thread") {
		t.Errorf("line after the unproven session = %q, want %q", got, bindHintFor("019a2b3c-thread"))
	}
	if got := lineAfter(t, output, "forged id"); got != bindHintFor("<session-id>") {
		t.Errorf("line after the forged id = %q, want the placeholder hint", got)
	}
	if strings.ContainsRune(output, '\x1b') {
		t.Errorf("a raw session id reached the terminal: %q", output)
	}
	if n := strings.Count(output, "hint: "); n != 2 {
		t.Errorf("%d hint lines, want one per unproven session:\n%s", n, output)
	}
	for _, marker := range []string{"bound work", "claude work"} {
		if got := lineAfter(t, output, marker); strings.Contains(got, "hint:") {
			t.Errorf("%s got a hint %q, want none", marker, got)
		}
	}
}

// widget-json never shows the association fields: its output for a snapshot
// with them is byte-identical to the same snapshot without them.
func TestWidgetJSON_ShapeUnchangedByAssociationFields(t *testing.T) {
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

	plain := ipc.WindowState{TaskName: "stale thread", WindowName: "stale thread", Status: "running", Agent: "codex"}
	associated := plain
	associated.Association, associated.SessionID = watch.AssociationUnproven, "019a-thread"
	var outputs []string
	for _, w := range []ipc.WindowState{plain, associated} {
		srv.Broadcast(ipc.StateSnapshot{Windows: []ipc.WindowState{w}, Summary: ipc.StatusSummary{Total: 1, Running: 1}})
		time.Sleep(20 * time.Millisecond)
		out, err := exec.Command(binaryPath, "widget-json", "-socket", socket).CombinedOutput()
		if err != nil {
			t.Fatalf("widget-json: %v\n%s", err, out)
		}
		outputs = append(outputs, string(out))
	}
	if outputs[0] != outputs[1] {
		t.Errorf("widget-json changed with the association fields:\nwithout: %s\nwith:    %s", outputs[0], outputs[1])
	}
	if strings.Contains(outputs[1], "association") || strings.Contains(outputs[1], "019a-thread") {
		t.Errorf("widget-json leaked the association fields: %s", outputs[1])
	}
}
