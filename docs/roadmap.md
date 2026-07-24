# cenci roadmap

This is the package-level source of truth for user-visible capability status. The
three internal plugins keep independent release versions, while active implementation
and maintenance follow-ups remain in the
[GitHub issue tracker](https://github.com/matteobortolazzo/cenci/issues).

## Available

- One installer and updater for Claude Code, Codex, or a dual-client setup
- Docker/Podman isolation with per-repository mounts and tailored images
- Claude Code's gated ticket-to-merged-PR workflow
- Client-neutral persistent PR babysitting
- Native Claude Code and Codex monitoring hooks with self-bootstrapping binaries
- tmux plus optional Linux desktop and macOS menu-bar status surfaces
- Persisted-plan handoff, `Planned` auto-pickup, capacity/budget gates, and failure
  reconciliation
- Sandbox lifecycle cleanup with `cenci sandbox prune` and optional volume removal
- `/cenci:maintain` — on-demand audit-and-repair of workflow structure, docs, client
  adapters, and accumulated rules, independent of lazyboards; rules curation (formerly
  the standalone garden skill, now retired) lives in its `rules` mode

## In development

- Native Codex gated workflows, Plan-mode handoff, and end-to-end acceptance

- Hardened and more visible sandbox boundary warnings
  ([#148](https://github.com/matteobortolazzo/cenci/issues/148))
- Cenci dispatch/status hardening and operational recovery work
  ([issues](https://github.com/matteobortolazzo/cenci/issues?q=is%3Aissue+is%3Aopen+label%3Awatch))
- Release and repository security hygiene
  ([issues](https://github.com/matteobortolazzo/cenci/issues?q=is%3Aissue+is%3Aopen+label%3Asecurity))

## Planned

- Continued hardening of native Codex workflow gates and optional integrations
- Additional attention surfaces where they add a maintained, testable integration
- Further orchestration policies built on the stable label, plan-file, and dispatch
  contracts

Items move between these sections only when their user-visible behavior changes.
Smaller bugs, maintenance, dependencies, and follow-ups are tracked only as GitHub
issues so this roadmap stays readable.
