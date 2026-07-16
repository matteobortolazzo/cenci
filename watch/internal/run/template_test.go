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
	for _, workflow := range []string{"configure", "refine", "design", "implement", "review", "address-review", "refactor", "sync", "garden"} {
		argv, err := cfg.BuildCommand("codex", workflow, "42", "", false)
		if err != nil {
			t.Fatalf("%s: %v", workflow, err)
		}
		if got := argv[len(argv)-1]; got != "$cenci:"+workflow+" 42" {
			t.Errorf("%s = %q", workflow, got)
		}
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
