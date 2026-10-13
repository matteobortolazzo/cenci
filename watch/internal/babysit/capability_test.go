package babysit

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// hasAllExcept returns a capability predicate that is true for every pair
// except the listed (agent, workflow) denials, recording nothing.
func hasAllExcept(denied ...[2]string) func(agent, workflow string) bool {
	return func(agent, workflow string) bool {
		for _, d := range denied {
			if d[0] == agent && d[1] == workflow {
				return false
			}
		}
		return true
	}
}

func TestArmWorkflowsAreTheThreeSortedArmingWorkflows(t *testing.T) {
	want := []string{"address-review", "babysit-attention", "ci-repair"}
	if !slices.Equal(ArmWorkflows, want) {
		t.Fatalf("ArmWorkflows = %v, want %v", ArmWorkflows, want)
	}
}

func TestCapabilityRefusal(t *testing.T) {
	t.Run("all required workflows present is accepted", func(t *testing.T) {
		if got := CapabilityRefusal("claude", hasAllExcept()); got != "" {
			t.Fatalf("CapabilityRefusal = %q, want empty", got)
		}
	})

	t.Run("missing ci-repair is refused naming ci-repair", func(t *testing.T) {
		got := CapabilityRefusal("codex", hasAllExcept([2]string{"codex", "ci-repair"}))
		if got == "" {
			t.Fatal("CapabilityRefusal accepted an agent without ci-repair")
		}
		if !strings.Contains(got, "ci-repair") || !strings.Contains(got, "codex") {
			t.Fatalf("reason %q must name the agent and the missing workflow", got)
		}
	})

	t.Run("missing babysit-attention is refused naming babysit-attention", func(t *testing.T) {
		got := CapabilityRefusal("codex", hasAllExcept([2]string{"codex", "babysit-attention"}))
		if got == "" || !strings.Contains(got, "babysit-attention") {
			t.Fatalf("reason %q must be non-empty and name babysit-attention", got)
		}
	})

	t.Run("custom agent with no workflows is refused naming the first required workflow", func(t *testing.T) {
		none := func(agent, workflow string) bool { return false }
		got := CapabilityRefusal("fakeagent", none)
		if got == "" {
			t.Fatal("CapabilityRefusal accepted an agent with no workflows")
		}
		if !strings.Contains(got, "address-review") || !strings.Contains(got, "fakeagent") {
			t.Fatalf("reason %q must name the agent and address-review (the first missing workflow)", got)
		}
	})

	t.Run("nil predicate refuses", func(t *testing.T) {
		if got := CapabilityRefusal("claude", nil); got == "" {
			t.Fatal("CapabilityRefusal accepted a nil predicate")
		}
	})

	t.Run("merge-repair is not part of the arm-time requirement", func(t *testing.T) {
		got := CapabilityRefusal("codex", hasAllExcept([2]string{"codex", "merge-repair"}))
		if got != "" {
			t.Fatalf("CapabilityRefusal = %q; merge-repair is pinned to Claude and must not gate arming", got)
		}
	})

	t.Run("only the queried agent is checked", func(t *testing.T) {
		got := CapabilityRefusal("claude", hasAllExcept([2]string{"codex", "ci-repair"}))
		if got != "" {
			t.Fatalf("CapabilityRefusal = %q, want empty: the denial is for another agent", got)
		}
	})
}

// launchWorkflowRe matches a babysit launch call site and captures its workflow
// literal: launch(<state>, "<workflow>", ...).
var launchWorkflowRe = regexp.MustCompile(`launch\(\w+,\s*"([a-z-]+)"`)

// launchCallRe matches any launch( token; the definition and comment mentions
// are removed before counting.
var launchCallRe = regexp.MustCompile(`\blaunch\(`)

// nonTestSource returns the babysit package's non-test Go source with line
// comments stripped, so comment mentions of launch( never count as call sites.
func nonTestSource(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if i := strings.Index(line, "//"); i >= 0 {
				line = line[:i]
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// Every workflow the babysit package launches, except the Claude-pinned
// merge-repair, must be in ArmWorkflows, so a workflow added later cannot
// bypass the arm-time capability gate (#1215). Every launch( call site must
// also be a literal-workflow call, so a differently shaped call fails here
// rather than slipping past the guard.
func TestArmWorkflowsCoverEveryLaunchCallSite(t *testing.T) {
	src := nonTestSource(t)
	matches := launchWorkflowRe.FindAllStringSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatal("found no launch(<state>, \"<workflow>\" call sites in the babysit source; the drift guard is stale")
	}
	calls := len(launchCallRe.FindAllString(src, -1)) - strings.Count(src, "func launch(")
	if calls != len(matches) {
		t.Fatalf("babysit source has %d launch( call sites but only %d match a literal workflow; a non-literal call bypasses the ArmWorkflows guard", calls, len(matches))
	}
	seen := map[string]bool{}
	for _, m := range matches {
		wf := m[1]
		seen[wf] = true
		if wf == "merge-repair" {
			continue
		}
		if !slices.Contains(ArmWorkflows, wf) {
			t.Errorf("the babysit source launches %q but ArmWorkflows %v does not require it", wf, ArmWorkflows)
		}
	}
	for _, wf := range ArmWorkflows {
		if !seen[wf] {
			t.Errorf("ArmWorkflows requires %q but the babysit source never launches it", wf)
		}
	}
}
