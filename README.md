# agent-stack

agent-stack lets a coding agent work autonomously without making the human disappear
from the decisions that matter. It combines one security boundary, one gated delivery
workflow, and one attention router. It is one product installed with one command; its
three internal plugins retain independent versions as an implementation detail.

```text
attention  agentwatch      hooks → daemon → tmux and desktop status surfaces
workflow   agentflow       refine → design → plan → implementation → reviewed PR
isolation  agent-sandbox   Docker/Podman boundary for full-permission agent sessions
```

The human refines scope, approves design and plans, and answers genuine questions. The
agent handles the mechanical work between those gates. The container, not the agent
client's prompt system, is the security boundary. AgentWatch makes waiting decisions
visible instead of letting an autonomous session stall silently.

An optional [lazyboards](https://github.com/matteobortolazzo/lazyboards) board can
orchestrate agent-stack through its documented labels and dispatch commands. It is a
separate project, not an installation requirement.

## Install

Requirements: Linux, macOS, or WSL2; git; Docker or Podman; and at least one supported
client—Claude Code, Codex, or both.

```bash
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash
```

The installer detects available clients, registers the marketplace in each one, and
installs the workflow, attention, and isolation components independently. It also
creates the appropriate sandbox launcher (`agent-sand` for Claude, `sb` for either
agent).

```bash
# Inspect prerequisites and installation state without changing anything
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash -s -- doctor

# Update every installed component in every detected client
curl -fsSL https://raw.githubusercontent.com/matteobortolazzo/agent-stack/main/install.sh | bash -s -- update
```

See [Getting started](docs/getting-started.md) for the first-run path and
[the roadmap](docs/roadmap.md) for delivery status. Standalone component installation
is documented only in each layer's advanced/development section.

## Client capabilities

| Capability | Claude Code | Codex |
|---|---|---|
| Container isolation | `agent-sand` | `sb xt` (or `sb --agent codex`) |
| Session monitoring and self-bootstrap | Native hooks | Native hooks; re-trust changed hooks with `/hooks` |
| Portable shell, testing, stack, worktree, and review conventions | Yes | Yes |
| Interactive ticket refinement, design, implementation, and PR babysitting | Yes | Not yet |
| Documented implementation recipe | Built into the interactive workflow | [`agentflow/docs/codex.md`](agentflow/docs/codex.md) |

Claude Code currently provides the full interactive ticket-to-PR experience. Codex
provides the same isolation and monitoring layers plus portable engineering conventions
and an implementation recipe; it does not expose Claude-specific interactive commands.

## Work lifecycle

```text
New → Refined → [Designed] → Planned → Working → In Review → Implemented
```

- `Designed` is used when a UI ticket needs the optional design branch. A dedicated
  design ticket produces and approves the spec, then hands the implementation ticket
  forward.
- Planning persists an approved `.plans/` file and applies `Planned`. That file is the
  handoff between the human-gated planning session and implementation.
- `Working` is deliberately transient: it begins only when implementation picks up the
  persisted plan. AgentWatch dispatch can perform that pickup automatically.
- Opening the PR changes `Working` to `In Review`. Review comments and CI are handled
  before merge; merge completion applies `Implemented`.

`Followup` is an orthogonal label for separately tracked deferred work. It is not a
state in the lifecycle above.

## Internal layers

### Isolation: agent-sandbox

[`dev-sandbox/`](dev-sandbox) runs Claude Code or Codex at full permissions inside a
Docker/Podman boundary. From a git repository it mounts only that repository root at
`/workspace`; outside a repository it retains a documented legacy `~/Repos` fallback.
Per-repository images and explicit cleanup commands are available.

### Workflow: agentflow

[`agentflow/`](agentflow) owns human gates and the delivery recipe. On Claude Code it
offers the full interactive GitHub ticket-to-merged-PR workflow. On Codex its portable
skills and documented recipe provide the supported subset.

### Attention: agentwatch

[`agentwatch/`](agentwatch) turns Claude Code and Codex hooks into shared live state for
tmux, Waybar, Noctalia, DMS, GNOME, KDE Plasma, and the macOS menu bar. The plugin
bootstraps its matching binary and daemon from either client's cache on first session.
Display widgets are optional integrations, not extra agent-stack installation steps.

## References

- [Getting started](docs/getting-started.md)
- [Roadmap](docs/roadmap.md)
- [Security model](SECURITY.md)
- [Orchestration contract](docs/orchestration.md)
- [Contributing](CONTRIBUTING.md)
- Internal layer references: [agentflow](agentflow/README.md),
  [agentwatch](agentwatch/README.md), [agent-sandbox](dev-sandbox/README.md)

## License

MIT
