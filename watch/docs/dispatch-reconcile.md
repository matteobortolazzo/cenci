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
`Escalated` and never crash-recovers it; the plan heal below is its only other action) additionally emits a
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

`Reconcile`'s `hasLiveWindow(...) || t.HasOpenPR` label-mutation sites (the
`Input Needed` + `Working` crash-cleanup branch, the ordinary failure path,
and the plan-driven heals: #926 A/B, #1241 C) all additionally guard on
`t.OpenPRProbe`, mirroring the existing blind `in.Snapshot == nil` guard's
shape immediately above each: a non-complete probe means `t.HasOpenPR` could
not actually be proven false this pass, so no site may mutate a
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
guard (both sites), the inverse-leak branch's plan-probe-error guard, and
plan heals A and C's guards — so the "preserve the pending grace observation, no `Recovery`, `continue`"
shape stays byte-identical across every guard site instead of being
hand-duplicated per guard.

## Plan-driven heal (ticket #926)

A ticket whose plan file already reads `status: planned` but whose board labels
never caught up was invisible to the reconciler: the failure path requires
`Working`, the inverse-leak branch requires `Planned`, and `Decide` skips a
`Refined` ticket without `Planned` as "not Planned". `Reconcile` now has three
plan-file-driven heals, all emitting `Kind: RecoveryRetry` with `Attempt: 0`,
told apart from the ordinary retry (and from each other) only by `Detail`:
`plan heal (Refined): ...`, `plan heal (Input Needed): ...` and `plan heal
(Planned): ...`. Heals A and B post a `planHealComment()` body; heal C (#1241)
posts `planHealAwaitingInputComment()`.

**Heal A, the `Refined` entry path.** Predicate: carries `Refined`, carries
neither `Planned` nor `Working`, and has a matched plan with `Status ==
"planned"`. Action: `+Planned`, no `RemoveLabels` (`Working` is provably absent).
Placed between the inverse-leak branch and the failure-path `Working` gate;
disjoint from both (the first requires `Planned`, the second `Working`), so a
`Refined` + `Working` ticket keeps the budgeted failure path verbatim, including
its `AttemptsUnknown` deferral, attempt tally, and `RecoveryFailed` terminal. The
`Refined` gate keeps a human who parked a ticket by removing board-state labels
from having `Planned` forcibly re-added. This also closes the self-inflicted
stranding: the stage-aware retry drops only `Working` for a `Refined` ticket
with a verified-absent plan, and a plan that becomes visible later is now healed.

**Heal B, the `Input Needed` entry path.** Predicate: carries `Input Needed` and
has a matched plan with `Status == "planned"` (no `Refined` requirement). This is
the escalation restore (`phase-1-plan.md` "Resume From Draft" step 6) running
`--transition input-needed` after a post-replace verification failure although
the `mv` had in fact landed: the board reads `Input Needed` while the draft reads
`planned`, and `Decide` skips it because the draft is not `awaiting-input`.
Action: `+Planned`, `RemoveLabels: [Working, Input Needed]`, emitted literally
even when `Working` is absent (removing an unapplied label is a no-op for
`gh issue edit`). The ticket is still appended to `Escalated` on every pass,
whether the heal fires or a guard defers it. An `Input Needed` ticket whose plan
reads `awaiting-input`, or with no matched plan, is untouched and records no
observation.

**Guards, in order** (all three heals): verified plan inventory, `in.Snapshot !=
nil`, `OpenPRProbe == OpenPRProbeComplete`, no live window and no open PR, then
an elapsed `GracePeriod` observation. A new ticket always defers on its first pass
(rule 5). Heal B's inventory check is inline and load-bearing: its branch runs
upstream of the top-level plan-inventory hold, so a matched plan under a
`Partial` inventory is reachable there. Heals A's and C's are defense-in-depth
behind the top-level hold.

**The live-window difference.** The unverified-signal guards (inventory,
snapshot, probe) defer via `deferObservation` in all three heals. A live window or
open PR differs: heals A and C also defer (keep the pending grace observation),
while heal B does a plain `continue` that drops it, copying the `Input Needed`
branch's existing stray-`Working` cleanup. Grace-not-elapsed keeps `firstSeen` in
all three.

**Why budget-free.** A heal is label convergence toward a plan file whose state
the board has not caught up with, not a retry of failed work, so it never consumes
`RetryBudget`. It cannot loop: once `+Planned` lands the ticket no longer matches
heal A (and heal B no longer sees `Input Needed`), and a later crash lands in the
budgeted failure path, which still terminates at `dispatch-failed`. Heal C cannot
loop either: once `+Input Needed` lands, the `Input Needed` branch owns the ticket
and it no longer matches heal C.

**Why heal B supersedes the stray-`Working` cleanup.** It is evaluated first, and
when it fires the cleanup never runs: the cleanup would remove `Working` and leave
the ticket stranded at `Input Needed`, and that branch never reaches the budgeted
failure path, so the heal is its only recovery. With an `awaiting-input` plan the
cleanup is unchanged.

**Marker rule.** `planHealMarker` (`<!-- cenci-plan-heal -->`) is distinct from
`attemptMarker`, so `countAttempts` never tallies a heal as a dispatch attempt,
and it carries the `<!-- cenci-` prefix so the escalation answer classifier never
mistakes it for a human reply. The runner's escalation passthrough reuses
`rec.RemoveLabels` as-is: an exhausted apply of heal A becomes `+reconcile-stuck`
with an empty remove; heal B drops `Working` and `Input Needed` and becomes
terminal `reconcile-stuck`; heal C, like heal A, removes nothing, so `Planned`
stays and the ticket becomes terminal `reconcile-stuck`.

**Heal C, the `Planned` entry path (#1241).** The mirror of heal A and B.
Predicate: carries `Planned`, carries neither `Working` nor `Input Needed` (nor a
terminal label), and has a matched plan with `Status == "awaiting-input"` (no
`Refined` requirement). `Decide` skips such a ticket as "plan not ready", and
without `Input Needed` the `resuming` path is never taken; the inverse-leak branch
`continue`d on any matched plan, so nothing claimed it. It lives inside the
inverse-leak branch's matched-plan block, before its `continue`. Action:
`+Input Needed`, `Planned` kept, no `RemoveLabels` (the shape
`--transition input-needed` natively produces for a re-planned ticket), with a
`planHealAwaitingInputComment()` body. Guards: the same order as the other heals,
all via `deferObservation` (inventory is defense-in-depth behind the top-level
hold). Budget-free: a heal is label convergence, so it posts `planHealMarker`,
leaves `Attempt` at zero and never consumes `RetryBudget`.

It cannot loop: once `Input Needed` lands, the `Input Needed` branch owns the
ticket. On the next pass it is recorded in `Escalated` (not `Failed`), and
dispatch auto-resume picks it up once a human answers, under its existing anchor
and permission gates. A `Planned` + `Working` ticket with an `awaiting-input` plan
never reaches this branch and keeps the budgeted interrupted-resume path.

Human dismissal: if a human removes `Input Needed` again while the draft still
reads `awaiting-input`, the heal re-adds it after another grace period. This is
documented, not gated. The ways out are to answer the escalation, delete or
replace the draft, or remove `Planned`.

`Decide` is deliberately unchanged: it must not dispatch off a plan file directly,
since that would race the reconciler.

**Pipeline stage after heal B.** After a successful HS-R12 `mv` the pipeline stage
may still read `waiting_for_input`. `stageGateSkip` does not gate that stage, so
`Decide` dispatches the healed ticket. How the implement pipeline then handles a
`planned` plan at that stage belongs to the flow side and is out of scope here.

## Launch-ID liveness (#1292)

`hasLiveWindow` only approximates "the agent process is running": an agent OOM-killed
inside a surviving pane keeps its window `running`/`done`, and a session started by hand
runs in a window that is not named for the ticket. For the two `Working`-gated branches of
`Reconcile` -- the failure path and the `Input Needed` + `Working` stray cleanup -- the
live check is therefore two steps, in `sessionLive(t, key, in) (live, deferred)`:

1. **A recorded launch has a verdict** (`in.LaunchVerdicts[planKey]`): `live` is live
   whatever the window says, `dead` is not live whatever the window says (so a surviving
   `42-implement` window no longer keeps the ticket alive), and anything else defers.
2. **No verdict** (`LaunchVerdictNone`, the zero value) keeps `hasLiveWindow`, unchanged.

Heal A, B and C keep `hasLiveWindow`: the launch triple is written by the `working` claim,
so it only means something while the ticket is `Working`. An open PR still resets the
ticket whatever the verdict; only a ticket that would otherwise be acted on defers.

**The verdict is a closed set**, not a bool: `LaunchVerdictNone` (`""`, no recorded
launch), `live`, `dead`, `unknown`. `Reconcile` stays pure; the impure runner
(`resolveLaunchVerdicts`, `launchprobe.go`) computes the map once per pass, after
`ReadSnapshot`, exactly like `Attempts`/`AttemptsUnknown`. Default-deny: an `unknown` or
unrecognized value is never read as dead. It defers through `deferObservation` -- grace
clock preserved, no `Recovery`, no label write -- the same shape as the nil-snapshot and
non-complete-open-PR guards. Treat a new `LaunchVerdict` value like any closed-set enum:
add it to `sessionLive`'s switch deliberately, never let it fall into `dead`.

**What the runner probes.** A ticket is probed only when `needsLaunchProbe` holds: it has
`Working`, no terminal label, a non-nil snapshot, a complete open-PR probe, no open PR, and a
non-empty recorded launch (`Ticket.Launch`, filled by `probeStageState` from the same
`GetArtifacts` read as the stage -- no extra state-file read). At most one probe per ticket
per pass. The recorded values are agent-writable (`.cenci/pipeline/` is in the bind-mounted
repo), so the triple is re-validated (`ValidLaunchID`, `ValidSandboxContainerName`, runtime
in `docker`/`podman`) *before any call*; an invalid or incomplete triple is never probed,
earns no verdict (window-name rule) and logs one line naming the ticket, never the value. A
Working ticket whose state file is unreadable (`StageProbeError`) is logged once and judged by
the window-name rule; it never defers on that alone.

**Probe outcomes** (`probeLaunch`):

- `sandbox.ListLaunchProcesses` returns at least one process -> `live`; none -> `dead`.
- `sandbox.ErrUnreadableProcesses` with at least one process -> `live`; with none -> `unknown`
  (death cannot be proven).
- Any other probe error consults the runtime's running containers, listed **once per runtime
  per pass** (`sandbox.RunningContainerNames`, cached whether it succeeded or
  not): a container absent from a *successful* listing -- removed, or stopped -- is `dead`
  ("container gone" counts as dead); a failed listing, or a container that is still running
  while the probe keeps failing, is `unknown`. The exec error text is never classified.
- Every subprocess runs under a timeout context with `WaitDelay` and captured stderr
  (`runRuntime`), inside a whole-step budget (`launchProbePassTimeout`, 60s). A probe that
  overruns it is `unknown`, and tickets reached after it is spent are `unknown` without
  being probed.

**Dead in a surviving pane.** When the verdict is `dead` and a window named for the ticket
still exists, the retry, interrupted-resume and failed comments use a variant that names the
window and says its "agent process was gone" (`recoveryComments`). Markers are unchanged
(`attemptMarker` for retry and interrupted-resume, `failedMarker` for failed), so
`countAttempts` and the answer classifier see the same comments; every other case keeps
today's text byte for byte, and no launch ID or container name is ever written to a ticket.
The reconciler never kills a window and never signals a process -- the finished-ticket window
cleanup owns the window.

**The dispatcher clears the record at spawn** (`clearLaunchFn`, `dispatch.go`). In the
per-decision loop, before the resume `+Working -Input Needed` claim and before `runFn`, it
calls `pipeline.ClearLaunch` for the ticket (repo dir set only; `session.runId`/`reviewPath`
are kept). A failed clear (an unreadable state file) logs `dispatch: #N clearing recorded
launch failed` and skips that ticket for the pass -- no spawn, no label edit, and no returned
error (one broken state file must not fail the pass); a missing state file is not a failure.
Without the clear, a launch recorded before a crash and recovery would read `dead` for the
next dispatched session until it re-claims.

**Limits, by design.** A manually opened session is protected only once its run has claimed
the ticket with `cenci pipeline label --transition working` inside a sandbox (that call
records the launch; outside a sandbox it removes any stale one). A process that scrubbed
`CENCI_LAUNCH_ID` (`env -i`) is invisible to the probe and reads `dead`, the same limit the
reaper and `cenci close` have. A hostile agent can write its own record, so it can at most
keep its own ticket looking `live`; validation stops any argv injection.

**Host-side state access is hostile input.** The dispatcher's `pipeline.ClearLaunch` is the
first automatic host-side write to `.cenci/pipeline/<id>.json`, a file the sandboxed agent can
write through the bind mount. Any host-side (daemon, dispatcher, babysit) read or write of a
file under the repo must therefore assume the agent planted a symlink or FIFO at that path.
Do not use `path + ".tmp"` or `path + ".lock"` with `os.WriteFile`/`O_CREATE`: `saveState`
creates its temp file with `os.CreateTemp` (exclusive, randomized name), `withLock` opens the
lock with `O_NOFOLLOW`, and `ClearLaunch` `Lstat`s the state file and its directories and
refuses anything that is not a regular file or that resolves outside the repo. Code that is
safe inside the container is not automatically safe once the same code runs on the host. A
swap that races those checks, and the unbounded `os.ReadFile` in the collector's read, remain
open until host-side state access moves onto an `os.Root`-scoped helper (followup).

**Tests.** `reconcile_test.go` drives `Reconcile` with hand-built `LaunchVerdicts`;
`reconcile_run_test.go` drives `resolveLaunchVerdicts` through `launchProbeFn` /
`runningContainersFn`, once through `windowproctest`'s fake docker/podman (real argv, empty
call log for an invalid triple, no `kill`), and `RunReconcileOnce` end to end over a real state
file. Tests that run a probe set `PATH` to fakes only or stub both seams -- never a real
runtime.

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

**Passing the association prefilter is necessary but not sufficient (#882).**
`isAuthorizedAssociation` only narrows the candidate set once the
marker/bot-shape checks already pass — it is not itself the final
authorization gate. The replying login must also positively hold `admin` or
`write` permission on the repository, re-verified every time against the
authoritative `gh api repos/<owner>/<repo>/collaborators/<login>/permission`
endpoint (`permission.go`) — a read/triage collaborator, a removed
collaborator, or an organization member without this repository's write
access is never authorized to resume an unattended planning session, no
matter how favorable their author association looks. See `permission.go` and
`watch/README.md`'s escalation section for the full authorization contract.

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
