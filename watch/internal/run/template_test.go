package run

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/sandbox"
)

// updateReadme is a test-only flag: `go test ./internal/run -run
// TestReadmeCapabilityTableInSync -update-readme` rewrites the generated
// capability block in watch/README.md from CapabilityTableMarkdown().
var updateReadme = flag.Bool("update-readme", false, "rewrite the generated capability table block in watch/README.md")

const (
	capabilityTableBegin = "<!-- cenci:capability-table:begin -->"
	capabilityTableEnd   = "<!-- cenci:capability-table:end -->"
)

// Expected built-in workflow sets, spelled out literally so an accidental
// deletion from builtinConfig() fails a test (#1214 AC 5: Claude and Codex
// built-in capabilities are kept).
var (
	wantClaudeWorkflows = []string{"address-review", "babysit", "babysit-attention", "ci-repair", "implement", "merge-repair", "refine"}
	wantCodexWorkflows  = []string{"address-review", "babysit", "babysit-attention", "ci-repair", "configure", "implement", "maintain", "refactor", "refine", "review", "sync"}
)

// acmeConfig defines a custom, unsupported agent with a refine workflow only
// and no sandboxCommand -- the generic stand-in for missing-template and
// unsatisfied-sandbox cases, independent of the disabled-OpenCode guard.
var acmeConfig = acmeConfigWith("")

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

// writeConfigFile writes body to a fresh config.json and returns its path.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuiltinClaudeTemplatesResolve(t *testing.T) {
	cfg := builtinConfig()
	for _, wf := range []string{"refine", "implement"} {
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
	for _, workflow := range []string{"configure", "refine", "implement", "review", "address-review", "refactor", "sync", "maintain"} {
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

// --- Capability query (#1214 AC 1) ---

func sortedCapabilities(agent string, workflows []string) []Capability {
	out := make([]Capability, 0, len(workflows))
	for _, wf := range workflows {
		out = append(out, Capability{Agent: agent, Workflow: wf})
	}
	return out
}

// TestBuiltinCapabilitiesExactClaudeCodexSets pins the full, exact built-in
// capability list (sorted by agent then workflow) so an accidental deletion
// from builtinConfig() -- or a disabled agent leaking in -- fails loudly.
func TestBuiltinCapabilitiesExactClaudeCodexSets(t *testing.T) {
	want := append(sortedCapabilities("claude", wantClaudeWorkflows), sortedCapabilities("codex", wantCodexWorkflows)...)
	got := BuiltinCapabilities()
	if len(got) != len(want) {
		t.Fatalf("BuiltinCapabilities() has %d entries, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("BuiltinCapabilities()[%d] = %+v, want %+v (list must be sorted by agent, then workflow)\n got: %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}

// TestBuiltinCapabilitiesDeriveFromBuiltinConfig proves there is no second
// hand-maintained workflow list: every reported capability maps back to a
// real builtinConfig() template, and every builtin template of an enabled
// agent is reported.
func TestBuiltinCapabilitiesDeriveFromBuiltinConfig(t *testing.T) {
	caps := BuiltinCapabilities()
	if len(caps) == 0 {
		t.Fatal("BuiltinCapabilities() is empty; the derivation check would pass vacuously")
	}
	cfg := builtinConfig()
	reported := map[Capability]bool{}
	for _, c := range caps {
		reported[c] = true
		if _, ok := cfg.Agents[c.Agent].Workflows[c.Workflow]; !ok {
			t.Errorf("capability %+v has no builtinConfig() template", c)
		}
		if !BuiltinCapability(c.Agent, c.Workflow) {
			t.Errorf("BuiltinCapability(%q, %q) = false for a listed capability", c.Agent, c.Workflow)
		}
	}
	for agent, ac := range cfg.Agents {
		if _, disabled := DisabledAgent(agent); disabled {
			continue
		}
		for wf := range ac.Workflows {
			if !reported[Capability{Agent: agent, Workflow: wf}] {
				t.Errorf("builtinConfig() template (%s, %s) of an enabled agent is missing from BuiltinCapabilities()", agent, wf)
			}
		}
	}
}

func TestBuiltinCapabilityRejectsUnknownPairs(t *testing.T) {
	if !BuiltinCapability("claude", "implement") {
		t.Fatal("control: BuiltinCapability(claude, implement) must be true")
	}
	for _, tc := range []struct{ agent, workflow string }{
		{"claude", "frobnicate"},
		{"ghost", "implement"},
		{"acme", "refine"},
		{"claude", ""},
	} {
		if BuiltinCapability(tc.agent, tc.workflow) {
			t.Errorf("BuiltinCapability(%q, %q) = true, want false", tc.agent, tc.workflow)
		}
	}
}

// TestOpenCodeIsNeverEligibleCapability: a disabled client never counts as
// eligible capability, even when a loaded file config supplies opencode
// workflows and a sandboxCommand.
func TestOpenCodeIsNeverEligibleCapability(t *testing.T) {
	if !BuiltinCapability("claude", "implement") {
		t.Fatal("control: BuiltinCapability(claude, implement) must be true")
	}
	body := `{"agents":{"opencode":{"command":"opencode","sandboxCommand":"cenci open","workflows":{"implement":{"args":["--prompt","{ticket}"]},"custom":{"args":["x"]}}}}}`
	cfg, err := Load(writeConfigFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	for _, wf := range []string{"refine", "implement", "address-review", "babysit", "babysit-attention", "ci-repair", "custom"} {
		if BuiltinCapability("opencode", wf) {
			t.Errorf("BuiltinCapability(opencode, %s) = true, want false", wf)
		}
	}
	for _, c := range BuiltinCapabilities() {
		if c.Agent == "opencode" {
			t.Errorf("BuiltinCapabilities() lists disabled agent opencode: %+v", c)
		}
	}
	// The file config cannot re-enable it either.
	if _, err := cfg.BuildCommand("opencode", "implement", "1", "", false); !errors.Is(err, ErrAgentDisabled) {
		t.Errorf("BuildCommand(opencode) with file-supplied workflows: err = %v, want ErrAgentDisabled", err)
	}
}

func TestDisabledAgentReportsOpenCodeOnly(t *testing.T) {
	reason, disabled := DisabledAgent("opencode")
	if !disabled {
		t.Fatal("DisabledAgent(opencode) = false, want true")
	}
	if !strings.Contains(reason, "temporarily disabled") {
		t.Errorf("reason = %q, want it to say \"temporarily disabled\"", reason)
	}
	// The reason is the shared disable mechanism's text, not a second table.
	if want := sandbox.AgentDisabledReason("opencode"); reason != want {
		t.Errorf("reason = %q, want the shared sandbox.AgentDisabledReason text %q", reason, want)
	}
	// The disable must never suggest a config override can bypass it.
	for _, banned := range []string{"config", "add one"} {
		if strings.Contains(strings.ToLower(reason), banned) {
			t.Errorf("reason = %q, must not mention %q", reason, banned)
		}
	}
	for _, agent := range []string{"claude", "codex", "acme", ""} {
		if r, d := DisabledAgent(agent); d {
			t.Errorf("DisabledAgent(%q) = (%q, true), want not disabled", agent, r)
		}
	}
}

// --- BuildCommand failure classes and sentinels (#1214 ACs 2, 3, 5) ---
//
// Each sentinel gets its own direct errors.Is test at the package boundary
// (watch/docs/error-handling.md #412), and each failure class its own
// content assertions (#446).

func TestBuildCommandMissingTemplateWrapsErrMissingTemplate(t *testing.T) {
	path := writeConfigFile(t, acmeConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, agent, workflow string
	}{
		{"custom agent, missing workflow", "acme", "implement"},
		{"built-in agent, missing workflow", "claude", "frobnicate"},
		{"unknown agent", "ghost", "implement"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cfg.BuildCommand(tc.agent, tc.workflow, "1", "", false)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrMissingTemplate) {
				t.Errorf("err = %v, want errors.Is(_, ErrMissingTemplate)", err)
			}
			if errors.Is(err, ErrAgentDisabled) || errors.Is(err, ErrSandboxUnavailable) {
				t.Errorf("err = %v, must not also wrap another failure class", err)
			}
			for _, want := range []string{tc.agent, tc.workflow, "agents." + tc.agent + ".workflows." + tc.workflow, path} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestBuildCommandDisabledAgentWrapsErrAgentDisabled(t *testing.T) {
	_, err := builtinConfig().BuildCommand("opencode", "implement", "1", "", false)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrAgentDisabled) {
		t.Errorf("err = %v, want errors.Is(_, ErrAgentDisabled)", err)
	}
	if errors.Is(err, ErrMissingTemplate) || errors.Is(err, ErrSandboxUnavailable) {
		t.Errorf("err = %v, must not also wrap another failure class", err)
	}
	if !strings.Contains(err.Error(), "temporarily disabled") {
		t.Errorf("err = %q, want \"temporarily disabled\"", err)
	}
	for _, banned := range []string{"config.json", "add one"} {
		if strings.Contains(err.Error(), banned) {
			t.Errorf("err = %q, must not contain %q", err, banned)
		}
	}
}

func TestBuildCommandUnsatisfiedSandboxWrapsErrSandboxUnavailable(t *testing.T) {
	cases := []struct{ name, sandboxCommand string }{
		{"no sandboxCommand", ""},
		{"whitespace-only sandboxCommand", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"agents":{"acme":{"command":"acme","sandboxCommand":"` + tc.sandboxCommand + `","workflows":{"refine":{"args":["--task","{ticket}"]}}}}}`
			path := writeConfigFile(t, body)
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = cfg.BuildCommand("acme", "refine", "1", "", true)
			if err == nil {
				t.Fatal("expected an error: sandbox requested but no usable sandboxCommand")
			}
			if !errors.Is(err, ErrSandboxUnavailable) {
				t.Errorf("err = %v, want errors.Is(_, ErrSandboxUnavailable)", err)
			}
			if errors.Is(err, ErrMissingTemplate) || errors.Is(err, ErrAgentDisabled) {
				t.Errorf("err = %v, must not also wrap another failure class", err)
			}
			for _, want := range []string{"acme", "agents.acme.sandboxCommand", path, "--no-sandbox", `"sandbox": false`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to contain %q", err, want)
				}
			}
			// Host launch of the same template is the explicit opt-out and works.
			argv, err := cfg.BuildCommand("acme", "refine", "1", "", false)
			if err != nil {
				t.Fatalf("host launch must still resolve: %v", err)
			}
			if want := []string{"acme", "--task", "1"}; !equalArgs(argv, want) {
				t.Errorf("host argv = %v, want %v", argv, want)
			}
		})
	}
}

// TestUnknownAgentHintExcludesDisabledAgents: the configured-agents hint in a
// missing-template error must not advertise the disabled opencode entry.
func TestUnknownAgentHintExcludesDisabledAgents(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, acmeConfig))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.BuildCommand("ghost", "implement", "1", "", false)
	if !errors.Is(err, ErrMissingTemplate) {
		t.Fatalf("err = %v, want ErrMissingTemplate", err)
	}
	for _, want := range []string{"claude", "codex", "acme"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("hint should list configured agent %q: %q", want, err)
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "opencode") {
		t.Errorf("hint must not list the disabled agent opencode: %q", err)
	}
}

// --- Provenance (#1214 AC 4) ---

func TestTemplateProvenanceLabels(t *testing.T) {
	body := `{"agents":{
	  "claude":{"workflows":{"implement":{"args":["--","/x {ticket}"]},"file-added":{"args":["--","/y {ticket}"]}}},
	  "acme":{"command":"acme","workflows":{"refine":{"args":["{ticket}"]}}}
	}}`
	path := writeConfigFile(t, body)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	userDefined := "user-defined (" + path + ")"
	for _, tc := range []struct{ agent, workflow, want string }{
		{"claude", "refine", "built-in"},
		{"claude", "implement", "built-in"}, // args-only override stays built-in
		{"codex", "implement", "built-in"},
		{"claude", "file-added", userDefined},
		{"acme", "refine", userDefined},
	} {
		if got := cfg.TemplateProvenance(tc.agent, tc.workflow); got != tc.want {
			t.Errorf("TemplateProvenance(%s, %s) = %q, want %q", tc.agent, tc.workflow, got, tc.want)
		}
	}
}

// --- README capability table (#1214 AC 6) ---

// readmePath resolves watch/README.md via runtime.Caller, following the
// internal/errcode atlas_sync_test.go precedent: independent of the test
// binary's working directory.
func readmePath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to resolve this test file's own path")
	}
	// file: <repo-root>/watch/internal/run/template_test.go
	return filepath.Join(filepath.Dir(file), "..", "..", "README.md")
}

// TestCapabilityTableMarkdownReflectsRegistry guards the renderer itself so
// the drift test below cannot pass against an empty or partial table.
func TestCapabilityTableMarkdownReflectsRegistry(t *testing.T) {
	md := CapabilityTableMarkdown()
	if strings.TrimSpace(md) == "" {
		t.Fatal("CapabilityTableMarkdown() is empty")
	}
	for _, c := range BuiltinCapabilities() {
		if !strings.Contains(md, c.Workflow) {
			t.Errorf("table is missing workflow %q", c.Workflow)
		}
	}
	for _, agent := range []string{"claude", "codex"} {
		if !strings.Contains(md, agent) {
			t.Errorf("table is missing enabled agent %q", agent)
		}
	}
	reason, disabled := DisabledAgent("opencode")
	if !disabled || reason == "" {
		t.Fatal("control: DisabledAgent(opencode) must report a reason")
	}
	if !strings.Contains(md, "opencode") || !strings.Contains(md, reason) {
		t.Errorf("table must list the disabled agent opencode with its reason %q:\n%s", reason, md)
	}
	for _, r := range md {
		if r >= 0x1F000 {
			t.Errorf("table contains emoji rune %U", r)
			break
		}
	}
	if !sort.StringsAreSorted(wantClaudeWorkflows) || !sort.StringsAreSorted(wantCodexWorkflows) {
		t.Fatal("test fixture workflow lists must be sorted")
	}
}

// TestReadmeCapabilityTableInSync fails when the generated capability block in
// watch/README.md differs from CapabilityTableMarkdown(). Run with
// -update-readme to regenerate it. Missing markers or an unreadable README are
// hard failures, never skips.
func TestReadmeCapabilityTableInSync(t *testing.T) {
	want := strings.TrimSpace(CapabilityTableMarkdown())
	if want == "" {
		t.Fatal("CapabilityTableMarkdown() is empty; the sync check would pass vacuously")
	}
	path := readmePath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v (a missing README must fail loudly, not be skipped)", path, err)
	}
	readme := string(data)
	begin := strings.Index(readme, capabilityTableBegin)
	end := strings.Index(readme, capabilityTableEnd)
	if begin < 0 || end < 0 || end < begin {
		t.Fatalf("%s must contain %s ... %s markers (in order) in ### Configuration", path, capabilityTableBegin, capabilityTableEnd)
	}
	bodyStart := begin + len(capabilityTableBegin)
	if *updateReadme {
		updated := readme[:bodyStart] + "\n" + want + "\n" + readme[end:]
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}
	if got := strings.TrimSpace(readme[bodyStart:end]); got != want {
		t.Fatalf("watch/README.md capability table is out of date with builtinConfig().\n"+
			"Regenerate it: go test ./internal/run -run TestReadmeCapabilityTableInSync -update-readme\n"+
			"--- README block ---\n%s\n--- generated ---\n%s", got, want)
	}
}
