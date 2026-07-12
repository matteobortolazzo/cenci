---
name: babysit
description: "Claude Code-only: follow an open PR by periodically checking CI and review comments until it merges or closes. Runs only through /agentflow:babysit or its loop."
compatibility: Requires Claude Code loop scheduling, slash commands, subagents, and interactive gates.
argument-hint: <pr-number> [interval e.g. 15m]
user-invocable: true
model: sonnet
allowed-tools: Read, Write, Edit, Bash, Glob, Grep, Task, AskUserQuestion, SlashCommand, ScheduleWakeup
---

> **Interaction rule**: Every question, confirmation, or approval directed at the user — anywhere in this skill, including error recovery — MUST be asked with the `AskUserQuestion` tool. Never ask in plain text. If an instruction says "ask the user" or "confirm", that means `AskUserQuestion`.

Read the `subagent-safety` reference skill before delegating work to subagents.
Read the `shell-rules` skill before running any `gh` commands (covers the heredoc temp-file pattern, one-command-per-Bash-call, and no cross-dir `cd` compounds).

## What this is

`/agentflow:babysit <pr>` keeps an open PR moving while you're away. On each run it does one
**tick**: fetch PR state, auto-fix red CI, and drive any genuinely new review comments
through `/agentflow:address-review`. It arms a self-paced Claude Code `/loop` so the tick
repeats (~15m by default) and stops itself when the PR merges or closes.

On the terminal tick, when the PR **merges**, babysit also performs the board-state
transition that Phase 9 deferred: it swaps each closed issue's `In Review` label for
`Implemented` (Phase 9 applies `In Review` at PR-open; merge is what makes it done). A
PR that closes **without** merging leaves the labels untouched.

This skill is **model-invocable on purpose** (note the deliberate absence of
`disable-model-invocation` in the frontmatter): a scheduled loop fire re-delivers the
`/agentflow:babysit <pr>` command, which only runs if the skill can be invoked by the model.
Its description is scoped tightly to a specific PR number so it does not auto-trigger on
unrelated turns.

## Context

**Config check**: Before anything else, verify `.claude/config.json` exists by reading it.
If the file does not exist, **stop immediately** and tell the user:
"agentflow is not configured for this project. Run `/agentflow:configure` first to set up."

Read `.claude/config.json`.

**Parse `$ARGUMENTS`:**
- **PR number**: the first whitespace-delimited token, with any leading `#` stripped
  (`#42` → `42`, `7` → `7`). If no PR number is present, stop and tell the user to pass
  one: `/agentflow:babysit <pr-number>`.
- **Interval** (optional second token): a duration like `15m`, `10m`, `600s`, or a bare
  number of seconds. Default `15m`. Convert to seconds and **clamp to `[60, 3600]`**
  (`ScheduleWakeup`'s allowed range). Store as `intervalSeconds` (default `900`).

**Derive `<owner>/<repo>`** from `git remote get-url origin`
(e.g. `git@github.com:owner/repo.git` → `owner/repo`).

**State file** (session-scoped, matching `/loop`'s scope):
`/tmp/claude/babysit-<pr>.json`. It is the watermark **and** the arm-once guard. Shape:

```json
{
  "armed": true,
  "intervalSeconds": 900,
  "currentDelaySeconds": 900,
  "lastCommentTimestamp": "2026-07-09T12:00:00Z",
  "addressedCommentIds": [123, 456],
  "lastCiHeadSha": "abc123…",
  "ciFixAttempts": 0
}
```

`intervalSeconds` is the immutable base interval parsed from `$ARGUMENTS` (clamped
`[60, 3600]`). `currentDelaySeconds` is the mutable pacing value — it starts equal to
`intervalSeconds` and backs off on quiet ticks (step 6), resetting to `intervalSeconds`
whenever a tick is actionable (step 7).

Read it at the start of every tick (via `Read`; treat "file not found" as **absent** —
this is the arming run). Write it back through the `shell-rules` temp-file pattern or the
`Write` tool. `CI_FIX_CAP = 3`.

**Backward compat**: a state file written before this field existed (an in-progress loop
from an older babysit version) will lack `currentDelaySeconds`. Treat that as
`currentDelaySeconds = intervalSeconds` for that tick, then persist the field going
forward.

**Deterministic tick data**: `scripts/tick.sh` gathers steps 1, 4, and 5's raw data (PR
state, CI checks, and mechanically-filtered candidate comments) in one deterministic call
instead of four separate `gh` calls re-derived in-model each tick. Invoke it as a single
Bash call (the `shell-rules` reviewed-repository-script carve-out):

```bash
"${CLAUDE_PLUGIN_ROOT}/skills/babysit/scripts/tick.sh" <owner> <repo> <pr> <state-file>
```

Each of steps 1, 4, and 5 below states its own preferred-script / fallback-to-raw-`gh`
behavior; the fallback text in each step is the literal pre-script behavior, unchanged.

## Tick pipeline

Execute these steps in order on **every** invocation — the first manual run and each loop
re-fire alike.

### 1. Fetch PR state

Prefer `scripts/tick.sh`'s output for this step — its `terminal` (`state`, `mergedAt`,
`closingIssuesReferences`) and `headRefOid` fields cover it. On a nonzero exit or
non-JSON stdout from `tick.sh`, fall back to the raw call exactly as below:

```bash
gh pr view <pr> --repo <owner>/<repo> --json number,title,state,headRefName,headRefOid,mergedAt,closingIssuesReferences
```

### 2. Terminal check (stop the loop)

If `state` is `MERGED` or `CLOSED`:
- **On `MERGED` only** (never on a `CLOSED`-unmerged PR): promote the board state of every
  issue this PR closes. For each issue in `closingIssuesReferences`, swap its label from
  `In Review` to `Implemented` — this is the `In Review → Implemented` board transition that
  Phase 9 deferred to merge:

  `gh issue edit --add-label` fails when the label does not exist in the repository, so ensure
  it exists first (its own Bash call; `|| true` swallows only the "already exists" error):

  ```bash
  gh label create "Implemented" --repo <owner>/<repo> --color "6F42C1" --description "PR merged — done" 2>/dev/null || true
  ```

  ```bash
  gh issue edit <n> --repo <owner>/<repo> --add-label "Implemented" --remove-label "In Review"
  ```

  Run one `gh issue edit` per issue as its own Bash call (per `shell-rules`' one-command-per-call
  rule). This is a **mutating `gh` call**, so it stays in the **main agent** per `subagent-safety` —
  never delegate it. GitHub's `Fixes #<n>` reference has already auto-closed each issue; the relabel
  only moves it into the done column. A `CLOSED`-unmerged PR abandons the work, so it must **not**
  relabel anything to `Implemented`.
- Report a final one-paragraph summary (merged vs. closed, title, link).
- If a loop was armed (state file exists / `armed: true`), call
  `ScheduleWakeup(stop: true)` to end the self-paced loop. If nothing was armed this is a
  safe no-op — calling it is harmless.
- Delete the state file: `rm -f /tmp/claude/babysit-<pr>.json`.
- **STOP.** Do not arm a loop, do not schedule another wakeup.

### 3. Arm-once (self-arm the loop)

- **If the state file is absent** → this is the arming run:
  1. Write the state file with `armed: true`, `intervalSeconds`,
     `currentDelaySeconds: intervalSeconds`, empty `addressedCommentIds`,
     `ciFixAttempts: 0`, and `lastCommentTimestamp` / `lastCiHeadSha` left null (they are
     set as steps 4–5 run this same tick).
  2. Arm the self-paced loop by invoking the `/loop` slash command **without an interval**
     (self-paced mode) via the `SlashCommand` tool:
     `/loop /agentflow:babysit <pr> <interval>`
     Self-paced mode is what lets the tick pace itself (step 7) and stop itself (step 2);
     a fixed-interval `/loop` cannot be stopped from inside the body.
- **If the state file exists** → a loop is already armed. **Skip arming** (this prevents
  double-arming when the loop re-fires).

**Graceful degradation**: if the `SlashCommand` tool is unavailable, `/loop` errors, or
`ScheduleWakeup` is unavailable, do **not** let loop setup block the tick. Complete steps
4–6 for this one tick, then tell the user to run `/loop /agentflow:babysit <pr>` manually to
keep it going. (Mirror the `/goal` autopilot's "native-command + graceful no-op" shape in
`implement/SKILL.md`.)

### 4. CI check (auto-fix red, capped)

Prefer `scripts/tick.sh`'s `checks.failing` / `checks.allPass` / `checks.anyPending`
fields — the `bucket` categorization below is already done. On a nonzero exit or
non-JSON stdout from `tick.sh`, fall back to the raw call exactly as below:

```bash
gh pr checks <pr> --repo <owner>/<repo> --json bucket,name,state
```

Categorize each check by its `bucket` field (`pass` / `fail` / `pending` / `skipping` /
`cancel`).

- **Reset the cap** first: if `headRefOid` differs from `lastCiHeadSha`, the branch has
  advanced — set `ciFixAttempts = 0` (a new push gets a fresh budget of fix attempts).
- **If any check is `fail`** *and* `headRefOid` differs from `lastCiHeadSha` *and*
  `ciFixAttempts < CI_FIX_CAP`:
  1. Delegate a **diagnosis + fix** to the `implementer` subagent (subagent-safe: it reads
     the failing job logs, finds the root cause, edits files, and runs the project's local
     build/test — no pushes, no mutating `gh`). Pass it the failing check names, the
     `<owner>/<repo>`, and the PR's `headRefName`; tell it to checkout/enter the PR branch
     worktree, fix the root cause, and confirm the local build/test pass.
  2. Back in the **main agent** (never the subagent): commit the fix and
     `git push origin <headRefName>`. Never force-push.
  3. Set `lastCiHeadSha = headRefOid` and increment `ciFixAttempts`.
- **If any check is `fail`** but the cap is reached (`ciFixAttempts >= CI_FIX_CAP`), or the
  failure cause is **ambiguous** (can't localize a root cause, flaky/infra failure,
  external dependency) → escalate via `AskUserQuestion` (this surfaces as NeedInput to a
  watcher). Offer, e.g., "Keep retrying", "I'll fix it manually", "Stop babysitting".
  Do **not** silently loop on the same red SHA.
- **If all checks `pass` / `pending` / `skipping`** → nothing to do for CI this tick.

The existing `deny: Bash(git push --force:*)` rule holds — never force-push under any
branch of this step.

### 5. New-comment check (delegate to address-review)

Prefer `scripts/tick.sh`'s `candidateComments` / `excludedCounts` fields — the mechanical
exclusions (bot logins, resolved threads, outdated comments) are already applied. Read
the `pr-comment-filter` reference skill and apply its **semantic** subset only (requesting
changes vs. informational, inline-suggestion judgment) to `candidateComments`.

**Pagination guard**: if `tick.sh`'s `warnings` array contains a `reviewThreads`
pagination warning (more resolved-thread data exists than was fetched), its `terminal`
and `checks` data are still good, but ignore `candidateComments` for this tick and fall
back to the raw fetch + in-model filter below instead.

On a nonzero exit or non-JSON stdout from `tick.sh` (or the pagination case above), fall
back to fetching reviews and comments **in parallel**:

```bash
gh api repos/<owner>/<repo>/pulls/<pr>/reviews
```
```bash
gh api repos/<owner>/<repo>/pulls/<pr>/comments
```

Read the `pr-comment-filter` reference skill and apply its full include/exclude filter —
the same single source of truth `address-review` Step 1D uses, so the watermark matches
exactly what address-review would act on.

Then apply the **watermark** — keep only comments that are **both**:
- newer than `lastCommentTimestamp` (by `created_at`/`updated_at`), **and**
- **not** already in `addressedCommentIds`.

(Replying to a comment does not resolve its thread — threads are resolved by the reviewer,
per `address-review` Phase 5 — so without this watermark a re-run would re-address the same
comments forever.)

- **If new actionable comments remain** → invoke `/agentflow:address-review <pr>` via the
  `SlashCommand` tool. Its own pipeline runs the evaluate → **approval gate (Phase 3F,
  `AskUserQuestion`)** → fix → reply → push — that Phase 3F gate is the preserved human
  gate; do not bypass it. After it returns, update `lastCommentTimestamp` to the newest
  handled comment's timestamp and append the handled comment ids to `addressedCommentIds`.
- **If none remain** → nothing to do for comments this tick.

### 6. Quiet tick

If neither CI nor comments were actionable this tick (no CI fix was delegated or
escalated in step 4, and `/agentflow:address-review` was not invoked in step 5), back off:
set `currentDelaySeconds = min(currentDelaySeconds * 2, 3600)`. Report a single line
naming the new delay, e.g.:
`PR #<pr> quiet — CI green, no new comments. Next check in ~<currentDelaySeconds/60>m
(backing off).`

### 7. Pace the next tick

If this tick **was** actionable (a CI fix was delegated or escalated in step 4, or
`/agentflow:address-review` was invoked in step 5), reset the backoff:
`currentDelaySeconds = intervalSeconds` — a PR that just saw activity is worth checking on
sooner, not later. A quiet tick leaves `currentDelaySeconds` at the doubled value step 6
set.

Persist the updated state file (watermark, `lastCiHeadSha`, `ciFixAttempts`,
`currentDelaySeconds`), then schedule the next tick using the paced delay, not the fixed
base interval:

```
ScheduleWakeup(
  delaySeconds: currentDelaySeconds,
  prompt: "/agentflow:babysit <pr>",
  reason: "next babysit check for PR <pr>"
)
```

This is what re-fires the tick in self-paced mode. On the next fire, step 3 sees the state
file present and skips re-arming; the loop continues until step 2's terminal check stops it.

## Human gates (never bypass)

- `address-review`'s Phase 3F approval gate handles all comment fixes.
- CI escalation goes through `AskUserQuestion` when the cap is hit or the cause is ambiguous.
- Never force-push; never resolve disputed threads silently.
- Per `subagent-safety`: all mutating `gh`, `git push`, and `AskUserQuestion` stay in the
  **main agent**. Only read-only analysis and local fix work (build/test/edit) is delegated
  to the `implementer` subagent.

## Runtime notes

- **`/loop` is session-scoped and expires after 7 days.** Babysitting lives as long as the
  Claude Code session (and at most 7 days). If the session ends, re-run
  `/agentflow:babysit <pr>` to resume.
- **Self-paced pacing needs native support.** On Bedrock / Vertex / Foundry, self-paced
  `/loop` falls back to a fixed ~10-minute schedule; the `intervalSeconds` pacing above is
  best-effort there.
- **Outliving the session: use `/schedule`, not a second `/loop`.** `/loop`'s 7-day cap
  means a PR that needs babysitting longer than that — or across a machine
  restart/session end — needs a cloud routine instead: set up `/schedule` to periodically
  re-invoke `/agentflow:babysit <pr>` on a cron interval. Never run both a `/loop` and a
  `/schedule` routine against the same PR at once — each would independently wake, tick,
  and re-arm/re-schedule, double-waking the PR and racing on the same state file.
