package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuiltinClaudeTemplatesResolve(t *testing.T) {
	cfg := builtinConfig()
	for _, wf := range []string{"refine", "design", "implement"} {
		argv, err := cfg.BuildCommand("claude", wf, "40", "", false)
		if err != nil {
			t.Fatalf("BuildCommand(claude, %s): %v", wf, err)
		}
		want := []string{"claude", "--", "/cenci:" + wf + " 40"}
		if !equalArgs(argv, want) {
			t.Errorf("BuildCommand(claude, %s) = %v, want %v", wf, argv, want)
		}
	}
}

func TestSandboxSwapsClaudeToCenciOpen(t *testing.T) {
	cfg := builtinConfig()
	argv, err := cfg.BuildCommand("claude", "implement", "40", "", true)
	if err != nil {
		t.Fatal(err)
	}
	// The multi-token sandbox command must arrive as separate argv entries,
	// never as one shell-quoted word.
	want := []string{"cenci", "open", "--", "/cenci:implement 40"}
	if !equalArgs(argv, want) {
		t.Errorf("sandbox argv = %v, want %v", argv, want)
	}
}

func TestSandboxCommandWithModelInjectsAfterCommandTokens(t *testing.T) {
	cfg := builtinConfig()
	argv, err := cfg.BuildCommand("claude", "implement", "42", "sonnet", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cenci", "open", "--model", "sonnet", "--", "/cenci:implement 42"}
	if !equalArgs(argv, want) {
		t.Errorf("sandbox argv = %v, want %v", argv, want)
	}
}

func TestModelAppendedWhenNoPlaceholder(t *testing.T) {
	cfg := builtinConfig()
	argv, err := cfg.BuildCommand("claude", "implement", "40", "opus", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--model", "opus", "--", "/cenci:implement 40"}
	if !equalArgs(argv, want) {
		t.Errorf("model append = %v, want %v", argv, want)
	}
}

func TestModelPlaceholderSubstituted(t *testing.T) {
	cfg := builtinConfig()
	cfg.Agents["claude"] = AgentConfig{
		Command: "claude",
		Workflows: map[string]WorkflowTemplate{
			"implement": {Args: []string{"--model", "{model}", "--", "/x {ticket}"}},
		},
	}
	argv, err := cfg.BuildCommand("claude", "implement", "42", "sonnet", false)
	if err != nil {
		t.Fatal(err)
	}
	// Placeholder is filled in place; model is NOT also appended.
	want := []string{"claude", "--model", "sonnet", "--", "/x 42"}
	if !equalArgs(argv, want) {
		t.Errorf("placeholder subst = %v, want %v", argv, want)
	}
}

func TestUnknownAgentAndWorkflowError(t *testing.T) {
	cfg := builtinConfig()
	if _, err := cfg.BuildCommand("other", "implement", "40", "", false); err == nil {
		t.Error("expected error for unknown agent other")
	} else if !strings.Contains(err.Error(), "other") {
		t.Errorf("error should mention the agent: %v", err)
	}
	if _, err := cfg.BuildCommand("claude", "frobnicate", "40", "", false); err == nil {
		t.Error("expected error for unknown workflow")
	}
}

func TestCodexNativeWorkflowTemplates(t *testing.T) {
	cfg := builtinConfig()
	for _, workflow := range []string{"configure", "refine", "design", "implement", "review", "address-review", "refactor", "sync", "maintain"} {
		argv, err := cfg.BuildCommand("codex", workflow, "42", "", false)
		if err != nil {
			t.Fatalf("%s: %v", workflow, err)
		}
		got := argv[len(argv)-1]
		want := "$cenci:" + workflow + " 42"
		if codexPlanningWorkflow(workflow) {
			want = "/plan\n" + want
		}
		if got != want {
			t.Errorf("%s = %q, want %q", workflow, got, want)
		}
	}
}

func TestCodexApplyStageLeavesPlanMode(t *testing.T) {
	cfg := builtinConfig()
	argv, err := cfg.BuildCommand("codex", "implement", "apply .plans/42.md", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(argv[len(argv)-1], "/plan") {
		t.Fatalf("apply entered plan mode: %q", argv[len(argv)-1])
	}
}

func TestLoadMissingFileReturnsBuiltins(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing config should not error: %v", err)
	}
	if _, err := cfg.BuildCommand("claude", "implement", "1", "", false); err != nil {
		t.Errorf("builtins should still resolve: %v", err)
	}
}

// --- OpenCode adapter tests (#488) ---
//
// OpenCode shares Claude's "/cenci:<workflow>" skill-invocation format (unlike
// Codex's "$cenci:" convention and /plan staging), so the built-in workflow
// set mirrors Claude's exactly. Per the ticket Q&A, dispatch is TUI + --auto
// in a tmux window (never `opencode run ...`), so the prompt travels via the
// OpenCode CLI's top-level `--prompt` flag, not a positional/`--` argument.

func TestBuiltinOpenCodeTemplatesResolve(t *testing.T) {
	cfg := builtinConfig()
	for _, wf := range []string{"refine", "design", "implement", "address-review", "babysit", "babysit-attention", "ci-repair"} {
		argv, err := cfg.BuildCommand("opencode", wf, "40", "", false)
		if err != nil {
			t.Fatalf("BuildCommand(opencode, %s): %v", wf, err)
		}
		want := []string{"opencode", "--auto", "--prompt", "/cenci:" + wf + " 40"}
		if !equalArgs(argv, want) {
			t.Errorf("BuildCommand(opencode, %s) = %v, want %v", wf, argv, want)
		}
	}
}

// TestOpenCodeModelInjectedVerbatimNoRewrite guards the "provider/model"
// precedence rule (watch/README.md): the model string is never split or
// rewritten, it is passed straight through to opencode's --model flag exactly
// as configured/flagged, same as the existing Claude/Codex --model handling.
func TestOpenCodeModelInjectedVerbatimNoRewrite(t *testing.T) {
	cfg := builtinConfig()
	argv, err := cfg.BuildCommand("opencode", "implement", "40", "anthropic/claude-sonnet-4-5", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"opencode", "--model", "anthropic/claude-sonnet-4-5", "--auto", "--prompt", "/cenci:implement 40"}
	if !equalArgs(argv, want) {
		t.Errorf("opencode model argv = %v, want %v", argv, want)
	}
}

// TestOpenCodeConfigModelUsedWhenFlagEmpty and
// TestOpenCodeFlagModelOverridesConfigModel together pin the config/flag
// precedence rule: an explicit --model always wins over the configured
// agents.opencode.model, which itself is used only when no flag is passed.
func TestOpenCodeConfigModelUsedWhenFlagEmpty(t *testing.T) {
	cfg := builtinConfig()
	ac := cfg.Agents["opencode"]
	ac.Model = "openai/gpt-5.1-codex"
	cfg.Agents["opencode"] = ac

	argv, err := cfg.BuildCommand("opencode", "implement", "40", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, " "), "--model openai/gpt-5.1-codex") {
		t.Errorf("expected configured model to be used, got %v", argv)
	}
}

func TestOpenCodeFlagModelOverridesConfigModel(t *testing.T) {
	cfg := builtinConfig()
	ac := cfg.Agents["opencode"]
	ac.Model = "openai/gpt-5.1-codex"
	cfg.Agents["opencode"] = ac

	argv, err := cfg.BuildCommand("opencode", "implement", "40", "anthropic/claude-sonnet-4-5", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(argv, " "), "gpt-5.1-codex") {
		t.Errorf("flag model should override config model, got %v", argv)
	}
	if !strings.Contains(strings.Join(argv, " "), "--model anthropic/claude-sonnet-4-5") {
		t.Errorf("expected flag model to win, got %v", argv)
	}
}

// TestOpenCodeTicketPassesThroughWithoutCodexPlanStaging guards structural
// independence from Codex's plan-mode staging: a ticket shaped like a
// dispatch resume/apply target (".plans/...") or an explicit "resume "/"apply
// " prefix must reach opencode's --prompt argument unmodified, with no
// "/plan" stage injected (that mechanism is Codex-only).
func TestOpenCodeTicketPassesThroughWithoutCodexPlanStaging(t *testing.T) {
	cfg := builtinConfig()
	for _, ticket := range []string{".plans/42-implement.md", "resume the plan", "apply .plans/42.md"} {
		argv, err := cfg.BuildCommand("opencode", "implement", ticket, "", false)
		if err != nil {
			t.Fatalf("BuildCommand(opencode, implement, %q): %v", ticket, err)
		}
		got := argv[len(argv)-1]
		want := "/cenci:implement " + ticket
		if got != want {
			t.Errorf("ticket %q: prompt = %q, want %q (no codex plan-mode staging for opencode)", ticket, got, want)
		}
	}
}

// TestOpenCodeSandboxWiringOutOfScope guards ticket #488 Q&A #1: sandbox
// launcher wiring for opencode is deferred to #490, so the built-in config
// must not set SandboxCommand for opencode. Passing sandbox=true must still
// resolve successfully to the bare host command rather than erroring or
// silently routing through "cenci open".
func TestOpenCodeSandboxWiringOutOfScope(t *testing.T) {
	cfg := builtinConfig()
	argv, err := cfg.BuildCommand("opencode", "implement", "40", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "opencode" {
		t.Errorf("opencode sandbox=true argv[0] = %q, want %q (no sandboxCommand configured yet, #490)", argv[0], "opencode")
	}
}

func TestLoadMergesFileOverBuiltins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
	  "defaultAgent": "codex",
	  "agents": {
	    "codex": {
	      "command": "codex",
	      "workflows": { "implement": { "args": ["exec", "/cenci:implement {ticket}"] } }
	    },
	    "claude": { "model": "opus" }
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultAgent != "codex" {
		t.Errorf("defaultAgent = %q, want codex", cfg.DefaultAgent)
	}

	// New agent + workflow added by the file.
	argv, err := cfg.BuildCommand("codex", "implement", "7", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"codex", "exec", "/cenci:implement 7"}; !equalArgs(argv, want) {
		t.Errorf("codex argv = %v, want %v", argv, want)
	}

	// Built-in claude workflows survive the merge and gain the configured model.
	argv2, err := cfg.BuildCommand("claude", "implement", "7", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv2, " "), "--model opus") {
		t.Errorf("claude should inherit model opus from merge, got %v", argv2)
	}
}
