package main

// White-box tests for parseBabysitArgs, the pure argument-parsing/validation
// helper extracted from runBabysit (ticket #771). runBabysit itself cannot
// be exercised directly without side effects: a valid invocation calls
// babysit.Run (spawns the supervisor / real agent commands) and an invalid
// one calls os.Exit(2), which would kill the test binary. parseBabysitArgs
// isolates just the parsing/validation logic so both outcomes can be
// asserted without a subprocess or os.Exit.
//
// Ticket #771: `cenci babysit <pr> --agent opencode` was rejected by a stale
// two-agent whitelist even though every other surface (usage string,
// flow/README.md, install-skills.sh) accepted opencode. These tests pin the
// three names the parser accepts and the two usage-error paths so the guard
// cannot silently regress back to claude/codex only. Parsing only validates
// the name: runBabysit refuses a disabled agent after parsing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/babysit"
	"github.com/matteobortolazzo/cenci/watch/v2/internal/run"
)

// TestParseBabysitArgs_ValidAgents_OK covers the three agent names the
// parser accepts (claude|codex|opencode): each must parse successfully with
// the given PR carried through unchanged. The disabled-agent refusal for
// opencode happens after parsing, in runBabysit.
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

// TestBabysitAgentRefusal pins the gate runBabysit applies after parsing: a
// disabled agent keeps its own disabled text, a non-disabled agent lacking an
// arm-time workflow is refused naming it, and claude/codex pass with the real
// built-in query (#1215).
func TestBabysitAgentRefusal(t *testing.T) {
	t.Run("opencode keeps the disabled text", func(t *testing.T) {
		got := babysitAgentRefusal("opencode", run.BuiltinCapability)
		if !strings.Contains(got, "OpenCode is temporarily disabled") {
			t.Fatalf("babysitAgentRefusal(opencode) = %q, want the OpenCode disabled text", got)
		}
	})

	t.Run("a denying predicate refuses a non-disabled agent naming ci-repair", func(t *testing.T) {
		denyCIRepair := func(agent, workflow string) bool { return workflow != "ci-repair" }
		got := babysitAgentRefusal("claude", denyCIRepair)
		if got == "" || !strings.Contains(got, "ci-repair") {
			t.Fatalf("babysitAgentRefusal = %q, want a reason naming ci-repair", got)
		}
	})

	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent+" passes with the built-in capability query", func(t *testing.T) {
			if got := babysitAgentRefusal(agent, run.BuiltinCapability); got != "" {
				t.Fatalf("babysitAgentRefusal(%q) = %q, want empty", agent, got)
			}
		})
	}
}

// TestBabysitCapabilityRefusal_UsesBuiltinCapabilityQuery pins the real query
// runBabysit hands to babysit.CapabilityRefusal (#1215): claude and codex have
// built-in address-review, babysit-attention and ci-repair; opencode (disabled,
// no workflows) and a custom agent do not. A config.json that defines
// fakeagent's workflows must not change the answer, because unattended arming
// accepts only built-in pairs.
func TestBabysitCapabilityRefusal_UsesBuiltinCapabilityQuery(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run("accepts "+agent, func(t *testing.T) {
			if got := babysit.CapabilityRefusal(agent, run.BuiltinCapability); got != "" {
				t.Fatalf("CapabilityRefusal(%q) = %q, want empty", agent, got)
			}
		})
	}
	for _, agent := range []string{"opencode", "fakeagent"} {
		t.Run("refuses "+agent, func(t *testing.T) {
			if got := babysit.CapabilityRefusal(agent, run.BuiltinCapability); got == "" {
				t.Fatalf("CapabilityRefusal(%q) = empty, want a refusal", agent)
			}
		})
	}

	t.Run("a user-defined fakeagent config is still refused", func(t *testing.T) {
		xdg := t.TempDir()
		if err := os.MkdirAll(filepath.Join(xdg, "cenci"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := `{"agents":{"fakeagent":{"command":"fakeagent","sandboxCommand":"cenci open","workflows":{` +
			`"address-review":{"args":["x"]},"babysit-attention":{"args":["x"]},"ci-repair":{"args":["x"]}}}}}`
		if err := os.WriteFile(filepath.Join(xdg, "cenci", "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", xdg)
		fc, err := run.Load("")
		if err != nil {
			t.Fatal(err)
		}
		for _, wf := range babysit.ArmWorkflows {
			if _, ok := fc.Agents["fakeagent"].Workflows[wf]; !ok {
				t.Fatalf("fixture config does not define fakeagent %s", wf)
			}
		}
		if got := babysit.CapabilityRefusal("fakeagent", run.BuiltinCapability); got == "" {
			t.Fatal("a config.json-defined agent passed the arm-time capability gate")
		}
	})
}
