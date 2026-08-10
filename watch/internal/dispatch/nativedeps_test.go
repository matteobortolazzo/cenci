package dispatch

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

// -- native blocked-by conversion --------------------------------------------

// TestNativeDependencies_StateComesInlineWithNoGhCalls pins the core property
// that motivates preferring native links over the "Depends on #N" prose path:
// each blockedBy node carries its own state, so conversion resolves every
// dependency with zero subprocesses. resolveDependencyStates' open-set,
// cache, and budget parameters are not even reachable from this path -- if a
// future change reintroduces a fallback call here, this test's nil budget
// would panic rather than silently burn the pass's gh budget.
func TestNativeDependencies_StateComesInlineWithNoGhCalls(t *testing.T) {
	conn := blockedByConnection{
		TotalCount: 2,
		Nodes: []blockedByNode{
			{Number: 101, URL: "https://github.com/o/r/issues/101", State: "OPEN"},
			{Number: 102, URL: "https://github.com/o/r/issues/102", State: "CLOSED"},
		},
	}
	nums, states, anomalies := nativeDependencies("o/r", conn)
	if want := []int{101, 102}; !reflect.DeepEqual(nums, want) {
		t.Fatalf("nums = %v, want %v", nums, want)
	}
	if got := states[101]; got != DependencyStateOpen {
		t.Errorf("#101 state = %q, want %q", got, DependencyStateOpen)
	}
	if got := states[102]; got != DependencyStateClosed {
		t.Errorf("#102 state = %q, want %q", got, DependencyStateClosed)
	}
	if len(anomalies) != 0 {
		t.Errorf("anomalies = %v, want none", anomalies)
	}
}

// TestNativeDependencies_LowercaseStateIsAccepted pins the case-insensitive
// normalization, matching what fetchDependencyState already does for `gh
// issue view`'s state field: the GraphQL enum is uppercase today, and a
// tightening to an exact "OPEN" match would silently reclassify every
// dependency as Unresolved and wedge the whole fleet.
func TestNativeDependencies_LowercaseStateIsAccepted(t *testing.T) {
	conn := blockedByConnection{
		TotalCount: 1,
		Nodes:      []blockedByNode{{Number: 7, URL: "https://github.com/o/r/issues/7", State: "open"}},
	}
	_, states, anomalies := nativeDependencies("o/r", conn)
	if got := states[7]; got != DependencyStateOpen {
		t.Errorf("state = %q, want %q", got, DependencyStateOpen)
	}
	if len(anomalies) != 0 {
		t.Errorf("anomalies = %v, want none", anomalies)
	}
}

// TestNativeDependencies_UnrecognizedStateFailsClosed pins that an unknown
// IssueState value resolves Unresolved (which blocks dispatch), never Closed
// (which would release it) -- watch/docs/error-handling.md's default-deny
// rule, and the same direction fetchDependencyState's default branch takes.
func TestNativeDependencies_UnrecognizedStateFailsClosed(t *testing.T) {
	conn := blockedByConnection{
		TotalCount: 1,
		Nodes:      []blockedByNode{{Number: 7, URL: "https://github.com/o/r/issues/7", State: "TRIAGED"}},
	}
	_, states, _ := nativeDependencies("o/r", conn)
	if got := states[7]; got != DependencyStateUnresolved {
		t.Errorf("state = %q, want %q", got, DependencyStateUnresolved)
	}
}

// TestNativeDependencies_CrossRepoLinkBecomesAnomalyNotAMisKeyedNumber is the
// regression guard for the collision this gate's int-keyed representation
// would otherwise hit: GitHub permits a blocked-by link into another repo of
// the same org, and issue #5 over there is a different issue from issue #5
// here. Recording the bare number would let a *local* #5's state decide
// whether a *remote* #5 blocks. The link must instead land in the anomaly
// channel (fail closed) and never appear in nums at all.
func TestNativeDependencies_CrossRepoLinkBecomesAnomalyNotAMisKeyedNumber(t *testing.T) {
	conn := blockedByConnection{
		TotalCount: 2,
		Nodes: []blockedByNode{
			{Number: 5, URL: "https://github.com/other/repo/issues/5", State: "OPEN"},
			{Number: 9, URL: "https://github.com/o/r/issues/9", State: "CLOSED"},
		},
	}
	nums, states, anomalies := nativeDependencies("o/r", conn)
	if want := []int{9}; !reflect.DeepEqual(nums, want) {
		t.Fatalf("nums = %v, want %v (cross-repo #5 must not be keyed by bare number)", nums, want)
	}
	if _, ok := states[5]; ok {
		t.Errorf("states must not contain a cross-repo number: %v", states)
	}
	if len(anomalies) != 1 {
		t.Fatalf("anomalies = %v, want exactly one", anomalies)
	}
	if !strings.Contains(anomalies[0], "other/repo") {
		t.Errorf("anomaly %q must name the offending URL", anomalies[0])
	}
}

// TestNativeDependencies_GheHostIsTreatedAsSameRepo pins that the same-repo
// check compares the URL path only, never the host: a GitHub Enterprise
// Server deployment serves the identical path shape from its own hostname,
// and rejecting it would make every native dependency on GHE an anomaly and
// permanently wedge dispatch there.
func TestNativeDependencies_GheHostIsTreatedAsSameRepo(t *testing.T) {
	conn := blockedByConnection{
		TotalCount: 1,
		Nodes:      []blockedByNode{{Number: 4, URL: "https://ghe.corp.example/o/r/issues/4", State: "OPEN"}},
	}
	nums, _, anomalies := nativeDependencies("o/r", conn)
	if want := []int{4}; !reflect.DeepEqual(nums, want) {
		t.Fatalf("nums = %v, want %v", nums, want)
	}
	if len(anomalies) != 0 {
		t.Errorf("anomalies = %v, want none", anomalies)
	}
}

// TestNativeDependencies_URLNumberMismatchBecomesAnomaly pins that the number
// and the URL must agree. They come from the same node so a mismatch means
// the payload is not what this code thinks it is; trusting the bare number
// then risks keying against an issue the URL says it is not.
func TestNativeDependencies_URLNumberMismatchBecomesAnomaly(t *testing.T) {
	conn := blockedByConnection{
		TotalCount: 1,
		Nodes:      []blockedByNode{{Number: 5, URL: "https://github.com/o/r/issues/77", State: "OPEN"}},
	}
	nums, _, anomalies := nativeDependencies("o/r", conn)
	if nums != nil {
		t.Fatalf("nums = %v, want nil", nums)
	}
	if len(anomalies) != 1 {
		t.Fatalf("anomalies = %v, want exactly one", anomalies)
	}
}

// TestNativeDependencies_TruncatedListBecomesAnomaly pins the partial-page
// guard: gh requests blockedBy(first:50) against GitHub's own 50-link cap, so
// a TotalCount above the node count means blockers exist that this pass
// cannot see. Gating on a knowingly incomplete blocker set would dispatch a
// ticket whose unseen blocker is still open, so the shortfall fails closed.
func TestNativeDependencies_TruncatedListBecomesAnomaly(t *testing.T) {
	conn := blockedByConnection{
		TotalCount: 51,
		Nodes:      []blockedByNode{{Number: 3, URL: "https://github.com/o/r/issues/3", State: "CLOSED"}},
	}
	nums, _, anomalies := nativeDependencies("o/r", conn)
	if want := []int{3}; !reflect.DeepEqual(nums, want) {
		t.Errorf("nums = %v, want %v (the visible link is still usable)", nums, want)
	}
	if len(anomalies) != 1 {
		t.Fatalf("anomalies = %v, want exactly one", anomalies)
	}
	if !strings.Contains(anomalies[0], "51") {
		t.Errorf("anomaly %q must name the expected total", anomalies[0])
	}
}

// TestNativeDependencies_NoLinksIsTheUngatedZeroValue pins that an issue with
// no native links yields the documented nil "ungated" zero value rather than
// empty non-nil collections, keeping every pre-existing Ticket construction
// site's behavior byte-identical.
func TestNativeDependencies_NoLinksIsTheUngatedZeroValue(t *testing.T) {
	nums, states, anomalies := nativeDependencies("o/r", blockedByConnection{})
	if nums != nil || states != nil || anomalies != nil {
		t.Fatalf("got (%v, %v, %v), want all nil", nums, states, anomalies)
	}
}

// -- merge of the native and prose gate inputs -------------------------------

// TestMergeDependencies_UnionGatesOnBothSources is the dual-read contract:
// during the migration a ticket may declare blockers natively, in prose, or
// both, and dispatch must be gated on the union. A regression that honored
// only one source would silently release tickets blocked by the other.
func TestMergeDependencies_UnionGatesOnBothSources(t *testing.T) {
	nativeStates := map[int]DependencyState{10: DependencyStateOpen}
	nums, states := mergeDependencies(
		[]int{10}, nativeStates, []int{20},
		func(nums []int) map[int]DependencyState {
			return map[int]DependencyState{20: DependencyStateClosed}
		})
	if want := []int{10, 20}; !reflect.DeepEqual(nums, want) {
		t.Fatalf("nums = %v, want %v", nums, want)
	}
	if states[10] != DependencyStateOpen || states[20] != DependencyStateClosed {
		t.Errorf("states = %v, want #10 open and #20 closed", states)
	}
}

// TestMergeDependencies_NumberDeclaredBothWaysCostsNoProseResolution pins the
// efficiency property the merge exists to enforce: a number already carrying
// an inline native state must never be handed to the prose resolver, which
// would spend a `gh issue view` call and a slot of the pass-wide
// maxDependencyResolutions budget re-deriving a state already in hand.
func TestMergeDependencies_NumberDeclaredBothWaysCostsNoProseResolution(t *testing.T) {
	var resolved [][]int
	nums, states := mergeDependencies(
		[]int{10}, map[int]DependencyState{10: DependencyStateOpen}, []int{10},
		func(nums []int) map[int]DependencyState {
			resolved = append(resolved, nums)
			return nil
		})
	if len(resolved) != 0 {
		t.Errorf("prose resolver called with %v, want no call at all", resolved)
	}
	if want := []int{10}; !reflect.DeepEqual(nums, want) {
		t.Errorf("nums = %v, want %v (no duplicate)", nums, want)
	}
	if states[10] != DependencyStateOpen {
		t.Errorf("states[10] = %q, want native state to win", states[10])
	}
}

// TestMergeDependencies_ProseResolverSeesOnlyLeftovers pins that the callback
// receives exactly the prose-only numbers -- the property the previous test
// checks for the fully-overlapping case, here for a partial overlap.
func TestMergeDependencies_ProseResolverSeesOnlyLeftovers(t *testing.T) {
	var resolved [][]int
	mergeDependencies(
		[]int{10}, map[int]DependencyState{10: DependencyStateOpen}, []int{10, 20, 30},
		func(nums []int) map[int]DependencyState {
			resolved = append(resolved, nums)
			return map[int]DependencyState{20: DependencyStateClosed, 30: DependencyStateClosed}
		})
	if len(resolved) != 1 {
		t.Fatalf("prose resolver called %d times, want 1", len(resolved))
	}
	if want := []int{20, 30}; !reflect.DeepEqual(resolved[0], want) {
		t.Errorf("resolver got %v, want %v", resolved[0], want)
	}
}

// TestMergeDependencies_DuplicateProseNumbersAreDeduped guards the resolver
// against being handed the same number twice when a body repeats a line.
func TestMergeDependencies_DuplicateProseNumbersAreDeduped(t *testing.T) {
	var resolved [][]int
	nums, _ := mergeDependencies(nil, nil, []int{20, 20},
		func(nums []int) map[int]DependencyState {
			resolved = append(resolved, nums)
			return map[int]DependencyState{20: DependencyStateOpen}
		})
	if want := []int{20}; !reflect.DeepEqual(nums, want) {
		t.Errorf("nums = %v, want %v", nums, want)
	}
	if want := []int{20}; !reflect.DeepEqual(resolved[0], want) {
		t.Errorf("resolver got %v, want %v", resolved[0], want)
	}
}

// TestMergeDependencies_NeitherSourceIsTheUngatedZeroValue pins that a ticket
// with no dependencies at all keeps the nil "ungated" zero value and never
// invokes the prose resolver.
func TestMergeDependencies_NeitherSourceIsTheUngatedZeroValue(t *testing.T) {
	called := false
	nums, states := mergeDependencies(nil, nil, nil, func([]int) map[int]DependencyState {
		called = true
		return nil
	})
	if nums != nil || states != nil {
		t.Fatalf("got (%v, %v), want both nil", nums, states)
	}
	if called {
		t.Error("prose resolver must not be called when there are no dependencies")
	}
}

// -- the gate's view of a native anomaly -------------------------------------

// TestDependencyGateSkip_NativeAnomaly_SkipsWithItsOwnDistinctReason pins that
// a native anomaly blocks dispatch AND is reported through its own reason
// format, not folded into the prose path's "malformed" wording. The two are
// different failure classes with different operator fixes, and collapsing
// distinct classes into one bucket is what #446/#598 forbid.
func TestDependencyGateSkip_NativeAnomaly_SkipsWithItsOwnDistinctReason(t *testing.T) {
	reason, gated := dependencyGateSkip(Ticket{
		NativeDependencyAnomalies: []string{"blocked-by link outside o/r: https://github.com/other/repo/issues/5"},
	})
	if !gated {
		t.Fatal("a native dependency anomaly must gate dispatch, not release it")
	}
	if !strings.Contains(reason, "native dependency unusable") {
		t.Errorf("reason = %q, want the native-specific format", reason)
	}
	if strings.Contains(reason, "malformed") {
		t.Errorf("reason = %q must not reuse the prose path's malformed wording", reason)
	}
}

// TestDependencyGateSkip_NativeAnomalyGatesEvenWhenEveryNumberIsClosed is the
// regression guard that matters most for the cross-repo case: the visible,
// same-repo blockers can all be closed while the unusable link is still open,
// so the anomaly must be checked independently of the numeric loop's verdict.
func TestDependencyGateSkip_NativeAnomalyGatesEvenWhenEveryNumberIsClosed(t *testing.T) {
	_, gated := dependencyGateSkip(Ticket{
		DependsOn:                 []int{9},
		DependencyStates:          map[int]DependencyState{9: DependencyStateClosed},
		NativeDependencyAnomalies: []string{"blocked-by link outside o/r: https://github.com/other/repo/issues/5"},
	})
	if !gated {
		t.Fatal("an unusable native link must gate even when every resolved dependency is closed")
	}
}

// -- gh version floor --------------------------------------------------------

// TestIsUnknownGhJSONField pins the detection of a gh predating the blockedBy
// field, which is what turns an opaque subprocess failure into an actionable
// "upgrade gh" message. Matching must be case-insensitive and must not fire
// on unrelated gh failures, which have their own diagnostics.
func TestIsUnknownGhJSONField(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "gh's own unknown-field error",
			stderr: "Unknown JSON field: \"blockedBy\"\nAvailable fields:\n  assignees\n  author\n",
			want:   true,
		},
		{
			name:   "lowercased variant",
			stderr: "unknown json field: \"blockedBy\"",
			want:   true,
		},
		{
			name:   "an unrelated auth failure must not be reported as a version problem",
			stderr: "gh: Bad credentials (HTTP 401)",
			want:   false,
		},
		{
			name:   "an unrelated rate-limit failure",
			stderr: "gh: API rate limit exceeded",
			want:   false,
		},
		{name: "empty stderr", stderr: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnknownGhJSONField(tc.stderr); got != tc.want {
				t.Errorf("isUnknownGhJSONField(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

// -- collector-level dual read -----------------------------------------------

// TestCollectRepoTickets_NativeBlockedByGatesWithoutAnyGhIssueViewCall is the
// end-to-end proof of the native path through the real collector: the fake gh
// answers `issue list` (with a blockedBy payload) and `api graphql`, but
// deliberately exits nonzero for anything else -- so if the collector were to
// fall back to `gh issue view` for a natively-linked number, the dependency
// would resolve Unresolved and this test would fail rather than silently
// costing an API call per dependency in production.
func TestCollectRepoTickets_NativeBlockedByGatesWithoutAnyGhIssueViewCall(t *testing.T) {
	installFakeGH(t, `
case "$1 $2" in
  "issue list") printf '[{"number":10,"title":"First","body":"","blockedBy":{"totalCount":1,"nodes":[{"number":99,"url":"https://github.com/o/r/issues/99","state":"OPEN"}]}}]' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  *) exit 1 ;;
esac
`)

	tickets, err := collectRepoTickets(RepoConfig{Repo: "o/r"}, MainSyncSkipped, true, io.Discard)
	if err != nil {
		t.Fatalf("collectRepoTickets returned unexpected error: %v", err)
	}
	if len(tickets) != 1 {
		t.Fatalf("got %d tickets, want 1: %+v", len(tickets), tickets)
	}
	if !equalInts(tickets[0].DependsOn, []int{99}) {
		t.Fatalf("DependsOn = %v, want [99]", tickets[0].DependsOn)
	}
	if got := tickets[0].DependencyStates[99]; got != DependencyStateOpen {
		t.Errorf("DependencyStates[99] = %q, want DependencyStateOpen (inline, no gh issue view)", got)
	}
	if _, gated := dependencyGateSkip(tickets[0]); !gated {
		t.Error("a ticket blocked by an open native link must be gated")
	}
}

// TestCollectRepoTickets_NativeAndProseAreUnioned is the dual-read contract at
// the collector level: a ticket carrying BOTH a native link and a legacy
// prose line is gated on both, so migrating the writers to native links
// cannot silently un-gate in-flight tickets that still carry prose.
func TestCollectRepoTickets_NativeAndProseAreUnioned(t *testing.T) {
	installFakeGH(t, `
case "$1 $2" in
  "issue list") printf '[{"number":10,"title":"First","body":"Depends on #11","blockedBy":{"totalCount":1,"nodes":[{"number":12,"url":"https://github.com/o/r/issues/12","state":"CLOSED"}]}},{"number":11,"title":"Second","body":""}]' ;;
  "api graphql") printf '`+emptyOpenPRPageJSON+`' ;;
  *) exit 1 ;;
esac
`)

	tickets, err := collectRepoTickets(RepoConfig{Repo: "o/r"}, MainSyncSkipped, true, io.Discard)
	if err != nil {
		t.Fatalf("collectRepoTickets returned unexpected error: %v", err)
	}
	var ten *Ticket
	for i := range tickets {
		if tickets[i].Number == 10 {
			ten = &tickets[i]
		}
	}
	if ten == nil {
		t.Fatalf("ticket #10 not found: %+v", tickets)
	}
	// Native first, then the prose leftover.
	if !equalInts(ten.DependsOn, []int{12, 11}) {
		t.Fatalf("DependsOn = %v, want [12 11] (native then prose)", ten.DependsOn)
	}
	if got := ten.DependencyStates[12]; got != DependencyStateClosed {
		t.Errorf("native #12 state = %q, want closed", got)
	}
	if got := ten.DependencyStates[11]; got != DependencyStateOpen {
		t.Errorf("prose #11 state = %q, want open (open-set fast path)", got)
	}
	// #12 is closed but #11 (prose) is open, so the ticket must still hold.
	if _, gated := dependencyGateSkip(*ten); !gated {
		t.Error("the legacy prose dependency must still gate after the native migration")
	}
}

// TestCollectRepoTickets_OldGhUnknownFieldFailsWithUpgradeGuidance pins that a
// gh predating the blockedBy field produces a named, actionable failure
// rather than either an opaque subprocess error or -- far worse -- a silent
// retry that drops every native link from the gate.
func TestCollectRepoTickets_OldGhUnknownFieldFailsWithUpgradeGuidance(t *testing.T) {
	installFakeGH(t, `
case "$1 $2" in
  "issue list") printf 'Unknown JSON field: "blockedBy"\nAvailable fields:\n  assignees\n' >&2; exit 1 ;;
  *) exit 1 ;;
esac
`)

	_, err := collectRepoTickets(RepoConfig{Repo: "o/r"}, MainSyncSkipped, true, io.Discard)
	if err == nil {
		t.Fatal("expected an error when gh does not know the blockedBy field")
	}
	if !strings.Contains(err.Error(), minGhVersionNativeDeps) {
		t.Errorf("error %q must name the required gh version %s", err, minGhVersionNativeDeps)
	}
}
