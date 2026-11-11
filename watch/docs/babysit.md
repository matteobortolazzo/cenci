# Babysit Tick Logic: Comments vs. Reviews Data Sources

Guidance for refactoring and testing `tick()`, the core reconciliation loop that detects and resolves feedback.

Merge conflicts dispatch `merge-repair` automatically through Claude Opus, including
supervisors armed with Codex or OpenCode. The supervisor retains its original agent for CI
repair and review feedback (the finite `babysit-review` worker), and retains its recorded
tmux session and directory.
The built-in repair template runs Claude in one-shot print mode, forwarding
`--print` through `cenci open --` in a sandbox. Completion or failure exits the
worker instead of leaving an interactive prompt that would block retries.
Host launches retain the user's configured tool permissions; a denial reports
failure rather than bypassing permissions.
Each successful repair launch records the head SHA. Each tick reads the panes of the
exact recorded tmux session at most once (#1457), lazily, the first time a decision
needs liveness: a conflict, failing checks, a green tick with budget state to reset,
unlaunched review keys, an outstanding review launch (#1472), a launched discussion key
that still has no receipt, or the fleet automerge switch being on (so a live review window
holds automerge). A quiet green tick with automerge off and nothing outstanding, a MERGED or
CLOSED tick and a state with no recorded session never read it. That one `list-panes -s`
read on the recorded socket,
format `#{window_name}\t#{pane_dead}\t#{@cenci-repo-dir}`, supplies CI, merge and review
liveness. Only the trailing `\n` is trimmed, so a row with an empty `@cenci-repo-dir`
keeps all three columns; any other column count, an empty name or a `pane_dead` other than
`0`/`1` is malformed. A pane is this PR's worker only when its window is exactly
`<pr>-ci-repair`, `<pr>-merge-repair`, `<pr>-babysit-review` or `<pr>-address-review`, `pane_dead` is `0`, and its `@cenci-repo-dir`
(written by `cenci run`) is this repository: equal after `filepath.Clean` to the recorded
launch directory or repository root, or in the same git common directory (one bounded
`git rev-parse --git-common-dir` call, only when the paths differ). A live same-name window
that provably belongs to another repository is ignored with a printed note. One whose
`@cenci-repo-dir` is empty, relative, or cannot be resolved is a live writer of unknown
ownership: it suppresses launches, prints and records in `automergeDetail` the instruction
to let that worker finish or to close its window once its edits are preserved (for a
`ci-repair` or `merge-repair` window only, `cenci babysit reset <pr>` re-arms the retry
budget; a review window has no retry budget), and stops blocking when it ends. The note never
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
failing head or the reset ends the hold; a legacy hold does not defer `babysit-review`.
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
recover), and a live PID that is not this PR's babysit process is also refused. A
`stopped` state (what `cenci babysit stop` leaves, below) has no agent or budget to
reset, so reset refuses it with the `cenci babysit <pr> --agent <agent>` re-arm hint.

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

A live CI, merge or review worker for this PR suppresses every other branch writer
(merge-repair, ci-repair, babysit-review) for the whole tick, across head changes, and at
most one writer launches per tick. With none live the priority is an observed conflict
(merge repair), then failing CI (ci-repair), then pending review feedback
(babysit-review), which also waits while CI repair is eligible, so its wait is bounded
by `fixCap`. A conflict seen
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

## PR discussion, the review worker and receipts (#1472)

Ordinary PR discussion is a fourth feedback source, read next to inline comments and
reviews. `cenci run address-review` still works for a human and keeps its behavior, but
the supervisor no longer launches it: its review worker is `babysit-review`, finite and
nonce-bound.

**Ingestion.** Every tick, after the inline-comment and review reads, the supervisor reads
`repos/<repo>/issues/<pr>/comments` through the same paged, completeness-signalled helper,
and the final pre-merge recheck reads it again (read-only: it never consumes a receipt). Each
comment is a `discussion:<comment id>` key. Its revision is the hex SHA-256 of the body, and
the state keeps a seen-set of `(key, revision)` (`discussionRevisions`), not a `>=` timestamp
watermark, so two comments with equal timestamps returned on different ticks both register
and an edit is detected. Only two mechanical exclusions apply: a `[bot]` login, and a comment
whose first line is the cenci banner (which covers every receipt). A human quoting a banner
or a receipt marker below their own first line is ordinary feedback. Every other comment is
pending; whether it is actionable is the worker's call, recorded as `no-action`. Discussion
keys never go through review-thread lookup, so a state holding only `discussion:` keys makes
no `gh api graphql` call in a tick.

An edit to a pending key leaves `launchedFeedbackKeys` (a new episode, so a new launch for
the new revision). An edit to an addressed key drops its receipt and moves it back to
pending under `review feedback reopened`. A pending comment GitHub no longer reports holds
under `review feedback state unknown` until `cenci babysit resolve` clears it (below); a
deleted already-addressed comment is a no-op, because `resolve` only accepts pending keys and
a hold on it would have no recovery. A state written before this feature has no discussion
fields: its seen-set is empty, so on its first tick every non-excluded human discussion comment
is pending and launches (one burst on upgrade).

**Launch.** A review launch needs keys in `pendingCommentKeys` but not in
`launchedFeedbackKeys`, a complete discussion read, no conflict hold, no CI repair launched
this tick and no live writer. The call is `cenci run babysit-review "<pr> --receipt-nonce
<nonce> discussion:<id>=<sha256> …"`, pinned `--unattended`. The nonce is 32 hex characters
from `crypto/rand`, minted per launch; each discussion key rides with its revision, so the
worker echoes what it was given.

**Author trust.** The worker is unattended and reads untrusted text, so it may only act on
feedback from someone with write access. Building a launch, the supervisor looks up each listed
discussion comment author's repository permission (one lookup per login per tick). A key whose
author is definitely denied (the lookup answered something other than `admin` or `write`) is
marked `:untrusted` in the launch argument, `discussion:<id>=<sha256>:untrusted`; a trusted
author's token is unchanged. A lookup that fails (a `gh` error, a timeout, an undecodable
answer) is not a denial: that key is skipped for this launch, left pending and unlaunched, so
the next tick looks again (with every candidate skipped there is no launch that tick and no
nonce is minted), and `automergeDetail` notes `author permission lookup failed for <key>; will
retry`. Trust is default-deny: the launch records the keys whose author was granted as an
allow-list, `reviewLaunch.trustedKeys` (additive, no schema bump), and a discussion key in the
launch that is not in it is untrusted, so a state with no such record reads as untrusted, never
as trusted. For an untrusted key the worker never changes code, pushes or posts replies:
its outcome is `no-action` (the comment requests nothing) or `blocked` (a write-access human
must decide), and the supervisor refuses an `addressed` outcome for it (see Receipt). Inline
(`comment:`) and review (`review:`) feedback carries no token: the worker checks each distinct
author's permission itself and never acts on an item from a non-write author, leaving it
pending on GitHub, where it keeps holding automerge. A launch carries at most 40 discussion
keys. The rest stay
pending and unlaunched and go out on a later tick with no live writer (a deferred launch, not
a retry policy). A key whose comment is no longer on the PR at its recorded revision is not
launched.
`comment:` and `review:` keys ride in the launch record only; the worker resolves them from
GitHub. The built-in template runs Claude in `--print` mode and Codex through `exec`, so the
window ends. The launch records the nonce, the launch time and the key set in `reviewLaunch`
and adds the keys to `launchedFeedbackKeys`; a nonce it supersedes before consumption is
recorded as consumed, so a late receipt for it is rejected. `cenci run` exit status 3 counts as
dispatched. Any other failure consumes nothing: no nonce is saved and the keys stay
unlaunched.

**Receipt.** The worker posts one comment per launch:

```
> 🤖 **cenci** — review receipt posted by `/cenci:address-review` (babysit-review).
<!-- cenci-review-receipt:<32-hex nonce> -->
head: <40|64-hex>
- discussion:<id> revision=<64-hex> outcome=<addressed|no-action|blocked|failed>
```

The parser drops blockquote lines first and requires exactly one marker line and exactly one
`head:` line. A line starting `- discussion:` that does not match, or a duplicate key, rejects
the whole receipt. A receipt may list zero keys (a launch that carried only `comment:` or
`review:` keys); it consumes the nonce and moves nothing. A comment with no marker line is
not a receipt and produces no rejection. Receipts are checked in comment-id order, and one is
accepted only when all of these hold:

- its nonce equals the outstanding launch's nonce and has not been consumed;
- it is unedited (`updated_at` equals `created_at`);
- `created_at` is strictly after the launch time truncated to the second;
- its author's collaborator permission is exactly `admin` or `write` (`read`, `triage`,
  `maintain`, `none` and anything unrecognized are denied, recorded as `author lacks write
  permission`; the lookup runs only for a comment carrying the outstanding nonce). A lookup
  that fails (a `gh` error or timeout, an undecodable answer) is a distinct, also fail-closed
  rejection, `write permission lookup failed; will retry`: it does not consume the nonce, so
  the same receipt is accepted on a later tick once the lookup answers.

An accepted receipt consumes the nonce (`consumedReviewNonces` maps it to the receipt's comment
id) and then applies per key. `addressed` and `no-action` move a key to
`addressedCommentKeys` when the key was in the launch's set and the listed revision equals the
comment's current body hash; the state records, per key, the comment id, revision, processed
head SHA, outcome and receipt comment id (`discussionReceipts`, source `receipt`). A key outside
the launch's set, with a stale revision or with no current comment is left unchanged. A stale
revision therefore leaves the key pending, and a new episode launches once the worker ends.
`blocked` and `failed` keep the key pending and launched (no relaunch for the same revision),
set the status to `needs-input` and name `cenci babysit resolve <pr> <key>` in
`automergeDetail` (the automerge reason stays `review feedback pending`). An `addressed` outcome
for a discussion key in the launch that is not in its `trustedKeys` is not accepted as
addressing it: the receipt is still authentic and consumes its nonce, but that key is recorded
as `blocked`, stays pending and launched, sets `needs-input` and names `cenci babysit resolve
<pr> <key>`, and a note says its comment author is not recorded as having write permission so
the receipt cannot mark it addressed. `no-action`,
`blocked` and `failed` are accepted as reported for an untrusted key. Recovery: a write-access
human reads the comment, handles it or decides it needs nothing, and runs `resolve`. The
`:untrusted` suffix lives only in the launch argument; the receipt grammar is unchanged. A rejected receipt addresses nothing, launches nothing and is recorded in
`automergeDetail` as `receipt comment <id> rejected: <reason>`: a wrong, unknown or superseded
nonce, a consumed nonce reused (the comment that consumed it is skipped silently on later
ticks; any other carrying it is a rejection), an edited receipt, one created before the launch,
an author without write permission, or a malformed grammar. A receipt posted while its worker
pane is still live addresses its keys, but automerge stays held with `branch repair worker still
running` until the pane ends. The outstanding launch is cleared once its nonce is consumed (or
none of its discussion keys is pending) and its window has ended; it is never cleared in the
tick that created it, so a launch carrying only `comment:` or `review:` keys (settled the moment
it exists) stays outstanding until the worker's zero-key receipt consumes its nonce, or, with no
receipt, until a later tick sees its window ended.

**Liveness and priority.** `<pr>-babysit-review` is a counted branch writer beside
`<pr>-ci-repair` and `<pr>-merge-repair`, under the same ownership rule. A live review
window makes zero `ci-repair`, `merge-repair` and `babysit-review` launches, at an unchanged
head, after a head change and after a stop and re-arm that reloads state. A conflict seen
meanwhile records `conflictRepairHeadSha` and launches nothing, and the first tick after the
window ends launches `merge-repair` before any `ci-repair` or `babysit-review`. A discussion
comment created while the window is live is pending but unlaunched, and the earlier receipt does
not address it (it is outside that launch's key set); the first tick after the window ends
launches it. A live `<pr>-address-review` window (human-launched, or interactive and launched
before an upgrade) also blocks every supervisor launch and automerge with `branch repair worker
still running`, and because it never exits by itself its note always adds that you can close
it once its edits are preserved. Liveness covers only windows in the recorded tmux session: a
`/cenci:address-review` typed into an unnamed window cannot be detected. An unreadable pane
inventory, or a recorded session that no longer exists, makes zero launches and records `workflow
launch failed`, with every key still pending. A live `<pr>-babysit-review` pane whose
`@cenci-repo-dir` is provably another repository does not block.

**Holds and their recovery.** Each reason below is a recorded `automergeReason`, except the last,
which refuses at arm and in `status` because no tick runs.

- `PR discussion unreadable`: the `issues/<pr>/comments` read failed (a `gh` failure, a page that
  does not decode, or a comment object without `id`, `user.login` or `body`). The tick returns the
  error with every key byte-identical, never an empty discussion set, and the recheck fails the
  same way. Recovery: a transient failure (a `gh` error, a timeout) clears on its own, since the
  supervisor retries with backoff and the hold clears on the first tick whose read succeeds. A
  deterministic one (a comment object returned without `user.login`, for instance `user: null`)
  never succeeds: the PR holds on every tick until the offending comment is
  fixed or removed on GitHub, or you merge the PR by hand; the supervisor keeps retrying with
  backoff meanwhile.
- `PR discussion detection read truncated`: the page cap (10 pages of 100) was reached with a
  full last page. That tick makes no discussion mutation, consumes no receipt and launches
  nothing; the `comment:` and `review:` pass runs unchanged. Recovery: the hold clears on the
  first tick whose read is proven complete; a conversation that stays at the cap never clears,
  so merge by hand.
- `review worker ended without receipt`: a launched key is still pending with no receipt for its
  current revision and its `<pr>-babysit-review` window has ended. It stays pending and launched
  (no relaunch for the same revision), at lowest precedence among feedback holds, and
  `automergeDetail` names `cenci babysit resolve <pr> <key>`. Recovery: handle the comment and
  run `resolve`, or edit the comment so a new revision starts a new launch.
- `babysit state unreadable`: the state file cannot be read or decoded. Arming exits 1 with
  `babysit state unreadable: <path>: <cause>`, leaves the file byte-identical and starts no
  supervisor (the old fail-open load started from an empty `pendingCommentKeys`). `cenci babysit
  status <pr>` prints the same line to stderr and exits 1; the daemon-relayed reason stays
  path-free. Recovery: on the host, `cenci babysit stop <pr>` removes the unreadable file (the one
  case where stop removes instead of keeping state), then re-arm. The feedback bookkeeping is
  lost, so every comment is detected again. A stop forwarded from a sandbox refuses it and keeps
  the file.

**`cenci babysit resolve <pr> discussion:<id> [--state-dir <dir>]`** is how a human settles
those holds. It is host-only and attended-only: it refuses, changing nothing and exiting 1, with
`CENCI_SANDBOX=1`, with `CENCI_ATTENDED=0` (whatever set it, so a supervisor-launched worker can
never use it) or with `CENCI_BABYSIT_SUPERVISOR` set. It accepts only a `discussion:` key that is
in `pendingCommentKeys`: a `comment:` or `review:` key exits 1 with a pointer to resolve it on
GitHub, and a key not pending exits 1. It records a receipt with source `human-cli` at the
recorded revision (an edit still reopens the key) and follows reset: a live owned supervisor is
terminated and re-armed with its recorded settings, while a `stopped` state is written but not
re-armed. Exit codes: 0 resolved, 1 refusal or runtime failure, 2 usage error or an agent
arming would refuse. It clears the `review feedback state unknown` hold of a deleted pending
comment.

**Stop keeps feedback.** `cenci babysit stop <pr>` terminates a verified supervisor, waits for it
to exit, removes the lock and rewrites the state with status `stopped` and PID 0, keeping only
the feedback bookkeeping: pending, addressed and launched keys, the comment watermarks, discussion
revisions and receipts, the outstanding review launch and consumed nonces. Budgets, the agent,
CI status and closing issues are cleared, so a re-arm adopts a fresh agent, the retry budgets
restart as they always have and unresolved feedback keeps its hold. A comment posted while
supervision was down is collected on the first tick after re-arm. `status` prints `stopped` for
such a state.

## Rules

- **Distinguish comment staleness from review-resolution completeness.** In `tick()`, comments are detected asynchronously via `detectNewFeedbackKeys()` and can arrive mid-tick on already-resolved review threads (a new comment on a closed thread is a valid, late event). Review resolution, by contrast, is fully determined by the `reviews` slice fetched at tick-start and cannot become stale within the tick — a `CHANGES_REQUESTED` review that appears alongside a later `APPROVED` review in the same first-ever tick is a valid supersession, not a race. When reordering `tick()` operations (e.g., moving `reconcileFeedback` before `detectNewFeedbackKeys` to fix new-comment detection), verify the reorder's scope against the data source's guarantee: a reorder that protects asynchronous comment arrival must preserve the existing same-tick review-supersession logic for already-fetched data, or fix it separately with a narrowly-scoped block gated on `reviewsComplete`. Do not assume that breaking existing cross-package tests with a reorder means the old code was incorrect — audit the data source's completeness first (#897).
