# Dispatch Reconciler: Bounded-Retry Counter Lifecycle

Captures the lesson from ticket #265: when a bounded-retry counter is added alongside the pure-engine/impure-runner split in `internal/dispatch`, the counter's lifecycle must be independently derived from and cross-checked against ALL of the pure engine's possible outputs.

## Rules

- **Distinguish state-changing vs. informational side effects**: When applying a recovery, separate label mutations (state-changing on GitHub) from comments (informational only). Count failures and bump the retry counter only on state-change failures, never on comment-only failures. GitHub's label state may have correctly transitioned even if the trailing comment failed.

- **Clear the retry counter only for healthy tickets, not deferred verdicts**: A ticket with no recovery produced this pass is healthy ONLY if it also has no observation carried into `result.NextObservations` (dropped, not deferred). If the pure engine deferred a verdict (nil Snapshot, or durable attempt-count read failed), it carries the observation forward; the counter must survive too, or a ticket whose apply keeps failing during an outage will reset mid-incident and escape the budget. Test: check that `recoveredKeys` and `result.NextObservations` membership are both consulted before clearing.

- **Check all recovery kinds in integration tests, not just the happy path**: Initial tests may only cover `RecoveryRetry` (which does not double-append to result aggregates), missing bugs in `RecoveryFailed`/`RecoveryPlanInvalid` (which are unconditionally appended by the pure engine before any apply is attempted). When the impure runner then escalates a failed apply for these kinds, ensure the same ticket is not appended twice to result aggregates — track which keys are already present and make the escalation append idempotent.

- **Carry the grace observation forward on apply failure**: When an apply mutation fails, the pure engine assumed the observation would clear because the mutation would land. It didn't, so re-inject the original first-seen time from the loaded state rather than letting the grace clock silently restart. This preserves the timing across retries (same pattern as the pure engine's grace-period logic).

## Example Pattern

In `applyReconcile`:
1. Load persisted state with both `Observations` (grace clock) and `ApplyFailures` (retry counter).
2. Call pure `Reconcile()` engine → collects all tickets/plans/snapshot/attempts with prior state.
3. For each recovery:
   - Call `applyRecovery()` to apply state-changing mutations; track `mutated` (true if label edit succeeded, regardless of comment).
   - If `mutated`, delete counter and observation (recovery landed).
   - If NOT `mutated`, re-inject the prior observation and increment counter; if counter exceeds budget, escalate.
4. After all recoveries, clear stale counters: only clear if the key is absent from BOTH `recoveredKeys` (no recovery this pass) AND `result.NextObservations` (not deferred).

## Interrupted-resume recovery (ticket #853)

`RecoveryResumeInterrupted` (`reconcile.go`) classifies a dead `Working`
ticket whose matched plan is still `status: awaiting-input` as an
interrupted resume — the manual or dispatch-triggered resume launch died
before it could finalize the plan — rather than an ordinary crashed
implementation run. It restores `+Input Needed` `−Working` instead of the
ordinary retry's `+Planned` `−Working`: converting an awaiting-input draft
to `Planned` would silently discard the still-open escalation (the human's
answer was never collected into a finalized plan) and route the ticket back
into ordinary dispatch as if it were a fresh, answerable plan. The
classification is checked in the `attempts < RetryBudget` branch, *before*
the stage-aware Refined-no-plan case, since `planByTicket[key] != nil`
already implies clean front-matter parsing (`ReadPlans` drops
probe-errored files) — no extra `PlanProbes` gate needed, unlike the
`plan == nil` cases.

**Shares the ordinary retry budget, not a second counter.** The recovery's
comment (`resumeInterruptedComment`) deliberately embeds the *same*
`attemptMarker` `retryComment` uses, so `countAttempts` tallies it into the
one durable, cross-restart counter every other retry consumes — a ticket
that alternates between ordinary retries and interrupted resumes for any
reason still terminates once `attempts >= RetryBudget`, escalating to the
existing `RecoveryFailed` branch (`+dispatch-failed −Working`) exactly like
any other stranded `Working` ticket. No unbounded restore loop, and no
distinct marker to double the budget.

**Crash-after-partial-label-mutation cleanup.** The `Input Needed`
short-circuit (the branch that ordinarily records a ticket into
`Escalated` and never touches it again) additionally emits a
`RecoveryResumeInterrupted` when the ticket carries **both** `Input Needed`
and `Working` — the signature of a crash between the two label edits of a
partial resume claim or rollback. This cleanup recovery uses `AddLabels:
nil` (Input Needed is already present — this is cleanup, not a fresh
restore) and `RemoveLabels: []string{labelWorking}` only. It applies the
same never-reconcile-blind, no-live-window/no-open-PR, and past-grace gates
the ordinary failure path uses, and the ticket is still recorded into
`Escalated` regardless of whether the cleanup recovery itself fires this
pass — it must never leak into `Failed` or feed dispatch's `NeedInput`
pause.

**Dispatch's own claim/rollback (`dispatch.go`)** realizes the mirror-image
contract on a failed resume launch: `errors.Is(err, run.ErrWindowSpawned)`
(the failure happened *after* `ctrl.NewWindow` already succeeded — a
demonstrably-created tmux window) retains `Working`, relying on this
section's interrupted-resume recovery as the backstop if that session turns
out to be dead. Every other resume spawn failure rolls the claim back to
`+Input Needed` `−Working` immediately via `mut.EditLabels`, so the ticket
stays resumable on the very next pass without waiting on the grace period.

## Cenci-authored comment markers

Every comment cenci itself posts to a ticket — the attempt marker on a retry
comment, the terminal-state comments (`dispatch-failed`, `plan-invalid`,
`reconcile-stuck`), and the unattended planner's escalation comment — carries
a hidden HTML comment of the form `<!-- cenci-<kind> -->` as the first line of
its body (`cenciMarkerPrefix`, `reconcile.go`).

This is load-bearing for ticket #827's dispatch auto-resume: the escalation
answer classifier (`resume.go`'s `classifyComments`) treats any comment
positioned after the anchor as a human answer **unless** it carries a
`<!-- cenci-` marker (after `>`-quoted blockquote lines are stripped first),
its author login is bot-shaped (`*[bot]`/`app/*`) or its `user.type` is
`"Bot"` (the REST comments API's first-class bot flag, #849), or its author
association is not one of `OWNER`, `MEMBER`, or `COLLABORATOR`
(`isAuthorizedAssociation`, #827 review fix #1) — a required, additional
check, not a substitute for the marker/bot-shape checks. A future comment
helper that forgets to embed a marker would silently become a false
"answered" trigger — it would look exactly like a human reply and could
resume a ticket nobody actually answered.

**The anchor itself is no longer located by scanning for a marker (#849).**
Pre-#849, `classifyComments` treated the *last* bot-authored comment
containing `<!-- cenci-planner-escalation -->` as "the" anchor — a content
scan, not an identity check. Ticket #849 replaces that with the persisted
plan's exact stored `escalationCommentId`: the anchor is the comment whose
numeric `id` matches that value, verified by confirming its
blockquote-stripped body contains the exact marker
`<!-- cenci-planner-escalation:<escalationNonce> -->`. The `<!-- cenci-`
marker-prefix convention above is now scoped to the *answer* exclusion only
(a candidate reply carrying any cenci marker is never treated as a human
answer) — it no longer has any role in finding the anchor itself, since a
forged or duplicate marker-shaped comment anywhere else in the thread can
never become "the" anchor once identity is a stored numeric ID rather than a
content match.

**Rule:** any new comment helper added to this package (or to a flow skill
that posts to a ticket cenci also dispatches) must embed a `<!-- cenci-<kind>
-->` marker as part of its body. `dispatch-failed`, `plan-invalid`, and
`reconcile-stuck` were back-filled with markers for exactly this reason
(`failedMarker`/`planInvalidMarker`/`reconcileStuckMarker`) — they previously
had none, since the classifier that would have cared about them didn't exist
yet. `attemptMarker`'s own `strings.Contains` check in `countAttempts` is
unaffected: each marker is a distinct string, so back-filling the other three
never inflates the attempt tally.

- **Strip blockquote-prefixed lines before verifying the anchor's marker:** Strip `>`-prefixed blockquote lines from a comment's body before checking whether it contains the escalation anchor's marker. A human using GitHub's "Quote reply" on the escalation comment copies the marker verbatim into their own body's blockquoted lines; if the check does not strip blockquotes first, a quote-reply comment could be misread as carrying a genuine marker of its own (#827; still load-bearing under #849's exact-ID anchor matching, since the stripped check now runs both on the anchor comment's own body and on every candidate answer after it).
