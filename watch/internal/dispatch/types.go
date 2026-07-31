// Package dispatch implements `cenci dispatch`: deterministic auto-pickup
// of planned, human-gated plans. The heart is a pure decision function
// (Decide) fed by impure adapters (GitHub ticket source, .plans front-matter
// reader, daemon snapshot, clock, budget provider). Dispatching an action is
// exactly the human keypress `cenci run implement .plans/<file>` — the
// intelligence stays inside the dispatched sessions, never in the dispatcher.
//
// The package is stdlib-only (no third-party deps).
//
// The reconciler (Reconcile/RunReconcileOnce) may terminally label a ticket
// `reconcile-stuck` (#265) when its apply-retry budget is exhausted, meaning
// reconciliation itself could not apply its verdict — distinct from
// `dispatch-failed`, which means the dispatched work failed.
package dispatch

// StageProbe classifies the collector's read of a ticket's persisted
// `cenci pipeline` stage (#732) into a closed set, rather than collapsing
// distinct failure classes (watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628). StageProbeAbsent is the zero value
// ("") so every existing Ticket construction site (reconcile paths, tests)
// keeps today's behavior unchanged without being touched.
type StageProbe string

const (
	// StageProbeAbsent is the zero value: no state file, or a persisted
	// stage that is literally "new" -- both mean "no pipeline run here",
	// which must never block dispatch (.cenci/pipeline/ is gitignored and
	// expendable; a deliberate permissive exception, #732).
	StageProbeAbsent StageProbe = ""
	// StageProbePresent means a readable, known stage was found; Stage
	// carries it verbatim.
	StageProbePresent StageProbe = "present"
	// StageProbeError means the read failed, the file could not be decoded,
	// or the persisted stage is not registered in pipeline's stage order --
	// broken input, not absent input, so it default-denies.
	StageProbeError StageProbe = "error"
)

// MainSync classifies the once-per-pass local `main` sync (#822) that runs at
// the top of RunOnce for every enrolled repo, into a closed set, rather than
// collapsing distinct failure classes (watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628). MainSyncSkipped is the zero value ("")
// so every existing Ticket construction site (reconcile paths, tests) keeps
// today's behavior unchanged without being touched, and so a repo the
// collector never synced (nil mainSync map, e.g. the reconciler's
// CollectTickets call) is never gated.
type MainSync string

const (
	// MainSyncSkipped is the zero value: the sync was never attempted for
	// this ticket's repo -- no `.plans`/pipeline gate is trustworthy or
	// untrustworthy either way, so dispatch proceeds as if the gate did not
	// exist. Also the explicit outcome when the repo's checked-out branch is
	// not `main` (or HEAD is detached, or dir is empty) -- the sync
	// deliberately never touches a tree it can't safely fast-forward.
	MainSyncSkipped MainSync = ""
	// MainSyncSynced means `main` is now caught up with (or was already at
	// or ahead of) origin/main -- includes both an actual fast-forward and
	// the "Already up to date" no-op case (local ahead is not divergence).
	MainSyncSynced MainSync = "synced"
	// MainSyncDiverged means local `main` and origin/main have both moved:
	// `git merge-base --is-ancestor` proves neither is an ancestor of the
	// other. Deterministic -- gates every ticket in the repo.
	MainSyncDiverged MainSync = "diverged"
	// MainSyncFailed means the merge --ff-only failed for a reason other
	// than divergence (e.g. a dirty tracked file the fast-forward would
	// overwrite, or dir is not a git repo at all). Gates every ticket in the
	// repo, distinct from MainSyncDiverged (#822 Q1).
	MainSyncFailed MainSync = "failed"
	// MainSyncFetchFailed means `git fetch origin` itself failed (network,
	// auth, unresolvable remote). Transient -- ungated, self-heals next
	// pass.
	MainSyncFetchFailed MainSync = "fetch_failed"
)

// String renders m for logging (#822 review nitpick fix #6). MainSyncSkipped
// is the zero value (""), so logging it verbatim (e.g. `syncMains`' "dispatch:
// main sync %s: %s (%s)" line) would print a blank token; render it as
// "skipped" instead. This is display-only -- it does NOT change the enum's
// underlying zero-value semantics used everywhere else (comparisons,
// Ticket.MainSync's default, mainSyncSkip's gate switch).
func (m MainSync) String() string {
	if m == MainSyncSkipped {
		return "skipped"
	}
	return string(m)
}

// DependencyState classifies the resolved openness of one entry in
// Ticket.DependsOn (#825) into a closed set, rather than collapsing distinct
// failure classes (watch/docs/go-gotchas.md #598, watch/docs/error-handling.md
// #628). Unlike StageProbe/MainSync above, there is deliberately NO zero-value
// ("unknown") constant here: this gate is opt-in per ticket -- only tickets
// whose body actually contains a `Depends on #N` line are ever gated at all,
// and Ticket.DependsOn empty/nil is the true "ungated" case, exercised by
// every pre-#825 Ticket construction site. A missing classification for a
// number the ticket DOES declare a dependency on (a DependencyStates map
// lookup miss, indistinguishable from the zero value) is never the
// expected/normal case, so it must fail closed via decide.go's
// dependencyGateSkip switch default, not be given its own permissive zero
// value the way StageProbeAbsent/MainSyncSkipped are.
type DependencyState string

const (
	// DependencyStateClosed means gh issue view resolved the referenced issue
	// as closed: it no longer blocks dispatch.
	DependencyStateClosed DependencyState = "closed"
	// DependencyStateOpen means the referenced issue is open -- either found
	// directly in the pass's own collected open-issue set, or via the gh
	// issue view fallback for numbers outside that set. Blocks dispatch while
	// it stays open.
	DependencyStateOpen DependencyState = "open"
	// DependencyStateUnresolved means the referenced issue's state could not
	// be determined (gh issue view failed, or returned malformed JSON) --
	// fails closed rather than assuming closed.
	DependencyStateUnresolved DependencyState = "unresolved"
)

// AnswerProbe classifies the resolved outcome of probing an `Input Needed`
// ticket's comment thread for a human answer to its escalation (#827) into a
// closed set, rather than collapsing distinct failure classes
// (watch/docs/go-gotchas.md #598, watch/docs/error-handling.md #628). Like
// DependencyState above, there is deliberately NO permissive zero-value
// constant here: a ticket carrying `Input Needed` that resolveAnswerProbes
// never covered (an Inputs.Answers map lookup miss) is indistinguishable
// from the zero value and must fail closed via decide.go's resumeGateSkip
// switch default, not be given its own permissive zero value the way
// StageProbeAbsent/MainSyncSkipped are.
type AnswerProbe string

const (
	// AnswerProbeAnswered means a non-bot comment with no `<!-- cenci-`
	// marker (blockquote-stripped first) and an authorized authorAssociation
	// (`OWNER`, `MEMBER`, or `COLLABORATOR` -- #827 review fix #1) was found
	// positioned after the last `<!-- cenci-planner-escalation -->` anchor
	// comment -- a human answered the escalation.
	AnswerProbeAnswered AnswerProbe = "answered"
	// AnswerProbeWaiting means an escalation anchor was found, but every
	// comment after it is either cenci-authored (carries a `<!-- cenci-`
	// marker), bot-authored (`*[bot]`/`app/*` login), or lacks an authorized
	// authorAssociation (`OWNER`, `MEMBER`, or `COLLABORATOR` -- #827 review
	// fix #1) -- still waiting on a human.
	AnswerProbeWaiting AnswerProbe = "waiting"
	// AnswerProbeNoAnchor means the comment thread contains no
	// `<!-- cenci-planner-escalation -->` anchor at all (including an empty
	// thread) -- there is nothing to resume from yet.
	AnswerProbeNoAnchor AnswerProbe = "no_anchor"
	// AnswerProbeUnresolved means the probe itself failed: the `gh issue
	// view` call errored, its JSON was malformed, or the pass's per-pass
	// call budget (maxAnswerProbes) was already spent -- fails closed
	// rather than assuming answered or waiting.
	AnswerProbeUnresolved AnswerProbe = "unresolved"
)

// Ticket is one open GitHub issue, as collected from a repo. Labels carry the
// board state (Planned, Blocked, agent:<name>, ...); Assignees carry GitHub
// logins; Agent is the pre-resolved agent:<name> value, if any.
type Ticket struct {
	Repo      string   // owner/repo
	Number    int      // issue number
	Title     string   // issue title
	Labels    []string // includes Planned, Blocked, agent:<name>, ...
	Assignees []string // GitHub logins; dispatch requires exactly CurrentUser
	HasOpenPR bool     // an open linked PR exists
	Agent     string   // resolved from an `agent:<name>` label, else ""

	// Stage is the ticket's persisted `cenci pipeline` stage, verbatim
	// (collector-filled, #732; mirrors Plan.CommitsBehind above). Set
	// whenever a read succeeded, including the unknown-stage ->
	// StageProbeError case (so logs can name the offending value); stays ""
	// when no probe happened or the read failed.
	Stage string
	// StageProbe classifies how Stage was obtained (collector-filled, #732;
	// mirrors Plan.CommitsBehind above).
	StageProbe StageProbe

	// MainSync classifies the once-per-pass local `main` sync outcome for
	// this ticket's repo (collector-filled, #822; mirrors StageProbe above).
	// Stays MainSyncSkipped (the zero value) when no sync map was supplied
	// to CollectTickets (the reconciler's path) or the repo's sync was
	// itself skipped.
	MainSync MainSync

	// DependsOn is the set of same-repo issue numbers this ticket's body
	// declares a "Depends on #N" line for (collector-filled, #825, via
	// parseDependsOn). Empty/nil, the zero value, is the true "ungated"
	// case -- every pre-#825 Ticket construction site keeps today's
	// behavior unchanged without being touched.
	DependsOn []int
	// DependencyStates classifies each DependsOn entry's resolved openness
	// (collector-filled, #825, via resolveDependencyStates). A number in
	// DependsOn with no corresponding key here is treated identically to an
	// unrecognized DependencyState value by dependencyGateSkip's switch
	// default -- it fails closed, never as satisfied.
	DependencyStates map[int]DependencyState
}

// Plan is the parsed front matter of one .plans/<id>-<slug>.md file.
type Plan struct {
	Repo          string // owner/repo the plan belongs to (its RepoConfig)
	Path          string // full path to the plan file
	TicketID      int    // ticketId front-matter field
	Status        string // "planned" when ready to pick up
	PlanCommitSha string // HEAD when the plan was written
	IsChild       bool   // part of a parent/child split
	IsLastChild   bool   // the last child of its parent (parent-close signal for cenci/#46)
	ParentID      int    // parentId; 0 = none
	CommitsBehind int    // default-branch commits since PlanCommitSha (collector-filled; 0 = current/unknown-fresh)

	// StalenessPaths are repo-relative paths from the stalenessPaths front-matter
	// key; when non-empty, only commits touching them count toward CommitsBehind.
	// Empty = whole-repo counting (plan files without the key).
	StalenessPaths []string
}

// Action is the outcome the engine chose for a ticket.
type Action string

const (
	ActionDispatch Action = "dispatch"
	ActionSkip     Action = "skip"
)

// Decision is the engine's verdict for a single ticket. Exactly one is produced
// per ticket. Reason is always set — for dispatched and skipped alike — so the
// loop can log every outcome (silent skips are undebuggable).
type Decision struct {
	Ticket Ticket
	Plan   *Plan // matched plan; non-nil on dispatch (drives the .plans/<file> arg)
	Action Action
	Reason string // always set
	Agent  string // resolved agent for the dispatch

	// Resume (#827) is true when this dispatch relaunches an `Input Needed`
	// ticket's `status: awaiting-input` draft after a human answered its
	// escalation, rather than picking up an ordinary `status: planned` plan.
	// Deliberately additive to Action (never a third Action value): a new
	// ActionResume would perturb formatDecision's `dispatch`/`skip:`
	// substrings, a documented downstream contract with lazyboards
	// (dispatch.go's formatDecision doc comment) -- Resume plus a
	// content-distinct Reason ("resume — human answered") keeps that
	// contract byte-unchanged.
	Resume bool
}
