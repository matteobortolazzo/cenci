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

The per-PR liveness above sees only this PR's windows. Another launcher can still be
writing the same branch, so the ledger-locked admission below also checks the branch
(`headRefName`) across launchers: a dispatch worker holding that repo and branch defers
the repair with the reason `branch writer active`, and dispatch skips a ticket whose
recorded pipeline branch a babysit repair holds, with the same reason. The per-PR check
and this one run in the same critical section as the capacity check, so they cannot
disagree. A dispatch pickup with no recorded branch skips the branch check (the ticket
exclusivity still applies).

## Capacity: the shared worker limit (#1469)

Every branch-writing launch (`merge-repair`, `ci-repair`, `babysit-review`) reserves a slot
in the shared admission ledger, `$XDG_STATE_HOME/cenci/capacity/ledger.json`, immediately
before `launch()` runs `cenci run` (`admitRepair`, `internal/babysit/capacity.go`).
`launch()` itself refuses to run any of the three without a permit of its own kind, so a
new call site cannot start an uncounted worker. The slot counts against
`dispatch.concurrencyCap` (the machine cap, default 3) and, for a repo listed in
`dispatch.repos[]`, its `concurrencyCap` (clamped to the machine cap); a repo absent from
`dispatch.repos[]` is bound by the machine cap only. The count is the one dispatch uses:
ledger entries, plus running sessions no entry accounts for (manual workers included),
plus orphaned container sessions. Supervisor processes and sessions in a non-running
status count zero. A machine cap of `0` (the pause) admits no repair. A repair is admitted
when one slot is free, first come first served. `babysit-attention` is a human attention
prompt, not branch-writing work, so it is never charged and takes no permit. Nested LLM
subagents inside a worker are governed by their own bounded fan-out, not by this cap: a
worker holds one slot however many subagents it starts. Lowering a cap never terminates a
worker or a repair; admission only waits until used slots drop below the new cap.

Every tick of a live PR first lets the ledger release the reservations a complete tmux
inventory proves gone (the supervisor's own socket and every socket the ledger names; a
ledger with no entry costs no tmux read), whoever made them, so the next admitter sees the
freed slots. The limits are re-read from the fleet `config.json` at every admission, and
the daemon snapshot, the orphan count and the inventory are read just before the ledger
lock is taken, never inside it.

A refused repair is a **capacity wait**. The supervisor prints `PR #<pr>: <reason>; <kind>
waits for capacity, no window opened` (the reason is `concurrency cap reached` when the host
is full), records `capacityWait` (`reason`, `since`, `refreshedAt`) in its state for every reason, and,
only when the reason is `concurrency cap reached` (the host is full), writes a wait record to
the ledger. Any other refusal (a writer on the branch, the repo cap, an unreadable limit or
snapshot) is not a wait for a slot the repair could use, so it leaves no ledger wait, and a
wait the PR already held is dropped, so it cannot throttle dispatch into another repo. Nothing
is consumed: `ciFixAttempts`, `conflictFixAttempts`,
`lastRepairedCiHeadSha` and the conflict episode are unchanged, and pending feedback keys
stay in `pendingCommentKeys`, unlaunched, with no review nonce minted. The tick stays at its
base poll interval, so the repair is retried on the next poll. A wait record holds no slot.
It is refreshed on every tick the repair is still refused and expires after 3 poll
intervals (3 times the supervisor's sleep, one minute at least) without a refresh. It is
dropped on the first tick that no longer needs a repair, when the PR merges or closes
(before the state file is removed), and by `cenci babysit stop <pr>`. Waiting repairs have
priority over new dispatch pickups: dispatch admits only when its free slots exceed the
live wait records, while a repair needs a single free slot, and waiting repairs do not block
each other.

A wait record is owned by the process that wrote it, the supervisor (`ownerPid` and
`ownerStart`, the pid and `/proc/<pid>/stat` start time the ledger also records for an
entry's launcher; a refresh by another process takes the ownership over). A supervisor killed
without cleaning up therefore does not leave dispatch one slot short until the wait expires:
a wait whose owner is dead, or whose pid now has a different start time (a reused pid), is
dropped by the next `Admit`, `Reconcile` or `Wait`, and a dispatch `Admit` with exactly one
free slot is then admitted. Where the start time cannot be read (`ownerStart` empty), a wait
is dropped only when its owner pid is not alive, and otherwise expires by time. A wait of a
schema 1 ledger (`ownerPid` 0) has no owner: it is never dropped by the owner check, only at
`expiresAt` or by `DropWait`. A live owner's wait keeps the priority rule above unchanged.
`ledger.json` is schema 2 because of the owner keys; an older binary reads it as unreadable
and admits nothing until upgraded or restarted (`watch/README.md`).

The wait reasons fail closed: whenever the host's load cannot be known, nothing is admitted
and the reason says why.

| Reason | Meaning |
|--------|---------|
| `concurrency cap reached` | the machine cap is full |
| `repo concurrency cap reached` | the PR's repo reached its `repos[].concurrencyCap` |
| `worker already reserved` | another launcher holds a slot for this PR |
| `branch writer active` | a worker of the other launcher family (dispatch) holds this branch, or a live writer was observed |
| `capacity observation unavailable: daemon snapshot` | the daemon is not running or did not answer a snapshot within 5s |
| `capacity observation unavailable: orphaned container sessions` | the orphan count failed |
| `capacity limits unreadable: <reason>` | the fleet `config.json` is unreadable or invalid |
| `capacity launch target unresolved: <reason>` | the tmux socket or launch directory `cenci run` would use cannot be resolved (`$TMUX` with an empty socket field, or a working directory that is gone) |
| `capacity ledger unreadable: <path>: <reason>` | the ledger is corrupt (a bad entry or wait is named by position and failing check, never a value), has an unknown schema, is symlinked, was replaced by a writer on all three read attempts (`a capacity file kept changing while it was read`) or its lock was not taken within 5s; it is never reset automatically (manual recovery: `watch/README.md`) |

The entry names the socket and directory `cenci run` will record for the window, because the
ledger matches a window by socket, name and directory: an empty recorded socket is
`tmux.ResolveSocket()` (the first field of `$TMUX`, else the cenci socket) and the recorded
`--dir` is made absolute (else the supervisor's working directory), the way `cenci run`
resolves them. When the empty socket or the directory cannot be resolved, the repair waits
with `capacity launch target unresolved: <reason>` and nothing is stored. (A recorded socket
that is not a valid path is refused by `launch()`'s own re-check with its typed error, as
before; its transient reservation is released.)

A restarted supervisor adopts its own entry (same repo, PR and kind) left by a killed
predecessor instead of admitting a second one; an entry of a live foreign launcher is never
adopted. Adoption makes the entry a launch in flight again: it is `reserved` with the adopter
as its launcher and its `updatedAt` is now, so no inventory, however fresh, can release it
before the adopter confirms or releases it (a supervisor never adopts while this PR's worker
is live, so a running worker keeps its `confirmed` entry). After the launch, once `cenci run`
ran, whatever its exit status (3 means the window exists), the reservation is confirmed to the
deterministic window `<PR>-<workflow>`, and only a later complete inventory showing no such
window frees it. When `cenci run` never ran (for example `launch()` refused with `no tmux
session was recorded`), a fresh reservation is released, but an **adopted** one is restored,
never deleted: the predecessor's entry may be a `confirmed` one with a live worker (with no
recorded `LaunchSession` the supervisor reads no inventory for its own socket, so adoption
could not tell), and releasing it would undercount that worker. `Ledger.Restore` puts the
entry back to its pre-adoption `state`, `launcherPid` and `launcherStart` (carried in memory
on the permit as `PrevLauncherPID`/`PrevLauncherStart`; the file format is unchanged) and
sets `updatedAt` to now, so an inventory read before the restore cannot judge it. It changes
the entry only while it is still `reserved` and launched by this supervisor, so an entry
another process or a confirm already took over is left as it is, and it creates nothing: an
entry that is gone is logged with the existing `reservation is missing from the ledger`
line below and none is created. A restored entry is left to the ordinary release rules: a
`confirmed` one is freed by a complete inventory with no matching live window, and a
`reserved` one also needs its (dead) predecessor launcher, as before the adoption. A restore
that fails twice leaves the entry `reserved` under this supervisor, and the next tick's
self-heal (below) settles it from the entry's own socket inventory, which is correct whether
the worker is live or gone.

A `Release`, `Restore` or `Confirm` that fails with a transient error (the store lock was busy or the
store was briefly unreadable) is retried once after a short pause, the same bounded retry
dispatch uses (`capacity.RetryTransient`); a permanent error (a corrupt or unknown-schema
ledger, an invalid request) is not retried. A `Confirm` whose entry is no longer in the
ledger logs `capacity reservation is missing from the ledger (removed or reset)`: the
ledger file was removed, reset or replaced, and the worker is counted only while the daemon
sees it running. One that still fails leaves the entry `reserved` and the log line says so
(after a transient error the next tick retries it; after a permanent one, which also fails
the next tick's ledger read, the line points at the `capacity ledger unreadable` line and the
manual recovery in `watch/README.md`):
the entry's launcher is this supervisor, and a `reserved` entry is freed only when its
launcher is dead, so no inventory frees it while the supervisor runs; it holds its slot,
`repairWorkerLive` reads true and a repair of a different kind for the PR is refused `worker
already reserved`. The supervisor therefore settles its own stranded entry at the start of
the next tick, before any admission (the tick is synchronous, so nothing it launched is still
in flight), from the inventory the tick reads anyway: for a `reserved` repair entry of this
PR whose launcher is this supervisor (its pid and start time, the ledger's own ownership
rule, so a reused pid is not), a worker window that is running confirms it
(`capacity: confirmed the <kind> reservation left reserved by an earlier failed settle`), a
complete inventory read after the entry last changed with no live worker window releases it
(`capacity: released the <kind> reservation ...`), and an inventory that cannot say (a socket
not read, an incomplete or malformed listing) leaves it for a later tick. A settle that fails
again is logged as `the <kind> reservation could not be settled (<reason>); it stays
reserved` (a confirm whose entry is gone is logged as missing from the ledger instead). An entry of another PR, another repo, a dispatch worker or another live launcher
is never touched, and a dead launcher's entry is the adoption path's, not this one's.

`cenci babysit status <pr>` adds an `activity` key to its one JSON object, derived from the
persisted state alone (no tmux or ledger read), with this precedence: `needs-input` (the
persisted status is `needs-input`), then `repairing` (`repairWorkerLive`), then
`waiting-for-capacity` (a `capacityWait` refreshed within 3 poll intervals), then
`monitoring`. Every existing key is unchanged, and the state fields `capacityWait` and
`repairWorkerLive` are additive `omitempty` fields with no schema bump. `repairWorkerLive`
is true when the tick launched a repair or saw a live `<PR>-ci-repair`,
`<PR>-merge-repair` or `<PR>-babysit-review` pane. A quiet tick that needed no decision
reads no pane inventory (#1457) and so observes nothing; it takes the flag from the ledger
instead, after its own release of the entries a complete inventory proves gone: true while
the ledger still holds a repair entry of this PR, so a repair that is still running keeps
reading `repairing`, and false once that entry is released (the worker is gone). When the
ledger cannot be read, nothing proved the worker gone and the previous value stands. A
repair worker with no ledger entry (launched before the ledger existed) reads
`monitoring` on a quiet tick until a tick that probes.

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

**Launch.** A review launch needs keys in `pendingCommentKeys` but neither in
`launchedFeedbackKeys` nor in `heldUntrustedKeys` (the trigger is pending minus launched minus
held-untrusted keys; see Author trust), a complete discussion read, no conflict hold, no CI
repair launched this tick and no live writer. The call is `cenci run babysit-review "<pr> --receipt-nonce
<nonce> discussion:<id>=<sha256> …"`, pinned `--unattended`. The nonce is 32 hex characters
from `crypto/rand`, minted per launch; each discussion key rides with its revision, so the
worker echoes what it was given.

**Author trust.** The worker is unattended and reads untrusted text, so it may only act on
feedback from someone with write access, and the supervisor enforces that in Go for every key
type (#1539). Building a launch, it looks up each candidate's author through the per-tick
permission resolver (one lookup per login per tick): a `discussion:` key's comment author, a
`comment:` key's inline-comment `user.login` and a `review:` key's review `user.login`, the
last two read from the tick's already-fetched lists. A key whose item is missing from that read
or whose login is not a valid GitHub login is denied with no `gh` call. An author who is
definitely denied (the lookup answered something other than `admin` or `write`) is untrusted; an
untrusted `discussion:` key is marked `:untrusted` in the launch argument,
`discussion:<id>=<sha256>:untrusted`, while a trusted author's token is unchanged and
`comment:`/`review:` keys carry no token. A lookup that fails (a `gh` error, a timeout, an
undecodable answer) is not a denial: that key is skipped for this launch, left pending and
unlaunched, so the next tick looks again (with every candidate skipped there is no launch that
tick and no nonce is minted), and `automergeDetail` notes `author permission lookup failed for
<key>; will retry`. Trust is default-deny: the launch records the keys whose author was granted
as an allow-list, `reviewLaunch.trustedKeys` (additive, no schema bump; it now covers
`comment:` and `review:` keys as well), and a key in the launch that is not in it is untrusted,
so a state with no such record (a launch saved by an earlier build) reads as untrusted, never as
trusted. For an untrusted discussion key the worker never changes code, pushes or posts
replies: its outcome is `no-action` (the comment requests nothing) or `blocked` (a write-access
human must decide), and the supervisor refuses an `addressed` outcome for it (see Receipt). The
worker still checks each author's permission itself, as defense in depth.

*No untrusted-only launches.* Untrusted keys ride along only with a trusted one. When no
candidate has a trusted author, nothing launches and no nonce is minted: every untrusted
candidate is held for a write-access human in `heldUntrustedKeys` (additive state). A held key
stays pending, so automerge holds under `review feedback pending`, status is `needs-input` and
`automergeDetail` names the recovery: `cenci babysit resolve <pr> discussion:<id>` for a
discussion key, and "resolve `<key>` on GitHub, then run `cenci babysit resolve <pr> <key>` if it
was resolved as the automation login" for each `comment:`/`review:` key (#862, see Resolution
actor below). Such a key moves to addressed through the ordinary feedback path once GitHub's
resolution passes the actor rule, and leaves the held set (status returns to `running` once
nothing else needs a human). A held key is never in `launchedFeedbackKeys`, so
it never raises `review worker ended without receipt`. When no candidate is trusted but some
lookup came back unknown, nothing launches and the unknown key is retried next tick; denied
candidates are held once that lookup settles. The held set is left out of the launch trigger, so
a held key is looked at again only when a human acts: an edit or reopen of a held `discussion:`
key releases it, and the tick that reads the edit looks its author up afresh (still denied: held
again with its launch count kept; now `admin`/`write`: launched as trusted). A `[bot]`
author's inline comment or review is still excluded from detection and triggers no lookup.

*Untrusted launch cap.* An untrusted `discussion:` key rides in at most `fixCap` (3) launches
across all of its edits, counted per key in `untrustedLaunchCounts` (additive state; every
dispatched launch counts, `cenci run` exit status 3 included). Once a key is at the cap and its
author is still denied it is held like an untrusted-only key, so an outside author editing one
comment over and over cannot drive unbounded launches. Trusted `discussion:` keys never count:
a write-access author's edits keep launching once per revision. The count is kept when a
held key is edited and survives `reset`. A key already at its cap stays held even on a tick where
an unknown lookup defers holding the other untrusted candidates.

A launch carries at most 40 discussion keys. The rest stay
pending and unlaunched and go out on a later tick with no live writer (a deferred launch, not
a retry policy). A key whose comment is no longer on the PR at its recorded revision is not
launched.
`comment:` and `review:` keys ride in the launch record only; the worker resolves them from
GitHub. The built-in template runs Claude in `--print` mode and Codex through `exec`, so the
window ends. The launch records the nonce, the launch time, the key set and the PR head it
started from (`reviewLaunch.headSha`, used by the head-move hold below) in `reviewLaunch`
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
none of its discussion keys is pending), its window has ended and its head-move check (below)
has run; it is never cleared in the
tick that created it, so a launch carrying only `comment:` or `review:` keys (settled the moment
it exists) stays outstanding until the worker's zero-key receipt consumes its nonce, or, with no
receipt, until a later tick sees its window ended.

**Nonce pruning (#1539).** On a tick whose discussion read is complete (never a truncated one,
which cannot say which receipts are still on the PR), `consumedReviewNonces` shrinks to the
nonces a receipt marker in a current PR conversation comment still carries, plus the
outstanding launch's nonce whether or not a comment carries it; the prune runs at the end of the
tick, after any launch. A kept nonce keeps its comment's silent skip or its `nonce already
consumed` rejection (a superseded nonce, recorded with comment id 0, stays while its late
receipt comment is on the PR). A pruned nonce can only come back in a new comment, which is
rejected as `nonce is not the outstanding launch's` and addresses nothing.

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

**Resolution actor (#862).** GitHub stays authoritative for `comment:` and `review:` keys, but a
GitHub-side resolution counts as reviewer acceptance only when its actor is acceptable. The
signals are a resolved thread (`resolvedBy`), a `DISMISSED` review (the actor of its
`ReviewDismissedEvent`, read from the PR timeline) and a newer same-reviewer `APPROVED` or
`DISMISSED` review (its author, or its own dismissal actor); a newer `CHANGES_REQUESTED` review
supersedes nothing and triggers no lookup. The rule, in order:

1. An attestation recorded for exactly this key and this actor (`actorAttestations`, below)
   accepts.
2. A null or unmatched actor (a deleted account, or a dismissal with no event in a complete
   timeline read) is denied: detail `resolver unknown` for a thread, `dismissal actor unknown` for
   a dismissed review, `approver unknown` for a superseding approval with no author login.
3. An actor that equals the automation login, compared case-insensitively (`strings.EqualFold`),
   is denied, even when it is the thread's or review's own author. The automation login is the
   login the supervisor's own `gh api user` reports.
4. An actor equal to the thread's (first comment's) or review's own author is accepted with no
   permission lookup.
5. Anyone else needs `admin` or `write` collaborator permission, through the per-tick permission
   resolver (one lookup per login). A login that does not look like a GitHub login (a `[bot]` or
   App login) is denied with no `gh` call.

The automation login, the dismissal timeline and each permission are read lazily, at most once per
tick and once per pre-merge recheck, only when a `comment:`/`review:` key's verdict depends on them:
a state with only `discussion:` keys makes none of those calls. The rule runs on every resolution
path: the tick's feedback pass, its same-tick review-supersession block and the pre-merge recheck
(which stays read-only). It also re-checks addressed keys on every tick, so a key already addressed
whose current resolution fails the rule moves back to pending under the new hold below.

*Upgrade wave.* Because addressed keys are re-checked, a PR already supervised when this rule
shipped can hold once for each addressed key GitHub reports resolved by the automation login. And
since the workers babysit launches act under the supervisor's own `gh` identity, every resolution a
worker makes (and any a maintainer makes through that same login) now needs an attended host
`cenci babysit resolve`, so expect more `needs-input` than before.

*Residual risk.* GitHub reports no resolution timestamp for a review thread, so a thread that the
same attested actor unresolves and re-resolves between two ticks is invisible: the attestation
still matches. A worker that uses a different GitHub credential than the supervisor's `gh` is
outside the automation-login match; it is accepted only if it is the thread's author or has write
access.

**Holds and their recovery.** Each reason below is a recorded `automergeReason`, except the last,
which refuses at arm and in `status` because no tick runs. (Held untrusted keys, above, are not a
separate reason: they stay pending under `review feedback pending` with status `needs-input`.)

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
- `head moved during untrusted review launch` (#1539, `headMoveHold`): a review launch carried
  an untrusted item (a key outside its `trustedKeys`, which for a launch saved by an earlier
  build includes every `comment:`/`review:` key) and the PR head differs from the head the
  launch recorded (`reviewLaunch.headSha`). The worker acts under the maintainer's GitHub
  identity and could forge any GitHub-side signal, so only a human on the host clears this
  hold. It is checked once per launch, on the first tick that sees the launch's
  `<pr>-babysit-review` window ended (before that tick's own launch, with or without a receipt,
  so a push made after the receipt is caught) and a known live head. An unknown live head defers
  the check to the next tick with a known head: until it runs, `reviewLaunch` is kept even when
  its receipt was accepted (or it carried only `comment:`/`review:` keys), and no new
  babysit-review launch is made, so newly pending keys stay pending and launch on that tick. A
  missing recorded head fails closed. No hold is raised for a launch whose keys
  were all trusted, or whose head is unchanged. The hold records the head it saw. A second raise
  while it is recorded replaces that head with the newer one, so a single resolve covers
  everything up to it and a resolve naming the older SHA exits 1. It ranks at the top tier, above
  every other feedback hold. It blocks automerge and sets status `needs-input` only: CI repair,
  merge repair and review launches keep running. It survives a green CI read, a further head
  change, `cenci babysit stop` followed by a re-arm, and `reset`; a re-armed supervisor starts
  as `needs-input`. `automergeDetail` names `cenci babysit resolve <pr> head:<sha>` for the
  recorded head. Recovery: review every change up to that head, then run
  `cenci babysit resolve <pr> head:<sha>` (below). The guarantee that only an attended host
  `resolve` clears it relies on the worker running in the sandbox, where the babysit state
  directory is not mounted and `resolve`'s `CENCI_SANDBOX`/`CENCI_ATTENDED`/
  `CENCI_BABYSIT_SUPERVISOR` refusals apply; a worker launched on the host as the same user is
  outside that guarantee.
- `review resolution actor not authorized` (#862): GitHub reports a `comment:`/`review:` key
  resolved (a resolved thread, a dismissed review, a newer approval) but its actor fails the
  actor rule above. The key stays pending (an addressed key moves back to pending) and is
  recorded in `actorHeldKeys` (additive state, key to the actor login seen, `""` for an unknown
  actor), automerge holds under this reason and status is `needs-input`. `automergeDetail` reads
  `<key>: <why>; run cenci babysit resolve <pr> <key>`, where `<why>` is `resolver unknown`,
  `dismissal actor unknown` or `approver unknown` (the actor is null or unmatched), or
  `resolved by <login>, which is the automation login or lacks write access`, with `dismissed by`
  (a dismissed review) or `approved by` (a superseding approval) in place of `resolved by`. A held key is never relaunched to `babysit-review`
  (GitHub already reports it resolved, so a worker has nothing to address), nor is a key a human
  attested (`actorAttestations`) while it is still pending; GitHub reporting the thread unresolved
  drops the attestation, so a genuine reopen relaunches. Two lookup failures
  are not denials and are not held under this reason: they hold under `review feedback state
  unreadable` with the keys left exactly where they were, no `actorHeldKeys` change, and the next
  tick retries. They are `automation identity lookup failed` (the `gh api user` read failed or
  reported an unusable login; it fails the whole comment/review pass closed) and
  `resolution actor permission lookup failed for <key>; will retry`. A dismissal-timeline read
  that fails holds under `review feedback state unreadable`, and one that hits the page cap with
  more pages reported holds under `review feedback state truncated`; neither resolves a `review:`
  key that tick. Only a definite denial, the automation login or a null actor enters
  `actorHeldKeys`; only the held-key case sets `needs-input`, while the unreadable and truncated
  holds clear on their own. It ranks in the top tier with the other classification-level holds.
  Recovery: if the resolution was yours, an attended host runs `cenci babysit resolve <pr>
  <key>` (below), and the next tick moves the key to addressed; otherwise have an authorized
  person resolve (or unresolve) the thread on GitHub. The note for a held
  untrusted `comment:`/`review:` key (`heldUntrustedKeys`) gives the same two steps.
- `babysit state unreadable`: the state file cannot be read or decoded. Arming exits 1 with
  `babysit state unreadable: <path>: <cause>`, leaves the file byte-identical and starts no
  supervisor (the old fail-open load started from an empty `pendingCommentKeys`). `cenci babysit
  status <pr>` prints the same line to stderr and exits 1; the daemon-relayed reason stays
  path-free. Recovery: on the host, `cenci babysit stop <pr>` removes the unreadable file (the one
  case where stop removes instead of keeping state), then re-arm. The feedback bookkeeping is
  lost, so every comment is detected again. A stop forwarded from a sandbox refuses it and keeps
  the file.

**`cenci babysit resolve <pr> discussion:<id>|comment:<id>|review:<id>|head:<sha> [--state-dir <dir>]`**
is how a human settles those holds. It is host-only and attended-only: it refuses, changing nothing and exiting
1, with `CENCI_SANDBOX=1`, with `CENCI_ATTENDED=0` (whatever set it, so a supervisor-launched
worker can never use it) or with `CENCI_BABYSIT_SUPERVISOR` set. A `discussion:` key must be in
`pendingCommentKeys` (this covers a held untrusted discussion key), and a key not pending exits 1.
It records a receipt with source `human-cli` at the recorded revision (an edit still reopens the
key) and removes the key from `heldUntrustedKeys`. A `comment:<id>` or `review:<id>` target (#862,
positive integer id, otherwise a usage error) is accepted only while the key is pending and in
`actorHeldKeys`; any other `comment:`/`review:` key exits 1, leaves the state byte-identical and
points at resolving it on GitHub. It never marks the key addressed: it records an attestation
(`actorAttestations`, key to the actor the supervisor saw, an attestation time and source
`human-cli`) and removes the key from `actorHeldKeys` and `heldUntrustedKeys`, then prints
`Resolved <key> for PR #<pr>.`. GitHub stays authoritative: on the next tick the key moves to
addressed while GitHub still reports it resolved by that same actor. An attestation vouches only
for that actor: a different resolver is re-evaluated under the actor rule, and one whose thread
GitHub later reports unresolved is dropped, so a later re-resolution by the automation login holds
the key again. A `head:<sha>` target (#1539) is the only way to clear
the `head moved during untrusted review launch` hold: `<sha>` must be 40 or 64 lowercase hex
characters (anything else is a usage error, exit 2), and it must name the head the hold
currently records. With no hold recorded, or a different SHA (including an older one after a
second raise replaced the head), it exits 1 and leaves the state byte-identical; otherwise it
clears `headMoveHold` and prints `Resolved the head-move hold at <sha> for PR #<pr>.`. Either
target follows reset: a live owned supervisor is terminated and re-armed with its recorded
settings, while a `stopped` state is written but not re-armed. Exit codes: 0 resolved, 1 refusal
or runtime failure, 2 usage error or an agent arming would refuse. It clears the `review
feedback state unknown` hold of a deleted pending comment.

**Stop keeps feedback.** `cenci babysit stop <pr>` terminates a verified supervisor, waits for it
to exit, removes the lock and rewrites the state with status `stopped` and PID 0, keeping only
the feedback bookkeeping: pending, addressed and launched keys, the comment watermarks, discussion
revisions and receipts, the outstanding review launch and consumed nonces, plus the untrusted
bookkeeping (`heldUntrustedKeys`, `untrustedLaunchCounts` and `headMoveHold`, so a stop and
re-arm never drops a security hold), the resolution-actor bookkeeping (`actorHeldKeys` and
`actorAttestations`, #862; `reset` never clears either) and `automergePin` (#870: the
closing-issue set and base ref pinned at arm or on the first successful tick; it survives stop,
re-arm and `reset`, is never overwritten, and is gone only with the state file). Budgets, the agent,
CI status and closing issues are cleared, so a re-arm adopts a fresh agent, the retry budgets
restart as they always have and unresolved feedback keeps its hold. A comment posted while
supervision was down is collected on the first tick after re-arm. `status` prints `stopped` for
such a state.

## Rules

- **Distinguish comment staleness from review-resolution completeness.** In `tick()`, comments are detected asynchronously via `detectNewFeedbackKeys()` and can arrive mid-tick on already-resolved review threads (a new comment on a closed thread is a valid, late event). Review resolution, by contrast, is fully determined by the `reviews` slice fetched at tick-start and cannot become stale within the tick — a `CHANGES_REQUESTED` review that appears alongside a later `APPROVED` review in the same first-ever tick is a valid supersession, not a race. When reordering `tick()` operations (e.g., moving `reconcileFeedback` before `detectNewFeedbackKeys` to fix new-comment detection), verify the reorder's scope against the data source's guarantee: a reorder that protects asynchronous comment arrival must preserve the existing same-tick review-supersession logic for already-fetched data, or fix it separately with a narrowly-scoped block gated on `reviewsComplete`. Do not assume that breaking existing cross-package tests with a reorder means the old code was incorrect — audit the data source's completeness first (#897).
- **When a hold set gates a launch, enumerate every key state that exists while the confirming read is failing, not only the held state.** An attestation or held set that suppresses a `babysit-review` launch must also exclude the attested or held key from the launch trigger itself: after a human attests a held key, a failed feedback read on the next tick leaves that key pending and unheld, and without that exclusion the tick relaunches a worker on a thread GitHub already reports resolved and attested. Two independent reviewers flagged this on #862; cover it with a test that fails the confirming read after the attestation.
