package tmux

// Ticket #927: (*ExecClient).HasSession must call `tmux has-session -t
// =<name>` (the exact-match form -- without the `=` prefix tmux
// prefix-matches, so a configured "work" would falsely resolve against an
// unrelated "work-2"), classify a nonzero tmux exit as (false, nil) --
// "absent/unusable", never a probe failure -- and classify a failure to run
// tmux at all as (false, non-nil error). These tests pin the argument shape
// and classification at the tmuxCmd boundary, with no real tmux server
// (per the ticket's explicit "no real-tmux integration test" AC): a
// PATH-shimmed fake `tmux` script, mirroring internal/dispatch/gh_test.go's
// installFakeGHOnPath pattern.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFakeTmuxOnPath writes a fake `tmux` shell script to a temp dir and
// prepends it to PATH for the duration of the test.
func installFakeTmuxOnPath(t *testing.T, script string) {
	t.Helper()
	shimDir := t.TempDir()
	full := "#!/bin/sh\n" + script
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(full), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestHasSession_ExactMatchArgumentShape pins the ticket's exact-match
// requirement: HasSession must invoke `tmux has-session -t =<name>`.
func TestHasSession_ExactMatchArgumentShape(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	installFakeTmuxOnPath(t, "echo \"$@\" > "+argsFile+"\nexit 0\n")

	client := &ExecClient{}
	if _, err := client.HasSession("work"); err != nil {
		t.Fatalf("HasSession returned unexpected error: %v", err)
	}

	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading recorded args: %v", err)
	}
	want := "has-session -t =work"
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("tmux invoked with args %q, want %q", strings.TrimSpace(string(got)), want)
	}
}

// TestHasSession_LiveSessionReportsTrue covers the positive classification:
// a zero tmux exit means the session exists.
func TestHasSession_LiveSessionReportsTrue(t *testing.T) {
	installFakeTmuxOnPath(t, "exit 0\n")

	client := &ExecClient{}
	exists, err := client.HasSession("work")
	if err != nil {
		t.Fatalf("HasSession returned unexpected error: %v", err)
	}
	if !exists {
		t.Error("HasSession = false, want true for a zero tmux exit")
	}
}

// TestHasSession_ExitErrorClassifiesAsAbsentNotProbeFailure covers the
// ticket's "a nonzero tmux exit (no such session / no server running)
// classifies as (false, nil)" rule: it is a normal negative result, not a
// probe failure that would carry a verbatim error into the skip log.
func TestHasSession_ExitErrorClassifiesAsAbsentNotProbeFailure(t *testing.T) {
	installFakeTmuxOnPath(t, "exit 1\n")

	client := &ExecClient{}
	exists, err := client.HasSession("work")
	if err != nil {
		t.Fatalf("HasSession err = %v, want nil (a tmux exit error classifies as absent, not a probe failure)", err)
	}
	if exists {
		t.Error("HasSession = true, want false for a nonzero tmux exit")
	}
}

// TestHasSession_RunFailureReturnsError covers the ticket's other
// classification: a failure to run tmux at all (not merely a nonzero exit,
// e.g. the binary isn't resolvable) surfaces as (false, non-nil error) --
// distinct from the exit-error case above, since the underlying probe error
// is worth carrying verbatim into the missing-session skip log.
func TestHasSession_RunFailureReturnsError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no tmux binary resolvable at all

	client := &ExecClient{}
	exists, err := client.HasSession("work")
	if err == nil {
		t.Fatal("HasSession err = nil, want a non-nil error when tmux cannot be run at all")
	}
	if exists {
		t.Error("HasSession = true, want false on a run failure")
	}
}
