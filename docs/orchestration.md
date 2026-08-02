# Board-orchestration recipe

> The board dispatches the work. The workflow owns the decisions. The container is
> the security boundary. The watcher routes your attention.

This is the supported recipe for driving the whole package from a
[lazyboards](https://github.com/matteobortolazzo/lazyboards) kanban board — the
orchestration layer that sits on top of cenci (workflow), cenci-watch (attention),
and cenci-sandbox (isolation). See the [root overview](../README.md) for the
architecture; this document is the wiring.

Every card is a GitHub issue. A keypress on a card dispatches a coding-agent workflow
into a detached tmux window; the agent moves the card across the board by relabelling
the issue; live status flows back onto the card. Nothing here is bespoke to one
machine — the pieces are `cenci run` (the launcher), the cenci workflow skills (the
workflows), and the per-repo committed `.lazyboards.yml`. lazyboards is an optional
separate project, not a fourth installed layer — the cenci installer offers to
install the binary for you (or later: `cenci-installer --lazyboards`).

## The state machine: columns are labels

A lazyboards column **is** a GitHub label. Placement is by label name, matched
case-insensitively:

- an issue with no matching label lands in the **first** column;
- an issue with one matching label lands in that column;
- an issue with several matching labels lands in the **rightmost** matching column.

So the board and the cenci workflow skills share one vocabulary: the skills relabel the
issue, and the card moves on the next refresh. The lifecycle is:

```
New → Refined → [Designed] → Planned → Working → In Review → Implemented
```

| Transition | cenci skill | Label change |
|---|---|---|
| New → Refined | `/cenci:refine` | `+Working` while running, then `+Refined` `−Working` |
| Refined → Designed (optional) | `/cenci:design` on the dedicated design ticket | Propagates `+Designed` to dependent implementation tickets |
| Refined/Designed → Planned | `/cenci:implement` planning | Persists `.plans/<id>-*.md`, then `+Planned` `−Working` (trivial-ticket fast path and lean planning with no escalations: `Working` is retained, not removed — see note below) |
| Refined → Working (planning pickup) | `cenci dispatch` (planning pickup, `dispatch.planRefined: true`) | `+Working`; `Refined` retained — no plan file existed yet, so `cenci run implement <n>` launches a fresh planning session unattended, in lean-planning repos only |
| Planned → Working | plan-file implementation or `cenci dispatch` pickup | `+Working`; `Planned` remains as a milestone |
| Planned → Working (re-plan) | `cenci dispatch` (autonomous re-plan, `dispatch.planRefined: true`) | `+Working`; `Planned` retained — the existing plan was stale (past `planStalenessTolerance`), so `cenci run implement "<n> replan"` relaunches planning against it unattended instead of terminally skipping |
| Working → Input Needed (escalation) | `/cenci:implement` planning (unattended, `planning.autonomy: "lean"`) | Persists a draft `.plans/<id>-*.md` (`status: awaiting-input`), then `+Input Needed` `−Working` (`Refined` retained) |
| Input Needed → Working (resume) | `cenci dispatch` (auto-resume) or a manual `/cenci:implement <id>` re-run | `+Working` `−Input Needed` (atomic, one label call) once a qualifying human reply is detected after the escalation anchor; `Refined` retained. Reverses to `+Input Needed` `−Working` on a failed auto-resume launch (unless the tmux window was demonstrably created) or on the reconciler's bounded interrupted-resume recovery |
| Working → In Review | `/cenci:implement` phase 9 | `+In Review` `−Working` when the PR opens |
| In Review → Implemented | `/cenci:babysit` (on PR merge) | `+Implemented` `−In Review` |

`automerge:ok` is a per-ticket grant confirmed at refine's Confirmation Gate, never inherited — a split child, the companion design ticket, and a followup ticket each earn it (or not) on their own merit, never from the parent.

`In Review` is applied when the PR **opens**, not when it merges — so a PR still
looping through review is visibly distinct from a merged one. `/cenci:babysit` owns
the final swap: it watches the open PR and, on merge, replaces `In Review` with
`Implemented` on every issue the PR closed (including a parent ticket reached via
`Fixes #<parent>` — written only after phase 9's Parent Close Gate verified the
parent's acceptance criteria against delivered evidence, and reconciled to the
current verdict on every re-entry rather than trusting an earlier attempt's write;
on gaps the last-child PR references the parent as `Related to` and the parent
stays open with a gap comment). PR-open never applies `Implemented`.

The `Refined → Planned → Working` transitions collapse into a single session on two
triggers: a ticket judged trivial by `/cenci:implement`'s Trivial-Ticket Triage, or a plan
produced under `planning.autonomy: "lean"` that comes back with no escalations (the Lean
Approval Path). In both cases there is no stop-and-relaunch between `Planned` and `Working`,
since planning and implementation run back-to-back without a human plan-review gate in
between. The labels and board columns themselves are unchanged; only the number of
sessions/relaunches differs.

The `Working → Input Needed` transition is the opposite case: `planning.autonomy: "lean"`
produced a plan that *does* have an unresolved escalation in an unattended run. The session
removes `Working` and keeps `Refined` — the ticket is no longer actively being worked (a
human's reply on the ticket is now the blocking dependency), but it was already refined
before the run started, so that milestone marker stays. Removing `Working` here is
deliberate, not cosmetic: a `Working` ticket whose tmux window/session has died is exactly
what the dispatch reconciler's crash-recovery retries (see `watch/docs/dispatch-reconcile.md`);
leaving `Working` on an escalated ticket would make it a retry candidate instead of the
human-input candidate it actually is. A human answer on the ticket, followed by a fresh
`/cenci:implement` session, resumes from the draft plan.

The `Input Needed → Working (resume)` transition is the round trip back, and it no longer
requires that manual re-run: `cenci dispatch` now probes every `Input Needed` ticket for a
qualifying human reply on the escalation comment — its anchor identity is the exact,
immutable numeric comment ID persisted in the draft's front matter (`escalationCommentId`),
verified by the matching `escalationNonce` marker in that comment's body, not a scan for
"the last comment that looks like an anchor" (ticket #849 hardens this after verifying the
pre-#849 marker-only anchor would silently never resume under an ordinary, non-bot-shaped
`gh` identity). A qualifying reply is positioned after that verified anchor, marker-free,
not authored by a `*[bot]`/`app/*` login or the REST API's `user.type == "Bot"`, posted
by an author whose association is one of `OWNER`, `MEMBER`, or `COLLABORATOR` (ticket #882:
a coarse prefilter only, never final authorization), AND currently holds `admin` or `write`
permission on the repository, resolved via `gh api repos/<owner>/<repo>/collaborators/<login>/permission`
(the endpoint's authoritative top-level `permission` field; `role_name` is never consulted).
A read/triage collaborator, a removed collaborator, or an organization member without this
repository's write access is denied even with an otherwise-qualifying association — and any
permission-probe failure (API error, timeout, truncated output, malformed JSON, a missing
`permission` field, or an unrecognized value) fails closed with its own distinct reason,
exactly like an unresolved comments probe. Permission is resolved fresh every pass — never
cached across passes, never treated as still current from an earlier resume. When a reply
qualifies, dispatch swaps `+Working` `−Input Needed` in one atomic label call (`ticket #853`: the same
transition contract the manual `/cenci:implement <id>` re-run's `pipeline label --transition
working` uses) before relaunching the planning session against the persisted
`status: awaiting-input` draft. A draft whose anchor fields are missing, malformed,
or unverifiable (the stored comment ID absent from the thread, or its body lacking the exact
nonce marker) fails closed instead — dispatch never repairs an anchor itself, it only reports
the gap; repair is a separate, human-triggered `/cenci:implement <id>` run. This is the same relaunch shape
as an ordinary `Planned` pickup, just pointed at a draft instead of a finished plan.

**Failed-launch rollback and interrupted-resume recovery (ticket #853).** A resume launch
that fails to spawn a tmux window at all rolls the claim straight back to `+Input Needed`
`−Working`, so the ticket stays resumable on the next pass. A launch failure that happens
*after* the window was demonstrably created (e.g. the trailing `set-window-option` call)
instead retains `Working` — the session may well be alive — and leaves recovery to the
reconciler: a dead `Working` ticket whose matched plan is still `status: awaiting-input` is
classified as an interrupted resume, not an ordinary crashed implementation run, and is
restored to `+Input Needed` `−Working` rather than ever being converted to `+Planned` (which
would silently discard the still-open escalation). This restore is bounded by the same
durable attempt budget ordinary retries use — once exhausted it escalates to
`dispatch-failed` like any other stranded `Working` ticket instead of restoring indefinitely
(see `watch/docs/dispatch-reconcile.md`).

The relaunched session's re-delegation to the planner is freshness-aware, not an
unconditional "no re-exploration" rule: `cenci pipeline plan-check` computes a git-only
`draft_freshness` verdict (`fresh`/`stale`/`unknown`, the latter treated exactly like
`stale`) by counting commits behind the draft's `planCommitSha`, scoped to its
`stalenessPaths`. On `fresh` the session re-delegates with the draft's
`## Architectural Context` as its prior exploration and no re-exploration — the classic
no-re-exploration contract. On `stale`/`unknown` it instead routes through an autonomous
re-plan, passing the human's already-collected answers as fixed decisions (never re-opened)
while the planner re-explores the codebase for its architectural context; the draft's
original `planCommitSha`/`stalenessPaths` are preserved verbatim on the fresh path and
regenerated on the re-plan path, so the SHA itself can never be spoofed to claim a freshness
it doesn't have. Freshness is fresh/stale relative to the plan's declared `stalenessPaths`,
not an absolute guarantee that no relevant code changed elsewhere: a plan whose
`stalenessPaths` under- or mis-scopes its actual dependencies can still return `fresh` while a
relevant change lands outside that declared scope. Either way, the session appends the human's
answers to the ticket's `### Decisions`
section, and either finalizes the plan (`+Planned` `−Working`, same as any other planning
session) or re-escalates with a follow-up comment and a new trusted anchor when the answers
are incomplete or a re-plan surfaces a new escalation — it never guesses. This mirrors the
collapsed `Refined → Planned → Working` sessions above in spirit (no human plan-review gate
on the relaunch), but it is a distinct case: the plan itself was already escalated once, so
this round trip can repeat if answers stay incomplete, bounded by dispatch's existing
concurrency/budget/quiet-hours gates.

**Resume/escalation abort is restored in-session, not by the reconciler (ticket #880).** Every
hard stop after the `Working → resume` claim above — across the re-escalation round trip, the
unattended escalation path, and the human-triggered anchor-repair path — restores
`+Input Needed` `−Working` on a valid `status: awaiting-input` draft *before that same session
stops*, not by waiting on a later pass. The dispatch reconciler's interrupted-resume recovery
(described above) remains a backstop for a genuinely dead tmux window past its grace period,
never the normal abort mechanism a clean-but-failing session relies on. One consequence is
accepted deliberately rather than mitigated in this ticket: because restoring `Input Needed`
leaves the anchor and the human's already-collected answer unchanged, `cenci dispatch`
re-resumes the same ticket on its very next pass — a persistent failure loops
restore-then-re-resume unbounded, with no attempt counter guarding it (unlike the reconciler's
own `RetryBudget`-bounded dead-window recovery). Bounding this loop is deferred to a later
ticket in the #661 series.

**Stage-aware pickup closes the loop (ticket #828).** With `dispatch.planRefined:
true` in a repo's `dispatch` config, `cenci dispatch` no longer stops at `Planned`
tickets: a `Refined` ticket with no plan file yet becomes a planning pickup (the
`Refined → Working (planning pickup)` row above), and a `Planned` ticket whose
plan has gone stale — the routine case after an automerged dependency PR shifts
the shared files a sibling plan touched — becomes an autonomous re-plan (the
`Planned → Working (re-plan)` row above) instead of a terminal `plan stale,
re-plan` skip. Both launch the same `cenci run implement` command an ordinary
pickup does, under every other gate (assignee, dependency, sibling
serialization, capacity, budget, quiet hours) unchanged. Chained end to end,
this closes the full autonomous loop: refine → plan → implement → PR →
automerge → next dependent ticket's stale plan self-heals and gets re-planned
→ implemented in turn, with no human touch between refine and merge. This is
**lean-planning repos only**: `dispatch.planRefined` remains a fleet-wide kill
switch, but it is no longer sufficient authorization on its own (#851) —
dispatch also reads the repo's own committed `planning.autonomy` setting from
`.cenci/config.json` at the remote-confirmed `refs/remotes/origin/main` object
(never local `HEAD`, never the working tree, and only when this pass's `git
fetch origin` actually succeeded — #877) and requires the literal value
`"lean"` before treating a planning pickup or autonomous re-plan as authorized;
a missing, unreadable, malformed, or non-`"lean"` repo config denies both, with
its own distinct skip reason, and so does a fetch failure this pass (a
distinct, retryable reason — ordinary `Planned` pickup in the same repo is
unaffected). A repo's own unpushed local commits can never grant or revoke this
authorization; only the fetched remote object counts. See [cenci-watch's
README](../watch/README.md#planning-pickup-and-autonomous-re-plan) for the
sibling-serialization and unbounded-re-plan limitations this loop accepts.

**`Working` is transient activity, not a persisted handoff.** lazyboards'
`working_label` (default
`Working`) renders a spinner on any card carrying that label **without** moving it,
and hides the label from the card's dot display. Each skill adds `Working` when it
starts and removes it when it hands off, so a card shows "an agent is on this right
now" while staying in its current column. `Planned` is the durable handoff and can be
picked up automatically by cenci dispatch.

**One GitHub assignee is the exclusive ticket owner.** Ticket-mode `refine`,
`design`, and `implement` workflows claim an unassigned issue for the active `gh`
account, but never replace an existing assignee. They stop on foreign or multiple
assignees. Split children and companion design tickets remain unassigned until their
own workflow starts. Dispatch applies the same rule: only a `Planned` ticket solely
assigned to the active `gh` user is eligible, so teammates can run independent
dispatch loops without selecting each other's work.

## The join key: `<number>-<skill>`

The ticket number ties the three layers together — the board card, the tmux window
the agent runs in, and the watcher's status snapshot:

```
board card  ──dispatch──▶  tmux window  ──daemon──▶  status snapshot
 issue #42                  42-implement               window_name: "42-implement"
```

`cenci run` names a numbered ticket's window `<number>-<skill>` — the skill is
the running workflow (`refine` / `design` / `implement`) — and sets
`automatic-rename off`. These names are short and uniform, so many tabs fit on the
tmux status line at once. When the cenci daemon later tracks that window it sees
the manual name and preserves it instead of overwriting it with the detected task — so
`<number>-<skill>` flows through to the snapshot's `window_name`, which lazyboards
reads over the public watcher client (`pkg/watch`,
[#39](https://github.com/matteobortolazzo/cenci/issues/39)) to badge the card.
See [cenci-watch's README](../watch/README.md#the-join-key-survives-the-daemon)
for the daemon side.

**lazyboards matches by ticket-number prefix.** Because the running skill isn't part
of a card's identity, lazyboards joins a snapshot window to a card when the window
name equals `<number>` or starts with `<number>-`, rather than reconstructing the full
name. The prefix boundary (`23-`) keeps card `#23` from matching `230-refine`. This is
backward-compatible with the older `<number>-<slug>` names, so it never needs to know
which skill is running.

## The board config

`/cenci:configure` generates the whole per-repo `.lazyboards.yml`, including its
`columns:`, when you answer its board-config question (step 5f — see
`flow/skills/configure/SKILL.md`). The file is self-contained and needs no other
config: lazyboards merges scalar fields and the `actions` map across a global and
a local config file, with local keys winning, so a legacy machine-global
`~/.config/lazyboards/config.yml` left over from an older cenci install is
harmless — nothing in it can override what's generated here.

The board-level excerpt below — the two agent-launch actions (`C` Claude, `X`
Codex) and the auto-close `cleanup` — is emitted at the top level of every
generated `.lazyboards.yml`, outside `columns:`:

```yaml
# Board-level actions (default scope is "card" — they act on the selected card)
actions:
  C: { name: Claude, type: shell, command: "tmux new-window cenci open --agent claude" }
  X: { name: Codex, type: shell, command: "tmux new-window cenci open --agent codex" }

# Auto-close a card's agent window when its ticket closes
cleanup: "cenci close {number}"
```

**Per-column actions** dispatch a workflow onto the selected card. The action key is
a single uppercase letter (`R`, `D`, `I`); `cenci run <workflow> {number}`
builds the `<number>-<skill>` window and launches the agent.
`cenci run` chooses `refine`/`design`/`implement` from its built-in Claude templates
with zero extra config. The design workflow — design dispatches on the host, since the
Pencil desktop app it drives is never reachable inside the cenci sandbox — always
resolves to the host command regardless of the sandbox default, and an explicit
`--sandbox` for it is a usage error.

**`cleanup`** fires when a card leaves the column (detected on refresh). A single
top-level `cleanup` covers every column that doesn't define its own. `cenci close
{number}` is the supported reaper: it asks the daemon for the window's exact
`session:index` target (correct across tmux sessions) and refuses to kill a window
whose agent is still running or awaiting input (unless passed `--force`). A
busy-skipped window is self-healing: `cenci close` registers it with the daemon as
pending-close, and the daemon closes it itself the moment it observes that session's
end — no second `cenci close` invocation (from lazyboards or anything else) is
required.

`cenci close` also refuses to kill a window whose ticket is still owned by a live
`cenci babysit` supervisor whose PR's CI is not green — including through the
deferred pending-close path above, where the daemon re-checks the guard instead of
killing at `SessionEnd`. This matters because Phase 9 relabels the ticket `Working` →
`In Review` (moving the card, which fires `cleanup`) and *then* arms the supervisor:
without the guard, cleanup reaps the window while CI is still running and review
feedback has not arrived. The skip is reported, `--force` overrides it, and the close
happens by itself once CI passes or the supervisor stops. Two caveats:

- **The daemon's re-check matches on ticket number alone.** A registered
  pending-close carries no repo, so two repos babysitting PRs that close the same
  issue number at the same time cross-match. The failure mode is benign — a window
  stays open a little longer — and `--force` overrides. `cenci close` itself does
  scope the match to the current checkout's repo root.
- **The guard reads babysit's on-disk state, refreshed once per supervision
  interval** (default `15m`), so a window can stay closable-but-open for up to one
  interval after CI turns green. That is deliberate: the close path must make no
  network calls, since lazyboards runs it on every board refresh. Everything unknown
  or unreadable fails *open* (closes), so machines that never run babysit are
  unaffected.

When no window matches at all, `cenci close` produces no output and exits
`0` — safe on cards that never had an agent, and quiet enough that lazyboards never
surfaces a legitimate no-op as a spurious warning. A raw
`tmux kill-window -t ={window}` still works but resolves bare names only within
lazyboards' own tmux session; the
[lazyboards README](https://github.com/matteobortolazzo/lazyboards#column-cleanup)
documents that sharp edge. lazyboards' own `session_max_length` default (40) already
matches cenci's window-name cap, so the `{session}` template variable names the
right window in actions that create one — no config needed to keep the two in sync.

**In Review actions are `scope: pr`**: they require the selected card to have a
linked PR (auto-detected from the issue timeline), run immediately with one PR, and
open lazyboards' PR picker with several. There is no global file in the recipe —
In Review actions are generated per-repo (next section); a repo with no runnable
project simply gets an In Review column with no actions.

**Per-repo worktree, run, and test actions are generated for you.** `/cenci:configure`
detects each project's serve **and** test command and writes a committed
`.lazyboards.yml` whose In Review actions open the PR's **registered worktree**
(`{pr_worktree}`, resolved from `git worktree list` at action time — never the main
checkout). **`W`** opens that worktree in a tmux window with a plain shell — no
command, no project path — and is always emitted regardless of whether any project
is runnable or testable. Per project, a separate action starts it (**serve**) or
runs its tests (**test** — `dotnet test`, `npm test`, `go test ./...`,
`ng test --watch=false`, …) in the same worktree. A one-keypress "run the PR's tests
before merging" is the payoff. lazyboards now supports multi-key sequences, so serve
and test keys no longer compete with `W` or with each other for scarce single
letters: a single-project repo gets plain `S` (serve) and `T` (test); a monorepo gets
`S`/`T` plus a project mnemonic — `Sb`/`Tb` for a backend project, `Sf`/`Tf` for a
frontend project, or the project slug's first letter when neither fits — skipping the
board-level keys `C`/`X` claimed above and never reusing `W`. `Planned` also carries local
`E` (Edit plan) and `V` (View plan) actions that open the ticket's saved
`.plans/<number>-*.md` file in `$EDITOR` and a pager respectively.

Configure evaluates lazyboards on **every** run: with no `.lazyboards.yml` it offers
to generate one; with an existing file it compares against the recommended action set
and either suggests the missing actions (e.g. an absent test action) or, when the
file is already complete, skips silently with a short log line. Because a local
`columns:` list replaces the global list entirely (it never merges), the generated
file declares every column and its actions inline: `New` gets a local `R`
(Refine) action, `Refined` gets local `I` (Implement) and, when `pencil.enabled`
is on, a gated `D` (Design) action — `cenci run design {number} --no-sandbox`,
since design dispatches on the host — and `Planned` gets a local `I` (Implement)
action so an already-planned ticket can still be manually re-dispatched from the
board, plus `E` (Edit plan) and `V` (View plan) actions on its saved plan file. `Designed`
and `Implemented` are labels in the ticket lifecycle but not board columns — only
`New`, `Refined`, `Planned`, and `In Review` are generated. When a repo has zero
runnable projects, `In Review` still carries `W` (Open worktree) — there is no
Checkout PR fallback beyond it.

**`C` (Claude) and `X` (Codex)** are the two board-level actions in the generated
`.lazyboards.yml`, at top level. Each opens a fresh agent in a detached tmux window
via the sandbox launcher (`cenci open --agent claude`, `cenci open --agent codex` — each
agent's own default model, not a pinned shortcut) so you can start
an ad-hoc session from the board without leaving it. They take no card variables,
so they work whether or not a card is selected.

The dispatch loop is no longer a board action — toggle it from the CLI with
`cenci dispatch loop on|off` (the board still reflects its state live; see the
dispatch panel below). Likewise there is no built-in jump-to-agent or annotate
action; the `{comment}` **comment mode** (Alt+Shift on any key that reads
`{comment}`) and the `{window}` variable remain available if you want to add your
own board-level action for either.

Available template variables: `{number}`, `{title}` (slugified), `{tags}`
(comma-joined labels), `{session}` (`<number>-<slug>`, capped at `session_max_length`),
`{window}` (live cenci window name, falling back to `{session}`), `{comment}`,
`{repo_owner}`, `{repo_name}`, `{provider}` — plus, in `scope: pr` actions only,
`{pr_branch}`, `{pr_number}`, `{pr_url}`, and `{pr_title}`. Actions default to
`scope: card`; `scope: board` actions (no selected card) may not use card- or
PR-specific variables. See the
[lazyboards README](https://github.com/matteobortolazzo/lazyboards#template-variables)
for the authoritative reference. `{pr_branch}` and `{pr_title}` come from the PR's
author and are not shell-safe inside a double-quoted command string — lazyboards'
single-quote escaping only defeats a single-quote-context breakout, so a value nested
inside an outer double-quoted string can still carry `$(...)`, backticks, or `\"`
through to the shell. Prefer `{pr_number}` (always a plain integer) for any
custom `scope: pr` action that shells out.

## Fleet dispatch from the board

Pressing `d` in lazyboards opens the dispatch panel for the current repo: it shows
enrollment state (`Enter` toggles it, backed by `cenci dispatch enroll|unenroll`)
and a read-only line for the daemon-owned dispatch loop. `o` triggers a one-off
`cenci dispatch` pass — fleet-wide, across **all** enrolled repos, picking up any
`Planned` ticket with an approved `.plans/<id>-*.md` file. The recurring loop is
toggled from the CLI (`cenci dispatch loop on|off`); while it's on, the status bar shows a
`⟳ dispatch` segment fed by the same watcher socket as the agent badges, so it tracks
the loop (and daemon reachability) live. Concurrency, quiet hours, and budgets live in
cenci's own `dispatch` config block — see the
[cenci-watch README](../watch/README.md#configuration-1).

## Dispatching into the sandbox

Sandboxed dispatch is the **default** — the cenci-sandbox container
([#29](https://github.com/matteobortolazzo/cenci/issues/29)) is the mandatory
runtime and the security boundary. A bare `cenci run implement {number}` already
launches inside the container, so a board action needs no extra flag:

```yaml
      I:
        name: Implement
        type: shell
        command: "cenci run implement {number}"
```

To escape to a host launch instead, pass `--no-sandbox` (or set `"sandbox": false` in
cenci's `~/.config/cenci/config.json`):

```yaml
      H:
        name: Implement (host)
        type: shell
        command: "cenci run implement {number} --no-sandbox"
```

The default routes the launch through the sandbox launcher (the same engine behind
`cenci open`), running the agent under `--dangerously-skip-permissions` with the
container as the security boundary. Status still surfaces on the **host** board: the
launcher mounts the host cenci socket
directory (not the raw socket file) into the container at `/run/user/1000/cenci`
and forwards `TMUX_PANE`, so the agent's hook events reach the host daemon and the join
key flows through unchanged. The card badges exactly as a host dispatch would.

## Mixed-agent boards

Prepare both client-local plugin stores once. The installer does this automatically
when both CLIs are present; manual setup is:

```bash
claude plugin marketplace add matteobortolazzo/cenci
claude plugin install cenci cenci-watch cenci-sandbox
codex plugin marketplace add matteobortolazzo/cenci
codex plugin add cenci@cenci
codex plugin add cenci-watch@cenci
codex plugin add cenci-sandbox@cenci
```

Codex then discovers the portable `cenci:*` convention skills directly from the
plugin. The full implementation sequence still comes from the repository's
`AGENTS.md`; copy or merge
[`flow/templates/agents-md-codex.md`](../flow/templates/agents-md-codex.md)
into the target repository.

Which agent runs a card is a **per-dispatch** choice — pass `--agent`:

```yaml
      I:
        name: Implement (Codex)
        type: shell
        command: "cenci run implement {number} --agent codex"
```

The state-machine labels are agent-neutral, so a board can dispatch some cards to
Claude Code and others to Codex and both drive the same columns. Instructions are
shared: each directory's `CLAUDE.md` (canonical) is read by Claude Code natively and by
Codex via `project_doc_fallback_filenames = ["CLAUDE.md"]` in `~/.codex/config.toml` (a
one-time, user-level line — a committed repo-level `.codex/config.toml` is ignored), so a
dispatched Codex card sees the same project context as a Claude Code card. `cenci run`
ships built-in Claude templates; merge
[`flow/templates/cenci-codex-config.json`](../flow/templates/cenci-codex-config.json)
into `~/.config/cenci/config.json` to add the Codex `implement` template, while
interactive `refine` and `design` remain Claude Code-only. Codex support across the
package is tracked in
[#33](https://github.com/matteobortolazzo/cenci/issues/33); see
[cenci-watch's README](../watch/README.md#dispatching-workflows-cenci-run)
for config precedence and launcher flags.
