package babysit

// #1475: the head-pinned, non-force branch update for a clean BEHIND PR. These
// fixtures drive the real Run loop over the ciCapEnv harness (ci_cap_lifecycle_test.go)
// with a fake GitHub that also answers the update's two GraphQL calls (the
// preflight read and the updatePullRequestBranch mutation) and records each
// one. A tick's `pr view` is served from a per-tick queue, so a head or base
// transition is just the next queue entry. saved[k] (after(k)) is the state
// after tick k. Every test also gets a command-log check at cleanup: no
// force-push, hard reset, stash, discard or `gh pr update-branch`/--rebase ever
// appears in a git or gh invocation.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/capacity"
)

// -- the PR views a tick reads -----------------------------------------------

type buSpec struct {
	mergeable, mergeState, baseRef string
	cross                          bool
	closing                        []int
}

// buView renders a `gh pr view` body at head and base (baseRefOid): by default
// an open, MERGEABLE PR that is BEHIND its base `main`.
func buView(head, base string, mods ...func(*buSpec)) string {
	sp := buSpec{mergeable: "MERGEABLE", mergeState: "BEHIND", baseRef: "main"}
	for _, m := range mods {
		m(&sp)
	}
	refs := make([]string, 0, len(sp.closing))
	for _, n := range sp.closing {
		refs = append(refs, fmt.Sprintf(`{"number":%d}`, n))
	}
	return fmt.Sprintf(`{"number":42,"title":"Change","state":"OPEN","headRefName":"feature","headRefOid":%q,"baseRefOid":%q,"isCrossRepository":%t,"baseRefName":%q,"mergeable":%q,"mergeStateStatus":%q,"isDraft":false,"changedFiles":1,"additions":5,"deletions":2,"files":[{"path":"watch/internal/babysit/x.go"}],"url":"https://example/pr/42","closingIssuesReferences":[%s]}`,
		head, base, sp.cross, sp.baseRef, sp.mergeable, sp.mergeState, strings.Join(refs, ","))
}

func buClean(head, base string, mods ...func(*buSpec)) string {
	return buView(head, base, append([]func(*buSpec){buState("CLEAN")}, mods...)...)
}

// buState sets mergeStateStatus; DIRTY pairs with mergeable CONFLICTING.
func buState(state string) func(*buSpec) {
	return func(sp *buSpec) {
		sp.mergeState = state
		if state == "DIRTY" {
			sp.mergeable = "CONFLICTING"
		}
	}
}
func buMergeable(m string) func(*buSpec) { return func(sp *buSpec) { sp.mergeable = m } }
func buCross() func(*buSpec)             { return func(sp *buSpec) { sp.cross = true } }
func buBaseRef(r string) func(*buSpec)   { return func(sp *buSpec) { sp.baseRef = r } }
func buClosing(n ...int) func(*buSpec)   { return func(sp *buSpec) { sp.closing = n } }

// buTruth is what the fake GitHub's preflight read reports. The default is what
// the tick's own view says, so a test overrides only what it moves.
type buTruth struct {
	ID, Head, Base, MergeState string
	Cross, InQueue             bool
}

func (t buTruth) body() string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"id":%q,"headRefOid":%q,"baseRefOid":%q,"isInMergeQueue":%t,"isCrossRepository":%t,"mergeStateStatus":%q}}}}`,
		t.ID, t.Head, t.Base, t.InQueue, t.Cross, t.MergeState)
}

// -- the harness -------------------------------------------------------------

type buEnv struct {
	*ciCapEnv
	// preflight, when set, answers the preflight read of tick `tick`.
	preflight func(tick int, truth buTruth) (stdout string, err error)
	// preflightStderr, when set, is the stderr a failed preflight read carries.
	preflightStderr string
	// mutate, when set, answers update call n (1-based); the default is a
	// success that returns the unchanged head, as an asynchronous update does.
	mutate func(n int, expectedHead string) (stdout, stderr string, err error)
	// onMutation runs as an update call arrives, before it is answered.
	onMutation func()
	// viewFail makes tick k's `pr view` fail.
	viewFail map[int]error
	// defaultBranch answers the repository metadata read (automerge on only).
	defaultBranch string

	preflightTicks []int
	mutationTicks  []int
	mutations      [][]string
}

func newBuEnv(t *testing.T, steps ...ciCapStep) *buEnv {
	t.Helper()
	e := newCICapEnv(t, steps...)
	b := &buEnv{ciCapEnv: e, viewFail: map[int]error{}, defaultBranch: "main"}
	base := e.gh
	execGh = func(args ...string) (string, string, error) {
		switch {
		case buGraphQLMentions(args, "updatePullRequestBranch"):
			return b.serveMutation(args)
		case buGraphQLMentions(args, "baseRefOid"):
			return b.servePreflight(args)
		case len(args) > 2 && args[0] == "api" && args[1] == "repos/o/r" && args[2] == "--jq":
			e.calls = append(e.calls, append([]string{"gh"}, args...))
			return fmt.Sprintf(`{"default_branch":%q}`, b.defaultBranch), "", nil
		case len(args) > 1 && args[0] == "pr" && args[1] == "view":
			out, stderr, err := base(args...)
			if f := b.viewFail[e.viewN]; f != nil {
				return "", "gh: pr view failed", f
			}
			return out, stderr, err
		}
		return base(args...)
	}
	t.Cleanup(func() {
		if bad := forbiddenCommands(e.calls); len(bad) > 0 {
			t.Errorf("forbidden commands in the command log: %q", bad)
		}
	})
	return b
}

func buGraphQLMentions(args []string, word string) bool {
	if len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
		return false
	}
	return slices.ContainsFunc(args[2:], func(a string) bool { return strings.Contains(a, word) })
}

// buGhField returns the value of a `-f key=value` / `-F key=value` pair.
func buGhField(args []string, key string) string {
	for i := 1; i < len(args); i++ {
		if (args[i-1] == "-f" || args[i-1] == "-F") && strings.HasPrefix(args[i], key+"=") {
			return strings.TrimPrefix(args[i], key+"=")
		}
	}
	return ""
}

func (b *buEnv) truth() buTruth {
	var v struct {
		HeadRefOid, BaseRefOid, MergeStateStatus string
		IsCrossRepository                        bool
	}
	if err := json.Unmarshal([]byte(b.steps[b.viewN-1].view), &v); err != nil {
		b.t.Fatalf("fixture view %d does not decode: %v", b.viewN, err)
	}
	return buTruth{ID: "PR_node42", Head: v.HeadRefOid, Base: v.BaseRefOid, MergeState: v.MergeStateStatus, Cross: v.IsCrossRepository}
}

func (b *buEnv) servePreflight(args []string) (string, string, error) {
	b.calls = append(b.calls, append([]string{"gh"}, args...))
	b.preflightTicks = append(b.preflightTicks, b.viewN)
	if b.preflight != nil {
		out, err := b.preflight(b.viewN, b.truth())
		if err != nil {
			stderr := "gh: preflight failed"
			if b.preflightStderr != "" {
				stderr = b.preflightStderr
			}
			return "", stderr, err
		}
		return out, "", nil
	}
	return b.truth().body(), "", nil
}

func (b *buEnv) serveMutation(args []string) (string, string, error) {
	b.calls = append(b.calls, append([]string{"gh"}, args...))
	b.mutations = append(b.mutations, args)
	b.mutationTicks = append(b.mutationTicks, b.viewN)
	if b.onMutation != nil {
		b.onMutation()
	}
	expected := buGhField(args, "expectedHeadOid")
	if b.mutate != nil {
		return b.mutate(len(b.mutations), expected)
	}
	return fmt.Sprintf(`{"data":{"updatePullRequestBranch":{"pullRequest":{"id":"PR_node42","headRefOid":%q}}}}`, expected), "", nil
}

// requirePinnedMergeUpdate asserts one mutation call is the head-pinned merge
// update of the PR node, never a rebase.
func requirePinnedMergeUpdate(t *testing.T, args []string, head string) {
	t.Helper()
	query := buGhField(args, "query")
	for _, want := range []string{"updatePullRequestBranch", "updateMethod:MERGE", "expectedHeadOid", "pullRequestId"} {
		if !strings.Contains(query, want) {
			t.Errorf("update query %q lacks %q", query, want)
		}
	}
	if strings.Contains(query, "REBASE") {
		t.Errorf("update query %q must never rebase", query)
	}
	if got := buGhField(args, "expectedHeadOid"); got != head {
		t.Errorf("expectedHeadOid = %q, want the observed head %q", got, head)
	}
	if got := buGhField(args, "pullRequestId"); got != "PR_node42" {
		t.Errorf("pullRequestId = %q, want PR_node42", got)
	}
}

func requireRecord(t *testing.T, label string, got *branchUpdate, head, base, outcome string, failed int) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: branchUpdate = nil, want %s at %s/%s", label, outcome, head, base)
	}
	if got.HeadSHA != head || got.BaseSHA != base || got.Outcome != outcome || got.FailedAttempts != failed {
		t.Errorf("%s: branchUpdate = {head %q base %q outcome %q failed %d}, want {%q %q %q %d}",
			label, got.HeadSHA, got.BaseSHA, got.Outcome, got.FailedAttempts, head, base, outcome, failed)
	}
}

// forbiddenFragments are what no git or gh invocation of the update may carry.
var forbiddenFragments = []string{"push", "--force", "--force-with-lease", "reset --hard", "stash", "checkout --", "restore", "update-branch", "--rebase", "updateMethod:REBASE"}

// forbiddenCommands lists the git and gh invocations in calls that carry a
// forbidden fragment. `cenci run` prompts are not git or gh commands.
func forbiddenCommands(calls [][]string) []string {
	var bad []string
	for _, c := range calls {
		if len(c) == 0 || (c[0] != "git" && c[0] != "gh") {
			continue
		}
		line := strings.Join(c, " ")
		if slices.ContainsFunc(forbiddenFragments, func(f string) bool { return strings.Contains(line, f) }) {
			bad = append(bad, line)
		}
	}
	return bad
}

// captureAllStdout is captureStdout without its 4 KiB cap: a goroutine drains
// the pipe, so a long multi-tick run can never block or truncate.
func captureAllStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	return func() string {
		os.Stdout = orig
		_ = w.Close()
		out := <-done
		_ = r.Close()
		return out
	}
}

func (b *buEnv) runLogged() string {
	b.t.Helper()
	done := captureAllStdout(b.t)
	err := b.run()
	out := done()
	if err != nil {
		b.t.Fatalf("Run: %v", err)
	}
	return out
}

const buComment = `[{"id":7,"updated_at":"2026-01-02T00:00:00Z","user":{"login":"reviewer"}}]`

// -- AC: one pinned update, then the new head's checks gate the merge ----------

func TestBehindUpdate_OnePinnedMergeUpdateThenTheNewHeadsChecksGateTheMerge(t *testing.T) {
	const issue = 782
	withFleetAutomergeEnabled(t, true)
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1", buClosing(issue)), ciCapGreen),    // 1: behind and green: one update
		ciCapTickOf(buClean("head-2", "base-1", buClosing(issue)), ciCapPending), // 2: the new head, checks pending
		ciCapTickOf(buClean("head-2", "base-1", buClosing(issue)), ciCapPending), // 3: still pending
		ciCapMerged(issue),
	)
	b.seed(State{Status: "running"})

	b.runLogged()

	if len(b.mutations) != 1 || !slices.Equal(b.mutationTicks, []int{1}) {
		t.Fatalf("update calls = %d at ticks %v, want exactly one, on tick 1", len(b.mutations), b.mutationTicks)
	}
	requirePinnedMergeUpdate(t, b.mutations[0], "head-1")
	first := b.after(1)
	requireRecord(t, "after tick 1", first.BranchUpdate, "head-1", "base-1", "issued", 0)
	if first.AutomergeDecision != "held" || first.AutomergeReason != reasonBranchBehind {
		t.Errorf("tick 1 automerge = %q (%q), want held (%q)", first.AutomergeDecision, first.AutomergeReason, reasonBranchBehind)
	}
	for _, tick := range []int{2, 3} {
		s := b.after(tick)
		if s.AutomergeDecision != "held" || s.AutomergeReason != reasonCICheckPending {
			t.Errorf("tick %d automerge = %q (%q), want held (%q): no merge while any check is pending", tick, s.AutomergeDecision, s.AutomergeReason, reasonCICheckPending)
		}
	}
	requireRecord(t, "after tick 2", b.after(2).BranchUpdate, "head-1", "base-1", "updated", 0)
	if b.ghCalled("pr", "merge") {
		t.Error("gh pr merge was called")
	}
}

// -- AC: two supervisors for one PR make at most one update call ---------------

// A second supervisor started for a PR whose supervisor is live is refused by the
// state-dir lock, whether it is the detached child (O_EXCL on the lock) or an
// arming parent (the lock's presence). It is started at every point the first
// supervisor is mid-run: while it reads the PR, before the update, and between
// ticks at the same (head, base) pair. Whichever way the lock is read, the fake
// GitHub sees one update call, and the refused supervisors read nothing.
func TestBehindUpdate_TwoSupervisorsForOnePRGiveOneLiveSupervisorAndOneUpdateCall(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 1: the update
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 2: the same pair
		ciCapMerged(),
	)
	b.seed(State{Status: "running"})
	lockPath := b.path + ".lock"

	started := 0
	originalStart := startSupervisor
	startSupervisor = func(*exec.Cmd) error { started++; return nil }
	t.Cleanup(func() { startSupervisor = originalStart })

	// refusal is one competing supervisor: how it was started, what Run said, and
	// how many PR reads, update calls and preflight reads it caused.
	type refusal struct {
		who                     string
		arm                     bool
		err                     error
		lockHeld                bool
		views, mutations, reads int
	}
	var refusals []refusal
	inCompetitor := false
	// competitor starts a second supervisor now: a detached child (the process
	// `cenci babysit` forks) or, with arm, a fresh `cenci babysit 42` parent.
	competitor := func(who string, arm bool) {
		if inCompetitor {
			return
		}
		inCompetitor = true
		defer func() { inCompetitor = false }()
		if arm {
			t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
			defer t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
		}
		views, mutations, reads := b.viewN, len(b.mutations), len(b.preflightTicks)
		r := refusal{who: who, arm: arm}
		r.err = Run(b.opts(false))
		r.views, r.mutations, r.reads = b.viewN-views, len(b.mutations)-mutations, len(b.preflightTicks)-reads
		_, statErr := os.Stat(lockPath)
		r.lockHeld = statErr == nil
		refusals = append(refusals, r)
	}

	inner := execGh
	execGh = func(args ...string) (string, string, error) {
		out, stderr, err := inner(args...)
		if len(args) > 1 && args[0] == "pr" && args[1] == "view" && b.viewN == 1 {
			competitor("the first supervisor's first read", false)
		}
		return out, stderr, err
	}
	innerSleep := sleep
	sleeps := 0
	sleep = func(d time.Duration) {
		sleeps++
		competitor(fmt.Sprintf("after tick %d", sleeps), sleeps == 2)
		innerSleep(d)
	}

	b.runLogged()

	if len(refusals) != 3 {
		t.Errorf("competing supervisors started = %d, want 3 (while reading, and after each of the two ticks)", len(refusals))
	}
	for _, r := range refusals {
		want := "supervisor already owns PR #42"
		if r.arm {
			want = "supervisor already running for PR #42"
		}
		if r.err == nil || !strings.Contains(r.err.Error(), want) {
			t.Errorf("%s: Run err = %v, want the lock refusal %q", r.who, r.err, want)
		}
		if !r.lockHeld {
			t.Errorf("%s: the live supervisor's lock is gone after the refusal", r.who)
		}
		if r.views != 0 || r.mutations != 0 || r.reads != 0 {
			t.Errorf("%s: the refused supervisor read the PR %d times, made %d update calls and %d preflight reads, want none",
				r.who, r.views, r.mutations, r.reads)
		}
	}
	if started != 0 {
		t.Errorf("supervisors forked = %d, want 0: the lock refuses the arming parent before it forks", started)
	}
	if len(b.mutations) != 1 || !slices.Equal(b.mutationTicks, []int{1}) {
		t.Fatalf("update calls = %d at ticks %v, want exactly one (head, base) pair attempted, on tick 1", len(b.mutations), b.mutationTicks)
	}
	requirePinnedMergeUpdate(t, b.mutations[0], "head-1")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("the lock outlived its supervisor (stat err = %v): one supervisor held it to the end", err)
	}
}

// -- AC: a conflict goes to merge-repair, never to an update -------------------

func TestBehindUpdate_ConflictingPRMakesNoUpdateCallAndOpensTheMergeRepairEpisode(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1", buState("DIRTY")), ciCapGreen),
		ciCapTickOf(buView("head-1", "base-1", buState("DIRTY")), ciCapGreen),
		ciCapTickOf(buView("head-1", "base-1", buState("DIRTY")), ciCapGreen),
		ciCapTickOf(buView("head-1", "base-1", buState("DIRTY")), ciCapGreen),
		ciCapMerged(),
	)
	b.seed(State{Status: "running"})

	b.runLogged()

	if len(b.mutations) != 0 || len(b.preflightTicks) != 0 {
		t.Errorf("update calls = %d, preflight reads = %d, want none for a DIRTY PR", len(b.mutations), len(b.preflightTicks))
	}
	want := []string{"merge-repair", "merge-repair", "merge-repair", "babysit-attention"}
	if got := runOrder(b.calls); !slices.Equal(got, want) {
		t.Errorf("cenci run calls = %q, want %q (three merge repairs, then input needed)", got, want)
	}
	if s := b.after(4); s.BranchUpdate != nil || s.Status != "needs-input" {
		t.Errorf("tick 4: branchUpdate=%+v status=%q, want none and needs-input", s.BranchUpdate, s.Status)
	}
}

func TestBehindUpdate_DirtyAfterTheUpdateRoutesToMergeRepairNotAnotherUpdate(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),                   // 1: behind: update
		ciCapTickOf(buView("head-2", "base-2", buState("DIRTY")), ciCapGreen), // 2: a base race made it conflict
		ciCapMerged(),
	)
	b.seed(State{Status: "running"})

	b.runLogged()

	if !slices.Equal(b.mutationTicks, []int{1}) {
		t.Errorf("update calls at ticks %v, want only tick 1", b.mutationTicks)
	}
	if got := runOrder(b.calls); !slices.Equal(got, []string{"merge-repair"}) {
		t.Errorf("cenci run calls = %q, want one merge-repair", got)
	}
	if s := b.after(2); s.ConflictFixAttempts != 1 {
		t.Errorf("tick 2: conflictFixAttempts = %d, want 1", s.ConflictFixAttempts)
	}
}

// -- AC: every live branch writer defers the update ----------------------------

func TestBehindUpdate_ALiveWriterWindowDefersWithZeroCalls(t *testing.T) {
	for _, tc := range []struct{ window, writer string }{
		{"42-ci-repair", "ci-repair"},
		{"42-merge-repair", "merge-repair"},
		{"42-babysit-review", "babysit-review"},
		{"42-address-review", "address-review"},
	} {
		t.Run(tc.writer, func(t *testing.T) {
			b := newBuEnv(t,
				ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 1: the writer is live
				ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 2: it ended
				ciCapMerged(),
			)
			b.panes = panesByTick(paneRowOf(tc.window, false, ownedRepoDir), "")
			b.seed(State{Status: "running"})

			out := b.runLogged()

			if want := "branch update deferred: " + tc.writer + " active"; !strings.Contains(out, want) {
				t.Errorf("log lacks %q:\n%s", want, out)
			}
			if !slices.Equal(b.mutationTicks, []int{2}) || !slices.Equal(b.preflightTicks, []int{2}) {
				t.Errorf("preflight reads at ticks %v, update calls at ticks %v, want only tick 2 (zero calls while the writer is live)", b.preflightTicks, b.mutationTicks)
			}
			if s := b.after(1); s.BranchUpdate != nil {
				t.Errorf("a deferred tick recorded %+v, want no attempt consumed", s.BranchUpdate)
			}
			for tick := 1; tick <= 2; tick++ {
				if b.inventoryReads[tick] != 1 {
					t.Errorf("tick %d: inventory reads = %d, want exactly 1", tick, b.inventoryReads[tick])
				}
			}
		})
	}
}

func TestBehindUpdate_ALedgerBranchWriterEntryDefers(t *testing.T) {
	admit := func(t *testing.T, h *capHarness) capacity.Permit {
		t.Helper()
		d, err := h.ledger.Admit(capacity.Request{
			Kind: capacity.KindImplement, Repo: capRepo, Ticket: 7, Branch: "feature",
			Window: "7-implement", Limits: capacity.Limits{Machine: 99},
		})
		if err != nil || !d.Admitted {
			t.Fatalf("admit: decision %+v, err %v", d, err)
		}
		return d.Permit
	}

	t.Run("a live entry for the branch defers", func(t *testing.T) {
		h := newCapHarness(t, 3)
		admit(t, h)
		b := newBuEnv(t, ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), ciCapMerged())
		b.seed(State{Status: "running"})

		out := b.runLogged()

		if want := "branch update deferred: implement active"; !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
		if len(b.mutations) != 0 || len(b.preflightTicks) != 0 {
			t.Errorf("preflight reads %v, update calls %v, want none", b.preflightTicks, b.mutationTicks)
		}
	})
	t.Run("an entry of another branch does not", func(t *testing.T) {
		h := newCapHarness(t, 3)
		d, err := h.ledger.Admit(capacity.Request{
			Kind: capacity.KindImplement, Repo: capRepo, Ticket: 7, Branch: "elsewhere",
			Window: "7-implement", Limits: capacity.Limits{Machine: 99},
		})
		if err != nil || !d.Admitted {
			t.Fatalf("admit: %+v %v", d, err)
		}
		b := newBuEnv(t, ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), ciCapMerged())
		b.seed(State{Status: "running"})

		b.runLogged()

		if !slices.Equal(b.mutationTicks, []int{1}) {
			t.Errorf("update calls at ticks %v, want one on tick 1", b.mutationTicks)
		}
	})
	t.Run("an unreadable ledger defers too", func(t *testing.T) {
		h := newCapHarness(t, 3)
		capacityLedgerFn = func() ledgerHandle {
			return peekFailsLedger{ledgerHandle: h.handle, err: errors.New("ledger read boom")}
		}
		b := newBuEnv(t, ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), ciCapMerged())
		b.seed(State{Status: "running"})

		out := b.runLogged()

		if want := "branch update deferred: capacity ledger unreadable"; !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
		if len(b.mutations) != 0 {
			t.Errorf("update calls = %d, want none while the ledger cannot be read", len(b.mutations))
		}
	})
}

// -- AC: a head or base that moved before the call makes no call ---------------

func TestBehindUpdate_AHeadOrBaseMovedBeforeTheCallMakesNoCallAndReEvaluates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		move     func(*buTruth)
		next     string
		wantHead string
		wantBase string
	}{
		{"another actor pushed the head", func(tr *buTruth) { tr.Head = "head-x" }, buView("head-x", "base-1"), "head-x", "base-1"},
		{"the base moved", func(tr *buTruth) { tr.Base = "base-x" }, buView("head-1", "base-x"), "head-1", "base-x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuEnv(t,
				ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),
				ciCapTickOf(tc.next, ciCapGreen),
				ciCapMerged(),
			)
			b.preflight = func(tick int, tr buTruth) (string, error) {
				if tick == 1 {
					tc.move(&tr)
				}
				return tr.body(), nil
			}
			b.seed(State{Status: "running"})

			out := b.runLogged()

			if !strings.Contains(out, "branch update skipped") {
				t.Errorf("log lacks the skipped note:\n%s", out)
			}
			if s := b.after(1); s.BranchUpdate != nil {
				t.Errorf("tick 1 recorded %+v, want no attempt consumed", s.BranchUpdate)
			}
			if !slices.Equal(b.mutationTicks, []int{2}) {
				t.Fatalf("update calls at ticks %v, want only tick 2", b.mutationTicks)
			}
			requirePinnedMergeUpdate(t, b.mutations[0], tc.wantHead)
			requireRecord(t, "after tick 2", b.after(2).BranchUpdate, tc.wantHead, tc.wantBase, "issued", 0)
		})
	}
}

func TestBehindUpdate_ServerRejectionOfTheExpectedHeadIsPreservedAndReEvaluated(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 1: the server rejects the pin
		ciCapTickOf(buView("head-x", "base-1"), ciCapGreen), // 2: the other actor's push, re-observed
		ciCapMerged(),
	)
	b.mutate = func(n int, head string) (string, string, error) {
		if n == 1 {
			return `{"data":{"updatePullRequestBranch":null},"errors":[{"type":"UNPROCESSABLE","message":"Head branch was modified. Review and try the merge again."}]}`,
				"gh: Head branch was modified", errors.New("exit status 1")
		}
		return fmt.Sprintf(`{"data":{"updatePullRequestBranch":{"pullRequest":{"id":"PR_node42","headRefOid":%q}}}}`, head), "", nil
	}
	b.seed(State{Status: "running"})

	out := b.runLogged()

	if !strings.Contains(out, "branch update failed") {
		t.Errorf("log lacks %q:\n%s", "branch update failed", out)
	}
	first := b.after(1).BranchUpdate
	requireRecord(t, "after tick 1", first, "head-1", "base-1", "failed", 1)
	if !strings.Contains(first.Reason, "Head branch was modified") {
		t.Errorf("reason = %q, want the server's message", first.Reason)
	}
	if !slices.Equal(b.mutationTicks, []int{1, 2}) {
		t.Fatalf("update calls at ticks %v, want one per observed pair (ticks 1 and 2)", b.mutationTicks)
	}
	requirePinnedMergeUpdate(t, b.mutations[1], "head-x")
	requireRecord(t, "after tick 2", b.after(2).BranchUpdate, "head-x", "base-1", "issued", 1)
}

// -- AC: fork, branch protection and a merge queue are unsupported -------------

func TestBehindUpdate_UnsupportedPRsMakeNoFurtherAttemptsAndNotifyOnce(t *testing.T) {
	forbidden := func(int, string) (string, string, error) {
		return `{"data":{"updatePullRequestBranch":null},"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`,
			"gh: Resource not accessible by integration", errors.New("exit status 1")
	}
	for _, tc := range []struct {
		name, reason string
		mods         []func(*buSpec)
		preflight    func(int, buTruth) (string, error)
		mutate       func(int, string) (string, string, error)
		wantCalls    int
	}{
		{"cross-repository PR", "cross-repository PR", []func(*buSpec){buCross()}, nil, nil, 0},
		{"merge queue", "merge queue", nil, func(_ int, tr buTruth) (string, error) { tr.InQueue = true; return tr.body(), nil }, nil, 0},
		{"branch protection", "branch protection", nil, nil, forbidden, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuEnv(t,
				ciCapTickOf(buView("head-1", "base-1", tc.mods...), ciCapGreen), // 1: classified
				ciCapTickOf(buView("head-1", "base-1", tc.mods...), ciCapGreen), // 2: same pair
				ciCapTickOf(buView("head-1", "base-2", tc.mods...), ciCapGreen), // 3: the base moved
				ciCapTickOf(buView("head-3", "base-3", tc.mods...), ciCapGreen), // 4: a new head and base
				ciCapMerged(),
			)
			b.preflight, b.mutate = tc.preflight, tc.mutate
			b.seed(State{Status: "running"})

			b.runLogged()

			wantNote := "input needed: branch update unsupported (" + tc.reason + ")"
			for tick := 1; tick <= 4; tick++ {
				s := b.after(tick)
				if s.BranchUpdate == nil || s.BranchUpdate.Outcome != "unsupported" || s.BranchUpdate.Reason != tc.reason {
					t.Errorf("tick %d: branchUpdate = %+v, want unsupported (%s)", tick, s.BranchUpdate, tc.reason)
				}
				if s.Status != "needs-input" {
					t.Errorf("tick %d: status = %q, want needs-input", tick, s.Status)
				}
			}
			if rec := b.after(1).BranchUpdate; rec == nil || !rec.Notified {
				t.Errorf("tick 1: branchUpdate = %+v, want the attention notice recorded as sent", rec)
			}
			if !strings.Contains(b.after(1).AutomergeDetail, wantNote) {
				t.Errorf("tick 1 detail = %q, want it to carry %q", b.after(1).AutomergeDetail, wantNote)
			}
			if len(b.mutations) != tc.wantCalls {
				t.Errorf("update calls = %d, want %d: an unsupported PR is never retried", len(b.mutations), tc.wantCalls)
			}
			if len(b.preflightTicks) > 1 {
				t.Errorf("preflight reads at ticks %v, want at most the first", b.preflightTicks)
			}
			arg, ok := launchArg(b.calls, "babysit-attention")
			if n := b.launches("babysit-attention"); n != 1 || !ok || arg != "42 "+wantNote {
				t.Errorf("babysit-attention launches = %d (arg %q), want exactly one carrying %q", n, arg, "42 "+wantNote)
			}
		})
	}
}

func TestBehindUpdate_AFailedAttentionLaunchIsRetriedUntilItSucceeds(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1", buCross()), ciCapGreen), // 1: the notice fails to launch
		ciCapTickOf(buView("head-1", "base-1", buCross()), ciCapGreen), // 2: the retry succeeds
		ciCapTickOf(buView("head-1", "base-1", buCross()), ciCapGreen), // 3: already notified
		ciCapMerged(),
	)
	b.sessionUp = func(attempt int) bool { return attempt > 1 }
	b.seed(State{Status: "running"})

	b.runLogged()

	if s := b.after(1); s.BranchUpdate == nil || s.BranchUpdate.Notified || s.Status != "retrying" {
		t.Errorf("tick 1: branchUpdate=%+v status=%q, want un-notified and retrying", s.BranchUpdate, s.Status)
	}
	if s := b.after(2); s.BranchUpdate == nil || !s.BranchUpdate.Notified {
		t.Errorf("tick 2: branchUpdate=%+v, want notified", s.BranchUpdate)
	}
	if n := b.launches("babysit-attention"); n != 1 || b.attempts != 2 {
		t.Errorf("babysit-attention runs = %d over %d attempts, want 1 over 2 (one failed, one succeeded, none repeated)", n, b.attempts)
	}
}

// -- AC: failures are bounded: three attempts, then exhausted, reset re-arms ---

func TestBehindUpdate_ThreeFailedAttemptsExhaustTheUpdateAndResetReArmsIt(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 1: attempt 1 fails
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 2: same pair, no call
		ciCapTickOf(buView("head-1", "base-2"), ciCapGreen), // 3: new base, attempt 2 fails
		ciCapTickOf(buView("head-1", "base-3"), ciCapGreen), // 4: new base, attempt 3 fails: exhausted
		ciCapTickOf(buView("head-1", "base-4"), ciCapGreen), // 5: new base, but exhausted: no call
		ciCapTickOf(buView("head-1", "base-4"), ciCapGreen), // the arming parent's read of the re-arm
		ciCapTickOf(buView("head-1", "base-4"), ciCapGreen), // 6: after the reset: one new attempt
		ciCapMerged(),
	)
	b.mutate = func(n int, head string) (string, string, error) {
		if n <= 3 {
			return "", "gh: HTTP 502", errors.New("exit status 1")
		}
		return fmt.Sprintf(`{"data":{"updatePullRequestBranch":{"pullRequest":{"id":"PR_node42","headRefOid":%q}}}}`, head), "", nil
	}
	b.seed(State{Status: "running"})

	var out string
	done := captureAllStdout(t)
	b.runKilledAtSleep(5)
	out = done()

	requireRecord(t, "after tick 1", b.after(1).BranchUpdate, "head-1", "base-1", "failed", 1)
	requireRecord(t, "after tick 3", b.after(3).BranchUpdate, "head-1", "base-2", "failed", 2)
	exhausted := b.after(4)
	requireRecord(t, "after tick 4", exhausted.BranchUpdate, "head-1", "base-3", "failed", 3)
	if exhausted.Status != "needs-input" || !exhausted.BranchUpdate.Notified {
		t.Errorf("tick 4: status=%q notified=%v, want needs-input and notified", exhausted.Status, exhausted.BranchUpdate.Notified)
	}
	if !strings.Contains(exhausted.AutomergeDetail, "input needed: branch update attempts exhausted") {
		t.Errorf("tick 4 detail = %q, want the exhausted hold", exhausted.AutomergeDetail)
	}
	if !slices.Equal(b.mutationTicks, []int{1, 3, 4}) {
		t.Fatalf("update calls at ticks %v, want exactly three, on ticks 1, 3 and 4", b.mutationTicks)
	}
	if got, ok := launchArg(b.calls, "babysit-attention"); !ok || got != "42 input needed: branch update attempts exhausted" || b.launches("babysit-attention") != 1 {
		t.Errorf("attention = %q x%d, want one %q", got, b.launches("babysit-attention"), "42 input needed: branch update attempts exhausted")
	}
	// The state tick 5 saved (the supervisor was killed right after): the new base
	// did not re-arm the exhausted update.
	parked := load(b.path)
	if parked.Status != "needs-input" || parked.BranchUpdate == nil || parked.BranchUpdate.FailedAttempts != 3 || parked.BranchUpdate.BaseSHA != "base-3" {
		t.Errorf("tick 5: status=%q branchUpdate=%+v, want the exhausted hold kept at base-3", parked.Status, parked.BranchUpdate)
	}
	if !strings.Contains(out, "branch update failed") {
		t.Errorf("log lacks %q:\n%s", "branch update failed", out)
	}

	// `cenci babysit reset 42` clears the record; the re-armed supervisor
	// makes one new attempt at the very pair that had been left alone.
	parked.PID = 0
	if err := save(b.path, parked); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "")
	originalStart, originalTmux := startSupervisor, currentTmuxSession
	started := 0
	startSupervisor = func(*exec.Cmd) error { started++; return nil }
	currentTmuxSession = func() (string, error) { return "work", nil }
	t.Cleanup(func() { startSupervisor, currentTmuxSession = originalStart, originalTmux })
	done = captureAllStdout(t)
	err := Reset("42", b.dir, nil)
	_ = done()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if started != 1 {
		t.Fatalf("supervisor starts = %d, want the reset to re-arm one", started)
	}
	if got := load(b.path).BranchUpdate; got != nil {
		t.Fatalf("branchUpdate after reset = %+v, want it cleared", got)
	}
	t.Setenv("CENCI_BABYSIT_SUPERVISOR", "1")
	b.runLogged()
	if len(b.mutations) != 4 {
		t.Fatalf("update calls = %d, want 4 (three failures, then one after the reset)", len(b.mutations))
	}
	requirePinnedMergeUpdate(t, b.mutations[3], "head-1")
}

func TestBehindUpdate_EveryGhOrAPIErrorCountsOneFailedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdout string
		stderr string
		err    error
	}{
		{"a nonzero exit with no body", "", "gh: HTTP 502", errors.New("exit status 1")},
		{"a timeout", "", "", errors.Join(errors.New("signal: killed"), errGhTimeout)},
		{"a GraphQL error body", `{"errors":[{"type":"UNPROCESSABLE","message":"nope"}]}`, "gh: nope", errors.New("exit status 1")},
		{"a zero exit with errors in the body", `{"data":null,"errors":[{"type":"INTERNAL","message":"nope"}]}`, "", nil},
		{"a zero exit with a null payload", `{"data":{"updatePullRequestBranch":null}}`, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuEnv(t,
				ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),
				ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),
				ciCapMerged(),
			)
			b.mutate = func(int, string) (string, string, error) { return tc.stdout, tc.stderr, tc.err }
			b.seed(State{Status: "running"})

			b.runLogged()

			s := b.after(1)
			requireRecord(t, "after tick 1", s.BranchUpdate, "head-1", "base-1", "failed", 1)
			if s.BranchUpdate.Reason == "" {
				t.Error("a failed attempt recorded no reason")
			}
			if s.Status == "needs-input" {
				t.Errorf("status = %q after one failure, want it still running", s.Status)
			}
			if !slices.Equal(b.mutationTicks, []int{1}) {
				t.Errorf("update calls at ticks %v, want only tick 1: a failed pair is never retried", b.mutationTicks)
			}
		})
	}
}

// -- AC: one call per pair, across polls and restarts --------------------------

func TestBehindUpdate_RepeatedPollsAndRestartsAtOnePairMakeOneCallAndANewBaseOneMore(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 1: the update
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 2: the head did not move
		// the supervisor is killed after tick 2's save and a new Run reloads
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 3: the restart, same pair
		ciCapTickOf(buView("head-1", "base-2"), ciCapGreen), // 4: the base moved
		ciCapMerged(),
	)
	b.seed(State{Status: "running"})

	b.runKilledAtSleep(2)
	if err := b.run(); err != nil {
		t.Fatalf("restarted Run: %v", err)
	}

	if !slices.Equal(b.mutationTicks, []int{1, 4}) {
		t.Fatalf("update calls at ticks %v, want one per (head, base) pair: ticks 1 and 4", b.mutationTicks)
	}
	for tick := 1; tick <= 3; tick++ {
		// A success with the head unchanged is never recorded as updated.
		requireRecord(t, fmt.Sprintf("after tick %d", tick), b.after(tick).BranchUpdate, "head-1", "base-1", "issued", 0)
	}
	requireRecord(t, "after tick 4", b.after(4).BranchUpdate, "head-1", "base-2", "issued", 0)
}

func TestBehindUpdate_AttemptIsCheckpointedBeforeTheCallSoARestartNeverCallsTwice(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 1: the supervisor dies during the call
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 2: the restart re-reads the same pair
		ciCapMerged(),
	)
	var onDisk *branchUpdate
	b.onMutation = func() {
		persisted, err := readStateStrict(b.path)
		if err != nil {
			b.t.Errorf("the state is unreadable at the call: %v", err)
		}
		onDisk = persisted.BranchUpdate
		b.onMutation = nil
		panic(ciCapKill{})
	}
	b.seed(State{Status: "running"})

	b.runKilledAtSleep(99)
	requireRecord(t, "on disk at the call", onDisk, "head-1", "base-1", "issued", 0)
	if err := b.run(); err != nil {
		t.Fatalf("restarted Run: %v", err)
	}

	if len(b.mutations) != 1 {
		t.Errorf("update calls = %d, want 1: the persisted attempt blocks a second call", len(b.mutations))
	}
	requireRecord(t, "after the restarted tick", b.after(2).BranchUpdate, "head-1", "base-1", "issued", 0)
}

func TestBehindUpdate_AFailedCheckpointMakesNoCall(t *testing.T) {
	b := newBuEnv(t, ciCapTickOf(buView("head-1", "base-1"), ciCapGreen))
	b.seed(State{Status: "running", LaunchSession: "work", LaunchDir: ownedRepoDir, RepoRoot: ownedRepoDir})
	s := load(b.path)

	_, _, err := tickWith(&s, func() error { return errors.New("disk full") })

	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("tickWith err = %v, want the checkpoint failure", err)
	}
	if len(b.mutations) != 0 {
		t.Errorf("update calls = %d, want none when the attempt cannot be persisted first", len(b.mutations))
	}
	if s.BranchUpdate != nil {
		t.Errorf("branchUpdate = %+v, want the unpersisted attempt rolled back", s.BranchUpdate)
	}
}

// -- AC: UNKNOWN and failed reads consume no attempt ---------------------------

func TestBehindUpdate_UnknownMergeableAndUnreadableReadsConsumeNothing(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1", buMergeable("UNKNOWN")), ciCapGreen), // 1: mergeable UNKNOWN
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),                         // 2: the PR read fails
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),                         // 3: the preflight read fails
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),                         // 4: readable: the one attempt
		ciCapMerged(),
	)
	b.viewFail[2] = errors.New("exit status 1")
	b.preflight = func(tick int, tr buTruth) (string, error) {
		if tick == 3 {
			return "", errors.New("exit status 1")
		}
		return tr.body(), nil
	}
	b.seed(State{Status: "running"})

	b.runLogged()

	for tick := 1; tick <= 3; tick++ {
		if s := b.after(tick); s.BranchUpdate != nil {
			t.Errorf("tick %d recorded %+v, want no attempt consumed", tick, s.BranchUpdate)
		}
	}
	if !slices.Equal(b.preflightTicks, []int{3, 4}) {
		t.Errorf("preflight reads at ticks %v, want 3 and 4 (UNKNOWN and a failed PR read make none)", b.preflightTicks)
	}
	if !slices.Equal(b.mutationTicks, []int{4}) {
		t.Errorf("update calls at ticks %v, want only tick 4", b.mutationTicks)
	}
	requireRecord(t, "after tick 4", b.after(4).BranchUpdate, "head-1", "base-1", "issued", 0)
}

// A failed preflight read's stderr is gh's own output (multi-line, possibly with
// escape sequences, unbounded): it must reach the stdout log as one bounded
// control-free line.
func TestBehindUpdate_AFailedPreflightReadsStderrAppearsAsOneBoundedLine(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen), // 1: the preflight read fails
		ciCapMerged(),
	)
	b.preflightStderr = "first line\nsecond line \x1b[31mred\x1b[0m\r\n" + strings.Repeat("x", 5000) + "\nTAIL-MARKER\n"
	b.preflight = func(int, buTruth) (string, error) { return "", errors.New("exit status 1") }
	b.seed(State{Status: "running"})

	out := b.runLogged()

	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "the preflight read failed") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no preflight-failure line in the output:\n%s", out)
	}
	if strings.ContainsAny(line, "\x1b\r\t") {
		t.Errorf("line %q carries a raw control byte", line)
	}
	if !strings.Contains(line, "first line second line") {
		t.Errorf("line %q lost the start of gh's stderr (newlines should collapse to spaces)", line)
	}
	if strings.Contains(line, "TAIL-MARKER") {
		t.Errorf("line %q carries the unbounded tail of gh's stderr", line)
	}
	if len(line) > 600 {
		t.Errorf("line is %d bytes, want it bounded", len(line))
	}
	if b.after(1).BranchUpdate != nil {
		t.Errorf("a failed preflight read recorded %+v, want no attempt", b.after(1).BranchUpdate)
	}
}

// -- AC: stacked PR, automerge off, review launch --------------------------------

func TestBehindUpdate_AStackedPRIsUpdatedAgainstItsOwnBaseAndNeverRetargeted(t *testing.T) {
	const issue = 782
	withFleetAutomergeEnabled(t, true)
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "stack-1", buBaseRef("stack-base"), buClosing(issue)), ciCapGreen),
		ciCapTickOf(buClean("head-2", "stack-1", buBaseRef("stack-base"), buClosing(issue)), ciCapGreen),
		ciCapMerged(issue),
	)
	b.seed(State{Status: "running"})

	b.runLogged()

	if !slices.Equal(b.mutationTicks, []int{1}) {
		t.Fatalf("update calls at ticks %v, want one on tick 1", b.mutationTicks)
	}
	requireRecord(t, "after tick 1", b.after(1).BranchUpdate, "head-1", "stack-1", "issued", 0)
	if s := b.after(2); s.AutomergeDecision != "held" || s.AutomergeReason != reasonBaseNotDefault {
		t.Errorf("tick 2 automerge = %q (%q), want held (%q): the update never makes a stacked PR mergeable", s.AutomergeDecision, s.AutomergeReason, reasonBaseNotDefault)
	}
	for _, c := range b.calls {
		if line := strings.Join(c, " "); strings.Contains(line, "--base") || strings.Contains(line, "pr edit") {
			t.Errorf("the stacked PR was retargeted: %q", line)
		}
	}
}

func TestBehindUpdate_WithAutomergeOffNothingMergesAndNoGrantIsWritten(t *testing.T) {
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),
		ciCapTickOf(buClean("head-2", "base-1"), ciCapGreen),
		ciCapTickOf(buClean("head-2", "base-1"), ciCapGreen),
		ciCapMerged(),
	)
	b.seed(State{Status: "running"})

	b.runLogged()

	if !slices.Equal(b.mutationTicks, []int{1}) {
		t.Errorf("update calls at ticks %v, want one on tick 1 with automerge off", b.mutationTicks)
	}
	for tick := 1; tick <= 3; tick++ {
		if s := b.after(tick); s.AutomergeDecision != "held" || s.AutomergeReason != reasonAutomergeDisabled {
			t.Errorf("tick %d automerge = %q (%q), want held (%q)", tick, s.AutomergeDecision, s.AutomergeReason, reasonAutomergeDisabled)
		}
	}
	if b.ghCalled("pr", "merge") {
		t.Error("gh pr merge was called with automerge off")
	}
	for _, c := range b.calls {
		if line := strings.Join(c, " "); strings.Contains(line, "automerge:ok") {
			t.Errorf("a call wrote or named the merge grant: %q", line)
		}
	}
}

func TestBehindUpdate_NoReviewLaunchOnTheTickThatIssuedTheUpdate(t *testing.T) {
	b := newBuEnv(t,
		ciCapStep{view: buView("head-1", "base-1"), checks: ciCapGreen, comments: buComment},  // 1: update, review waits
		ciCapStep{view: buClean("head-2", "base-1"), checks: ciCapGreen, comments: buComment}, // 2: the new head: review launches
		ciCapMerged(),
	)
	b.seed(State{Status: "running"})

	b.runLogged()

	if s := b.after(1); !slices.Equal(s.PendingKeys, []string{"comment:7"}) || len(s.LaunchedKeys) != 0 {
		t.Errorf("tick 1: pending=%v launched=%v, want the key pending and unlaunched", s.PendingKeys, s.LaunchedKeys)
	}
	if s := b.after(2); !slices.Equal(s.LaunchedKeys, []string{"comment:7"}) {
		t.Errorf("tick 2: launched=%v, want [comment:7]", s.LaunchedKeys)
	}
	if got := runOrder(b.calls); !slices.Equal(got, []string{"babysit-review"}) {
		t.Errorf("cenci run calls = %q, want one babysit-review, after the update", got)
	}
	if !slices.Equal(b.mutationTicks, []int{1}) {
		t.Errorf("update calls at ticks %v, want one on tick 1", b.mutationTicks)
	}
}

// -- edge case: the base moves again after a successful update -----------------

// BEHIND at (head-1, base-1) -> one pinned update -> the head moves to head-2
// and its checks are pending -> the base moves again to base-2, so the PR is
// BEHIND at (head-2, base-2) with head-2's checks already green -> exactly one
// new pinned update, recorded as a new pair -> head-3's checks pending. With
// automerge on, nothing merges at any of those ticks: the BEHIND hold is the only
// thing between a green, stale head-2 and `gh pr merge` at tick 3.
func TestBehindUpdate_TheBaseMovingAgainAfterAnUpdateStartsOneNewAttemptAndNeverMergesAStaleHead(t *testing.T) {
	const issue = 782
	withFleetAutomergeEnabled(t, true)
	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1", buClosing(issue)), ciCapGreen),    // 1: behind: update 1
		ciCapTickOf(buClean("head-2", "base-1", buClosing(issue)), ciCapPending), // 2: the new head, checks pending
		ciCapTickOf(buView("head-2", "base-2", buClosing(issue)), ciCapGreen),    // 3: the base moved: behind again, head-2 green
		ciCapTickOf(buClean("head-3", "base-2", buClosing(issue)), ciCapPending), // 4: update 2 landed, checks pending
		ciCapMerged(issue),
	)
	b.seed(State{Status: "running"})

	b.runLogged()

	if !slices.Equal(b.mutationTicks, []int{1, 3}) || !slices.Equal(b.preflightTicks, []int{1, 3}) {
		t.Fatalf("preflight reads at ticks %v, update calls at ticks %v, want one of each on tick 1 and on tick 3 only", b.preflightTicks, b.mutationTicks)
	}
	requirePinnedMergeUpdate(t, b.mutations[0], "head-1")
	requirePinnedMergeUpdate(t, b.mutations[1], "head-2")
	requireRecord(t, "after tick 1", b.after(1).BranchUpdate, "head-1", "base-1", "issued", 0)
	requireRecord(t, "after tick 2", b.after(2).BranchUpdate, "head-1", "base-1", "updated", 0)
	requireRecord(t, "after tick 3", b.after(3).BranchUpdate, "head-2", "base-2", "issued", 0)
	requireRecord(t, "after tick 4", b.after(4).BranchUpdate, "head-2", "base-2", "updated", 0)

	for tick, wantReason := range map[int]string{1: reasonBranchBehind, 2: reasonCICheckPending, 3: reasonBranchBehind, 4: reasonCICheckPending} {
		if s := b.after(tick); s.AutomergeDecision != "held" || s.AutomergeReason != wantReason {
			t.Errorf("tick %d automerge = %q (%q), want held (%q): no merge before the new head's checks pass", tick, s.AutomergeDecision, s.AutomergeReason, wantReason)
		}
	}
	if b.ghCalled("pr", "merge") {
		t.Error("gh pr merge was called on a stale or still-checking head")
	}
}

// -- AC: the update reserves no capacity ------------------------------------------

// countingLedger counts every call that could take or hold capacity.
type countingLedger struct {
	ledgerHandle
	admits, confirms, releases, waits int
}

func (c *countingLedger) Admit(r capacity.Request) (capacity.Decision, error) {
	c.admits++
	return c.ledgerHandle.Admit(r)
}

func (c *countingLedger) Confirm(p capacity.Permit, conf capacity.Confirmation) error {
	c.confirms++
	return c.ledgerHandle.Confirm(p, conf)
}

func (c *countingLedger) Release(p capacity.Permit) error {
	c.releases++
	return c.ledgerHandle.Release(p)
}

func (c *countingLedger) Wait(w capacity.WaitRequest) error {
	c.waits++
	return c.ledgerHandle.Wait(w)
}

// The update spawns no worker process, so it takes no slot: the ledger holds
// exactly what it held before (another branch's worker), with no entry and no wait
// record added, not even for the length of the call, and no window or `cenci run`
// is spawned. It is gated by reading the ledger, not by writing it.
func TestBehindUpdate_TheUpdateReservesNoCapacityAndSpawnsNoWorker(t *testing.T) {
	h := newCapHarness(t, 3)
	d, err := h.ledger.Admit(capacity.Request{
		Kind: capacity.KindImplement, Repo: capRepo, Ticket: 7, Branch: "elsewhere",
		Window: "7-implement", Limits: capacity.Limits{Machine: 99},
	})
	if err != nil || !d.Admitted {
		t.Fatalf("admit: decision %+v, err %v", d, err)
	}
	before, err := h.ledger.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Entries) != 1 || len(before.Waits) != 0 {
		t.Fatalf("ledger before = %d entries, %d waits, want the one foreign entry", len(before.Entries), len(before.Waits))
	}
	counting := &countingLedger{ledgerHandle: h.handle}
	capacityLedgerFn = func() ledgerHandle { return counting }

	b := newBuEnv(t,
		ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),  // 1: the update
		ciCapTickOf(buClean("head-2", "base-1"), ciCapGreen), // 2: it landed
		ciCapMerged(),
	)
	var during capacity.Snapshot
	b.onMutation = func() {
		snap, err := h.ledger.Peek()
		if err != nil {
			t.Errorf("Peek during the update call: %v", err)
		}
		during = snap
	}
	b.seed(State{Status: "running"})

	b.runLogged()

	if !slices.Equal(b.mutationTicks, []int{1}) {
		t.Fatalf("update calls at ticks %v, want one on tick 1", b.mutationTicks)
	}
	after, err := h.ledger.Peek()
	if err != nil {
		t.Fatal(err)
	}
	for label, snap := range map[string]capacity.Snapshot{"during the update call": during, "after the run": after} {
		if !reflect.DeepEqual(snap.Entries, before.Entries) || len(snap.Waits) != 0 {
			t.Errorf("ledger %s = entries %+v, waits %+v, want exactly the entries it held before and no wait", label, snap.Entries, snap.Waits)
		}
	}
	if counting.admits != 0 || counting.confirms != 0 || counting.releases != 0 || counting.waits != 0 {
		t.Errorf("ledger calls = %d admits, %d confirms, %d releases, %d waits, want none: the update reserves nothing", counting.admits, counting.confirms, counting.releases, counting.waits)
	}
	for _, c := range b.calls {
		if len(c) > 0 && c[0] != "gh" && c[0] != "git" {
			t.Errorf("a process was spawned during the update run: %q (the update starts no worker window)", c)
		}
	}
	if got := runOrder(b.calls); len(got) != 0 {
		t.Errorf("cenci run calls = %q, want none", got)
	}
}

// -- AC: the holds survive a restart; a clean PR is never touched ----------------

func TestBehindUpdate_UnsupportedAndExhaustedHoldsSurviveARestartAsNeedsInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  branchUpdate
	}{
		{"unsupported", branchUpdate{HeadSHA: "head-1", BaseSHA: "base-1", Outcome: "unsupported", Reason: "merge queue", Notified: true}},
		{"exhausted", branchUpdate{HeadSHA: "head-1", BaseSHA: "base-0", Outcome: "failed", FailedAttempts: 3, Reason: "boom", Notified: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuEnv(t,
				ciCapTickOf(buView("head-1", "base-1"), ciCapGreen),
				ciCapTickOf(buView("head-2", "base-2"), ciCapGreen),
				ciCapMerged(),
			)
			rec := tc.rec
			b.seed(State{Status: "needs-input", BranchUpdate: &rec})

			b.runLogged()

			if s := b.after(0); s.Status != "needs-input" {
				t.Errorf("status at startup = %q, want needs-input kept across the restart", s.Status)
			}
			for tick := 1; tick <= 2; tick++ {
				if s := b.after(tick); s.Status != "needs-input" || s.BranchUpdate == nil || s.BranchUpdate.Outcome != tc.rec.Outcome {
					t.Errorf("tick %d: status=%q branchUpdate=%+v, want the hold kept", tick, s.Status, s.BranchUpdate)
				}
			}
			if len(b.mutations) != 0 || len(b.preflightTicks) != 0 {
				t.Errorf("preflight reads %v, update calls %v, want none while held", b.preflightTicks, b.mutationTicks)
			}
			if b.attempts != 0 {
				t.Errorf("launch attempts = %d, want 0: the notice was already sent", b.attempts)
			}
		})
	}
}

func TestBehindUpdate_APRThatIsNotBehindIsNeverTouched(t *testing.T) {
	for _, state := range []string{"CLEAN", "UNSTABLE", "HAS_HOOKS", "BLOCKED"} {
		t.Run(state, func(t *testing.T) {
			b := newBuEnv(t,
				ciCapTickOf(buView("head-1", "base-1", buState(state)), ciCapGreen),
				ciCapTickOf(buView("head-1", "base-1", buState(state)), ciCapGreen),
				ciCapMerged(),
			)
			b.seed(State{Status: "running"})

			b.runLogged()

			if len(b.preflightTicks) != 0 || len(b.mutations) != 0 {
				t.Errorf("preflight reads %v, update calls %v, want none for a %s PR", b.preflightTicks, b.mutationTicks, state)
			}
			if s := b.after(2); s.BranchUpdate != nil {
				t.Errorf("branchUpdate = %+v, want none", s.BranchUpdate)
			}
		})
	}
}

// -- AC: the pr view the tick reads names the base commit ------------------------

func TestBehindUpdate_TheTickReadsTheBaseCommitWithThePRView(t *testing.T) {
	b := newBuEnv(t, ciCapTickOf(buClean("head-1", "base-1"), ciCapGreen), ciCapMerged())
	b.seed(State{Status: "running"})

	b.runLogged()

	for _, c := range b.calls {
		if len(c) > 2 && c[1] == "pr" && c[2] == "view" {
			fields := c[slices.Index(c, "--json")+1]
			if !slices.Contains(strings.Split(fields, ","), "baseRefOid") {
				t.Errorf("pr view fields %q lack baseRefOid", fields)
			}
			return
		}
	}
	t.Fatal("no pr view call was recorded")
}

// -- the command-log check can fail ---------------------------------------------

func TestBehindUpdate_TheCommandLogCheckCatchesEveryForbiddenCommand(t *testing.T) {
	for _, line := range []string{
		"git push --force origin feature",
		"git push --force-with-lease",
		"git reset --hard origin/main",
		"git stash",
		"git checkout -- .",
		"git restore .",
		"gh pr update-branch 42 --rebase",
		"gh pr update-branch 42",
		"gh api graphql -f query=mutation{updatePullRequestBranch(input:{updateMethod:REBASE})}",
	} {
		if bad := forbiddenCommands([][]string{strings.Fields(line)}); len(bad) != 1 {
			t.Errorf("forbiddenCommands(%q) = %q, want it flagged", line, bad)
		}
	}
	allowed := [][]string{
		{"gh", "pr", "view", "42", "--json", "number"},
		{"gh", "api", "graphql", "-f", "query=mutation{updatePullRequestBranch(input:{updateMethod:MERGE})}"},
		{"git", "rev-parse", "--show-toplevel"},
		{"cenci", "run", "ci-repair", "42 fix it, commit, and push without force-pushing"},
	}
	if bad := forbiddenCommands(allowed); len(bad) != 0 {
		t.Errorf("forbiddenCommands flagged allowed commands: %q", bad)
	}
}

// -- the GraphQL error decoder ---------------------------------------------------

func TestDecodeGraphQLErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []graphQLError
	}{
		{"forbidden", `{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible"}],"data":null}`, []graphQLError{{Type: "FORBIDDEN", Message: "Resource not accessible"}}},
		{"two errors keep their order", `{"errors":[{"type":"A","message":"one"},{"message":"two"}]}`, []graphQLError{{Type: "A", Message: "one"}, {Message: "two"}}},
		{"no errors", `{"data":{}}`, nil},
		{"empty body", ``, nil},
		{"not json", `<html>502</html>`, nil},
		{"errors is not an array", `{"errors":"boom"}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeGraphQLErrors(tc.body)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("decodeGraphQLErrors(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}
