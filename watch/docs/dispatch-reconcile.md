# Dispatch Reconciler: Bounded-Retry Counter Lifecycle

Captures the lesson from ticket #265: when a bounded-retry counter is added alongside the pure-engine/impure-runner split in `internal/dispatch`, the counter's lifecycle must be independently derived from and cross-checked against ALL of the pure engine's possible outputs.

## Rules

- **Distinguish state-changing vs. informational side effects**: When applying a recovery, separate label mutations (state-changing on GitHub) from comments (informational only). Count failures and bump the retry counter only on state-change failures, never on comment-only failures. GitHub's label state may have correctly transitioned even if the trailing comment failed.

- **Clear the retry counter only for healthy tickets, not deferred verdicts**: A ticket with no recovery produced this pass is healthy ONLY if it also has no observation carried into `result.NextObservations` (dropped, not deferred). If the pure engine deferred a verdict (nil Snapshot, or durable attempt-count read failed), it carries the observation forward; the counter must survive too, or a ticket whose apply keeps failing during an outage will reset mid-incident and escape the budget. Test: check that `recoveredKeys` and `result.NextObservations` membership are both consulted before clearing.

- **Check all recovery kinds in integration tests, not just the happy path**: Initial tests may only cover `RecoveryRetry` (which does not double-append to result aggregates), missing bugs in `RecoveryFailed`/`RecoveryPlanInvalid` (which are unconditionally appended by the pure engine before any apply is attempted). When the impure runner then escalates a failed apply for these kinds, ensure the same ticket is not appended twice to result aggregates — track which keys are already present and make the escalation append idempotent.

- **Carry the grace observation forward on apply failure**: When an apply mutation fails, the pure engine assumed the observation would clear because the mutation would land. It didn't, so re-inject the original first-seen time from the loaded state rather than letting the grace clock silently restart. This preserves the timing across retries (same pattern as the pure engine's grace-period logic).

- **First observation always defers mutations**: A ticket observed for the first time gets `firstSeen = Now`, so `Now.Sub(firstSeen) == 0 < GracePeriod` on that initial pass regardless of what `Now` is set to in the test. Tests expecting mutations from a single `applyReconcile` call against a virgin ticket will fail silently — either seed a prior observation timestamp in the initial `ReconcileState.Observations` before calling `applyReconcile`, or use a two-pass test where pass 1 records the observation and pass 2 (with advanced time) crosses the grace period. Tickets without prior state are always Write Only on their first pass (#883).

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

## Open-PR-inventory-completeness defer (ticket #881)

`Reconcile`'s two `hasLiveWindow(...) || t.HasOpenPR` label-mutation sites
(`reconcile.go:215`'s `Input Needed` + `Working` crash-cleanup branch, and
`reconcile.go:323`'s ordinary failure path) both additionally guard on
`t.OpenPRProbe`, mirroring the existing blind `in.Snapshot == nil` guard's
shape immediately above each: a non-complete probe means `t.HasOpenPR` could
not actually be proven false this pass, so neither site may mutate a
ticket's labels on unverified input. Each guard defers exactly like the
snapshot-nil guard — no `Recovery` is produced, and any pending grace
observation is preserved (carried into `NextObservations`, never dropped),
per this doc's existing "clear the retry counter only for healthy tickets,
not deferred verdicts" rule: a ticket whose open-PR probe is chronically
non-complete must never quietly reset its grace clock and must resume the
moment the probe clears.

At the crash-cleanup site (`reconcile.go:215`), the ticket is still recorded
into `Escalated` regardless of whether the cleanup recovery itself fires
this pass — deferring the stray-`Working` cleanup must never also drop the
ticket from the daemon's escalated-tickets view.

A complete probe (`OpenPRProbeComplete`, including the zero value) never
triggers this guard, so it must never be mistaken for disabling either site
entirely — both continue to produce their ordinary recovery
(`RecoveryResumeInterrupted` / `RecoveryRetry`/`RecoveryFailed`) exactly as
before ticket #881 whenever the probe is complete.

`deferObservation(observations, next, key)` (`reconcile.go`) is the shared
helper every "must not act on unverified/blind input" guard in `Reconcile`
calls — the nil-snapshot guard, this section's non-complete-open-PR-probe
guard (both sites), and the inverse-leak branch's plan-probe-error guard —
so the "preserve the pending grace observation, no `Recovery`, `continue`"
shape stays byte-identical across every guard site instead of being
hand-duplicated per guard.

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

## Reconciliation state persistence (#883)

`reconcile.json` (`state.go`) is safety state, not disposable cache: it holds the
grace-observation map and the apply-retry-failure counters this file's bounded-retry
lifecycle above depends on. Losing or corrupting it resets grace clocks and apply-retry
budgets, which can trigger premature or endlessly repeated recovery decisions — the exact
failure this ticket closes off.

**Abort-before-mutation contract.** `stateStore.Load` classifies every failure into a
closed `StateProbe` set (`StateProbeReadError`/`StateProbeDecodeError`/
`StateProbeSchemaError`/`StateProbeInvalid`), wrapped in a `*StateLoadError` whose
`Unwrap() []error` keeps both the sentinel `ErrReconcileStateUnreadable` and the
underlying cause reachable via `errors.Is`/`errors.As`. `applyReconcile` treats **any**
non-nil `Load` error (not only the sentinel — default-deny for a third-party
`ReconcileStore`) as an abort: it returns before `Reconcile()` ever runs and before the
`dryRun` early-return, so corruption remains a hold even in dry-run. Because the abort
happens before `Reconcile()`, `store.Save` is never reached in the same pass — a load
failure is never overwritten by a later save (AC #3). `StateProbeAbsent` (the zero value,
"no file yet") is explicitly *not* an abort case: a missing file is valid empty initial
state, so a first run still reconciles and mutates normally.

**Why the sentinel must outrank `collectErr` in `RunReconcileOnce`.** `RunReconcileOnce`
returns `firstNonNil(collectErr, applyErr)`; naively, a same-pass `CollectTickets` failure
(a very likely co-occurrence — both can be driven by the same underlying outage) would
mask `applyErr`'s corruption sentinel behind the more mundane `reconcile_pass_failed`.
`RunReconcileOnce` special-cases `errors.Is(applyErr, ErrReconcileStateUnreadable)` before
falling back to `firstNonNil`, so the sentinel always wins. A cheap pre-collection probe
(`store.Load()` called once before `CollectTickets`, result discarded) also holds the pass
*before* burning any `gh issue list`/`gh issue view` budget — it reuses the same `Load`
classification `applyReconcile`'s own authoritative second load will use, so it can never
diverge from the abort contract's real coverage. `combined.go`'s `passError` mirrors the
same precedence: the sentinel outranks a simultaneous `dispatchErr`, because unlike a
transient dispatch hiccup it is the only reason that persists until a human acts.

**Why a held pass retains badges instead of clearing them.** `runCombinedPass` normally
rebuilds the caller-owned `*windows` slice from `result.Failed`/`result.Escalated` every
pass. On a corruption-held pass, both are empty because the pass never ran, not because
every previously-failed/escalated ticket recovered — rebuilding from them would clear
every existing badge and render a held reconciler as "all healthy" in `cenci
status`/waybar/noctalia, exactly the opposite of the hold's intent. `runCombinedPass`
special-cases `errors.Is(reconcileErr, ErrReconcileStateUnreadable)` to skip the rebuild
and retain `*windows` verbatim. This is scoped to the sentinel only — a generic
`reconcile_pass_failed` (a `CollectTickets`/`ReadPlans` outage with a healthy state file)
keeps the existing rebuild-from-result behavior; broadening the retention to any
`reconcileErr` would silently mask a genuinely-recovered ticket's badge clearing too.

**Crash-safe `Save`.** `stateStore.Save` writes a same-directory `os.CreateTemp`-named
temp file (the randomized name defeats a pre-planted symlink at a predictable path, e.g.
the legacy `path + ".tmp"` shape), fsyncs it, closes it, renames it over the final path,
then fsyncs the parent directory so the rename itself is durable. Every failure branch
after `CreateTemp` removes the temp file, so a failed save never leaves a stray `*.tmp`
behind and the final path is always either the previous complete state or the new
complete state, never truncated. `writeTemp`/`syncTemp`/`renameTemp` are nil-able
unexported function fields on `stateStore` (mirroring `GHMutator.createLabel`) that same-
package tests use to inject a failure at each boundary. `sweepStaleTemps` removes leftover
temp files older than 1h at the start of `Save`, best-effort and age-gated so it never
deletes a concurrently in-flight daemon/`--reconcile` cron writer's own temp file.

**Schema version and the `StateProbe` closed set.** `reconcileState.SchemaVersion`
(`currentReconcileSchema = 1`) distinguishes the current on-disk shape from the legacy
pre-#883 format (no `schemaVersion` key, decodes to the int zero value — treated
identically to an explicit `0`). `migrateReconcileState` accepts `0`/`1` and back-fills
nil maps; any other value (including negative) is `StateProbeSchemaError`.
`validateReconcileState` then checks integrity invariants migration alone can't establish
(non-empty ticket keys, non-zero observation timestamps, non-negative apply-failure
counters) and classifies a violation as `StateProbeInvalid`. Every `StateProbe` value
besides the zero-value `StateProbeAbsent` is a broken-input class that must default-deny —
adding a new failure class to `Load` without adding it to this closed set (and to the
abort contract above) would silently let a new corruption shape slip past the hold.
