package main_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
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
	if !strings.Contains(string(output), "sess-a:0 - implement thing (running)") {
		t.Errorf("expected session line, got:\n%s", output)
	}
	if !strings.Contains(string(output), "daemon: not running") {
		t.Errorf("expected daemon not running (only the broadcast socket was live), got:\n%s", output)
	}
}
