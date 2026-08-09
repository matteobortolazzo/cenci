package dispatch

import (
	"net/url"
	"strconv"
	"strings"
)

// minGhVersionNativeDeps is the first gh release exposing issue dependencies
// (`blockedBy`/`blocking`) as `--json` fields on `gh issue list`/`gh issue
// view`, alongside the `--blocked-by`/`--add-blocked-by` write flags the flow
// skills use. Named in collectRepoTickets' unknown-field error so an operator
// running an older gh gets an actionable message instead of a bare
// "Unknown JSON field" from a subprocess they did not know was involved.
const minGhVersionNativeDeps = "2.94.0"

// isUnknownGhJSONField reports whether stderr is gh rejecting a `--json`
// field name it does not know, which is how a gh older than
// minGhVersionNativeDeps refuses the blockedBy field. Matched on gh's own
// error prefix, case-insensitively; gh follows it with an "Available fields:"
// list, so the match is deliberately anchored to the prefix alone rather than
// to the field name (which appears again in that list).
func isUnknownGhJSONField(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "unknown json field")
}

// blockedByNode mirrors one node of the `blockedBy` field `gh issue list
// --json blockedBy` emits for GitHub's native issue-dependency relationship
// ("Blocked by" in the issue sidebar's Relationships section). gh's exporter
// (cli/cli api/export_pr.go) emits exactly id/number/title/url/state per node;
// only the three fields this gate actually needs are decoded here.
//
// State is the GraphQL IssueState enum ("OPEN"/"CLOSED"), matched
// case-insensitively by nativeDependencyState -- the same normalization
// fetchDependencyState already applies to `gh issue view`'s state field.
//
// URL is load-bearing, not decoration: see nativeDependencies' cross-repo
// rationale.
type blockedByNode struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// blockedByConnection is the GraphQL connection wrapper gh emits around
// blockedByNode. gh's query requests `blockedBy(first:50)` -- exactly
// GitHub's own documented 50-links-per-relationship-type cap -- so Nodes is
// expected to hold every link. TotalCount is still compared against
// len(Nodes) by nativeDependencies so that if GitHub ever raises that cap
// past what gh pages in, the shortfall fails closed rather than silently
// under-reporting a ticket's blockers.
type blockedByConnection struct {
	Nodes      []blockedByNode `json:"nodes"`
	TotalCount int             `json:"totalCount"`
}

// nativeDependencies converts one issue's native `blockedBy` connection into
// the same (numbers, states, anomalies) triple parseDependsOn +
// resolveDependencyStates produce for the legacy `Depends on #N` prose path,
// so collectRepoTickets can merge the two into one gate input.
//
// Unlike the prose path this makes ZERO gh calls: each node carries its own
// state inline, already fetched by the single `gh issue list` call that
// collected the ticket. There is therefore no open-set fast path, no
// per-pass cache, and no resolution budget to consult -- the entire
// maxDependencyResolutions/dependencyResolutionBudget apparatus
// (dependency.go) exists only because the prose path has to resolve bare
// numbers after the fact.
//
// anomalies records, in node order, one entry per link that cannot be
// expressed as a same-repo issue number -- fed to decide.go's
// dependencyGateSkip as a distinct fail-closed gate class (mirroring #852's
// treatment of malformed prose references) rather than silently dropped:
//
//   - A cross-repo link. GitHub allows blocked-by links to issues in other
//     repositories of the same organization, but Ticket.DependsOn and
//     Ticket.DependencyStates are keyed by bare int number, which is only
//     meaningful within one repo. Keying a cross-repo dependency by its bare
//     number would collide with -- and silently take the state of -- a
//     same-numbered issue in this repo, so such a link is refused rather
//     than mis-keyed. Detected by URL rather than by number precisely
//     because the number alone cannot distinguish the two cases.
//   - A TotalCount exceeding the number of nodes actually returned, i.e. gh
//     paged in fewer links than exist. Gating on a knowingly partial blocker
//     list would let a ticket dispatch while an unseen blocker is still open.
//
// Each anomaly token is truncated to maxDependencyTokenBytes, mirroring
// parseDependsOn: a node URL is attacker-influenceable on a public repo and
// must never flood memory or a downstream one-record-per-line log.
func nativeDependencies(repo string, conn blockedByConnection) (nums []int, states map[int]DependencyState, anomalies []string) {
	if missing := conn.TotalCount - len(conn.Nodes); missing > 0 {
		anomalies = append(anomalies, truncateDetail(
			"blocked-by list incomplete: "+strconv.Itoa(len(conn.Nodes))+" of "+strconv.Itoa(conn.TotalCount)+" links returned",
			maxDependencyTokenBytes))
	}
	if len(conn.Nodes) == 0 {
		return nil, nil, anomalies
	}
	nums = make([]int, 0, len(conn.Nodes))
	states = make(map[int]DependencyState, len(conn.Nodes))
	for _, n := range conn.Nodes {
		if n.Number <= 0 || !sameRepoIssueURL(repo, n.Number, n.URL) {
			anomalies = append(anomalies, truncateDetail("blocked-by link outside "+repo+": "+n.URL, maxDependencyTokenBytes))
			continue
		}
		nums = append(nums, n.Number)
		states[n.Number] = nativeDependencyState(n.State)
	}
	if len(nums) == 0 {
		// Never hand back a non-nil-but-empty pair: empty/nil DependsOn is
		// the documented "ungated" zero value (types.go), and an empty
		// non-nil states map alongside it would be indistinguishable in
		// behavior but distinguishable under reflect.DeepEqual in tests.
		return nil, nil, anomalies
	}
	return nums, states, anomalies
}

// nativeDependencyState maps a blockedBy node's GraphQL IssueState onto the
// DependencyState closed set. An unrecognized value fails closed to
// DependencyStateUnresolved rather than being assumed closed, matching
// fetchDependencyState's default branch -- but needs no log line here,
// because unlike the prose path there was no failed subprocess to diagnose:
// decide.go reports the resulting gate skip with the number itself.
func nativeDependencyState(state string) DependencyState {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "OPEN":
		return DependencyStateOpen
	case "CLOSED":
		return DependencyStateClosed
	default:
		return DependencyStateUnresolved
	}
}

// sameRepoIssueURL reports whether rawURL is the canonical issue URL for
// issue n in repo ("owner/name"). It compares only the parsed path, never
// the host, so a GitHub Enterprise Server deployment (any hostname) is
// handled identically to github.com -- while still pinning owner, repo, and
// number, which is the whole point: a cross-repo blocked-by link must not be
// keyed by its bare number (see nativeDependencies).
//
// A URL that fails to parse is treated as not-same-repo, so it lands in the
// anomaly path and fails closed rather than being assumed local.
func sameRepoIssueURL(repo string, n int, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Path == "/"+repo+"/issues/"+strconv.Itoa(n)
}

// mergeDependencies unions the native blocked-by gate input with the legacy
// `Depends on #N` prose gate input for one ticket, which is what makes the
// migration to native dependencies safe for in-flight work: a ticket whose
// body still carries a prose line keeps being gated, a ticket using native
// links is gated, and a ticket carrying both is gated on the union.
//
// Native numbers come first and their states WIN on collision. That ordering
// is deliberate on both counts: native state was fetched inline with the
// ticket itself (no extra call, no staleness relative to the prose path's
// separate `gh issue view`), and a number already carrying a native state
// must not be handed to the prose resolver at all -- resolveProse is invoked
// only for the numbers left over, so declaring a dependency both ways costs
// zero additional gh calls and zero additional resolution budget.
//
// resolveProse is a callback rather than a resolved map so that this
// leftover-only property is enforced here, at the merge, instead of relying
// on every caller to pre-filter correctly. It is not called at all when no
// prose-only numbers remain.
func mergeDependencies(
	nativeNums []int,
	nativeStates map[int]DependencyState,
	proseNums []int,
	resolveProse func(nums []int) map[int]DependencyState,
) (nums []int, states map[int]DependencyState) {
	leftover := make([]int, 0, len(proseNums))
	seen := make(map[int]bool, len(nativeNums)+len(proseNums))
	for _, n := range nativeNums {
		seen[n] = true
	}
	for _, n := range proseNums {
		if seen[n] {
			continue
		}
		seen[n] = true
		leftover = append(leftover, n)
	}
	if len(nativeNums) == 0 && len(leftover) == 0 {
		return nil, nil
	}

	nums = make([]int, 0, len(nativeNums)+len(leftover))
	nums = append(nums, nativeNums...)
	nums = append(nums, leftover...)

	states = make(map[int]DependencyState, len(nums))
	for n, s := range nativeStates {
		states[n] = s
	}
	if len(leftover) > 0 {
		for n, s := range resolveProse(leftover) {
			// Guarded rather than assigned blind: resolveProse is only ever
			// given leftover numbers, so it cannot legitimately return a
			// native key -- but if a future caller widens it, native state
			// must still win, as documented above.
			if _, ok := states[n]; !ok {
				states[n] = s
			}
		}
	}
	return nums, states
}
