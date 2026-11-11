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
	"errors"
	"os"
	"os/exec"
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
// built-in babysit-attention, babysit-review and ci-repair; opencode (disabled,
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
			`"babysit-attention":{"args":["x"]},"babysit-review":{"args":["x"]},"ci-repair":{"args":["x"]}}}}}`
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

// #1431: `--tmux-socket <path>` names the server the recorded --session lives
// on. A value that fails the tmux.ValidSocket shape check is a usage error (the
// caller prints babysitUsage and exits 2), never a silent fallback.
func TestParseBabysitArgs_TmuxSocketFlag(t *testing.T) {
	opts, ok := parseBabysitArgs([]string{"123", "--agent", "claude", "--session", "work", "--tmux-socket", "/tmp/tmux-1000/cenci"})
	if !ok {
		t.Fatal("parseBabysitArgs with an absolute --tmux-socket ok = false, want true")
	}
	if opts.TmuxSocket != "/tmp/tmux-1000/cenci" || opts.Session != "work" {
		t.Errorf("opts = %+v, want TmuxSocket /tmp/tmux-1000/cenci and Session work", opts)
	}

	for _, bad := range []string{"relative/tmux.sock", "/tmp/tmux-1000/a\tb", "/tmp/tmux-1000/a\nb"} {
		if _, ok := parseBabysitArgs([]string{"123", "--agent", "claude", "--tmux-socket", bad}); ok {
			t.Errorf("parseBabysitArgs(--tmux-socket %q) ok = true, want false (shape check)", bad)
		}
	}

	opts, ok = parseBabysitArgs([]string{"123", "--agent", "claude"})
	if !ok || opts.TmuxSocket != "" {
		t.Errorf("without the flag opts = %+v, ok = %v, want an empty TmuxSocket", opts, ok)
	}
}

// The usage line documents the new flag.
func TestBabysitUsageNamesTmuxSocket(t *testing.T) {
	if !strings.Contains(babysitUsage, "--tmux-socket") {
		t.Errorf("babysitUsage = %q, want it to name --tmux-socket", babysitUsage)
	}
}

// `cenci babysit <pr> --agent claude --tmux-socket <bad>` through a real binary
// exits 2 with exactly the one-line babysit usage on stderr, before any
// supervisor state, gh call or daemon contact: a shape-invalid socket is a usage
// error, never a silent fallback to the ambient server (#1431). This file is
// package main, where main_test.go's shared binaryPath is not visible, so the test
// builds its own binary, as TestVersionStampedViaLdflags does.
func TestBabysitBadTmuxSocketExitsUsageErrorBeforeAnyState(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cenci")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build binary: %v", err)
	}

	for _, tc := range []struct{ name, socket string }{
		{"relative path", "tmux-1000/sock"},
		{"contains a tab", "/tmp/tmux-1000/a\tb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			stateDir := filepath.Join(root, "state")
			ghBin := filepath.Join(root, "bin")
			ghLog := filepath.Join(root, "gh.log")
			if err := os.MkdirAll(ghBin, 0o755); err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + ghLog + "\"\nexit 1\n"
			if err := os.WriteFile(filepath.Join(ghBin, "gh"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			socketDir := filepath.Join(root, "run")

			cmd := exec.Command(bin, "babysit", "123", "--agent", "claude", "--session", "work", "--tmux-socket", tc.socket, "--state-dir", stateDir)
			cmd.Env = []string{
				"PATH=" + ghBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME=" + home,
				"XDG_CONFIG_HOME=" + home,
				"XDG_STATE_HOME=" + home,
				"CENCI_SOCKET_DIR=" + socketDir,
			}
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr

			err := cmd.Run()

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Fatalf("err = %v, want exit code 2; stderr = %q", err, stderr.String())
			}
			if got := stderr.String(); got != babysitUsage+"\n" {
				t.Errorf("stderr = %q, want exactly the one-line babysit usage %q", got, babysitUsage)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			for _, side := range []string{stateDir, socketDir, home} {
				if _, statErr := os.Stat(side); !os.IsNotExist(statErr) {
					t.Errorf("a usage error left %s behind: %v", side, statErr)
				}
			}
			if data, readErr := os.ReadFile(ghLog); readErr == nil {
				t.Errorf("a usage error reached gh: %q", data)
			} else if !os.IsNotExist(readErr) {
				t.Errorf("reading gh log: %v", readErr)
			}
		})
	}
}
