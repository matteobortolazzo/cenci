# Board-orchestration recipe

> The board dispatches the work. The workflow owns the decisions. The container is
> the security boundary. The watcher routes your attention.

This is the supported recipe for driving the whole package from a
[lazyboards](https://github.com/matteobortolazzo/lazyboards) kanban board — the
orchestration layer that sits on top of agentflow (workflow), agentwatch (attention),
and agent-sandbox (isolation). See
[`cohesive-package.md` §2.4](./cohesive-package.md) for the architecture; this
document is the wiring.

Every card is a GitHub issue. A keypress on a card dispatches a coding-agent workflow
into a detached tmux window; the agent moves the card across the board by relabelling
the issue; live status flows back onto the card. Nothing here is bespoke to one
machine — the pieces are `agentwatch run` (the launcher), the agentflow skills (the
workflows), and a single `~/.config/lazyboards/config.yml`.

## The state machine: columns are labels

A lazyboards column **is** a GitHub label. Placement is by label name, matched
case-insensitively:

- an issue with no matching label lands in the **first** column;
- an issue with one matching label lands in that column;
- an issue with several matching labels lands in the **rightmost** matching column.

So the board and the agentflow skills share one vocabulary: the skills relabel the
issue, and the card moves on the next refresh. The lifecycle is five states plus one
transient marker:

```
New ──refine──▶ Refined ──design──▶ Designed ──implement──▶ In Review ──merge──▶ Implemented
```

| Transition | agentflow skill | Label change |
|---|---|---|
| New → Refined | `/agentflow:refine` | `+Working` while running, then `+Refined` `−Working` |
| Refined → Designed | `/agentflow:design` | `+Working` while running, then `+Designed` `−Working` |
| Designed → In Review | `/agentflow:implement` (phase 9, on PR open) | `+Working` while running, then `+In Review` `−Working` |
| In Review → Implemented | `/agentflow:babysit` (on PR merge) | `+Implemented` `−In Review` |

`In Review` is applied when the PR **opens**, not when it merges — so a PR still
looping through review is visibly distinct from a merged one. `/agentflow:babysit` owns
the final swap: it watches the open PR and, on merge, replaces `In Review` with
`Implemented` on every issue the PR closed (including a parent ticket reached via
`Fixes #<parent>`). PR-open never applies `Implemented`.

**`Working` is a marker, not a column.** lazyboards' `working_label` (default
`Working`) renders a spinner on any card carrying that label **without** moving it,
and hides the label from the card's dot display. Each skill adds `Working` when it
starts and removes it when it hands off, so a card shows "an agent is on this right
now" while staying in its current column.

## The join key: `<number>-<slug>`

One name ties the three layers together — the board card, the tmux window the agent
runs in, and the watcher's status snapshot:

```
board card  ──dispatch──▶  tmux window  ──daemon──▶  status snapshot
 issue #42                  42-<slug>                  window_name: "42-<slug>"
```

`agentwatch run` names the window `<number>-<slug>` (slug from `--slug`, else the
gh issue title, else any trailing context words) and sets `automatic-rename off`.
When the agentwatch daemon later tracks that window it sees the manual name and
preserves it instead of overwriting it with the detected task — so `<number>-<slug>`
flows through to the snapshot's `window_name`, which lazyboards reads over the public
watcher client (`pkg/watch`,
[#39](https://github.com/matteobortolazzo/agent-stack/issues/39)) to badge the card.
See [agentwatch's README](../agentwatch/README.md#the-join-key-survives-the-daemon)
for the daemon side.

**Keep the two length caps aligned.** `agentwatch run` caps the window name at 40
characters; lazyboards caps its `{session}` template variable at `session_max_length`
(default **32**). Both derive the slug from the same gh issue title, so the only way
they diverge is truncation. Set `session_max_length: 40` in the board config so the
`{session}` your cleanup hook passes to `tmux kill-window` matches the window that
`agentwatch run` actually created.

## The board config

A complete `~/.config/lazyboards/config.yml`. Column actions call `agentwatch run`
directly — no per-machine dispatch scripts. (`provider`, `repo`, and `project` are
only read from a project-local `.lazyboards.yml`; the global file ignores them.)

```yaml
# ~/.config/lazyboards/config.yml
session_max_length: 40        # match agentwatch's window-name cap (see join key)
action_refresh_delay: 5       # seconds after an action before refreshing — lets the
                              # agentflow skill apply its label before the board re-reads
working_label: "Working"      # spinner marker; a card keeps its column while set

columns:
  - name: New
    cleanup: "tmux kill-window -t {session} 2>/dev/null || true"
    actions:
      R:
        name: Refine
        type: shell
        command: "agentwatch run refine {number}"

  - name: Refined
    cleanup: "tmux kill-window -t {session} 2>/dev/null || true"
    actions:
      D:
        name: Design
        type: shell
        command: "agentwatch run design {number}"
      I:
        name: Implement
        type: shell
        command: "agentwatch run implement {number}"

  - name: Designed
    cleanup: "tmux kill-window -t {session} 2>/dev/null || true"
    actions:
      I:
        name: Implement
        type: shell
        command: "agentwatch run implement {number}"

  - name: In Review
  - name: Implemented

# Board-level actions (no selected card)
actions:
  A:
    name: Annotate
    type: shell
    command: "gh issue comment {number} --body {comment}"
```

**Per-column actions** dispatch a workflow onto the selected card. The action key is
a single uppercase letter (`R`, `D`, `I`); `agentwatch run <workflow> {number}`
resolves the gh title, builds the `<number>-<slug>` window, and launches the agent.
`agentwatch` chooses `refine`/`design`/`implement` from its built-in Claude templates
with zero extra config.

**`cleanup`** fires when a card leaves the column (detected on refresh). Reaping the
window with `tmux kill-window -t {session}` closes the agent session that dispatch
opened — which is why `session_max_length` must match agentwatch's cap, so `{session}`
names the right window.

**The annotate action** posts a comment back to the ticket. There is no dedicated
"comment" action type — `type` is only ever `shell` or `url`. Instead, any action can
read the `{comment}` variable, which lazyboards fills from **comment mode**: pressing
the plain key (`A`) runs it with `{comment}` empty; pressing **Alt+Shift+A** opens a
prompt to capture the comment text first, then runs the same command. That gives you
`gh issue comment {number} --body "<your note>"` without leaving the board.

Available template variables: `{number}`, `{title}` (slugified), `{tags}`
(comma-joined labels), `{session}` (`<number>-<slug>`, capped at `session_max_length`),
`{comment}`, `{repo_owner}`, `{repo_name}`, `{provider}`. Board-scoped actions (no
selected card) may not use `{number}`, `{title}`, `{tags}`, or `{session}`.

## Dispatching into the sandbox

Sandboxed dispatch is the **default** — the agent-sandbox container
([#29](https://github.com/matteobortolazzo/agent-stack/issues/29)) is the mandatory
runtime and the security boundary. A bare `agentwatch run implement {number}` already
launches inside the container, so a board action needs no extra flag:

```yaml
      I:
        name: Implement
        type: shell
        command: "agentwatch run implement {number}"
```

To escape to a host launch instead, pass `--no-sandbox` (or set `"sandbox": false` in
agentwatch's `~/.config/agentwatch/config.json`):

```yaml
      H:
        name: Implement (host)
        type: shell
        command: "agentwatch run implement {number} --no-sandbox"
```

The default swaps the launch command to `agent-sand`, running the agent under
`--dangerously-skip-permissions` with the container as the security boundary. Status
still surfaces on the **host** board: `agent-sand` mounts the host
`agentwatch-events.sock` into the container and forwards `TMUX_PANE`, so the agent's
hook events reach the host daemon and the join key flows through unchanged. The card
badges exactly as a host dispatch would.

## Mixed-agent boards

Prepare both client-local plugin stores once. The installer does this automatically
when both CLIs are present; manual setup is:

```bash
claude plugin marketplace add matteobortolazzo/agent-stack
claude plugin install agentflow agentwatch sandbox
codex plugin marketplace add matteobortolazzo/agent-stack
codex plugin add agentflow@agent-stack
codex plugin add agentwatch@agent-stack
codex plugin add sandbox@agent-stack
```

Codex then discovers the portable `agentflow:*` convention skills directly from the
plugin. The full implementation sequence still comes from the repository's
`AGENTS.md`; copy or merge
[`agentflow/templates/agents-md-codex.md`](../agentflow/templates/agents-md-codex.md)
into the target repository.

Which agent runs a card is a **per-dispatch** choice — pass `--agent`:

```yaml
      I:
        name: Implement (Codex)
        type: shell
        command: "agentwatch run implement {number} --agent codex"
```

The state-machine labels are agent-neutral, so a board can dispatch some cards to
Claude Code and others to Codex and both drive the same columns. Instructions are
shared: each directory's `CLAUDE.md` (canonical) is read by Claude Code natively and by
Codex via `project_doc_fallback_filenames = ["CLAUDE.md"]` in `~/.codex/config.toml` (a
one-time, user-level line — a committed repo-level `.codex/config.toml` is ignored), so a
dispatched Codex card sees the same project context as a Claude Code card. `agentwatch run`
ships built-in Claude templates; merge
[`agentflow/templates/agentwatch-codex-config.json`](../agentflow/templates/agentwatch-codex-config.json)
into `~/.config/agentwatch/config.json` to add the Codex `implement` template, while
interactive `refine` and `design` remain Claude Code-only. Codex support across the
package is tracked in
[#33](https://github.com/matteobortolazzo/agent-stack/issues/33); see
[agentwatch's README](../agentwatch/README.md#dispatching-workflows-agentwatch-run)
for config precedence and launcher flags.
