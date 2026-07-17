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
workflows), and a single `~/.config/lazyboards/config.yml`. lazyboards is an optional
separate project, not a fourth installed layer — the cenci installer offers to
install it for you and seeds the default board config below (or later:
`cenci-installer --lazyboards`).

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
| Refined/Designed → Planned | `/cenci:implement` planning | Persists `.plans/<id>-*.md`, then `+Planned` `−Working` (trivial-ticket fast path: `Working` is retained, not removed — see note below) |
| Planned → Working | plan-file implementation or `cenci dispatch` pickup | `+Working`; `Planned` remains as a milestone |
| Working → In Review | `/cenci:implement` phase 9 | `+In Review` `−Working` when the PR opens |
| In Review → Implemented | `/cenci:babysit` (on PR merge) | `+Implemented` `−In Review` |

`In Review` is applied when the PR **opens**, not when it merges — so a PR still
looping through review is visibly distinct from a merged one. `/cenci:babysit` owns
the final swap: it watches the open PR and, on merge, replaces `In Review` with
`Implemented` on every issue the PR closed (including a parent ticket reached via
`Fixes #<parent>`). PR-open never applies `Implemented`.

For a ticket judged trivial by `/cenci:implement`'s Trivial-Ticket Triage, the
`Refined → Planned → Working` transitions collapse into a single session — there is no
stop-and-relaunch between `Planned` and `Working`, since planning and implementation run
back-to-back without a human plan-review gate in between. The labels and board columns
themselves are unchanged; only the number of sessions/relaunches differs.

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

The complete default `~/.config/lazyboards/config.yml`. It ships with the flow
plugin as `flow/templates/lazyboards-config.yml`, and the installer seeds it to
`~/.config/lazyboards/config.yml` when installing lazyboards — only if no config
exists yet; an existing config is never merged into or overwritten. It carries no
`columns:` of its own — a repo's local `columns:` list replaces the global one
entirely, so columns are defined per-repo in each committed `.lazyboards.yml`
(generated by `/cenci:configure`); the global file holds only board-wide settings
and the two board-level agent-launch actions (`C` Claude, `X` Codex). (The block
below is kept byte-identical to the template file, enforced by
`sandbox/tests/lazyboards-install.test.sh`.)

```yaml
# cenci's default lazyboards board config. Seeded by the cenci installer to
# ~/.config/lazyboards/config.yml only when no config exists — never merged
# into or overwritten. Columns live in each repo's committed .lazyboards.yml —
# a local `columns:` list replaces the global one entirely — so this global
# file carries only board-wide settings and the board-level agent-launch actions.
provider: github
session_max_length: 40        # match cenci's window-name cap (see join key)
action_refresh_delay: 5       # seconds after an action before refreshing — lets the
                              # cenci skill apply its label before the board re-reads
working_label: "Working"      # spinner marker; a card keeps its column while set
cenci: true                   # live agent badges + status-bar counts (default)
cleanup: "cenci close {number}"

# Board-level actions (default scope is "card" — they act on the selected card)
actions:
  C: { name: Claude, type: shell, command: "tmux new-window cn cs" }
  X: { name: Codex, type: shell, command: "tmux new-window cn xt" }
```

**Per-column actions** dispatch a workflow onto the selected card. The action key is
a single uppercase letter (`R`, `D`, `I`); `cenci run <workflow> {number}`
builds the `<number>-<skill>` window and launches the agent.
`cenci run` chooses `refine`/`design`/`implement` from its built-in Claude templates
with zero extra config.

**`cleanup`** fires when a card leaves the column (detected on refresh). A single
top-level `cleanup` covers every column that doesn't define its own. `cenci close
{number}` is the supported reaper: it asks the daemon for the window's exact
`session:index` target (correct across tmux sessions), refuses to kill a window whose
agent is still running or awaiting input (unless passed `--force`), and exits `0`
when no window matches — safe on cards that never had an agent. A raw
`tmux kill-window -t ={window}` still works but resolves bare names only within
lazyboards' own tmux session; the
[lazyboards README](https://github.com/matteobortolazzo/lazyboards#column-cleanup)
documents that sharp edge. `session_max_length` must still match cenci's cap so the
`{session}` template variable names the right window in actions that create one.

**In Review actions are `scope: pr`**: they require the selected card to have a
linked PR (auto-detected from the issue timeline), run immediately with one PR, and
open lazyboards' PR picker with several. The global file defines no In Review action —
the column's actions are generated per-repo (next section); a repo with no runnable
project simply gets an In Review column with no actions.

**Per-repo run and test actions are generated for you.** `/cenci:configure` detects
each project's serve **and** test command and writes a committed `.lazyboards.yml`
whose In Review actions open the PR's **registered worktree** (`{pr_worktree}`,
resolved from `git worktree list` at action time — never the main checkout) and
either start the project (**`W`** serve) or run its tests in that worktree (**`T`**
test — `dotnet test`, `npm test`, `go test ./...`, `ng test --watch=false`, …). A
one-keypress "run the PR's tests before merging" is the payoff. Action keys are
single uppercase letters — key combinations don't exist in lazyboards yet — so serve
keys are assigned `W`, then `L`, then `O`, and test keys `T`, then the next unused
letters, skipping the global keys `C`/`X` claimed above. `Planned` also carries
local `E` (Edit plan) and `V` (View plan) actions that open the ticket's saved
`.plans/<number>-*.md` file in `$EDITOR` and a pager respectively.

Configure evaluates lazyboards on **every** run: with no `.lazyboards.yml` it offers
to generate one; with an existing file it compares against the recommended action set
and either suggests the missing actions (e.g. an absent `T` test action) or, when the
file is already complete, skips silently with a short log line. Because a local `columns:` list replaces
the global list entirely (it never merges, and bare `- name:` entries do **not**
inherit global actions), the generated file declares every column and its actions
inline — the global config no longer ships columns at all: `New` gets a local `R`
(Refine) action, `Refined` gets local `I` (Implement) and, when `pencil.enabled`
is on, a gated `D` (Design) action, and `Planned` gets a local `I` (Implement)
action so an already-planned ticket can still be manually re-dispatched from the
board, plus `E` (Edit plan) and `V` (View plan) actions on its saved plan file. `Designed`
and `Implemented` are labels in the ticket lifecycle but not board columns — only
`New`, `Refined`, `Planned`, and `In Review` are generated. When a repo has zero
runnable projects, `In Review` is emitted with no actions (there is no Checkout PR
fallback).

**`C` (Claude) and `X` (Codex)** are the two board-level actions in the global
config. Each opens a fresh agent in a detached tmux window via the sandbox launcher
(`cn cs` → Claude/Sonnet, `cn xt` → Codex) so you can start an ad-hoc session from
the board without leaving it. They take no card variables, so they work whether or
not a card is selected.

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
