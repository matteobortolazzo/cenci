package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dependsOnPattern matches a line-anchored, case-insensitive "Depends on #N"
// reference (#825), tolerant of an optional leading list marker ("- " or
// "* ") and arbitrary trailing text after the number (a trailing
// parenthetical, punctuation, or prose). It deliberately does NOT match
// "Related to #N" or "Parallel with #N" -- only the literal "depends on"
// phrase -- and, being line-anchored (the (?m) flag makes ^ match at the
// start of every line, not just the start of the string), never matches a
// mid-sentence, non-line-anchored occurrence.
var dependsOnPattern = regexp.MustCompile(`(?im)^(?:[-*]\s+)?depends on #(\d+)`)

// maxDependencyResolutions caps how many `gh issue view` fallback calls one
// collectRepoTickets pass (one repo, one pass) may make (#825 review fix
// #1). Without a cap, a hostile/large issue body with many distinct
// "Depends on #N" references -- or many tickets each depending on many
// distinct out-of-window numbers -- could spawn unbounded gh subprocesses /
// GitHub API calls per pass, stalling the dispatch daemon and exhausting the
// operator's gh rate limit. 50 is a defensible round number: comfortably
// above any legitimate ticket's real dependency count, while bounding
// worst-case fan-out to a small, cheap-to-audit number of subprocess spawns
// per pass. Once the cap is reached, any further cache-miss number resolves
// directly to DependencyStateUnresolved without shelling out -- still fails
// closed (blocks dispatch), it just stops making gh calls.
const maxDependencyResolutions = 50

// ghIssueViewTimeout bounds every individual `gh issue view` fallback call
// the dependency resolver makes (#825 review fix #2), mirroring gitTimeout's
// rationale (mainsync.go): a hung network call must never stall a dispatch
// pass indefinitely.
const ghIssueViewTimeout = 60 * time.Second

// ghIssueViewWaitDelay bounds how long cmd.Wait can block *after* the gh
// process itself has exited or been killed by ghIssueViewTimeout's context.
// Without it, a grandchild process that inherited the stdout/stderr pipes
// could keep those pipes open and stall CombinedOutput indefinitely even
// though gh itself is gone -- defeating ghIssueViewTimeout's "can never
// stall a dispatch pass indefinitely" guarantee, mirroring gitWaitDelay
// (mainsync.go).
const ghIssueViewWaitDelay = 5 * time.Second

// parseDependsOn extracts every "Depends on #N" reference from body, in body
// order. Pure -- no I/O. Returns nil when body contains no such line (plan
// test 9); duplicates are preserved in encounter order (the caller,
// resolveDependencyStates, tolerates a duplicate number).
func parseDependsOn(body string) []int {
	matches := dependsOnPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	nums := make([]int, 0, len(matches))
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue // unreachable: \d+ only matches digits
		}
		nums = append(nums, n)
	}
	return nums
}

// dependencyResolutionBudget bounds and tracks one collectRepoTickets pass's
// total `gh issue view` fallback calls for a single repo (#825 review fix
// #1). It is created once per collectRepoTickets call -- the same
// lifetime/scope as the caller's depCache -- and shared across every
// resolveDependencyStates call in that pass so the cap applies to the whole
// pass, not per-ticket. logged tracks whether the cap-hit line has already
// been emitted this pass, so hitting the cap logs exactly once regardless of
// how many additional cache-miss numbers follow.
type dependencyResolutionBudget struct {
	calls  int
	logged bool
}

// resolveDependencyStates resolves the openness of every number in nums for
// repo (#825). openNumbers is the pass's own already-collected open-issue set
// for repo (the fast path: a hit there is immediately DependencyStateOpen,
// with no gh call at all). cache is a caller-supplied, per-repo-per-pass map
// memoizing every gh issue view fallback call, so N tickets depending on the
// same out-of-window number cost exactly one gh issue view call, not N.
// budget bounds and tracks the total gh issue view fallback calls made
// across the whole pass (#825 review fix #1). out receives one log line if a
// gh issue view call or its JSON decode fails, or if the pass's resolution
// cap is reached.
func resolveDependencyStates(repo string, nums []int, openNumbers map[int]bool, cache map[int]DependencyState, budget *dependencyResolutionBudget, out io.Writer) map[int]DependencyState {
	states := make(map[int]DependencyState, len(nums))
	for _, n := range nums {
		states[n] = resolveDependencyState(repo, n, openNumbers, cache, budget, out)
	}
	return states
}

// resolveDependencyState resolves one number's state, consulting the
// open-set fast path first, then the shared cache, falling back to
// fetchDependencyState (gh issue view) only on a genuine cache miss -- and
// only while budget's pass-wide call cap has not yet been reached (#825
// review fix #1). Once the cap is reached, any further cache-miss number
// resolves directly to DependencyStateUnresolved without shelling out, and
// the cap-hit line is logged exactly once for this pass via budget.logged.
func resolveDependencyState(repo string, n int, openNumbers map[int]bool, cache map[int]DependencyState, budget *dependencyResolutionBudget, out io.Writer) DependencyState {
	if openNumbers[n] {
		return DependencyStateOpen
	}
	if s, ok := cache[n]; ok {
		return s
	}
	if budget.calls >= maxDependencyResolutions {
		if !budget.logged {
			logf(out, "dispatch: dependency resolution cap (%d) reached for %s, remaining unresolved\n", maxDependencyResolutions, repo)
			budget.logged = true
		}
		cache[n] = DependencyStateUnresolved
		return DependencyStateUnresolved
	}
	budget.calls++
	s := fetchDependencyState(repo, n, out)
	cache[n] = s
	return s
}

// fetchDependencyState shells out to `gh issue view` for issue n in repo,
// classifying the result into the DependencyState closed set. A nonzero exit
// or malformed JSON fails closed to DependencyStateUnresolved rather than
// assuming closed or open -- and logs one line to out, naming repo (#825
// review round 2 fix #4: an issue number alone is ambiguous across a
// multi-repo pass) with the failure detail so a broadly failing gh (expired
// token, rate limit, gh not installed) is diagnosable rather than a silent
// blind spot (#825 review fix #3). The call is bounded by
// ghIssueViewTimeout/ghIssueViewWaitDelay (#825 review fix #2) so a hung
// network call can never stall a dispatch pass indefinitely; a
// context-deadline kill surfaces as an exec error here and falls through to
// the same fail-closed DependencyStateUnresolved path.
//
// stdout and stderr are captured into separate buffers rather than via
// CombinedOutput (#825 review round 2 fix #2): a benign stderr diagnostic on
// an otherwise-successful (exit 0) call would otherwise get merged into the
// bytes decoded as JSON, corrupting the payload and silently misclassifying
// the result as Unresolved. Only stdout is decoded; stderr (collapsed
// alongside stdout for the log line, mirroring syncMains' newline-collapse
// convention, #825 review round 2 fix #3) is diagnostic detail only.
func fetchDependencyState(repo string, n int, out io.Writer) DependencyState {
	ctx, cancel := context.WithTimeout(context.Background(), ghIssueViewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "view", strconv.Itoa(n),
		"--repo", repo, "--json", "state,number")
	cmd.WaitDelay = ghIssueViewWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		detail := collapseLines(stdout.String() + stderr.String())
		logf(out, "dispatch: dependency #%d resolution failed for %s: %v (%s)\n", n, repo, err, detail)
		return DependencyStateUnresolved
	}
	var res struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		detail := collapseLines(stdout.String() + stderr.String())
		logf(out, "dispatch: dependency #%d resolution failed for %s: %v (%s)\n", n, repo, err, detail)
		return DependencyStateUnresolved
	}
	switch strings.ToUpper(res.State) {
	case "OPEN":
		return DependencyStateOpen
	case "CLOSED":
		return DependencyStateClosed
	default:
		logf(out, "dispatch: dependency #%d resolution for %s returned unrecognized state %q\n", n, repo, res.State)
		return DependencyStateUnresolved
	}
}

// collapseLines replaces every newline in s with "; ", mirroring syncMains'
// convention (mainsync.go) so a multi-line gh diagnostic never breaks
// downstream one-record-per-line log parsing (lazyboards) (#825 review
// round 2 fix #3).
func collapseLines(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "; ")
}
