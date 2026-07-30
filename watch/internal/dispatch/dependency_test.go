package dispatch

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -- #825: parseDependsOn (pure) ---------------------------------------------

// TestParseDependsOn covers plan tests 1-10: parseDependsOn is a pure,
// line-anchored, case-insensitive parser for "Depends on #N" references in a
// ticket body, tolerant of an optional leading list marker and arbitrary
// trailing text, but never matching "Related to #N"/"Parallel with #N" or a
// mid-sentence (non-line-anchored) occurrence.
func TestParseDependsOn(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []int
	}{
		{
			name: "test 1: bare line on its own",
			body: "Depends on #822",
			want: []int{822},
		},
		{
			// Q1: refine's own Pass 1 literal example.
			name: "test 2: bullet/prose form (Q1, refine's literal example)",
			body: "- Depends on #822 (local main sync) since this ticket serializes on `internal/dispatch`'s collector changes alongside it.",
			want: []int{822},
		},
		{
			name: "test 3: asterisk marker form (Q1)",
			body: "* Depends on #5",
			want: []int{5},
		},
		{
			name: "test 4: case-insensitive",
			body: "depends ON #822",
			want: []int{822},
		},
		{
			name: "test 5: Related to must never match",
			body: "Related to #5",
			want: nil,
		},
		{
			name: "test 6: Parallel with must never match",
			body: "Parallel with #7",
			want: nil,
		},
		{
			name: "test 7: non-line-anchored mid-sentence text must not match",
			body: "See note: Depends on #5",
			want: nil,
		},
		{
			name: "test 8: multiple lines interspersed with non-matching lines, extracted in body order",
			body: "Related to #1\nDepends on #2\nParallel with #3\nsome ordinary prose\nDepends on #4\n",
			want: []int{2, 4},
		},
		{
			name: "test 9: no dependency lines at all yields nil",
			body: "Just a title\nand some ordinary body text.\n",
			want: nil,
		},
		{
			name: "test 10a: trailing punctuation tolerated",
			body: "Depends on #822.",
			want: []int{822},
		},
		{
			name: "test 10b: trailing prose/markdown tolerated",
			body: "Depends on #822, blocks everything",
			want: []int{822},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDependsOn(tc.body)
			if !equalInts(got, tc.want) {
				t.Errorf("parseDependsOn(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// -- #825: resolveDependencyStates (impure, fake gh) -------------------------

// TestResolveDependencyStates_OpenSetFastPathNeverShellsOut covers plan test
// 11: a number present in the pass's own collected open-issue set resolves
// DependencyStateOpen without ever invoking gh issue view -- the fake gh
// script is scripted to fail every invocation, so if the fast path were
// bypassed the result would be DependencyStateUnresolved, not
// DependencyStateOpen.
func TestResolveDependencyStates_OpenSetFastPathNeverShellsOut(t *testing.T) {
	installFakeGHOnPath(t, "exit 1\n")

	openNumbers := map[int]bool{822: true}
	got := resolveDependencyStates("o/r", []int{822}, openNumbers, map[int]DependencyState{}, &dependencyResolutionBudget{}, io.Discard)

	if got[822] != DependencyStateOpen {
		t.Fatalf("DependencyStates[822] = %q, want DependencyStateOpen (open-set fast path must never shell out)", got[822])
	}
}

// TestResolveDependencyStates_OutsideWindowOpenViaGhIssueView covers plan
// test 12: a number absent from the open set, with `gh issue view` reporting
// state OPEN, resolves DependencyStateOpen -- the outside-the-200-window-but-
// still-open case.
func TestResolveDependencyStates_OutsideWindowOpenViaGhIssueView(t *testing.T) {
	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue view") printf '{"number":99,"state":"OPEN"}' ;;
  *) exit 1 ;;
esac
`)

	got := resolveDependencyStates("o/r", []int{99}, map[int]bool{}, map[int]DependencyState{}, &dependencyResolutionBudget{}, io.Discard)
	if got[99] != DependencyStateOpen {
		t.Fatalf("DependencyStates[99] = %q, want DependencyStateOpen", got[99])
	}
}

// TestResolveDependencyStates_OutsideWindowClosedViaGhIssueView covers plan
// test 13: a number absent from the open set, with `gh issue view` reporting
// state CLOSED, resolves DependencyStateClosed.
func TestResolveDependencyStates_OutsideWindowClosedViaGhIssueView(t *testing.T) {
	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue view") printf '{"number":99,"state":"CLOSED"}' ;;
  *) exit 1 ;;
esac
`)

	got := resolveDependencyStates("o/r", []int{99}, map[int]bool{}, map[int]DependencyState{}, &dependencyResolutionBudget{}, io.Discard)
	if got[99] != DependencyStateClosed {
		t.Fatalf("DependencyStates[99] = %q, want DependencyStateClosed", got[99])
	}
}

// TestResolveDependencyStates_GhIssueViewNonzeroExit_Unresolved covers plan
// test 14: a nonzero `gh issue view` exit resolves DependencyStateUnresolved
// (fails closed), and logs the failure to out (#825 review fix #3) rather
// than swallowing it silently.
func TestResolveDependencyStates_GhIssueViewNonzeroExit_Unresolved(t *testing.T) {
	installFakeGHOnPath(t, "exit 1\n")

	var buf bytes.Buffer
	got := resolveDependencyStates("o/r", []int{99}, map[int]bool{}, map[int]DependencyState{}, &dependencyResolutionBudget{}, &buf)
	if got[99] != DependencyStateUnresolved {
		t.Fatalf("DependencyStates[99] = %q, want DependencyStateUnresolved", got[99])
	}
	if !strings.Contains(buf.String(), "dependency #99 resolution failed") {
		t.Fatalf("expected a logged failure detail for #99, got: %q", buf.String())
	}
}

// TestResolveDependencyStates_MalformedJSON_Unresolved covers plan test 15:
// malformed JSON from `gh issue view` resolves DependencyStateUnresolved
// (fails closed), not a decode panic or a silent misclassification -- and
// logs the decode failure to out (#825 review fix #3).
func TestResolveDependencyStates_MalformedJSON_Unresolved(t *testing.T) {
	installFakeGHOnPath(t, `
case "$1 $2" in
  "issue view") printf 'not valid json' ;;
  *) exit 1 ;;
esac
`)

	var buf bytes.Buffer
	got := resolveDependencyStates("o/r", []int{99}, map[int]bool{}, map[int]DependencyState{}, &dependencyResolutionBudget{}, &buf)
	if got[99] != DependencyStateUnresolved {
		t.Fatalf("DependencyStates[99] = %q, want DependencyStateUnresolved", got[99])
	}
	if !strings.Contains(buf.String(), "dependency #99 resolution failed") {
		t.Fatalf("expected a logged failure detail for #99, got: %q", buf.String())
	}
}

// TestResolveDependencyStates_PerRepoPassCacheDedupesGhIssueViewCalls covers
// plan test 16: the same absent number depended on by two different tickets
// within one collectRepoTickets call must invoke `gh issue view` exactly
// once, proving the caller-supplied per-repo-pass cache is used. Mirrors two
// tickets sharing one cache map, exactly as collectRepoTickets' per-issue
// loop will call resolveDependencyStates once per ticket against one shared
// depCache.
func TestResolveDependencyStates_PerRepoPassCacheDedupesGhIssueViewCalls(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGHOnPath(t, fmt.Sprintf(`
printf 'x' >> "%s"
case "$1 $2" in
  "issue view") printf '{"number":99,"state":"OPEN"}' ;;
  *) exit 1 ;;
esac
`, countFile))

	cache := map[int]DependencyState{}
	openNumbers := map[int]bool{}
	budget := &dependencyResolutionBudget{}

	got1 := resolveDependencyStates("o/r", []int{99}, openNumbers, cache, budget, io.Discard)
	got2 := resolveDependencyStates("o/r", []int{99}, openNumbers, cache, budget, io.Discard)

	if got1[99] != DependencyStateOpen || got2[99] != DependencyStateOpen {
		t.Fatalf("DependencyStates[99] = (%q, %q), want (DependencyStateOpen, DependencyStateOpen)", got1[99], got2[99])
	}

	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("gh issue view invoked %d time(s) for #99 across two resolveDependencyStates calls sharing one cache, want exactly 1", len(data))
	}
}

// TestResolveDependencyStates_CapBoundsGhIssueViewFallbackCalls covers the
// #825 review fix #1 regression: a set of distinct out-of-open-set numbers
// larger than maxDependencyResolutions must invoke the fake gh issue view at
// most maxDependencyResolutions times for one pass (one shared budget,
// mirroring one collectRepoTickets call), with every number beyond the cap
// resolving to DependencyStateUnresolved without a gh call, and the cap-hit
// line logged exactly once.
func TestResolveDependencyStates_CapBoundsGhIssueViewFallbackCalls(t *testing.T) {
	dir := t.TempDir()
	countFile := filepath.Join(dir, "calls.count")
	installFakeGHOnPath(t, fmt.Sprintf(`
printf 'x' >> "%s"
case "$1 $2" in
  "issue view") printf '{"number":1,"state":"OPEN"}' ;;
  *) exit 1 ;;
esac
`, countFile))

	const extra = 10
	nums := make([]int, maxDependencyResolutions+extra)
	for i := range nums {
		nums[i] = 1000 + i // all distinct, all absent from openNumbers
	}

	var buf bytes.Buffer
	budget := &dependencyResolutionBudget{}
	got := resolveDependencyStates("o/r", nums, map[int]bool{}, map[int]DependencyState{}, budget, &buf)

	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("reading call-count file: %v", err)
	}
	if len(data) != maxDependencyResolutions {
		t.Fatalf("gh issue view invoked %d time(s) for %d distinct out-of-window numbers, want exactly maxDependencyResolutions (%d)", len(data), len(nums), maxDependencyResolutions)
	}

	for i, n := range nums {
		want := DependencyStateOpen
		if i >= maxDependencyResolutions {
			want = DependencyStateUnresolved
		}
		if got[n] != want {
			t.Fatalf("DependencyStates[%d] (index %d) = %q, want %q", n, i, got[n], want)
		}
	}

	capMsg := fmt.Sprintf("dependency resolution cap (%d) reached for o/r", maxDependencyResolutions)
	if got := strings.Count(buf.String(), capMsg); got != 1 {
		t.Fatalf("cap-hit line %q logged %d time(s), want exactly 1: %q", capMsg, got, buf.String())
	}
}

// equalInts compares two int slices for equality, treating nil and an empty
// slice as equal -- parseDependsOn's "no dependency lines" case is documented
// as "nil/empty slice" (plan test 9), so the comparison must not distinguish
// between the two.
func equalInts(a, b []int) bool {
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
