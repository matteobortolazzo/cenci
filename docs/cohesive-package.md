# Decision record: one cohesive agent-stack product

Status: completed (2026-07-13)

## Decision

agent-stack is presented and installed as one product with three internal layers:

- agent-sandbox: isolation
- agentflow: workflow and human decision gates
- agentwatch: attention and dispatch

The external product has one installer, one update command, one lifecycle, and one
support story. The internal plugins retain separate manifests, versions, release tags,
and build/test commands. Optional lazyboards orchestration remains a separate project
that integrates through agent-stack's labels, persisted plans, and dispatch interface.

Claude Code provides the full interactive workflow. Codex is supported for isolation,
monitoring, portable conventions, and the documented implementation recipe while its
interactive workflow support develops.

## Migration outcome

The migration delivered the full-stack installer, client-native plugin manifests,
self-bootstrapping monitoring, container-based security boundary, per-repository
sandboxing, persisted-plan pickup, shared lifecycle terminology, and retired the old
component names. Historical proposal details and pending-ticket lists were removed
because they described superseded architecture.

Current status belongs in the [package roadmap](roadmap.md). Bugs, active maintenance,
and follow-ups belong in the
[GitHub issue tracker](https://github.com/matteobortolazzo/agent-stack/issues).
