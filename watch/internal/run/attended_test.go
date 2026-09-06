package run

import (
	"strings"
	"testing"
)

// -- ticket #1087: dispatch-launched sessions pin CENCI_ATTENDED=0 ---------
//
// #1086's fleet gate already stops dispatch from starting a planning session
// while planning.attended is on. This is the defense-in-depth layer for a flag
// toggled mid-flight: a dispatched session must never be able to route into an
// interactive AskUserQuestion inside a detached tmux window, where it would
// wait forever with the ticket stuck on Working. Opts.Unattended pins the
// variable in the spawned window's own environment, which covers BOTH runtimes
// — the sandbox launcher honors the pinned value over the host config
// (assembleExecEnv, watch/internal/sandbox/launcher), and a --no-sandbox
// session's skill reads the same variable directly.

// spawnedCommand runs opts through Run with a stub controller and returns the
// single shell command handed to tmux.
func spawnedCommand(t *testing.T, opts Opts) string {
	t.Helper()
	m := &mockCtrl{session: "work"}
	if err := Run(opts, m); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(m.windows) != 1 {
		t.Fatalf("expected 1 NewWindow, got %d", len(m.windows))
	}
	return m.windows[0].cmd
}

// TestRunUnattendedPinsAttendedZero covers the AC that a session launched by
// `cenci dispatch` carries CENCI_ATTENDED=0.
func TestRunUnattendedPinsAttendedZero(t *testing.T) {
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Unattended = "implement", "40", true

	cmd := spawnedCommand(t, opts)
	if !strings.Contains(cmd, "CENCI_ATTENDED=0 ") {
		t.Errorf("unattended spawn command does not pin CENCI_ATTENDED=0; got: %s", cmd)
	}
	if strings.Contains(cmd, "CENCI_ATTENDED=1") {
		t.Errorf("unattended spawn command must never pin CENCI_ATTENDED=1; got: %s", cmd)
	}
}

// TestRunAttendedPinIsUnattendedOnly pins the negative: the interactive
// `cenci run` path leaves CENCI_ATTENDED entirely unset, preserving the absent
// third state a host run must keep for #1088's resolution order (absent means
// "no launcher resolved this; read the host config").
func TestRunAttendedPinIsUnattendedOnly(t *testing.T) {
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket = "implement", "40"

	cmd := spawnedCommand(t, opts)
	if strings.Contains(cmd, "CENCI_ATTENDED") {
		t.Errorf("interactive spawn command must not set CENCI_ATTENDED at all; got: %s", cmd)
	}
}

// TestRunUnattendedPinFollowsDirPrefix pins the ordering against Opts.Dir: the
// env assignment must sit between the `cd <dir> &&` prefix and the agent argv,
// never before the cd (where it would be a no-op assignment consumed by the
// shell's own `cd` builtin invocation instead of the agent's).
func TestRunUnattendedPinFollowsDirPrefix(t *testing.T) {
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Unattended = "implement", "40", true
	opts.Dir = "/tmp/repo"

	cmd := spawnedCommand(t, opts)
	prefix := "cd /tmp/repo && CENCI_ATTENDED=0 "
	if !strings.HasPrefix(cmd, prefix) {
		t.Errorf("unattended spawn command with Dir set = %q, want prefix %q", cmd, prefix)
	}
}

// TestRunUnattendedPinAppearsInDryRun pins that `--dry-run` previews the same
// command a real spawn would run: a preview that hid the pin would let an
// operator verify the wrong thing.
func TestRunUnattendedPinAppearsInDryRun(t *testing.T) {
	var out strings.Builder
	opts := noConfigOpts(t)
	opts.Workflow, opts.Ticket, opts.Unattended = "implement", "40", true
	opts.DryRun, opts.Out = true, &out

	if err := Run(opts, &mockCtrl{session: "work"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "CENCI_ATTENDED=0 ") {
		t.Errorf("dry-run output does not preview the CENCI_ATTENDED=0 pin; got:\n%s", out.String())
	}
}
