# cenci roadmap

Finish cenci's current Claude/Codex workflow on tmux, then evaluate Herdr as a
second terminal runtime. Tmux remains supported throughout that work.

The owner-authorized [2026-11-10 reset](roadmap-reset-2026-11-10.md) replaces the
parallel P1–P4 and Later queues. Historical milestones are archived, not declared
completed releases. Dropped work is closed as **not planned**, with its original
scope and the reason retained.

## Delivery order

Only the first unfinished stage is the active delivery queue. Finish its stated
outcome before starting the next stage; a milestone percentage is not acceptance.
Existing dependency chains still order work within each stage.

| Stage | Milestone | Exit outcome |
|---|---|---|
| 1 | [Finish planned work and tmux baseline](https://github.com/matteobortolazzo/cenci/milestone/19) | The three saved plans, current tmux integration, instruction-file correctness and missing native measurement evidence are finished and verified |
| 2 | [Verify reliable Claude and Codex delivery](https://github.com/matteobortolazzo/cenci/milestone/20) | Installed ticket-to-PR workflows, feedback handling, capacity and recovery are demonstrated; retained authorization findings and native evidence are resolved |
| 3 | [Finish everyday sandbox setup](https://github.com/matteobortolazzo/cenci/milestone/21) | Selective installation/provisioning and approved host inheritance work; supported authentication behavior and its limits are established |
| 4 | [Prove Herdr alongside tmux](https://github.com/matteobortolazzo/cenci/milestone/22) | A bounded runtime-contract/mobile pilot yields evidence and a proceed/defer decision before implementation is expanded |

These are scheduling gates, not new daemon behavior. Milestone numbers alone do
not constrain dispatch. On an automated runner, use the existing machine-local
`dispatch.repos[].filters.milestones` to select the active milestone (initially
`[19]`), then advance it after acceptance. See
[the machine-local filters reference](../watch/README.md#machine-local-filters).
This reset changes no machine configuration, enrollment, merge grant or running
worker. The Herdr investigation also has native issue blockers on the retained
top-level work.

Parent trackers are not extra implementation PRs. Their children being closed
does not prove acceptance: inspect merged work and missing native evidence before
closing a parent. New bugs can enter the relevant stage; optional ideas do not
automatically extend its exit criteria or recreate a Later queue.

## Stage 1: finish work already in hand

- Preserve saved plans [#1509](https://github.com/matteobortolazzo/cenci/issues/1509),
  [#1510](https://github.com/matteobortolazzo/cenci/issues/1510) and
  [#1472](https://github.com/matteobortolazzo/cenci/issues/1472).
  Run #1510's attended configure evidence after #1509's classifier fix.
  Parent [#1182](https://github.com/matteobortolazzo/cenci/issues/1182) retains
  its acceptance requirements.
- Finish [#1433](https://github.com/matteobortolazzo/cenci/issues/1433), then audit
  [#646](https://github.com/matteobortolazzo/cenci/issues/646)'s dedicated-socket
  workspace outcome. Repair host Codex association in
  [#1514](https://github.com/matteobortolazzo/cenci/issues/1514).
- Record [#1184](https://github.com/matteobortolazzo/cenci/issues/1184)'s missing
  live measurement evidence. Its code children are merged; parent acceptance
  remains incomplete. Resolve current maintenance drift
  [#1511](https://github.com/matteobortolazzo/cenci/issues/1511).
- Retained-window cleanup [#1458](https://github.com/matteobortolazzo/cenci/issues/1458)
  shipped in [#1515](https://github.com/matteobortolazzo/cenci/pull/1515) during
  this reset. It is not remaining work.

## Stage 2: demonstrate reliable delivery

The Claude rollout chain is #1472 → #1469 → #1475 → #1026, tracked by
[#1463](https://github.com/matteobortolazzo/cenci/issues/1463).
Keep that scope; do not create another overlapping rollout epic.

Codex evidence follows #1395 → #1397 under #1193, and #1396 → #1398 under #1156.
[#1196](https://github.com/matteobortolazzo/cenci/issues/1196) consumes those
results for native end-to-end acceptance. The Claude pilot need not wait for
Codex-only evidence, but both are required before this stage is complete.

Retained authorization, policy and recovery findings are listed in the
[reset record](roadmap-reset-2026-11-10.md#stage-2). Revalidate old implementation
details during refinement. Resolve the relevant findings before final
eligible-automerge acceptance; a milestone move is not a security waiver.
The already-built Azure identity still needs owner-run staging evidence in
#1408 under #1210.

## Stage 3: finish daily sandbox use

- Selective host installation: #1004 → #1005 → #1006, tracked by #938.
- In-container plugin selection: #1003 completes #939.
- Host inheritance: #1516 supplies the shared policy/content contract, then
  #1517 (preferences), #1518 (versions) and #1519 (plugins). #1519 also needs #1003.
- #1520 investigates supported authentication lifecycle options. An evidenced
  conclusion that seamless shared login is unsupported is a valid investigation
  result; it does not authorize a custom credential broker or promise a feature.

Keep the container boundary and per-client compatibility requirements. Private
company sources, enterprise proxy/CA expansion and Azure DevOps credential
brokerage are not scheduled baseline requirements.

## Stage 4: prove Herdr before expanding scope

[#1524](https://github.com/matteobortolazzo/cenci/issues/1524) owns one bounded
design and compatibility pilot after the preceding stages are accepted. It
evaluates a backend-neutral launch/lifecycle contract and a Herdr implementation
path while keeping tmux as a supported backend.

Cenci retains workflow policy, ticket/PR state and sandbox isolation. Runtime
backends supply launch, stable identity, inspection, attach/focus and stop.
Backend selection applies to new launches; existing runs retain their backend.
Unknown or unreachable runtime state is never proof that a run exited.

Demonstrate ticket → sandboxed agent → phone interaction → PR/check visibility →
cleanup, including disconnect/reconnect and partial launch failures. Verify
desktop/mobile capabilities rather than assuming mods are webviews or
automatically work on phones.

Only then scope the necessary implementation tickets. PR-review views, Actions
views and Azure alert dashboards are possible subsequent integrations, not
precommitted prerequisites or claimed Herdr features.

## Scope retained and retired

Claude Code and Codex remain the selected clients. OpenCode stays disabled;
its unscheduled re-enablement tickets are archived without removing retained
implementation. Reopening it requires an explicit owner decision.

Stacked PR delivery, the Paseo experiment, separate optional status surfaces,
cosmetic plan relocation, speculative testing MCPs and optional installer/repair
refactors are not scheduled. Shipped improvements remain. No closure in this
reset claims an unperformed experiment passed.

The [issue-by-issue record](roadmap-reset-2026-11-10.md) is the audit trail; live
milestones are the current queues. Operational capabilities and limits remain
documented in [getting started](getting-started.md) and
[the autonomous loop](autonomous-loop.md).
