# cenci roadmap

cenci is one product with three cooperating layers: Watch makes sessions visible,
Sandbox contains full-permission execution, and Flow adds the guarded GitHub delivery
workflow. Users can stop after any outcome; unattended dispatch and automerge are an
explicit final step, not an installation side effect.

GitHub milestones are the authoritative work queues. Their P1–P4 prefixes show the
intended priority; dependencies can cross milestone boundaries and still take precedence.
An umbrella issue tracks its children and is not a second unit of work. Each implementation
ticket delivers one PR.

## Delivery order

| Order | Milestone | Outcome |
|---|---|---|
| P1 | [Safe execution and reliable Codex workflows](https://github.com/matteobortolazzo/cenci/milestone/17) | Close host-permission, sandbox-fallback, and container-resource gaps; deliver native Codex planning, recovery, guards, repair, and end-to-end acceptance |
| P2 | [Lean workflows and reliable recovery](https://github.com/matteobortolazzo/cenci/milestone/13) | Reduce unnecessary questions while preserving material decisions; repair stage/label and checkpoint recovery |
| P3 | [Pipeline efficiency and maintainability](https://github.com/matteobortolazzo/cenci/milestone/14) | Reduce measured context use, repeated verification, and duplicated guidance without weakening correctness |
| P4 | [Runtime reliability and daily usability](https://github.com/matteobortolazzo/cenci/milestone/15) | Improve daemon startup, diagnostics, health gates, sandbox usability, and the dedicated tmux workspace |
| Later | [Optional capabilities and advanced hardening](https://github.com/matteobortolazzo/cenci/milestone/16) | Unscheduled selective setup, enterprise connectivity, stacked delivery, optional integrations, and additional trust hardening |

Work follows the dependency chains in milestone descriptions and native issue links;
independent tickets may proceed together. For example, P1's Codex acceptance consumes
the shared stage/label recovery fixes in P2. Later is not a promised release, and
`Followup` remains an untriaged capture label rather than a release gate.

The previous [Babysit works end to end](https://github.com/matteobortolazzo/cenci/milestone/12)
milestone is complete and closed. That records delivery of its scoped tickets, not a
claim that every client adapter or deferred trust-hardening item is complete.

## Current delivery groups

- Codex: [#1191](https://github.com/matteobortolazzo/cenci/issues/1191) agent configuration,
  [#1193](https://github.com/matteobortolazzo/cenci/issues/1193) runtime/recovery,
  [#1190](https://github.com/matteobortolazzo/cenci/issues/1190) watch lifecycle,
  [#1041](https://github.com/matteobortolazzo/cenci/issues/1041) guards, and
  [#1156](https://github.com/matteobortolazzo/cenci/issues/1156) repair feed
  [#1196](https://github.com/matteobortolazzo/cenci/issues/1196) native acceptance.
- Lean planning: [#1199](https://github.com/matteobortolazzo/cenci/issues/1199)
  consolidates #1070/#1071; broader decide-and-notify policy remains #1131.
- Health gates: [#1200](https://github.com/matteobortolazzo/cenci/issues/1200)
  consolidates #992/#993. Sandbox forwarding: [#1201](https://github.com/matteobortolazzo/cenci/issues/1201)
  consolidates #1012/#1015 before the separate user-configuration feature #1016.
- The workspace migration remains under [#646](https://github.com/matteobortolazzo/cenci/issues/646).
  Reaper safety (#1007) is complete; #645 and #1008 precede #1009, then #1010 integrates
  the result. #1008 still needs its explicit socket-security decision.
- Existing parents #938 (installer selection), #939 (plugin provisioning), and #1100
  (pipeline token usage) retain their child scopes. Grouping does not turn them into
  additional implementation PRs.

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
  deferred under [#938](https://github.com/matteobortolazzo/cenci/issues/938).
- The complete interactive ticket-to-PR path is stable for Claude Code. Codex has the
  native workflow foundation but remains in behavioral end-to-end acceptance.
- OpenCode supports direct sandbox sessions, monitoring, and portable conventions. It
  does not currently have native cenci workflows or supported workflow dispatch; see
  [#1019](https://github.com/matteobortolazzo/cenci/issues/1019).
- Full autonomy remains an advanced opt-in. P1/P2 address current execution and
  recovery gaps; additional trust hardening remains explicitly deferred in Later.
  Every switch remains off by default and the per-ticket merge grant remains explicit.
- GitHub is the workflow, dispatch, and automerge host today. Watch and Sandbox are the
  layers intended to grow corporate/Azure DevOps connectivity first.
- Ordered split children are serialized: every PR targets `main`, and a child cannot
  start until its blocker merges. Stacked delivery (#1061–#1067) is deferred, with
  adoption still gated on GitHub's feature leaving public preview. Its spike must
  verify the then-current availability and squash/retarget behavior before implementation;
  scheduling must also account for the gate and merge hardening it depends on.

Small bugs and maintenance work stay in the issue tracker rather than being repeated
here. A user-visible claim moves sections only when the shipped behavior changes.
