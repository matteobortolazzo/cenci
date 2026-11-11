package run

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
	wantClaudeWorkflows = []string{"address-review", "babysit", "babysit-attention", "babysit-review", "ci-repair", "implement", "merge-repair", "refine"}
	wantCodexWorkflows  = []string{"address-review", "babysit", "babysit-attention", "babysit-review", "ci-repair", "configure", "implement", "maintain", "refactor", "refine", "review", "sync"}
)

// skillOf names the flow skill a built-in workflow key runs. Every key runs the
// skill of the same name except babysit-review, the supervisor-only finite
// registration of address-review (#1472): flow's dispatch-target-invocable
// contract requires a skills/<name>/codex.md for each codexWF("<name>") target,
// so the key and the skill differ.
func skillOf(workflow string) string {
	if workflow == "babysit-review" {
		return "address-review"
	}
	return workflow
}

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
	// Every built-in Codex workflow (#1232): the host launch passes the prompt
	// to codex directly; the sandbox launch routes it through `cenci open
	// --agent codex --` as one verbatim element, with an explicit model (flag
	// or agents.codex.model) injected before the agent flag.
	workflows := []struct{ workflow, prompt string }{
		{"configure", "$cenci:configure 42"},
		{"refine", "$cenci:refine 42"},
		{"implement", "$cenci:implement 42"},
		{"review", "$cenci:review 42"},
		{"address-review", "$cenci:address-review 42"},
		{"refactor", "$cenci:refactor 42"},
		{"sync", "$cenci:sync 42"},
		{"maintain", "$cenci:maintain 42"},
		{"babysit", "$cenci:babysit 42"},
		{"babysit-attention", "$cenci:babysit-attention 42"},
	}
	// ci-repair (#1455) and babysit-review (#1472) are the finite (`codex exec`)
	// Codex workflows; their expectations live in
	// TestBuiltinCIRepairTemplatesAreFinite and
	// TestBuiltinBabysitReviewTemplatesAreFinite.
	if len(workflows)+2 != len(wantCodexWorkflows) {
		t.Fatalf("table covers %d workflows, want every built-in Codex workflow except ci-repair and babysit-review (%d)", len(workflows), len(wantCodexWorkflows)-2)
	}
	sandboxCommand := []string{"cenci", "open", "--agent", "codex", "--"}
	modelCommand := []string{"cenci", "open", "--model", "m", "--agent", "codex", "--"}
	for _, launch := range []struct {
		name                string
		sandbox             bool
		flagModel, cfgModel string
		command             []string
	}{
		{"host", false, "", "", []string{"codex"}},
		{"sandbox", true, "", "", sandboxCommand},
		{"sandbox/flag-model", true, "m", "", modelCommand},
		{"sandbox/config-model", true, "", "m", modelCommand},
	} {
		cfg := builtinConfig()
		ac := cfg.Agents["codex"]
		ac.Model = launch.cfgModel
		cfg.Agents["codex"] = ac
		for _, workflow := range workflows {
			t.Run(launch.name+"/"+workflow.workflow, func(t *testing.T) {
				argv, err := cfg.BuildCommand("codex", workflow.workflow, "42", launch.flagModel, launch.sandbox)
				if err != nil {
					t.Fatal(err)
				}
				want := append(append([]string{}, launch.command...), workflow.prompt)
				if !equalArgs(argv, want) {
					t.Errorf("argv = %q, want %q", argv, want)
				}
			})
		}
	}
}

// #1455 AC3: the built-in ci-repair templates are one-shot -- Claude print mode
// and `codex exec` -- on host and through `cenci open`, so a finished repair
// never leaves an interactive prompt behind. An injected model keeps the
// selected client's model: before the passthrough marker on `cenci open` (the
// launcher places it inside the container), and after the leading `exec` on a
// host Codex launch.
func TestBuiltinCIRepairTemplatesAreFinite(t *testing.T) {
	const ticket = "42 PR #42 (feat/x) has failing CI checks: lint."
	claudePrompt := "/cenci:ci-repair " + ticket
	codexPrompt := "$cenci:ci-repair " + ticket
	for _, tc := range []struct {
		name, agent, model string
		sandbox            bool
		want               []string
	}{
		{"claude/host", "claude", "", false, []string{"claude", "--print", "--", claudePrompt}},
		{"claude/sandbox", "claude", "", true, []string{"cenci", "open", "--", "--print", claudePrompt}},
		{"codex/host", "codex", "", false, []string{"codex", "exec", codexPrompt}},
		{"codex/sandbox", "codex", "", true, []string{"cenci", "open", "--agent", "codex", "--", "exec", codexPrompt}},
		{"claude/host/model", "claude", "m", false, []string{"claude", "--model", "m", "--print", "--", claudePrompt}},
		{"claude/sandbox/model", "claude", "m", true, []string{"cenci", "open", "--model", "m", "--", "--print", claudePrompt}},
		{"codex/host/model", "codex", "m", false, []string{"codex", "exec", "--model", "m", codexPrompt}},
		{"codex/sandbox/model", "codex", "m", true, []string{"cenci", "open", "--model", "m", "--agent", "codex", "--", "exec", codexPrompt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := builtinConfig().BuildCommand(tc.agent, "ci-repair", ticket, tc.model, tc.sandbox)
			if err != nil {
				t.Fatal(err)
			}
			if !equalArgs(argv, tc.want) {
				t.Errorf("argv = %q, want %q", argv, tc.want)
			}
		})
	}
}

// babysitReviewTicket is the launch argument the supervisor passes (#1472): the
// PR number, the per-launch receipt nonce and one `discussion:<id>=<sha256>`
// token per pending discussion key.
const babysitReviewTicket = "42 --receipt-nonce 0123456789abcdef0123456789abcdef " +
	"discussion:1001=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa " +
	"discussion:1002=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// #1472 AC 75 (argv half): the supervisor-only babysit-review templates are
// one-shot -- Claude print mode and `codex exec` -- on host and through
// `cenci open`, so a finished review worker never leaves an interactive prompt
// (address-review's Phase 3F) behind. The key registers the address-review
// skill, so the prompt names address-review while the template key stays
// babysit-review. The arguments (PR, nonce, per-key revisions) pass through
// intact as the tail of the single prompt element.
func TestBuiltinBabysitReviewTemplatesAreFinite(t *testing.T) {
	claudePrompt := "/cenci:address-review " + babysitReviewTicket
	codexPrompt := "$cenci:address-review " + babysitReviewTicket
	for _, tc := range []struct {
		name, agent, model string
		sandbox            bool
		want               []string
	}{
		{"claude/host", "claude", "", false, []string{"claude", "--print", "--", claudePrompt}},
		{"claude/sandbox", "claude", "", true, []string{"cenci", "open", "--", "--print", claudePrompt}},
		{"codex/host", "codex", "", false, []string{"codex", "exec", codexPrompt}},
		{"codex/sandbox", "codex", "", true, []string{"cenci", "open", "--agent", "codex", "--", "exec", codexPrompt}},
		{"claude/host/model", "claude", "m", false, []string{"claude", "--model", "m", "--print", "--", claudePrompt}},
		{"codex/host/model", "codex", "m", false, []string{"codex", "exec", "--model", "m", codexPrompt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := builtinConfig().BuildCommand(tc.agent, "babysit-review", babysitReviewTicket, tc.model, tc.sandbox)
			if err != nil {
				t.Fatal(err)
			}
			if !equalArgs(argv, tc.want) {
				t.Errorf("argv = %q, want %q", argv, tc.want)
			}
		})
	}
}

// #1472 AC 75: the interactive address-review templates a human launches keep
// their exact argv -- no print or exec mode leaks in from babysit-review, on
// host and in the sandbox, for both clients -- and babysit-review is a separate
// capability, not an alias of it.
func TestBuiltinAddressReviewTemplatesStayInteractive(t *testing.T) {
	const ticket = "42"
	for _, tc := range []struct {
		name, agent string
		sandbox     bool
		want        []string
	}{
		{"claude/host", "claude", false, []string{"claude", "--", "/cenci:address-review 42"}},
		{"claude/sandbox", "claude", true, []string{"cenci", "open", "--", "/cenci:address-review 42"}},
		{"codex/host", "codex", false, []string{"codex", "$cenci:address-review 42"}},
		{"codex/sandbox", "codex", true, []string{"cenci", "open", "--agent", "codex", "--", "$cenci:address-review 42"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := builtinConfig().BuildCommand(tc.agent, "address-review", ticket, "", tc.sandbox)
			if err != nil {
				t.Fatal(err)
			}
			if !equalArgs(argv, tc.want) {
				t.Errorf("address-review argv = %q, want unchanged %q", argv, tc.want)
			}
		})
	}
	for _, agent := range []string{"claude", "codex"} {
		if !BuiltinCapability(agent, "babysit-review") || !BuiltinCapability(agent, "address-review") {
			t.Errorf("%s must keep address-review and add babysit-review as separate built-in capabilities", agent)
		}
	}
}

// #1472: `cenci run babysit-review <pr> --receipt-nonce ...` names its tmux
// window `<pr>-babysit-review`, the name babysit's liveness probe looks for, even
// though the template runs the address-review skill: the window name takes the
// leading PR token and the workflow key, never the prompt or the nonce.
func TestBuiltinBabysitReviewWindowNameIsPRAndWorkflowKey(t *testing.T) {
	for _, tc := range []struct{ ticketArg, workflow, want string }{
		{babysitReviewTicket, "babysit-review", "42-babysit-review"},
		{"7", "babysit-review", "7-babysit-review"},
		{babysitReviewTicket, "address-review", "42-address-review"},
	} {
		if got := windowName(tc.ticketArg, tc.workflow, ""); got != tc.want {
			t.Errorf("windowName(%q, %q) = %q, want %q", tc.ticketArg, tc.workflow, got, tc.want)
		}
	}
}

// #1455 AC10: every built-in workflow other than ci-repair resolves to exactly
// the bytes it did before the finite ci-repair templates, on host and in the
// sandbox, for both agents. Spelled out literally so a modifier leaking into a
// sibling template fails by name.
func TestBuiltinNonCIRepairTemplatesUnchanged(t *testing.T) {
	type pin struct {
		agent, workflow string
		host, sandbox   []string
	}
	var pins []pin
	for _, wf := range []string{"implement", "refine", "address-review", "babysit", "babysit-attention"} {
		pins = append(pins,
			pin{"claude", wf, []string{"claude", "--", "/cenci:" + wf + " 42"}, []string{"cenci", "open", "--", "/cenci:" + wf + " 42"}},
			pin{"codex", wf, []string{"codex", "$cenci:" + wf + " 42"}, []string{"cenci", "open", "--agent", "codex", "--", "$cenci:" + wf + " 42"}},
		)
	}
	pins = append(pins, pin{"claude", "merge-repair",
		[]string{"claude", "--print", "--", "/cenci:merge-repair 42"},
		[]string{"cenci", "open", "--", "--print", "/cenci:merge-repair 42"}})
	for _, p := range pins {
		for _, sandbox := range []bool{false, true} {
			t.Run(p.agent+"/"+p.workflow+"/sandbox="+strconv.FormatBool(sandbox), func(t *testing.T) {
				argv, err := builtinConfig().BuildCommand(p.agent, p.workflow, "42", "", sandbox)
				if err != nil {
					t.Fatal(err)
				}
				want := p.host
				if sandbox {
					want = p.sandbox
				}
				if !equalArgs(argv, want) {
					t.Errorf("argv = %q, want unchanged %q", argv, want)
				}
			})
		}
	}
	// A model on a non-exec Codex template stays root-level, as before.
	argv, err := builtinConfig().BuildCommand("codex", "implement", "42", "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"codex", "--model", "m", "$cenci:implement 42"}; !equalArgs(argv, want) {
		t.Errorf("codex implement with model = %q, want %q", argv, want)
	}
	if ac := builtinConfig().Agents["opencode"]; len(ac.Workflows) != 0 {
		t.Errorf("opencode ships workflows %v, want none", ac.Workflows)
	}
	if _, disabled := DisabledAgent("opencode"); !disabled {
		t.Error(`DisabledAgent("opencode") reports enabled, want disabled`)
	}
}

// #1455 edge case: a user ci-repair template is used verbatim, never rewritten
// to print/exec mode, even when it is interactive.
func TestCustomCIRepairArgsAreUsedVerbatim(t *testing.T) {
	path := writeConfigFile(t, `{"agents": {
	  "claude": {"workflows": {"ci-repair": {"args": ["--", "/cenci:ci-repair {ticket}"]}}},
	  "codex": {"model": "m", "workflows": {"ci-repair": {"args": ["$cenci:ci-repair {ticket}"]}}}
	}}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := cfg.BuildCommand("claude", "ci-repair", "42", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude", "--", "/cenci:ci-repair 42"}; !equalArgs(argv, want) {
		t.Errorf("custom claude ci-repair = %q, want %q", argv, want)
	}
	argv, err = cfg.BuildCommand("codex", "ci-repair", "42", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"codex", "--model", "m", "$cenci:ci-repair 42"}; !equalArgs(argv, want) {
		t.Errorf("custom codex ci-repair = %q, want %q", argv, want)
	}
}

// #1234: the launcher never stages Plan mode. Whatever the target says (a bare
// ticket, a persist or apply step, a plan path), the Codex prompt is the bare
// `$cenci:<wf> <target>`; the planning companion's own self-check asks the
// user for `/plan`.
func TestCodexPromptNeverStagesPlanMode(t *testing.T) {
	cfg := builtinConfig()
	for _, workflow := range wantCodexWorkflows {
		for _, target := range []string{"42", "apply 42", "persist 42", ".plans/42.md"} {
			for _, sandbox := range []bool{false, true} {
				t.Run(workflow+"/"+target+"/sandbox="+strconv.FormatBool(sandbox), func(t *testing.T) {
					argv, err := cfg.BuildCommand("codex", workflow, target, "", sandbox)
					if err != nil {
						t.Fatal(err)
					}
					got, want := argv[len(argv)-1], "$cenci:"+skillOf(workflow)+" "+target
					if got != want {
						t.Fatalf("prompt = %q, want %q", got, want)
					}
					if strings.Contains(got, "/plan") {
						t.Fatalf("prompt %q stages Plan mode", got)
					}
				})
			}
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
	for _, wf := range []string{"refine", "implement", "address-review", "babysit", "babysit-attention", "babysit-review", "ci-repair", "custom"} {
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

// --- Capability adapters (#1237 AC 1) ---

// flowDir resolves <repo-root>/flow via runtime.Caller. A missing flow tree is
// a hard failure, never a skip: the adapter invariant must not pass vacuously.
func flowDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed to resolve this test file's own path")
	}
	// file: <repo-root>/watch/internal/run/template_test.go
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "flow")
	if info, err := os.Stat(filepath.Join(dir, "skills")); err != nil || !info.IsDir() {
		t.Fatalf("%s/skills is not readable (%v): the adapter invariant needs the flow tree and must not be skipped", dir, err)
	}
	return dir
}

// TestBuiltinCapabilityAdaptersExistInFlow: every built-in capability declares
// an adapter, the declared file exists and is non-empty under flow/, and every
// codex.md-adapted capability's SKILL.md carries the Codex dispatch line that
// flow/tests/dispatch-target-invocable.test.sh also requires.
func TestBuiltinCapabilityAdaptersExistInFlow(t *testing.T) {
	caps := BuiltinCapabilities()
	if len(caps) == 0 {
		t.Fatal("BuiltinCapabilities() is empty; the adapter invariant would pass vacuously")
	}
	flow := flowDir(t)
	checkedCompanions := 0
	for _, c := range caps {
		t.Run(c.Agent+"/"+c.Workflow, func(t *testing.T) {
			adapter := c.Adapter()
			if adapter == "" {
				t.Fatalf("capability %+v declares no adapter", c)
			}
			if got, ok := BuiltinAdapter(c.Agent, c.Workflow); !ok || got != adapter {
				t.Errorf("BuiltinAdapter(%q, %q) = (%q, %v), want (%q, true)", c.Agent, c.Workflow, got, ok, adapter)
			}
			data, err := os.ReadFile(filepath.Join(flow, adapter))
			if err != nil {
				t.Fatalf("declared adapter %s is not readable under flow/: %v", adapter, err)
			}
			if strings.TrimSpace(string(data)) == "" {
				t.Fatalf("declared adapter %s is empty", adapter)
			}
			if filepath.Base(adapter) != "codex.md" {
				return
			}
			checkedCompanions++
			name := skillOf(c.Workflow)
			skill, err := os.ReadFile(filepath.Join(flow, "skills", name, "SKILL.md"))
			if err != nil {
				t.Fatalf("skills/%s/SKILL.md: %v", name, err)
			}
			needle := "> **Client dispatch**: In Codex, read `codex-runtime` and `" + name + "/codex.md`"
			found := false
			for _, line := range strings.Split(string(skill), "\n") {
				if strings.HasPrefix(line, needle) {
					found = true
				}
			}
			if !found {
				t.Errorf("skills/%s/SKILL.md has no line starting %q: Codex would not be routed to codex.md", name, needle)
			}
		})
	}
	if checkedCompanions == 0 {
		t.Fatal("no codex.md-adapted capability was checked; the Client dispatch assertion would pass vacuously")
	}
}

// TestBuiltinAdapterExactValues pins the declared adapters literally, so a
// regression in claudeWF/codexWF/portable() (e.g. Codex babysit losing its
// portable adapter) fails by name.
func TestBuiltinAdapterExactValues(t *testing.T) {
	for _, tc := range []struct{ agent, workflow, want string }{
		{"claude", "implement", "skills/implement/SKILL.md"},
		{"claude", "babysit", "skills/babysit/SKILL.md"},
		{"claude", "merge-repair", "skills/merge-repair/SKILL.md"},
		{"codex", "ci-repair", "skills/ci-repair/codex.md"},
		// babysit-review reuses the address-review skill (#1472): no
		// skills/babysit-review exists, so both clients adapt to address-review.
		{"claude", "babysit-review", "skills/address-review/SKILL.md"},
		{"codex", "babysit-review", "skills/address-review/codex.md"},
		{"codex", "implement", "skills/implement/codex.md"},
		{"codex", "babysit-attention", "skills/babysit-attention/codex.md"},
		{"codex", "babysit", "skills/babysit/SKILL.md"}, // portable: no codex.md
	} {
		got, ok := BuiltinAdapter(tc.agent, tc.workflow)
		if !ok || got != tc.want {
			t.Errorf("BuiltinAdapter(%q, %q) = (%q, %v), want (%q, true)", tc.agent, tc.workflow, got, ok, tc.want)
		}
		if got := (Capability{Agent: tc.agent, Workflow: tc.workflow}).Adapter(); got != tc.want {
			t.Errorf("Capability{%s,%s}.Adapter() = %q, want %q", tc.agent, tc.workflow, got, tc.want)
		}
	}
}

func TestBuiltinAdapterRejectsNonCapabilities(t *testing.T) {
	for _, tc := range []struct{ agent, workflow string }{
		{"opencode", "implement"}, // disabled agent
		{"claude", "frobnicate"},
		{"ghost", "implement"},
		{"codex", "merge-repair"}, // Claude-only workflow
		{"codex", ""},
	} {
		if got, ok := BuiltinAdapter(tc.agent, tc.workflow); ok || got != "" {
			t.Errorf("BuiltinAdapter(%q, %q) = (%q, %v), want (\"\", false)", tc.agent, tc.workflow, got, ok)
		}
		if got := (Capability{Agent: tc.agent, Workflow: tc.workflow}).Adapter(); got != "" {
			t.Errorf("Capability{%s,%s}.Adapter() = %q, want empty", tc.agent, tc.workflow, got)
		}
	}
}

// TestFileConfigCannotChangeAdapter: neither a file that overrides a built-in
// template's args nor one that adds a user-defined workflow can change the
// declared adapter of a built-in pair, and a user-defined workflow declares none.
func TestFileConfigCannotChangeAdapter(t *testing.T) {
	body := `{"agents":{"codex":{"workflows":{
	  "implement":{"args":["exec","/x {ticket}"],"adapter":"skills/evil/codex.md","Adapter":"skills/evil/codex.md"},
	  "custom":{"args":["x"]}}}}}`
	cfg, err := Load(writeConfigFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	const want = "skills/implement/codex.md"
	if got, ok := BuiltinAdapter("codex", "implement"); !ok || got != want {
		t.Errorf("BuiltinAdapter after file override = (%q, %v), want (%q, true)", got, ok, want)
	}
	if got := cfg.Agents["codex"].Workflows["implement"].adapter; got != want {
		t.Errorf("merged codex implement adapter = %q, want %q (merge must preserve the built-in adapter)", got, want)
	}
	if got := cfg.Agents["codex"].Workflows["custom"].adapter; got != "" {
		t.Errorf("user-defined workflow adapter = %q, want empty", got)
	}
	if _, ok := BuiltinAdapter("codex", "custom"); ok {
		t.Error("BuiltinAdapter(codex, custom) = true for a file-only workflow")
	}
}
