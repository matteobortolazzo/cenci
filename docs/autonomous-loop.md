# The autonomous loop

> Refine a ticket, walk away, review the merged PR.

cenci's default posture is human-gated: you approve the refined ticket, you approve
the plan, you approve the merge. Four opt-in switches remove those gates one at a
time, up to a loop that runs **refine → plan → implement → PR → merge → next ticket**
with no human touch between refinement and merge.

This page is the map: what each switch buys, how to turn them on, what still stops
the machine, and how to read a decision when it doesn't do what you expected. The
exhaustive reference for each piece lives elsewhere — see
[Where the details live](#where-the-details-live) at the bottom.

## The four switches

Nothing here is on by default, and no switch implies another. Each one is
independently reversible.

| # | Switch | Where | Default | What it removes |
|---|---|---|---|---|
| 1 | `planning.autonomy: "lean"` | repo `.cenci/config.json` (committed) | `"interactive"` | The **plan-review gate**. A plan with no escalations approves itself and implementation continues in the same session. |
| 2 | `dispatch.planRefined: true` | `~/.config/cenci/config.json` | `false` | The **manual planning launch**. `cenci dispatch` starts planning sessions for `Refined` tickets and re-plans stale plans by itself. |
| 3 | `automerge` policy block | repo `.cenci/config.json` (committed) | absent = deny | Nothing on its own — it *defines* the risk envelope automerge is allowed to act inside. |
| 4 | `automerge.enabled: true` | `~/.config/cenci/config.json` | `false` | The **merge gate**. `cenci babysit` merges the PR itself once every condition holds. |

Switches 1 and 3 are per-repo and committed, so the repo decides its own autonomy.
Switches 2 and 4 are fleet-wide kill switches on your machine: they can only ever
*permit* what a repo already opted into, never grant it. Turning on `planRefined`
fleet-wide does nothing to a repo that hasn't committed `planning.autonomy: "lean"`.

## What the loop looks like

```
  ┌─ you: /cenci:refine 42 ──────────────────────────────────────┐
  │   ticket scoped, acceptance criteria written                 │
  │   Confirmation Gate → you grant (or withhold) automerge:ok   │
  └──────────────────────────────┬───────────────────────────────┘
                                 │  label: Refined
                                 ▼
             cenci dispatch  ──── planning pickup ────▶  plan written
              (switch 2)                                 (switch 1:
                                 │                        self-approved)
                                 │  label: Planned + Working
                                 ▼
                         implementation runs ───▶ tests, review agents, PR
                                 │
                                 │  label: In Review
                                 ▼
                        cenci babysit supervises
                        CI green? feedback clear?
                                 │
                                 │  switches 3 + 4 + automerge:ok
                                 ▼
                              squash merge ───▶ label: Implemented
                                 │
                                 │  merged PR shifts shared files
                                 ▼
                    the next ticket's plan goes stale ───▶ auto re-plan
                                 │                          (switch 2)
                                 └────────▶ and around again
```

The re-plan step is what makes it a *loop* rather than a one-shot: after a dependency
merges, a sibling plan written against the old tree is detected as stale and
re-planned automatically instead of being skipped as unusable.

Two off-ramps exist and both are normal:

- **`Input Needed`** — planning hit one of five escalation classes (security-sensitive,
  destructive/irreversible, contradicts the refined ticket, genuine product ambiguity,
  scope blowup). It writes a draft plan, posts the question on the ticket, and stops.
  The posted question opens with a cenci banner telling you that replying on the ticket
  is what resumes the run. Answer on the ticket and dispatch resumes it on its next pass
  — no manual re-run.
- **A held merge** — babysit logs exactly why and retries next tick. Merge by hand any
  time; babysit never fights you for it.

## Quick start

### 0. Get dispatch working manually first

Autonomy is an amplifier, not a starting point. Before enabling anything below,
confirm an ordinary dispatch pickup works: enroll the repo, leave the loop off, and
run one pass by hand against a ticket that already has an approved plan.

```bash
cenci dispatch enroll                # from inside the repo
cenci dispatch status
cenci dispatch                       # one-off pass, fleet-wide
```

`enroll` detects the repo and directory but cannot guess where its windows should
spawn: set that repo's `repos[].session` to an existing tmux session name in
`~/.config/cenci/config.json` (enroll prints a reminder). A repo with no session, or
one naming a session that doesn't exist, is skipped for the whole pass.

If a `Planned` ticket still isn't picked up, fix that first — every switch below runs
through the same gate chain (assignee, dependencies, capacity, budget, quiet hours).

### 1. Let plans approve themselves

In the repo's committed `.cenci/config.json`:

```json
{
  "planning": { "autonomy": "lean" }
}
```

Any value other than the exact string `"lean"` — including a missing block — means
`"interactive"`, unchanged behavior.

`/cenci:configure` offers `planning.autonomy` when the key is absent, defaulting to
`interactive`.

**Commit and push this to `main`.** Dispatch reads it from
`refs/remotes/origin/main` after a successful `git fetch`, never from your working
tree and never from local `HEAD`. An unpushed local edit grants nothing; a revocation
pushed to `origin/main` takes effect on the next pass.

### 2. Let dispatch start planning sessions

In `~/.config/cenci/config.json`:

```jsonc
// ~/.config/cenci/config.json — your machine's fleet config, not the repo's
{
  "dispatch": {
    "planRefined": true,
    "planStalenessTolerance": 5,
    "concurrencyCap": 3,
    "dailyQuota": 20,
    "quietHours": { "startHour": 22, "endHour": 7 }
  }
}
```

The switch itself doesn't need hand-editing:

```bash
cenci dispatch plan-refined on       # or: off
cenci dispatch plan-refined status   # fleet flag + this repo's remote-confirmed autonomy + combined verdict
```

writes `planRefined` with an atomic, key-preserving update (creating the file if
it doesn't exist yet); the tuning fields above (`planStalenessTolerance`, caps,
quiet hours) remain hand-edited.

`planRefined` turns two terminal skips into work: a `Refined` ticket with no plan
file becomes a planning pickup, and a `Planned` ticket whose plan has fallen more
than `planStalenessTolerance` commits behind becomes an autonomous re-plan.

> **Trust boundary.** A planning pickup consumes the ticket body and comments as its
> primary input, with no author-authorization check on that text. Do not enable
> `planRefined` for a repo that accepts issues from untrusted parties. (The
> `Input Needed` resume path is stricter: the replying author must currently hold
> `admin` or `write` on the repo, re-resolved every pass.)

### 3. Declare what a merge is allowed to touch

In the repo's committed `.cenci/config.json`. This block is **deny-by-default**:
absent, unreadable, or malformed means no PR merges, and there are no built-in
fallback thresholds.

```json
{
  "automerge": {
    "protectedPaths": [
      "*.github/workflows/*",
      "*install.sh",
      "*/security/*"
    ],
    "maxChangedFiles": 25,
    "maxDiffLines": 800,
    "mergeMethod": "squash"
  }
}
```

- `maxChangedFiles` and `maxDiffLines` are **required** whenever the block exists.
  Missing or non-positive makes the whole block malformed, which denies.
- `protectedPaths` are globs where `*` matches any character including `/`,
  case-insensitive. Any changed file matching any pattern denies that PR. A pattern
  ending in `/` with no trailing `*` matches that directory and everything under it
  (e.g. `flow/skills/` protects the whole directory, not a literal-string match).
  Each pattern is anchored against the **whole repo-relative path from the root**, so
  a bare pattern with no leading `*` (e.g. `install.sh`) only matches a file literally
  at the repo root — prefix with `*` to match anywhere (e.g. `*install.sh`).
- `mergeMethod` is read for compatibility, but only `squash` is ever executed —
  `merge` or `rebase` produces a logged hold, not a merge.
- The block is always read from the **PR's base branch**, so a PR can never widen its
  own policy to approve itself.

In a monorepo, put a block on each `projects[]` entry (a file falls to the entry with
the longest matching `path` prefix, then to the top-level block). When one PR spans
several blocks the effective policy is the *most restrictive* merge: the minimum of
each cap and the union of every `protectedPaths`.

`/cenci:configure` offers to scaffold this block only when `automerge` is absent — an
existing block is reported verbatim and never re-prompted, narrowed, or removed.

### 4. Arm the merge

Fleet switch, in `~/.config/cenci/config.json`:

```jsonc
// ~/.config/cenci/config.json — `enabled` lives here and nowhere else
{
  "automerge": { "enabled": true }
}
```

Or, without hand-editing:

```bash
cenci automerge on       # or: off
cenci automerge status   # fleet switch + this repo's per-scope policy summary
```

Then, per ticket, grant `automerge:ok` at `/cenci:refine`'s Confirmation Gate. This
is the one thing in the loop that is always a human decision and has no repo-level
default. It is **never inherited** — a split child, a companion design ticket, and a
followup each earn it on their own merit, or don't. A PR that closes several issues
needs the grant on *every* one of them.

The refiner withholds by default for security-sensitive paths, release/CI workflow
files, visually verifiable UI work, irreversible migrations — and whenever it's
uncertain.

### 5. Start the loop

```bash
cenci dispatch loop on      # recurring fleet-wide passes
cenci dispatch loop status
```

A good first run: refine one small ticket with `automerge:ok` granted, then watch it
land. `cenci status` shows live sessions; babysit's decision line (below) shows why a
PR did or didn't merge.

## What still stops the machine

These are not configurable away, and they're the reason the loop is safe to leave
running.

**Planning refuses to guess.** Even in lean mode, a plan is only self-approved when
*all* of these hold. Each can only disqualify — none can ever promote a ticket onto
the fast path:

- no escalation in the five named classes;
- no unresolved `### Open Questions` in the planner's output;
- no file in `### Files to Modify`/`### Files to Create` matching the sensitive-path
  set (auth, session, credential, token, `.pem`, `.env`, permission, rbac, crypto,
  payment, migration, schema, … unioned with your `security.sensitivePaths`);
- size estimate is not `L` and there's no split recommendation;
- no `awaiting-input` draft already on disk for the ticket.

Anything inconclusive — an unreadable `.plans/` directory, a malformed config —
fails closed to the interactive path.

**The merge chain is fail-closed at every link.** "Green" is pass-only and strict: at
least one check must exist and every check's bucket must be exactly `pass`. A
cancelled, skipped, empty, or unrecognized bucket each hold under their own reason.
Review feedback resolution is GitHub-authoritative — pushing a commit does not clear
a thread; only `isResolved`, a `DISMISSED` review, or a newer `APPROVED` does. Any
state babysit cannot positively confirm (unreadable, truncated pagination, unknown,
unsupported) holds rather than proceeding.

**The verdict is re-checked immediately before mutating.** The fleet switch, the
feedback state, and the PR's head SHA are all re-read at merge time. A thread
reopened, a check regressed, a commit pushed, or the kill switch flipped between
evaluation and merge holds the merge — each under its own distinct reason, so a
late flip is never confused with an ordinary first-pass hold.

**Merges are squash-only** and never `--delete-branch` (a PR worktree still
references the branch). A merge rejected by branch protection is logged and retried,
never bypassed. A zero-exit `gh pr merge` isn't taken as proof: babysit refetches
once and requires `MERGED`, otherwise the tick is indeterminate, not successful.

## Reading a decision

Every enabled tick logs exactly one line per PR, including ticks that failed to read
upstream state:

```
babysit: automerge PR #42 held: ticket lacks automerge:ok [enabled=yes label=no ci=- review=- mergeable=- headsha=- policy=- files=- filecap=- lines=- protected=- method=- queue=-]
```

The bracket is the condition chain in evaluation order. `yes` = passed, `no` = the
stage that failed, `-` = never reached because an earlier stage short-circuited. So
the line above reads: the fleet switch is on, the label check failed, nothing after
it was evaluated.

| Key | Stage | Fails when |
|---|---|---|
| `enabled` | Fleet kill switch | `automerge.enabled` is not `true` in `~/.config/cenci/config.json` |
| `label` | Per-ticket grant | The PR closes no issue, an issue's labels are unreadable, or any closed issue lacks `automerge:ok` |
| `ci` | Strict pass-only CI | No checks reported, or any check's bucket is `fail`, `pending`, `cancel`, `skipping`, empty, or unrecognized |
| `review` | Feedback | CI repair in flight, pending feedback, a reopened resolution, or a detection read that couldn't be proven complete |
| `mergeable` | PR state | Draft, `MERGEABLE` unknown, or not mergeable |
| `headsha` | Head commit | The PR's head SHA is unreadable at evaluation time |
| `files` | Diff readability | Zero changed files, or a truncated file list |
| `policy` | Policy block | `.cenci/config.json` on the base branch is unreadable, absent, or malformed |
| `filecap` | `maxChangedFiles` | The diff changes more files than the cap |
| `lines` | `maxDiffLines` | Additions + deletions exceed the cap |
| `protected` | `protectedPaths` | A changed file matches a protected glob |
| `method` | Merge method | Policy method isn't `squash`, or the repo disallows/doesn't report squash |
| `queue` | Merge queue | The PR requires or is in a merge queue, or the queue probe was unreadable |

A trailing `class=<class>` appears when the hold came from a `gh` failure
(command/timeout/cancelled/truncated/parse). Every hold has its own distinct reason
string — dozens of them, never collapsed into a shared one — precisely so a log line
tells you which link broke rather than a generic "not ready".

Three reasons need a human and will not clear on their own: `review feedback state
unreadable`, `review feedback state unknown` (GitHub stopped reporting a comment or
thread — deleted or purged), and `unsupported review feedback type`. Merge those by
hand.

## Turning it off

| To stop… | Do this | Takes effect |
|---|---|---|
| All merging, everywhere | `cenci automerge off` (sets `automerge.enabled: false`) | Next tick, including a tick already mid-evaluation |
| Merging for one repo | Remove/narrow the repo's `automerge` block on the base branch | Next tick |
| Merging for one ticket | Remove `automerge:ok` from the issue | Next tick |
| Autonomous planning, everywhere | `cenci dispatch plan-refined off` | Next pass |
| Autonomous planning for one repo | Push `planning.autonomy` off `"lean"` to `origin/main` | Next pass with a successful fetch |
| All dispatch | `cenci dispatch loop off` | Immediately; in-flight sessions finish |

A revocation pushed to `origin/main` is honored even if your local checkout still has
`"lean"` cached — the remote object is the only authority.

## Known limits

Accepted and documented, not bugs:

- **Re-plans are unbounded.** Nothing caps how often a ticket can be re-planned. A
  successful re-plan rewrites the plan's commit baseline, which self-limits the common
  case, but an over-broad `stalenessPaths` can re-plan repeatedly. `dailyQuota` and
  `concurrencyCap` are the rate limiter; raising `planStalenessTolerance` raises the
  trigger threshold.
- **Sibling serialization is inert for planning pickups.** It's derived from plan-file
  front matter, which doesn't exist yet for a `Refined` ticket, so several children of
  one parent can enter planning in the same pass. Declared blocked-by chains
  still serialize.
- **A persistently failing resume can loop.** A session that claims `Working` and then
  fails restores `Input Needed` in-session, so dispatch re-resumes it next pass, with
  no attempt counter guarding that specific loop. Bounding it is deferred.
- **Staleness is scoped, not absolute.** A plan whose `stalenessPaths` under-scopes its
  real dependencies can report `fresh` while a relevant change landed outside that
  scope.
- **Merge state can lag by one supervision interval.** babysit re-evaluates on its own
  cadence (`babysitInterval`, default `15m`), so a PR can sit merge-ready for up to one
  interval.

## Where the details live

| For… | Read… |
|---|---|
| The full board lifecycle, labels, and every transition | [Orchestration recipe](orchestration.md) |
| Dispatch gates, pickup rules, reconciliation, config reference | [cenci-watch README — Auto-dispatch](../watch/README.md#auto-dispatch-cenci-dispatch) |
| Every automerge condition, in full | [cenci-watch README — Automerge](../watch/README.md#automerge-cenci-babysit) |
| The `automerge` config schema, field by field | [configure skill](../flow/skills/configure/SKILL.md) |
| Lean planning as the planner actually executes it | [Phase 1 — Plan](../flow/skills/implement/phases/phase-1-plan.md) |
| Which test pins which claim on this page | [Pipeline coverage map](pipeline-coverage-map.md) |
