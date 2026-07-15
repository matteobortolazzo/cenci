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
exists yet; an existing config is never merged into or overwritten. Column actions
call `cenci run` directly — no per-machine dispatch scripts. (The block below is
kept byte-identical to the template file, enforced by
`sandbox/tests/lazyboards-install.test.sh`.)

```yaml
# cenci's default lazyboards board config. Seeded by the cenci installer to
# ~/.config/lazyboards/config.yml only when no config exists — never merged
# into or overwritten. Column actions call `cenci run` directly; provider,
# repo, and project are only read from a project-local .lazyboards.yml, never
# from this global file.
session_max_length: 40        # match cenci's window-name cap (see join key)
action_refresh_delay: 5       # seconds after an action before refreshing — lets the
                              # cenci skill apply its label before the board re-reads
working_label: "Working"      # spinner marker; a card keeps its column while set
cenci: true                   # live agent badges + status-bar counts (default)
cleanup: "cenci close {number}"

columns:
  - name: New
    actions:
      R:
        name: Refine
        type: shell
        command: "cenci run refine {number}"

  - name: Refined
    actions:
      D:
        name: Design
        type: shell
        command: "cenci run design {number}"
      I:
        name: Implement
        type: shell
        command: "cenci run implement {number}"

  - name: Designed
    actions:
      I:
        name: Implement
        type: shell
        command: "cenci run implement {number}"

  - name: Planned

  - name: In Review
    actions:
      W:
        name: Checkout PR
        type: shell
        scope: pr
        command: 'tmux new-window -d -n pr-{pr_number} "git fetch origin {pr_branch} && git switch {pr_branch}"'

  - name: Implemented

# Global actions (default scope is "card" — they act on the selected card)
actions:
  G:
    name: Jump to agent
    type: shell
    command: 'tmux switch-client -t "={window}"'
  A:
    name: Annotate
    type: shell
    command: "gh issue comment {number} --body {comment}"
  S:
    name: Start dispatch loop
    type: shell
    scope: board
    command: "cenci dispatch loop on"
  X:
    name: Stop dispatch loop
    type: shell
    scope: board
    command: "cenci dispatch loop off"
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

**`W` on In Review** is a `scope: pr` action: it requires the selected card to have a
linked PR (auto-detected from the issue timeline), runs immediately with one PR, and
opens lazyboards' PR picker with several. Checking out `{pr_branch}` in a detached
tmux window puts the agent's PR one keypress from a local review — append the
project's run command (`ng serve`, `dotnet run`, `go run .`, …) in that project's
`.lazyboards.yml` to also start it.

**Per-repo run actions are generated for you.** `/cenci:configure` (question 10)
detects each runnable project's serve command and writes a committed
`.lazyboards.yml` whose In Review actions open the PR's **registered worktree**
(`{pr_worktree}`, resolved from `git worktree list` at action time — never the main
checkout) and start the project. Action keys are single uppercase letters — key
combinations don't exist in lazyboards yet — so a repo with several runnable
projects gets one key per project, assigned `W`, then `L`, then `O`, skipping the
global keys `G`/`A`/`S`/`X` claimed above. Because a local `columns:` list replaces
the global list (it never merges), the generated file re-lists every column as a
bare `- name:` entry, which inherits that column's global actions and cleanup.

**`G` (jump to agent)** switches the tmux client straight to the card's live agent
window via `{window}` — the reverse direction of the badge: the badge tells you an
agent needs attention, `G` takes you there.

**`S`/`X` (dispatch loop)** start and stop the daemon-owned background dispatch loop
(`cenci dispatch loop on|off`). lazyboards deliberately never toggles the loop
itself; these board-scope actions are the supported switch, and the board reflects the
result live (see the dispatch panel below).

**The annotate action** posts a comment back to the ticket. There is no dedicated
"comment" action type — `type` is only ever `shell` or `url`. Instead, any action can
read the `{comment}` variable, which lazyboards fills from **comment mode**: pressing
the plain key (`A`) runs it with `{comment}` empty; pressing **Alt+Shift+A** opens a
prompt to capture the comment text first, then runs the same command. That gives you
`gh issue comment {number} --body "<your note>"` without leaving the board.

Available template variables: `{number}`, `{title}` (slugified), `{tags}`
(comma-joined labels), `{session}` (`<number>-<slug>`, capped at `session_max_length`),
`{window}` (live cenci window name, falling back to `{session}`), `{comment}`,
`{repo_owner}`, `{repo_name}`, `{provider}` — plus, in `scope: pr` actions only,
`{pr_branch}`, `{pr_number}`, `{pr_url}`, and `{pr_title}`. Actions default to
`scope: card`; `scope: board` actions (no selected card) may not use card- or
PR-specific variables. See the
[lazyboards README](https://github.com/matteobortolazzo/lazyboards#template-variables)
for the authoritative reference.

## Fleet dispatch from the board

Pressing `d` in lazyboards opens the dispatch panel for the current repo: it shows
enrollment state (`Enter` toggles it, backed by `cenci dispatch enroll|unenroll`)
and a read-only line for the daemon-owned dispatch loop. `o` triggers a one-off
`cenci dispatch` pass — fleet-wide, across **all** enrolled repos, picking up any
`Planned` ticket with an approved `.plans/<id>-*.md` file. The recurring loop is
switched with the `S`/`X` board actions above; while it's on, the status bar shows a
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
