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
//
// #851: a checked-out branch that is not `main`, a detached HEAD, or a
// configured-but-absent directory used to all collapse into the ungated
// MainSyncSkipped below -- they are now their own gated states
// (MainSyncNotMain/MainSyncDetached/MainSyncMissing), each blocking every
// pickup in the repo exactly like MainSyncDiverged/MainSyncFailed already
// did, per the plan's Q&A 1 resolution. MainSyncSkipped itself is preserved
// only for dir == "" (no Dir configured at all -- "no sync attempted"),
// distinct from a configured Dir that is absent from disk.
type MainSync string

const (
	// MainSyncSkipped is the zero value: dir == "" for this ticket's repo, so
	// no Dir was configured at all and no sync was ever attempted -- no
	// `.plans`/pipeline gate is trustworthy or untrustworthy either way, so
	// dispatch proceeds as if the gate did not exist. Distinct from
	// MainSyncMissing (#851), which is a configured, non-empty Dir that does
	// not exist on disk.
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
	// MainSyncNotMain means the repo is checked out on a branch other than
	// `main` (#851, previously the ungated MainSyncSkipped). A non-main
	// checkout is untrustworthy for both plan-freshness and pipeline-stage
	// gating, so it gates every pickup in the repo, not only planning.
	MainSyncNotMain MainSync = "not_main"
	// MainSyncDetached means the repo's HEAD is detached (#851, previously
	// the ungated MainSyncSkipped) -- the dir IS a repo, it just isn't on any
	// branch. Gates every pickup in the repo, distinct from MainSyncNotMain
	// and from the non-repo-dir MainSyncFailed case.
	MainSyncDetached MainSync = "detached"
	// MainSyncMissing means a configured, non-empty Dir does not exist on
	// disk at all (#851, a new os.Stat precheck) -- distinct from
	// MainSyncFailed (a real, existing dir that merely isn't a git repo) and
	// from the ungated MainSyncSkipped zero value reserved for dir == "" (no
	// Dir configured). Gates every pickup in the repo.
	MainSyncMissing MainSync = "missing"
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
	// marker (blockquote-stripped first), an authorized authorAssociation
	// (`OWNER`, `MEMBER`, or `COLLABORATOR` -- #827 review fix #1, now a
	// coarse prefilter only, never final authorization -- #882) AND a
	// positively-verified CURRENT repository write permission (`admin` or
	// `write`, resolved via the authoritative collaborator-permission
	// endpoint, #882) was found positioned after the comment classifyComments
	// verified is the anchor (#849): anchor identity is the persisted
	// EscalationCommentID/EscalationNonce pair -- the exact stored comment ID
	// whose blockquote-stripped body contains the exact
	// `escalationAnchorPrefix + nonce` marker, never a content scan for "the
	// last anchor-shaped comment" -- a human answered the escalation.
	AnswerProbeAnswered AnswerProbe = "answered"
	// AnswerProbeWaiting means classifyComments (#849) located and verified
	// the persisted anchor (the exact stored comment ID/nonce pair), but
	// every comment after it is either cenci-authored (carries a `<!--
	// cenci-` marker), bot-authored (`*[bot]`/`app/*` login), or lacks an
	// authorized authorAssociation (`OWNER`, `MEMBER`, or `COLLABORATOR` --
	// #827 review fix #1, a coarse prefilter only -- #882) -- still waiting
	// on a human. A candidate that passes the association prefilter but is
	// then positively denied or left unresolved by the write-permission
	// check (#882) never lands here -- see AnswerProbeUnauthorized and the
	// permission-failure classes below.
	AnswerProbeWaiting AnswerProbe = "waiting"
	// AnswerProbeUnresolved means the probe itself failed: the `gh api
	// .../comments --paginate` call errored, its JSON was malformed, or the
	// pass's per-pass call budget (maxAnswerProbes) was already spent --
	// fails closed rather than assuming answered or waiting.
	AnswerProbeUnresolved AnswerProbe = "unresolved"

	// AnswerProbeAnchorUnset means the ticket's matched Plan carries no
	// usable escalation anchor (#849): EscalationNonce is empty or fails
	// escalationNoncePattern, or EscalationCommentID is <= 0 -- the
	// persisted draft itself never recorded a usable anchor, a fail-closed
	// case the human-triggered repair ladder
	// (flow/skills/implement/phases/phase-1-plan.md's
	// `## Repair Escalation Anchor`) exists to fix. Dispatch never repairs
	// it itself; it only fails closed.
	AnswerProbeAnchorUnset AnswerProbe = "anchor_unset"
	// AnswerProbeAnchorMismatch means the Plan carries a well-formed
	// nonce/comment-ID pair, but the stored comment ID could not be located
	// in the thread, or the comment at that ID does not contain the exact
	// nonce marker (#849) -- fails closed rather than falling back to a
	// content/last-comment scan.
	AnswerProbeAnchorMismatch AnswerProbe = "anchor_mismatch"

	// AnswerProbeUnauthorized (#882) means every post-anchor candidate that
	// passed the cheap association prefilter was positively denied CURRENT
	// repository write permission (the collaborator-permission endpoint's
	// top-level `permission` resolved to a recognized but insufficient value
	// -- "read", "triage", or "none") -- distinct from every unresolved
	// permission-probe class below, per the plan's Assumptions precedence
	// rule ("unresolved beats denied" only changes which reason is
	// *reported* when both occur; a lone positive denial reports this
	// reason).
	AnswerProbeUnauthorized AnswerProbe = "permission_denied"
	// AnswerProbeLoginInvalid (#882) means a candidate's author login failed
	// GitHub's login grammar (githubLoginPattern, permission.go) -- a
	// path-injection guard that fails closed before the login is ever
	// interpolated into the collaborator-permission endpoint path, without
	// spending a gh call.
	AnswerProbeLoginInvalid AnswerProbe = "permission_login_invalid"
	// AnswerProbePermissionAPIError (#882) means the write-permission `gh
	// api .../collaborators/<login>/permission` call itself failed (a
	// nonzero exit not otherwise classified as a timeout or output
	// truncation).
	AnswerProbePermissionAPIError AnswerProbe = "permission_api_error"
	// AnswerProbePermissionTimeout (#882) means the write-permission probe
	// was killed by its own bounded timeout (permissionProbeTimeout,
	// permission.go) -- distinct from an ordinary API error.
	AnswerProbePermissionTimeout AnswerProbe = "permission_timeout"
	// AnswerProbePermissionTruncated (#882) means the write-permission
	// probe's response exceeded its bounded stdout cap
	// (maxPermissionStdoutBytes, permission.go) -- fails closed rather than
	// decoding a truncated/corrupted partial payload.
	AnswerProbePermissionTruncated AnswerProbe = "permission_truncated"
	// AnswerProbePermissionMalformed (#882) means the write-permission
	// probe's response body could not be decoded as JSON at all.
	AnswerProbePermissionMalformed AnswerProbe = "permission_malformed"
	// AnswerProbePermissionMissingField (#882) means the write-permission
	// probe's response decoded as JSON but carried no top-level `permission`
	// field -- e.g. a response carrying only `role_name` (a custom org
	// role), which per the plan's Q1 must never be consulted as a
	// substitute signal.
	AnswerProbePermissionMissingField AnswerProbe = "permission_missing_field"
	// AnswerProbePermissionUnknownValue (#882) means the write-permission
	// probe's `permission` field held a value outside both the accepted
	// granting set (`admin`/`write`) and the recognized denied set
	// (`read`/`triage`/`none`) -- a future/unrecognized GitHub permission
	// value fails closed here rather than being silently granted or folded
	// into a positive denial. Also the default-deny mapping for a
	// zero-value/unrecognized WritePermission (writePermissionAnswerProbe,
	// permission.go).
	AnswerProbePermissionUnknownValue AnswerProbe = "permission_unknown_value"
	// AnswerProbePermissionCapExhausted (#882) means the per-pass, per-repo
	// write-permission lookup budget (maxPermissionLookupsPerRepo,
	// permission.go) was already spent for this candidate's repo -- fails
	// closed rather than assuming granted or denied.
	AnswerProbePermissionCapExhausted AnswerProbe = "permission_cap_exhausted"
)

// WritePermission classifies the resolved outcome of probing a comment
// author's CURRENT repository write permission (#882, permission.go's
// fetchWritePermission/permissionCache) into a closed set, rather than
// collapsing distinct failure classes (watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628). Like DependencyState/AnswerProbe above,
// there is deliberately NO permissive zero-value constant here: a caller that
// forgets to resolve a login's permission (a zero WritePermission("")) must
// fail closed via writePermissionAnswerProbe's switch default, not be
// silently treated as granted or denied.
type WritePermission string

const (
	// WritePermissionGranted means the collaborator-permission endpoint's
	// top-level `permission` field resolved to a value in the accepted
	// granting set (writePermissionGrantingValues: exactly "admin" or
	// "write", plan Q1) -- the login currently holds CURRENT repository
	// write authority.
	WritePermissionGranted WritePermission = "granted"
	// WritePermissionDenied means `permission` resolved to a recognized but
	// insufficient value -- "read", "triage", or "none" (an org member,
	// removed collaborator, or read/triage collaborator with no current
	// write access) -- a positive, authoritative denial, distinct from every
	// unresolved/error class below.
	WritePermissionDenied WritePermission = "denied"
	// WritePermissionLoginInvalid means the login failed GitHub's login
	// grammar (githubLoginPattern) before ever being interpolated into the
	// endpoint path -- a path-injection guard that fails closed without
	// spending a gh call.
	WritePermissionLoginInvalid WritePermission = "login_invalid"
	// WritePermissionAPIError means the `gh api
	// .../collaborators/<login>/permission` call itself failed (a nonzero
	// exit not otherwise classified as a timeout or output truncation).
	WritePermissionAPIError WritePermission = "api_error"
	// WritePermissionTimeout means the probe was killed by
	// permissionProbeTimeout's bound (errGhTimeout) -- distinct from an
	// ordinary API error.
	WritePermissionTimeout WritePermission = "timeout"
	// WritePermissionTruncated means the probe's response exceeded
	// maxPermissionStdoutBytes (errGhOutputTruncated) -- fails closed rather
	// than decoding a truncated/corrupted partial payload.
	WritePermissionTruncated WritePermission = "truncated"
	// WritePermissionMalformed means the response body could not be decoded
	// as JSON at all.
	WritePermissionMalformed WritePermission = "malformed"
	// WritePermissionMissingField means the response decoded as JSON but
	// carried no top-level `permission` field -- e.g. a response carrying
	// only `role_name` (a custom org role), which per the plan's Q1 must
	// never be consulted as a substitute signal.
	WritePermissionMissingField WritePermission = "missing_field"
	// WritePermissionUnknownValue means `permission` held a value outside
	// both the accepted granting set and the recognized denied set -- a
	// future/unrecognized GitHub permission value fails closed here rather
	// than being silently granted or folded into a positive denial.
	WritePermissionUnknownValue WritePermission = "unknown_value"
	// WritePermissionCapExhausted means the per-pass, per-repo lookup budget
	// (maxPermissionLookupsPerRepo) was already spent for this login's repo
	// -- fails closed rather than assuming granted or denied.
	WritePermissionCapExhausted WritePermission = "cap_exhausted"
)

// RepoAutonomy classifies the resolved per-repository `planning.autonomy`
// authorization (#851), read from the enrolled repo's committed
// `.cenci/config.json` after main sync, into a closed set, rather than
// collapsing distinct failure classes (watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628). Like DependencyState/AnswerProbe
// above, there is deliberately NO permissive zero-value constant here:
// dispatch.planRefined remains the fleet kill switch, but is insufficient
// alone to authorize unattended planning/re-plan -- a repo this pass never
// probed (a nil Inputs.RepoAutonomy map, or a map lookup miss) must fail
// closed via decide.go's autonomyGateSkip switch default, not silently
// permit.
type RepoAutonomy string

const (
	// RepoAutonomyLean means the repo's committed `.cenci/config.json` has
	// `planning.autonomy` set to the exact, case-sensitive string "lean" --
	// the sole state that authorizes an unattended Refined pickup or
	// autonomous re-plan.
	RepoAutonomyLean RepoAutonomy = "lean"
	// RepoAutonomyInteractive means the committed config was read
	// successfully but does not grant lean: a missing `planning` block, a
	// missing `autonomy` key, an explicit non-"lean" value, or a wrong-case
	// match ("Lean") all resolve here -- default-deny, not a distinct
	// failure class per value.
	RepoAutonomyInteractive RepoAutonomy = "interactive"
	// RepoAutonomyMissing means `.cenci/config.json` is absent from the
	// resolved ref entirely (never committed), or dir == "" (no Dir
	// configured -- never probed, mirrors probeStage/syncMain's own
	// dir == "" convention).
	RepoAutonomyMissing RepoAutonomy = "missing"
	// RepoAutonomyMalformed means the committed config.json was read but
	// failed to decode as JSON.
	RepoAutonomyMalformed RepoAutonomy = "malformed"
	// RepoAutonomyUnreadable means the probe itself failed to run: a bad ref,
	// a non-git directory, or any other git command failure distinct from
	// "path absent from an otherwise-resolvable ref" (RepoAutonomyMissing).
	RepoAutonomyUnreadable RepoAutonomy = "unreadable"
	// RepoAutonomyFetchUnconfirmed means probeRepoAutonomies (#877) never ran
	// the probe at all this pass, because the repo's mainSyncResult carried no
	// confirmed AutonomyRef -- either `git fetch origin` itself failed this
	// pass (MainSyncFetchFailed), or the repo had no entry at all in the
	// syncs map. Distinct from RepoAutonomyUnreadable (the probe ran and the
	// git command itself failed) and from RepoAutonomyMissing (the probe ran
	// against a resolvable ref/dir but found no committed config, or dir ==
	// "" -- never probed for an unrelated reason). Fails closed exactly like
	// the other non-lean values, with its own distinct reason
	// (reasonAutonomyFetchUnconfirmed, decide.go) so an operator can tell "no
	// fetch confirmed this pass, retryable" apart from every other denial.
	RepoAutonomyFetchUnconfirmed RepoAutonomy = "fetch_unconfirmed"
)

// PlanProbe classifies the collector's read of one .plans/<ticketId>-*.md
// file (#852) into a closed set, rather than collapsing distinct failure
// classes (watch/docs/go-gotchas.md #598, watch/docs/error-handling.md
// #628): a plan file that is missing entirely (verified absence) is a
// fundamentally different situation from one that exists but could not be
// read, whose front matter could not be parsed, whose ticketId could not be
// resolved, or whose staleness (CommitsBehind) could not be computed --
// ReadPlans previously collapsed every one of these into "no matched plan",
// which under the stage-aware planning-pickup gate (decide.go) launches a
// fresh planning session, and under the reconciler (reconcile.go) escalates
// to plan-invalid. PlanProbeAbsent is the zero value ("") so every
// pre-#852 construction site (tests, reconcile paths) keeps today's
// behavior unchanged without being touched -- mirrors StageProbeAbsent/
// MainSyncSkipped above. Unlike those two, every non-absent value here is a
// broken-input class that must default-deny, not a permissive exception:
// `.plans/` files are real, present-but-unreadable input, the opposite of
// `.cenci/pipeline/`'s gitignored-and-expendable StageProbeAbsent rationale.
type PlanProbe string

const (
	// PlanProbeAbsent is the zero value: no plan file matched this ticket at
	// all -- the true "verified absence" case (#852). Every pre-#852
	// construction site (ReadPlans callers, tests) keeps today's behavior
	// unchanged without being touched.
	PlanProbeAbsent PlanProbe = ""
	// PlanProbeOk means the plan file was read, its front matter parsed, and
	// its ticketId resolved cleanly -- a normal, healthy plan.
	PlanProbeOk PlanProbe = "ok"
	// PlanProbeReadError means the plan file exists (matched the .plans/*.md
	// glob) but os.ReadFile failed -- e.g. a permission error, or a transient
	// filesystem hiccup. Broken input, not absent input: must default-deny.
	PlanProbeReadError PlanProbe = "read_error"
	// PlanProbeParseError means the plan file was read but its front matter
	// could not be parsed (planfile.ParseFrontMatter returned false) --
	// e.g. a mid-write file with a truncated/never-closed front-matter
	// fence. Broken input, not absent input: must default-deny.
	PlanProbeParseError PlanProbe = "parse_error"
	// PlanProbeTicketIDError means the plan file's front matter parsed, but
	// its ticketId field could not be attributed to a real ticket (e.g. the
	// field is missing/malformed AND the filename itself carries no
	// attributable numeric prefix either). Broken input, not absent input:
	// must default-deny.
	PlanProbeTicketIDError PlanProbe = "ticket_id_error"
	// PlanProbeStalenessError means the plan file itself was read and
	// parsed cleanly, but its CommitsBehind count could not be computed
	// (the commitsBehind seam returned an error) -- must never be treated
	// as unknown-fresh (0 commits behind); fails closed instead.
	PlanProbeStalenessError PlanProbe = "staleness_error"
	// PlanProbeAmbiguous (#884) means two or more claims (any mix of
	// healthy/broken) attribute themselves to this ticket's plan key --
	// AC3/AC4: never resolved first-wins, and a broken duplicate never
	// silently absorbed into a healthy single. Broken input, not absent
	// input: must default-deny.
	PlanProbeAmbiguous PlanProbe = "ambiguous"
	// PlanProbeIDMismatch (#884, Q2) means a plan file's numeric filename
	// prefix and front-matter ticketId disagree (or the ticketId is
	// missing entirely, Q4: no legacy exemption) -- both the filename
	// claim and the front-matter claim are held with this same
	// classification. Broken input, not absent input: must default-deny.
	PlanProbeIDMismatch PlanProbe = "id_mismatch"
	// PlanProbePathAnomaly (#884) means a .plans entry attributable to this
	// ticket by filename is not a regular file (a symlink, a directory, or
	// other non-regular entry) -- classified from the directory entry's
	// type alone, never opened/read. Broken input, not absent input: must
	// default-deny.
	PlanProbePathAnomaly PlanProbe = "path_anomaly"
)

// PlanInventory classifies the collector's per-repo read of the whole
// `.plans` directory (#884) into a closed set, rather than collapsing
// distinct failure classes (watch/docs/go-gotchas.md #598,
// watch/docs/error-handling.md #628): a directory that could not be fully
// enumerated (unreadable, or a mid-enumeration partial read) can never prove
// absence for ANY ticket in the repo, so it must hold every pickup in that
// repo -- ordinary Planned dispatch, resume, and planning pickup alike (Q5)
// -- rather than only the tickets whose own plan file happened to be
// unreadable. PlanInventoryVerified is the zero value ("") so every existing
// Inputs/ReconcileInputs construction site (decide_test.go, reconcile_test.go,
// chain*_test.go) keeps today's behavior unchanged without being touched --
// mirrors PlanProbeAbsent/StageProbeAbsent/MainSyncSkipped above. Unlike
// those permissive zero values, every non-verified value here is a
// broken-input class that must default-deny (mirrors PlanProbe's own
// discipline): the two production writers (readPlansForRepos,
// RunReconcileOnce) are single call sites, each with an explicit "populated
// for every configured repo, every pass" wiring test, so the permissive zero
// value is never silently relied upon in production.
type PlanInventory string

const (
	// PlanInventoryVerified is the zero value: a proven complete
	// enumeration of `.plans` for this repo this pass (absent, empty, or a
	// complete directory listing) -- the true "verified" case.
	PlanInventoryVerified PlanInventory = ""
	// PlanInventoryUnreadable means `.plans` itself could not be opened at
	// all (permission denied, or the path is not a directory) -- a partial
	// enumeration can never prove no second file claims any given ticket,
	// so this holds every ticket in the repo (Q5).
	PlanInventoryUnreadable PlanInventory = "unreadable"
	// PlanInventoryPartial means directory enumeration started but failed
	// partway through (a mid-enumeration read error) -- the entries read
	// before the failure are known, but completeness is not proven, so
	// this holds every ticket in the repo exactly like
	// PlanInventoryUnreadable (Q5).
	PlanInventoryPartial PlanInventory = "partial"
)

// OpenPRProbe classifies the collector's bounded, cursor-paginated `gh api
// graphql` open-PR inventory read (#881, openpr.go's openPRInventory) into a
// closed set, rather than collapsing distinct failure classes
// (watch/docs/go-gotchas.md #598, watch/docs/error-handling.md #628):
// pagination truncation, cap exhaustion, a malformed page, a probe timeout,
// and a mid-pagination failure are each operationally distinct causes for
// "this repo's open-PR set could not be proven complete", and collapsing
// them would lose exactly the attribution an operator needs to fix the
// underlying cause. OpenPRProbeComplete is the zero value ("") so every
// pre-#881 Ticket construction site (reconcile paths, the ~100 existing test
// literals across this package) keeps today's behavior unchanged without
// being touched -- mirrors StageProbeAbsent/MainSyncSkipped/PlanProbeAbsent
// above: collectRepoTickets is the sole production construction site and
// always stamps this field explicitly on every ticket, so the permissive
// zero value is never what makes a real pass's happy path pass.
type OpenPRProbe string

const (
	// OpenPRProbeComplete is the zero value: the traversal reached
	// pageInfo.hasNextPage == false (the authoritative completeness signal)
	// without hitting any bound or failure -- every open PR in the repo was
	// actually seen, so HasOpenPR is trustworthy verbatim.
	OpenPRProbeComplete OpenPRProbe = ""
	// OpenPRProbeCapExhausted means either the totalCount pre-flight check
	// exceeded maxOpenPRRecords (a cheap short-circuit, never a strict
	// post-traversal equality proof) or the page bound (maxOpenPRPages) was
	// reached while the server still reported hasNextPage: true -- the repo
	// legitimately has more open PRs than this probe is bounded to traverse.
	// A partial inventory still sets HasOpenPR = true for every PR actually
	// seen; this classification governs the completeness verdict, not the
	// map contents.
	OpenPRProbeCapExhausted OpenPRProbe = "cap_exhausted"
	// OpenPRProbeTruncated means at least one PR's nested
	// closingIssuesReferences connection itself overflowed its own page
	// bound (pageInfo.hasNextPage == true on the nested connection) --
	// distinct from OpenPRProbeCapExhausted, which is the outer
	// pullRequests connection's own bound. Per Q5, this gates the whole
	// repo as incomplete, not only the affected PR's issues.
	OpenPRProbeTruncated OpenPRProbe = "truncated"
	// OpenPRProbeMalformed means a page's response body could not be
	// trusted at all: non-JSON stdout, a GraphQL errors[] payload, a
	// stdout-cap overflow (errGhOutputTruncated), or hasNextPage: true paired
	// with an empty/non-advancing endCursor (a cursor that never advances
	// must never be trusted to terminate the loop).
	OpenPRProbeMalformed OpenPRProbe = "malformed"
	// OpenPRProbeTimeout means a `gh api graphql` invocation was killed by
	// execGhBounded's ghTimeout bound (errGhTimeout) -- distinct from an
	// ordinary command failure (OpenPRProbeUnreadable below).
	OpenPRProbeTimeout OpenPRProbe = "timeout"
	// OpenPRProbeUnreadable means an ordinary `gh` command failure occurred
	// mid-pagination (a nonzero exit not otherwise classified as a timeout
	// or output truncation) -- the traversal broke, not merely hit a bound.
	OpenPRProbeUnreadable OpenPRProbe = "unreadable"
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
	// DependencyAnomalies records, in body order, one entry per "Depends on
	// #N" reference whose number could not be classified as a valid
	// dependency at all -- e.g. one that overflows strconv.Atoi's int range,
	// or is otherwise out of range (#852, via parseDependsOn). Each entry is
	// truncated to maxDependencyTokenBytes so a hostile digit run can never
	// flood memory or a log line. Nil/empty, the zero value, is the true
	// "no anomalies" case -- every pre-#852 Ticket construction site keeps
	// today's behavior unchanged without being touched.
	DependencyAnomalies []string

	// OpenPRProbe classifies how HasOpenPR above was obtained
	// (collector-filled, #881, via openpr.go's openPRInventory; mirrors
	// StageProbe/MainSync above). Stays OpenPRProbeComplete (the permissive
	// zero value) when no probe has run for this ticket's repo -- every
	// pre-#881 Ticket construction site keeps today's behavior unchanged
	// without being touched; collectRepoTickets, the sole production
	// construction site, always stamps this field explicitly.
	OpenPRProbe OpenPRProbe
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

	// EscalationNonce/EscalationCommentID (#849) are the awaiting-input
	// draft's persisted escalation-anchor fields, collector-filled by
	// ReadPlans from the plan's escalationNonce/escalationCommentId
	// front-matter keys. Left at their zero value ("" / 0) when absent or
	// malformed (nonce not matching escalationNoncePattern, or the comment
	// ID failing strconv.ParseInt base 10/64 or resolving <= 0) -- never a
	// plan-file drop; the malformed value is simply not trusted.
	EscalationNonce     string
	EscalationCommentID int64
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
	Plan   *Plan // matched plan; non-nil on dispatch except a planning pickup (Planning && !Replan), which is nil by design
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

	// Planning (#828) is true when this dispatch is a stage-aware planning
	// session rather than an ordinary status: planned pickup: either a
	// Refined ticket with no plan yet (Plan == nil), or an autonomous
	// re-plan of an existing-but-stale plan (Plan != nil, Replan also
	// true). Deliberately additive to Action (never a third Action value),
	// for the same lazyboards-substring-contract reason documented on
	// Resume above. Invariant: Replan ⇒ Planning.
	Planning bool

	// Replan (#828) is true when Planning is also true and the dispatch is
	// an autonomous re-plan of a stale plan, rather than a fresh planning
	// pickup for a Refined ticket with no plan yet. Invariants: Replan ⇒
	// Plan != nil; Planning && !Replan ⇒ Plan == nil.
	Replan bool
}
