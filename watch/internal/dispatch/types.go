// Package dispatch implements `cenci dispatch`: deterministic auto-pickup
// of approved, human-gated plans. The heart is a pure decision function
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
}

// Plan is the parsed front matter of one .plans/<id>-<slug>.md file.
type Plan struct {
	Repo          string // owner/repo the plan belongs to (its RepoConfig)
	Path          string // full path to the plan file
	TicketID      int    // ticketId front-matter field
	Status        string // "approved" when ready to pick up
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
}
