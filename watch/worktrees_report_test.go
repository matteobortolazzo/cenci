package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/worktreeclean"
)

// A removal error or a half-done removal cannot be provoked black-box without
// permission tricks that break under root, so the decision that turns a Removal
// into the exit code is tested directly: it is the one place that maps a Remove
// result to "failed" (exit 1) or not.
func TestReportRemoval_FailureMakesExit1(t *testing.T) {
	boom := errors.New("remove admin dir: boom")
	tests := []struct {
		name       string
		rem        worktreeclean.Removal
		wantFailed bool
		wantOut    string
		wantErrOut string
	}{
		{name: "clean removal", rem: worktreeclean.Removal{Removed: true, Verdict: worktreeclean.VerdictStale, BytesFreed: 2048},
			wantOut: "removed w: stale, freed 2.0 KiB (2048 bytes)"},
		{name: "half-done removal", rem: worktreeclean.Removal{Removed: true, Verdict: worktreeclean.VerdictFinished, BytesFreed: 10, Err: boom},
			wantFailed: true, wantOut: "removed w: finished, freed 10 B (10 bytes)", wantErrOut: "removed but not cleaned up completely: remove admin dir: boom"},
		{name: "removal error", rem: worktreeclean.Removal{Reason: worktreeclean.KeepGitError, Err: boom},
			wantFailed: true, wantErrOut: "not removed: remove admin dir: boom"},
		{name: "changed since classified is a keep", rem: worktreeclean.Removal{Reason: worktreeclean.KeepChanged},
			wantOut: "kept w: changed since classified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer

			failed := reportRemoval(&out, &errOut, "w", tt.rem)

			if failed != tt.wantFailed {
				t.Errorf("failed = %v, want %v", failed, tt.wantFailed)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantOut)
			}
			if !strings.Contains(errOut.String(), tt.wantErrOut) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErrOut)
			}
			if tt.wantErrOut == "" && errOut.Len() != 0 {
				t.Errorf("stderr = %q, want none", errOut.String())
			}
		})
	}
}

// Untrusted text must not move the cursor, restyle the terminal or reorder what
// follows: C0, DEL, C1 and bidi controls all become spaces; ordinary text stays.
func TestOneLine_NeutralizesControlAndBidiCharacters(t *testing.T) {
	in := "a\nb\x1b[31mc\x7fd" + string([]rune{0x9b}) + "e" + string([]rune{0x202e}) + "f" +
		string([]rune{0x2066}) + "g" + string([]rune{0x200f}) + "h ünï"
	const want = "a b [31mc d e f g h ünï"
	if got := oneLine(in); got != want {
		t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
	}
}
