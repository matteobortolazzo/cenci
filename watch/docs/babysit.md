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
Each successful repair launch records the head SHA. The supervisor checks every
pane in the exact recorded tmux session for a live `<pr>-merge-repair` worker.
Live workers suppress another launch, including across head changes; missing
windows or retained dead panes permit another attempt even at the same SHA.
Unreadable, empty, truncated or malformed pane inventories fail visibly without
authorizing a duplicate worker. Failed launches leave the marker and attempt count
unchanged so the next tick retries. A manually started live repair also suppresses
an initial duplicate and retains the conflict episode across unknown mergeability.

A conflict episode allows three repair launches before
opening `babysit-attention` for input while continuing to poll for recovery. A known
non-conflicting GitHub observation clears the episode; unknown mergeability
preserves its retry budget.
The older `conflictNotifiedHeadSha` state field remains readable, but an old
attention notification does not suppress the first automatic repair after upgrade.

While conflicting, or while mergeability is unknown during a conflict episode,
CI repair and review dispatch wait for merge repair. Deferred CI work consumes
neither its retry budget nor its head dedup marker, so a failing
check at the same SHA can dispatch after the conflict clears. Review feedback
remains pending and unlaunched until it can dispatch safely. Neither repair launch
nor a changed head counts as proof that CI or review feedback has been resolved;
the existing automerge gates still apply.

## Rules

- **Distinguish comment staleness from review-resolution completeness.** In `tick()`, comments are detected asynchronously via `detectNewFeedbackKeys()` and can arrive mid-tick on already-resolved review threads (a new comment on a closed thread is a valid, late event). Review resolution, by contrast, is fully determined by the `reviews` slice fetched at tick-start and cannot become stale within the tick — a `CHANGES_REQUESTED` review that appears alongside a later `APPROVED` review in the same first-ever tick is a valid supersession, not a race. When reordering `tick()` operations (e.g., moving `reconcileFeedback` before `detectNewFeedbackKeys` to fix new-comment detection), verify the reorder's scope against the data source's guarantee: a reorder that protects asynchronous comment arrival must preserve the existing same-tick review-supersession logic for already-fetched data, or fix it separately with a narrowly-scoped block gated on `reviewsComplete`. Do not assume that breaking existing cross-package tests with a reorder means the old code was incorrect — audit the data source's completeness first (#897).
