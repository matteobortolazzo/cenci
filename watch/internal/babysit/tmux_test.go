package babysit

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests exercise defaultCurrentTmuxSession/defaultTmuxHasSession
// directly (not the currentTmuxSession/tmuxHasSession seams every other
// package test stubs) via a PATH-shimmed fake `tmux`, mirroring
// gh_test.go/installFakeGH's technique -- the seam-stubbed tests elsewhere in
// this package never invoke the real bounded subprocess/exit-code parsing
// these defaults implement, so this is the only coverage for that behavior.

// installFakeTmux writes a fake `tmux` on a fresh PATH (replacing it
// wholly, matching gh_test.go's installFakeGH -- no shell-pipeline
// dependency exists in these scripts).
func installFakeTmux(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestDefaultTmuxHasSession_ExistingSessionReturnsTrue(t *testing.T) {
	installFakeTmux(t, `
case "$1" in
  has-session) [ "$3" = "=work" ] && exit 0 || exit 1 ;;
esac
`)
	ok, err := defaultTmuxHasSession("work")
	if err != nil {
		t.Fatalf("defaultTmuxHasSession: %v", err)
	}
	if !ok {
		t.Fatal("defaultTmuxHasSession = false, want true for an existing session")
	}
}

// TestDefaultTmuxHasSession_UsesExactMatchForm pins docs/tmux.md's rule: the
// probe must pass the leading `=` exact-match marker, not a bare session
// name (which would prefix-match a same-prefixed sibling).
func TestDefaultTmuxHasSession_UsesExactMatchForm(t *testing.T) {
	installFakeTmux(t, `
case "$1" in
  has-session) [ "$3" = "=work" ] && exit 0 || exit 1 ;;
esac
`)
	// A bare "work-2" target must NOT satisfy the "=work" exact-match check
	// the fake enforces above -- proving defaultTmuxHasSession("work-2")
	// asks for exactly "=work-2", not a prefix match against "work".
	ok, err := defaultTmuxHasSession("work-2")
	if err != nil {
		t.Fatalf("defaultTmuxHasSession: %v", err)
	}
	if ok {
		t.Fatal("defaultTmuxHasSession(\"work-2\") = true, want false: the fake only accepts an exact =work target")
	}
}

func TestDefaultTmuxHasSession_AbsentSessionReturnsFalseNilError(t *testing.T) {
	installFakeTmux(t, "exit 1\n")
	ok, err := defaultTmuxHasSession("gone")
	if err != nil {
		t.Fatalf("defaultTmuxHasSession: err = %v, want nil for an ordinary nonzero exit (no such session)", err)
	}
	if ok {
		t.Fatal("defaultTmuxHasSession = true, want false")
	}
}

// TestDefaultTmuxHasSession_StartFailureReturnsDistinctError pins
// watch/docs/error-handling.md's rule against collapsing "probe errored"
// into "condition false": a failure to even run tmux (binary unresolvable)
// must be a non-nil error, not silently folded into the negative-boolean
// case a nonzero exit produces.
func TestDefaultTmuxHasSession_StartFailureReturnsDistinctError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux binary anywhere on PATH
	ok, err := defaultTmuxHasSession("work")
	if err == nil {
		t.Fatal("defaultTmuxHasSession: err = nil, want a non-nil error when tmux itself cannot be started")
	}
	if ok {
		t.Fatal("defaultTmuxHasSession = true, want false alongside the start-failure error")
	}
}

func TestDefaultCurrentTmuxSession_NoTmuxPaneReturnsErrorWithoutSpawning(t *testing.T) {
	t.Setenv("TMUX_PANE", "")
	// No tmux binary resolvable at all: if defaultCurrentTmuxSession spawned
	// anything despite TMUX_PANE being unset, this would surface as an
	// "executable file not found" error rather than the TMUX_PANE-unset
	// message asserted below.
	t.Setenv("PATH", t.TempDir())
	_, err := defaultCurrentTmuxSession()
	if err == nil {
		t.Fatal("defaultCurrentTmuxSession: err = nil, want an error when TMUX_PANE is unset")
	}
	if !strings.Contains(err.Error(), "TMUX_PANE") {
		t.Fatalf("defaultCurrentTmuxSession err = %q, want it to name TMUX_PANE", err.Error())
	}
}

func TestDefaultCurrentTmuxSession_ResolvesSessionName(t *testing.T) {
	t.Setenv("TMUX_PANE", "%5")
	installFakeTmux(t, `
case "$1" in
  display-message) [ "$3" = "%5" ] && printf 'work' || exit 1 ;;
esac
`)
	session, err := defaultCurrentTmuxSession()
	if err != nil {
		t.Fatalf("defaultCurrentTmuxSession: %v", err)
	}
	if session != "work" {
		t.Fatalf("defaultCurrentTmuxSession = %q, want %q", session, "work")
	}
}

// TestDefaultCurrentTmuxSession_EmptyOutputIsError pins docs/tmux.md's
// documented trap: `tmux display-message -p` exits 0 with empty stdout when
// -t names a pane that no longer exists, rather than erroring like most
// other tmux subcommands do.
func TestDefaultCurrentTmuxSession_EmptyOutputIsError(t *testing.T) {
	t.Setenv("TMUX_PANE", "%5")
	installFakeTmux(t, "exit 0\n") // prints nothing, exits 0
	_, err := defaultCurrentTmuxSession()
	if err == nil {
		t.Fatal("defaultCurrentTmuxSession: err = nil, want an explicit error for empty display-message output")
	}
}

// TestDefaultCurrentTmuxSession_StartFailureReturnsError pins the other
// error path: tmux itself cannot be resolved/run.
func TestDefaultCurrentTmuxSession_StartFailureReturnsError(t *testing.T) {
	t.Setenv("TMUX_PANE", "%5")
	t.Setenv("PATH", t.TempDir())
	_, err := defaultCurrentTmuxSession()
	if err == nil {
		t.Fatal("defaultCurrentTmuxSession: err = nil, want a non-nil error when tmux cannot be started")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("defaultCurrentTmuxSession err = %v, want a start failure, not an *exec.ExitError", err)
	}
}

func TestDefaultMergeRepairActive(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantActive   bool
		wantErr      bool
	}{
		{"live exact worker", "42-merge-repair\t0\n", true, false},
		{"retained dead worker", "42-merge-repair\t1\n", false, false},
		{"missing worker", "other\t0\n", false, false},
		{"prefix sibling ignored", "42-merge-repair-old\t0\n", false, false},
		{"another PR ignored", "142-merge-repair\t0\n", false, false},
		{"any live pane means active", "42-merge-repair\t1\n42-merge-repair\t0\n", true, false},
		{"malformed pane status", "42-merge-repair\tunknown\n", false, true},
		{"missing pane status", "42-merge-repair\n", false, true},
		{"malformed row after live worker", "42-merge-repair\t0\nother\tunknown\n", false, true},
		{"empty output cannot prove absent", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeTmux(t, `
[ "$1" = list-panes ] && [ "$2" = -s ] && [ "$3" = -t ] && [ "$4" = =work ] || exit 1
[ "$5" = -F ] && [ "$6" = "$FAKE_FORMAT" ] || exit 1
printf '%s' "$FAKE_PANES"
`)
			t.Setenv("FAKE_PANES", tc.output)
			t.Setenv("FAKE_FORMAT", "#{window_name}\t#{pane_dead}")
			active, err := defaultMergeRepairActive("work", "42")
			if active != tc.wantActive || (err != nil) != tc.wantErr {
				t.Fatalf("active=%v err=%v, want active=%v err=%v", active, err, tc.wantActive, tc.wantErr)
			}
		})
	}
}

func TestDefaultMergeRepairActiveProbeFailure(t *testing.T) {
	installFakeTmux(t, "printf 'server unavailable' >&2\nexit 1\n")
	if active, err := defaultMergeRepairActive("work", "42"); active || err == nil || !strings.Contains(err.Error(), "server unavailable") {
		t.Fatalf("probe failure active=%v err=%v", active, err)
	}
}

func TestDefaultMergeRepairActiveMissingBinaryIsProbeFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if active, err := defaultMergeRepairActive("work", "42"); active || err == nil {
		t.Fatalf("missing binary active=%v err=%v", active, err)
	}
}

func TestDefaultMergeRepairActiveTruncationCannotProveWorkerAbsent(t *testing.T) {
	// A truncated inventory may omit the worker even if the buffered prefix
	// ends on a complete record. Reject it before interpreting any rows.
	output := strings.Repeat("other\t0\n", ghOutputCap/8+1)
	installFakeTmux(t, "printf '%s' '"+output+"'\n")
	if active, err := defaultMergeRepairActive("work", "42"); active || err == nil || !strings.Contains(err.Error(), "bounded cap") {
		t.Fatalf("truncated inventory active=%v err=%v", active, err)
	}
}
