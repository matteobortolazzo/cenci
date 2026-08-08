package main

// White-box tests for parseBabysitArgs, the pure argument-parsing/validation
// helper extracted from runBabysit (ticket #771). runBabysit itself cannot
// be exercised directly without side effects: a valid invocation calls
// babysit.Run (spawns the supervisor / real agent commands) and an invalid
// one calls os.Exit(2), which would kill the test binary. parseBabysitArgs
// isolates just the parsing/validation logic so both outcomes can be
// asserted without a subprocess or os.Exit.
//
// Ticket #771: `cenci babysit <pr> --agent opencode` is rejected by a stale
// two-agent whitelist even though every other surface (usage string,
// flow/README.md, install-skills.sh, watch/internal/run/template.go)
// advertises opencode support. These tests pin the three valid agents and
// the two usage-error paths so the guard cannot silently regress back to
// claude/codex only.

import (
	"testing"
	"time"
)

// TestParseBabysitArgs_ValidAgents_OK covers the three agents the CLI
// promises support for (usage string, README, install-skills.sh, and
// template.go all advertise claude|codex|opencode): each must parse
// successfully with the given PR carried through unchanged.
func TestParseBabysitArgs_ValidAgents_OK(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			opts, ok := parseBabysitArgs([]string{"123", "--agent", agent})
			if !ok {
				t.Fatalf("parseBabysitArgs(pr=123, agent=%s) ok = false, want true", agent)
			}
			if opts.PR != "123" {
				t.Errorf("PR = %q, want %q", opts.PR, "123")
			}
			if opts.Agent != agent {
				t.Errorf("Agent = %q, want %q", opts.Agent, agent)
			}
		})
	}
}

// TestParseBabysitArgs_UnknownAgent_Rejected covers the closed-set guard:
// an agent outside claude/codex/opencode must be rejected (usage error),
// proving the whitelist doesn't degrade into an open set.
func TestParseBabysitArgs_UnknownAgent_Rejected(t *testing.T) {
	_, ok := parseBabysitArgs([]string{"123", "--agent", "cursor"})
	if ok {
		t.Fatal("parseBabysitArgs(pr=123, agent=cursor) ok = true, want false (cursor is not a supported agent)")
	}
}

// TestParseBabysitArgs_MissingPR_Rejected covers the other usage-error path:
// no positional <pr> argument at all.
func TestParseBabysitArgs_MissingPR_Rejected(t *testing.T) {
	_, ok := parseBabysitArgs([]string{"--agent", "claude"})
	if ok {
		t.Fatal("parseBabysitArgs(no pr) ok = true, want false (missing <pr>)")
	}
}

// TestParseBabysitArgs_ExtraTrailingArg_Rejected locks in the existing
// rejectExtra-style behavior (unexpected trailing positional after flags)
// so the extraction didn't silently drop it.
func TestParseBabysitArgs_ExtraTrailingArg_Rejected(t *testing.T) {
	_, ok := parseBabysitArgs([]string{"123", "--agent", "claude", "unexpected"})
	if ok {
		t.Fatal("parseBabysitArgs(trailing extra arg) ok = true, want false")
	}
}

// TestParseBabysitArgs_OptionsCarryIntervalOnceStateDir is a sanity check
// that the remaining flags still flow through the extraction unchanged.
func TestParseBabysitArgs_OptionsCarryIntervalOnceStateDir(t *testing.T) {
	opts, ok := parseBabysitArgs([]string{"123", "--agent", "claude", "--interval", "5m", "--once", "--state-dir", "/tmp/x"})
	if !ok {
		t.Fatal("parseBabysitArgs ok = false, want true")
	}
	if opts.Interval != 5*time.Minute {
		t.Errorf("Interval = %v, want 5m", opts.Interval)
	}
	if !opts.Once {
		t.Error("Once = false, want true")
	}
	if opts.StateDir != "/tmp/x" {
		t.Errorf("StateDir = %q, want %q", opts.StateDir, "/tmp/x")
	}
}

// TestParseBabysitArgs_SessionAndDirCarryThrough is ticket #975's AC 2/3:
// `cenci babysit <pr> --agent <agent> --session <name> --dir <path>` must
// parse successfully and carry both new flags through into
// babysit.Options.Session/Dir unchanged -- the exact flags parent ticket
// #966 specifies for the daemon-spawn path (#977), landed here.
func TestParseBabysitArgs_SessionAndDirCarryThrough(t *testing.T) {
	opts, ok := parseBabysitArgs([]string{"123", "--agent", "claude", "--session", "work", "--dir", "/repo/root"})
	if !ok {
		t.Fatal("parseBabysitArgs ok = false, want true")
	}
	if opts.Session != "work" {
		t.Errorf("Session = %q, want %q", opts.Session, "work")
	}
	if opts.Dir != "/repo/root" {
		t.Errorf("Dir = %q, want %q", opts.Dir, "/repo/root")
	}
}
