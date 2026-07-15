# agent-stack roadmap

This is the package-level source of truth for user-visible capability status. The
three internal plugins keep independent release versions, while active implementation
and maintenance follow-ups remain in the
[GitHub issue tracker](https://github.com/matteobortolazzo/agent-stack/issues).

## Available

- One installer and updater for Claude Code, Codex, or a dual-client setup
- Docker/Podman isolation with per-repository mounts and tailored images
- Claude Code's gated ticket-to-merged-PR workflow
- Portable engineering convention skills and a Codex implementation recipe
- Native Claude Code and Codex monitoring hooks with self-bootstrapping binaries
- tmux plus optional Linux desktop and macOS menu-bar status surfaces
- Persisted-plan handoff, `Planned` auto-pickup, capacity/budget gates, and failure
  reconciliation
- Sandbox lifecycle cleanup with `cenci-sand --prune` and optional volume removal

## In development

- Hardened and more visible sandbox boundary warnings
  ([#148](https://github.com/matteobortolazzo/agent-stack/issues/148))
- AgentWatch dispatch/status hardening and operational recovery work
  ([issues](https://github.com/matteobortolazzo/agent-stack/issues?q=is%3Aissue+is%3Aopen+label%3Aagentwatch))
- Release and repository security hygiene
  ([issues](https://github.com/matteobortolazzo/agent-stack/issues?q=is%3Aissue+is%3Aopen+label%3Asecurity))

## Planned

- Broader native Codex support for interactive workflow gates
- Additional attention surfaces where they add a maintained, testable integration
- Further orchestration policies built on the stable label, plan-file, and dispatch
  contracts

Items move between these sections only when their user-visible behavior changes.
Smaller bugs, maintenance, dependencies, and follow-ups are tracked only as GitHub
issues so this roadmap stays readable.
