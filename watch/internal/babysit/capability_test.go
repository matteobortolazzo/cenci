package babysit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	want := []string{"babysit-attention", "babysit-review", "ci-repair"}
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
		if !strings.Contains(got, "babysit-attention") || !strings.Contains(got, "fakeagent") {
			t.Fatalf("reason %q must name the agent and babysit-attention (the first missing workflow)", got)
		}
	})

	t.Run("nil predicate refuses", func(t *testing.T) {
		if got := CapabilityRefusal("claude", nil); got == "" {
			t.Fatal("CapabilityRefusal accepted a nil predicate")
		}
	})

	t.Run("missing babysit-review is refused naming babysit-review", func(t *testing.T) {
		got := CapabilityRefusal("codex", hasAllExcept([2]string{"codex", "babysit-review"}))
		if got == "" || !strings.Contains(got, "babysit-review") {
			t.Fatalf("reason %q must be non-empty and name babysit-review", got)
		}
	})

	t.Run("address-review is no longer an arm-time requirement", func(t *testing.T) {
		got := CapabilityRefusal("codex", hasAllExcept([2]string{"codex", "address-review"}))
		if got != "" {
			t.Fatalf("CapabilityRefusal = %q; the supervisor no longer launches address-review", got)
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

// branchWriterWorkflows are the launches that write to the supervised PR's
// branch. Each one needs a capacity reservation (#1469): launch() refuses one
// without it, and every call site must obtain it from admitRepair.
var branchWriterWorkflows = []string{"ci-repair", "merge-repair", "babysit-review"}

// branchWriterLaunchViolations parses one Go source file and reports every
// launch(...) call that could bypass admission: a branch-writer workflow
// launched with a nil-literal permit, or from a function that never calls
// admitRepair, and any call whose workflow is not a string literal or that does
// not carry the permit argument. It also returns the branch-writer call sites it
// saw, by workflow, so the caller can prove the check is not vacuous.
func branchWriterLaunchViolations(t *testing.T, filename string, src any) (violations []string, sites []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	callsAdmit := func(body ast.Node) bool {
		found := false
		ast.Inspect(body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "admitRepair" {
					found = true
				}
			}
			return !found
		})
		return found
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		admits := callsAdmit(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "launch" {
				return true
			}
			where := fset.Position(call.Pos()).String()
			if len(call.Args) != 4 {
				violations = append(violations, where+": launch call without the permit argument")
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				violations = append(violations, where+": launch call with a non-literal workflow")
				return true
			}
			workflow, _ := strconv.Unquote(lit.Value)
			if !slices.Contains(branchWriterWorkflows, workflow) {
				return true
			}
			sites = append(sites, workflow)
			if id, ok := call.Args[3].(*ast.Ident); ok && id.Name == "nil" {
				violations = append(violations, where+": "+workflow+" is launched with a nil permit")
			}
			if !admits {
				violations = append(violations, where+": "+workflow+" is launched from "+fn.Name.Name+", which never calls admitRepair")
			}
			return true
		})
	}
	return violations, sites
}

// #1469: a branch-writer launch (ci-repair, merge-repair, babysit-review) must
// never bypass the admission ledger. The check walks the non-test source with
// go/ast: every such launch( call passes a permit that is not the nil literal and
// sits in a function that calls admitRepair. A new call site, or a changed one,
// that skips either fails here instead of launching an uncounted worker.
func TestBranchWriterLaunchesAreAdmitted(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		violations, sites := branchWriterLaunchViolations(t, f, nil)
		for _, v := range violations {
			t.Error(v)
		}
		for _, wf := range sites {
			seen[wf]++
		}
	}
	for _, wf := range branchWriterWorkflows {
		if seen[wf] == 0 {
			t.Errorf("found no launch(<state>, %q, ...) call site; the admission guard is stale", wf)
		}
	}
}

// The call-site check must catch the bypasses it exists for, so it is run over
// synthetic sources: a nil permit, a call outside any admitRepair function, a
// closure inside one, an attention launch (uncharged, nil allowed) and a missing
// permit argument.
func TestBranchWriterLaunchCheckerCatchesBypasses(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int // violations
	}{
		{"admitted with a permit", `package p
func f(s *State) { permit, _ := admitRepair(s); _ = launch(s, "ci-repair", "x", permit) }`, 0},
		{"nil permit", `package p
func f(s *State) { admitRepair(s); _ = launch(s, "merge-repair", "x", nil) }`, 1},
		{"no admitRepair in the function", `package p
func f(s *State, permit *P) { _ = launch(s, "babysit-review", "x", permit) }`, 1},
		{"nil permit and no admitRepair", `package p
func f(s *State) { _ = launch(s, "ci-repair", "x", nil) }`, 2},
		{"closure inside an admitting function", `package p
func f(s *State) { permit, _ := admitRepair(s); func() { _ = launch(s, "ci-repair", "x", permit) }() }`, 0},
		{"closure inside a non-admitting function", `package p
func f(s *State, permit *P) { func() { _ = launch(s, "ci-repair", "x", permit) }() }`, 1},
		{"attention is uncharged", `package p
func f(s *State) { _ = launch(s, "babysit-attention", "x", nil) }`, 0},
		{"missing permit argument", `package p
func f(s *State) { _ = launch(s, "ci-repair", "x") }`, 1},
		{"non-literal workflow", `package p
func f(s *State, wf string) { _ = launch(s, wf, "x", nil) }`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			violations, _ := branchWriterLaunchViolations(t, "synthetic.go", tc.src)
			if len(violations) != tc.want {
				t.Errorf("violations = %q, want %d", violations, tc.want)
			}
		})
	}
}
