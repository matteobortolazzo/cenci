# cenci roadmap

cenci is one product with three cooperating layers: Watch makes sessions visible,
Sandbox contains full-permission execution, and Flow adds the guarded GitHub delivery
workflow. Users can stop after any outcome; unattended dispatch and automerge are an
explicit final step, not an installation side effect.

GitHub milestones are the authoritative work queues. Their numeric prefixes show the
intended order; an umbrella issue tracks its children and is not a second unit of work.

## Delivery order

| Order | Milestone | Outcome |
|---|---|---|
| 2.0 | [Safety and correctness](https://github.com/matteobortolazzo/cenci/milestone/5) | Close host-boundary, execution-routing, and core observability defects before expanding adoption |
| 2.1 | [Selective setup](https://github.com/matteobortolazzo/cenci/milestone/2) | Select only the needed layers and keep updates faithful to that installed set |
| 2.2 | [Corporate-ready sandbox](https://github.com/matteobortolazzo/cenci/milestone/6) | Add controlled proxy/CA, ADO credential, private tooling, and multi-repository support |
| 2.3 | [One-command workspace](https://github.com/matteobortolazzo/cenci/milestone/7) | Ship the opinionated tmux experience and the single-command workspace entry point ([#646](https://github.com/matteobortolazzo/cenci/issues/646)) |
| 2.4 | [Product clarity](https://github.com/matteobortolazzo/cenci/milestone/8) | Keep onboarding, visuals, prerequisites, and client capability claims coherent |
| 2.5 | [Refactor round](https://github.com/matteobortolazzo/cenci/milestone/4) | Reduce prompt, installer, and Go maintenance cost before autonomy expands |
| 3.0 | [Trusted autonomy](https://github.com/matteobortolazzo/cenci/milestone/9) | Make dispatch, CI gates, babysitting, and automerge authorization reliably fail closed |
| 3.1 | [Autonomy experience](https://github.com/matteobortolazzo/cenci/milestone/10) | Add a clear Full loop setup/readiness surface over the trusted primitives |
| 4 | [Later exploration](https://github.com/matteobortolazzo/cenci/milestone/3) | Optional integrations, research, and lower-priority improvements |

Finish the milestones in order. Work inside a milestone follows the dependency chain
in its GitHub description; independent tickets at the same step may proceed together.

## Available now

- One verified installer and updater for Claude Code, Codex, or a dual-client setup
- Docker/Podman isolation with per-repository mounts and tailored images
- Claude Code's gated ticket-to-reviewed-PR workflow
- Native Claude Code and Codex monitoring plus OpenCode live-session integration
- tmux and optional Linux desktop/macOS menu-bar status surfaces; no board required
- Persisted-plan handoff, planned-ticket pickup, capacity gates, and reconciliation
- Client-neutral persistent PR babysitting, subject to each client's corrective-workflow capability
- On-demand maintenance of workflow structure, documentation, client adapters, and rules

## Capability boundaries

- The installer currently reconciles all three components. Selective installation is
  the 2.1 outcome, tracked by [#938](https://github.com/matteobortolazzo/cenci/issues/938).
- The complete interactive ticket-to-PR path is stable for Claude Code. Codex has the
  native workflow foundation but remains in behavioral end-to-end acceptance.
- OpenCode supports direct sandbox sessions, monitoring, and portable conventions. It
  does not currently have native cenci workflows or supported workflow dispatch; see
  [#1019](https://github.com/matteobortolazzo/cenci/issues/1019).
- Full autonomy is available only as an advanced opt-in while 3.0 closes known
  correctness and authorization gaps. Every switch remains off by default and the
  per-ticket merge grant remains explicit.
- GitHub is the workflow, dispatch, and automerge host today. Watch and Sandbox are the
  layers intended to grow corporate/Azure DevOps connectivity first.

Small bugs and maintenance work stay in the issue tracker rather than being repeated
here. A user-visible claim moves sections only when the shipped behavior changes.
