# Decision record: one cohesive cenci product

Status: completed (2026-07-13)

## Decision

cenci is presented and installed as one product with three internal layers:

- cenci-sandbox: isolation
- cenci (flow): workflow and human decision gates
- cenci-watch: attention and dispatch

The external product has one installer, one update command, one lifecycle, and one
support story. The internal plugins retain separate manifests, versions, release tags,
and build/test commands. Optional lazyboards orchestration remains a separate project
that integrates through cenci's labels, persisted plans, and dispatch interface.

Amendment (2026-07-15): the cenci installer can optionally install and update
lazyboards (opt-in prompt or `--lazyboards`) and seeds its default board config when
none exists. lazyboards remains a separate, optional project with its own releases —
this changes distribution convenience, not the layering.

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
[GitHub issue tracker](https://github.com/matteobortolazzo/cenci/issues).
