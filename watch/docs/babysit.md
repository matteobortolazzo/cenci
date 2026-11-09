# Babysit Tick Logic: Comments vs. Reviews Data Sources

Guidance for refactoring and testing `tick()`, the core reconciliation loop that detects and resolves feedback.

Merge conflicts dispatch `merge-repair` automatically through Claude Opus, including
supervisors armed with Codex or OpenCode. The supervisor retains its original agent for CI
repair and review feedback, and retains its recorded tmux session and directory.
The built-in repair template runs Claude in one-shot print mode, forwarding
`--print` through `cenci open --` in a sandbox. Completion or failure exits the
worker instead of leaving an interactive prompt that would block retries.
Host launches retain the user's configured tool permissions; a denial reports
failure rather than bypassing permissions.
Each successful repair launch records the head SHA. Each tick reads the panes of the
exact recorded tmux session at most once (#1457), lazily, the first time a decision
needs liveness: a conflict, failing checks, a green tick with budget state to reset, or
unlaunched review keys. A quiet green tick, a MERGED or CLOSED tick and a state with no
recorded session never read it. That one `list-panes -s` read on the recorded socket,
format `#{window_name}\t#{pane_dead}\t#{@cenci-repo-dir}`, supplies both CI and merge
liveness. Only the trailing `\n` is trimmed, so a row with an empty `@cenci-repo-dir`
keeps all three columns; any other column count, an empty name or a `pane_dead` other than
`0`/`1` is malformed. A pane is this PR's worker only when its window is exactly
`<pr>-ci-repair` or `<pr>-merge-repair`, `pane_dead` is `0`, and its `@cenci-repo-dir`
(written by `cenci run`) is this repository: equal after `filepath.Clean` to the recorded
launch directory or repository root, or in the same git common directory (one bounded
`git rev-parse --git-common-dir` call, only when the paths differ). A live same-name window
that provably belongs to another repository is ignored with a printed note. One whose
`@cenci-repo-dir` is empty, relative, or cannot be resolved is a live writer of unknown
ownership: it suppresses launches, prints and records in `automergeDetail` the instruction
to let that worker finish or to close its window once its edits are preserved (`cenci babysit
reset <pr>` re-arms the retry budget), and stops blocking when it ends. The note never
echoes the raw repo-dir. The supervisor never kills a worker.
Live workers suppress another launch, including across head changes; missing
windows or retained dead panes permit another attempt even at the same SHA.
Unreadable, empty, truncated or malformed pane inventories fail visibly without
authorizing a duplicate worker. If the recorded session no longer exists, `list-panes`
fails and every tick that needs liveness fails with `workflow launch failed` and
launches nothing; re-arm the supervisor from a host tmux pane so it records the live
session. Failed launches leave the marker and attempt count
unchanged so the next tick retries. A manually started live repair also suppresses
an initial duplicate and retains the conflict episode across unknown mergeability.

CI repair is a finite worker too (#1455). For failing checks the supervisor runs `cenci run ci-repair "<pr> PR #<pr> (<branch>) has failing CI checks:
<names>. Diagnose, fix, test, commit, and push without force-pushing."` with its
recorded agent, session and directory. The leading token is the validated `State.PR`,
so the window is named `<pr>-ci-repair` (the numeric identity liveness and window
cleanup join on) whatever the branch and check names contain; the prose after it is
unchanged. The built-in templates are one-shot: Claude print mode (`--print`, through
`cenci open --` in a sandbox) and `codex exec` (through `cenci open --agent codex --
exec` in a sandbox, where the launcher puts the Codex flags after `exec`). Host
launches keep the user's configured permissions, so a denial ends the run with a
reported failure. Before dispatching, the supervisor uses the pane inventory above: a
live `<pr>-ci-repair` or `<pr>-merge-repair` window suppresses the launch and consumes
no attempt; a retained dead pane, or a live sibling such as `42-ci-repair-old` or
`142-ci-repair` for PR 42, does not block; an unreadable inventory fails the tick with
`workflow launch failed` and consumes nothing. With no recorded session the launch fails
with its own error.
A pre-spawn refusal (missing session, grouped session, unavailable Codex adapter)
leaves `ciFixAttempts` and the dedup state unchanged. `cenci run` exit status 3
means the window was spawned but its post-spawn setup failed; the supervisor prints a
warning and counts that as a dispatched attempt, so while that window is live the next
tick opens no second worker. A `@cenci-repo-dir` write failure only warns (`cenci run`
exits 0) and is dispatched too.

The CI retry episode is liveness-based (#1457). A worker that has ended (a dead pane, or
no window) without the checks going green is retried at the same failing head, one
attempt per tick, until `ciFixAttempts` reaches `fixCap`; only a live worker suppresses
the launch, and a head change alone grants no extra attempt. Each dispatched launch
records `lastRepairedCiHeadSha` (history only) and sets `ciRepairTracked`; `cenci babysit
reset <pr>` clears both. A state written by an earlier version has `lastRepairedCiHeadSha`
without `ciRepairTracked`; its worker (possibly a pre-#1455 slug-named window) cannot be
probed, so while it equals the failing head that legacy hold keeps the old same-head
dedup, and the supervisor prints and records in `automergeDetail` the instruction to let
that worker finish in the recorded session or run `cenci babysit reset <pr>`. A different
failing head or the reset ends the hold; a legacy hold does not defer address-review.
A custom `agents.<agent>.workflows.ci-repair` template is used verbatim, never
rewritten to print or exec mode, so it must itself be finite: a custom worker that keeps
its pane live suppresses every relaunch for as long as it lives, and nothing kills it.

A conflict episode allows three repair launches before
opening `babysit-attention` for input while continuing to poll for recovery. A known
non-conflicting GitHub observation clears the episode; unknown mergeability
preserves its retry budget.
The older `conflictNotifiedHeadSha` state field remains readable, but an old
attention notification does not suppress the first automatic repair after upgrade.
Apart from those automatic clears (a fresh green read of the current head, a conflict
episode ending), `cenci babysit reset <pr>` (host-only; refuses inside a sandbox) is the
supported way to zero a paused PR's budget while keeping its feedback bookkeeping. It
also clears `ciRepairPaused`, `ciCapNotified` and `ciRepairTracked`. A recorded PID that is no longer
running is treated as gone: its stale lock is cleared before the reset re-arms. A lock
naming a live supervisor for the PR, or an empty or unparseable lock, is refused and
nothing is changed (confirm no supervisor is running and delete the lock file to
recover), and a live PID that is not this PR's babysit process is also refused.

CI repair has its own budget, separate from the merge-repair budget; each is capped
at `fixCap` (3) launches. When a tick reaches the CI cap (failing checks, no conflict hold, no live
CI or merge worker, budget used up, whether or not the head ever changed) the supervisor records a lasting `ciRepairPaused` episode, sets the status to
`needs-input` and opens one `babysit-attention` session for that episode, then marks it
sent in `ciCapNotified`. The session is not reopened on later failing heads, on retried
reads or across supervisor restarts. If the launch fails, the cap and the automerge reason
`workflow launch failed` stay, the tick returns the error, `Run`'s retry backoff tries the
notification again, and `ciCapNotified` is set only after a successful launch. The
supervisor does not exit and keeps its PID. While CI repair is paused every tick still
reads the PR and its checks, reconciles review feedback, runs merge repair on its own
budget (conflict repair goes first) and detects MERGED or CLOSED. A notification still
pending while a conflict hold is active or any CI or merge worker is live (the merge-repair
cap attention included) waits until none is, and opens at most once per episode.

The episode ends only on a fresh green read of the current head, taken in that tick, on
a tick with no conflict hold and no live CI or merge worker. That includes a failed check
that is rerun green at the same head with no push. It resets `ciFixAttempts`,
`ciRepairPending` and both markers, and the status goes back to `running`. A green read
taken while a worker is live changes none of that: it may still push, so `ciRepairPending`
stays set and automerge is held with the reason `branch repair worker still running` (the
`review` stage, ahead of `CI repair pending`; any unknown-ownership note still reaches
`automergeDetail` when automerge is disabled). Pending, unknown (for example a `cancel` bucket),
zero checks, read errors, a head change alone, a restart, a push, a poll and an attention
launch never reset it, and a head change alone never authorizes a merge. A status file
written before the markers existed (`needs-input`, `ciFixAttempts` at the cap, empty
`conflictNotifiedHeadSha`) is read as already paused and notified when the supervisor
loads it, so arming it again does not open a second attention session.

A live CI or merge worker for this PR suppresses every other branch writer (merge-repair,
ci-repair, address-review) for the whole tick, across head changes, and at most one writer
launches per tick. With none live the priority is an observed conflict (merge repair),
then failing CI (ci-repair), then pending review feedback (address-review), which also
waits while CI repair is eligible, so its wait is bounded by `fixCap`. A conflict seen
while this PR's CI worker is live launches nothing but records the conflict episode, so
a later unknown mergeability keeps CI and review deferred. A known non-conflicting read
while this PR's merge-repair worker is still live keeps the episode
(`conflictRepairHeadSha`, `conflictFixAttempts`) and keeps CI and review deferred; it is
released on the first clear tick after that worker has ended, and a later conflict starts a
fresh episode. While conflicting, or while mergeability is unknown during a conflict
episode, CI repair and review dispatch wait for merge repair. Deferred CI work consumes
neither its retry budget nor its dedup state, so a failing
check at the same SHA can dispatch after the conflict clears. Review feedback
remains pending and unlaunched until it can dispatch safely. Neither repair launch
nor a changed head counts as proof that CI or review feedback has been resolved;
the existing automerge gates still apply.

## Rules

- **Distinguish comment staleness from review-resolution completeness.** In `tick()`, comments are detected asynchronously via `detectNewFeedbackKeys()` and can arrive mid-tick on already-resolved review threads (a new comment on a closed thread is a valid, late event). Review resolution, by contrast, is fully determined by the `reviews` slice fetched at tick-start and cannot become stale within the tick — a `CHANGES_REQUESTED` review that appears alongside a later `APPROVED` review in the same first-ever tick is a valid supersession, not a race. When reordering `tick()` operations (e.g., moving `reconcileFeedback` before `detectNewFeedbackKeys` to fix new-comment detection), verify the reorder's scope against the data source's guarantee: a reorder that protects asynchronous comment arrival must preserve the existing same-tick review-supersession logic for already-fetched data, or fix it separately with a narrowly-scoped block gated on `reviewsComplete`. Do not assume that breaking existing cross-package tests with a reorder means the old code was incorrect — audit the data source's completeness first (#897).
