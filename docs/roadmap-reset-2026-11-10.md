# Roadmap reset — 2026-11-10

Owner decision: finish cenci on tmux, preserving the three saved plans, then
evaluate Herdr through a shared runtime interface. Tracking:
[#1523](https://github.com/matteobortolazzo/cenci/issues/1523).
The current sequence and exit outcomes live in [the roadmap](roadmap.md).

## Accounting

The initial snapshot contained 73 open issues. During the reset, #1458 was
closed by merged PR #1515. Of the remaining 72, 45 are retained and 27 are
closed as **not planned**. None was deleted. Nine retained issues are parent
trackers; others include attended/manual evidence, so this is not a promise
of 45 implementation PRs.

Two new issues record this work: #1523 for this documentation PR and #1524 for
the future Herdr investigation. They are additional to the 45 retained issues.
Historical milestone issue/PR counts are not acceptance percentages.

## Retained work

Milestone-only moves preserve original criteria, labels, assignees and native
parent/child relations. Saved plans #1472, #1509 and #1510 are unchanged.
Native ordering is added for #1509 → #1510 and the already-written host-profile
dependencies (#1516 → #1517/#1518/#1519, #1003 → #1519).

The Herdr investigation is blocked by retained top-level issues, including
parent trackers whose acceptance spans their children. It also waits for
stages 1–3 acceptance; later issues added to those stages need a scheduling
review. Milestone order alone is not enforced by dispatch: select the active
milestone through its existing machine-local filter.

### Stage 1

[Milestone 19](https://github.com/matteobortolazzo/cenci/milestone/19).

| Issue | Retained outcome |
|---|---|
| [#1472](https://github.com/matteobortolazzo/cenci/issues/1472) | watch: handle PR feedback with durable holds and one branch writer |
| [#1509](https://github.com/matteobortolazzo/cenci/issues/1509) | flow: enforce exact inert-adapter shapes in configure and the migration classifier |
| [#1510](https://github.com/matteobortolazzo/cenci/issues/1510) | flow: record a real configure rerun idempotence run in claude-instruction-files.md |
| [#1182](https://github.com/matteobortolazzo/cenci/issues/1182) | flow: support AGENTS.md-only repositories and retire generated CLAUDE.md import adapters |
| [#1433](https://github.com/matteobortolazzo/cenci/issues/1433) | watch: prove the join key on the cenci socket and update dispatch hint and docs (6/6) |
| [#646](https://github.com/matteobortolazzo/cenci/issues/646) | watch: cenci up and the dedicated-socket tmux runtime migration |
| [#1514](https://github.com/matteobortolazzo/cenci/issues/1514) | watch(codex): restore host tmux association with stale shared app-server pane identity |
| [#1184](https://github.com/matteobortolazzo/cenci/issues/1184) | watch: ingest native Claude context and plan-quota measurements separately from estimated headroom |
| [#1511](https://github.com/matteobortolazzo/cenci/issues/1511) | Scheduled cenci maintenance audit found drift |

### Stage 2

[Milestone 20](https://github.com/matteobortolazzo/cenci/milestone/20).

| Issue | Retained outcome |
|---|---|
| [#1469](https://github.com/matteobortolazzo/cenci/issues/1469) | watch: enforce shared concurrency limits across dispatch and repairs |
| [#1475](https://github.com/matteobortolazzo/cenci/issues/1475) | watch: automatically sync the base checkout and update behind PRs |
| [#1026](https://github.com/matteobortolazzo/cenci/issues/1026) | watch: deliver local per-repo autonomy setup and verify the Claude rollout |
| [#1463](https://github.com/matteobortolazzo/cenci/issues/1463) | Velka rollout: reliable Claude-first refinement to PR and optional automerge |
| [#1193](https://github.com/matteobortolazzo/cenci/issues/1193) | flow(codex): make planning, checkpoints, and goal recovery reliable |
| [#1156](https://github.com/matteobortolazzo/cenci/issues/1156) | flow(codex): ship babysit repair/attention adapters and reject unavailable dispatch targets |
| [#1395](https://github.com/matteobortolazzo/cenci/issues/1395) | flow(codex): run and record the native Plan-mode, handoff and goal evidence (manual) |
| [#1396](https://github.com/matteobortolazzo/cenci/issues/1396) | flow(codex): run and record the native ci-repair/babysit-attention smoke evidence (manual) |
| [#1397](https://github.com/matteobortolazzo/cenci/issues/1397) | flow(codex): commit the native Plan-mode/handoff/goal record and reconcile Codex docs |
| [#1398](https://github.com/matteobortolazzo/cenci/issues/1398) | flow(codex): commit the native ci-repair/babysit-attention record |
| [#1196](https://github.com/matteobortolazzo/cenci/issues/1196) | flow(codex): deliver lean planning with native end-to-end acceptance |
| [#1135](https://github.com/matteobortolazzo/cenci/issues/1135) | watch: harden host arm-request identity and supervisor resource bounds |
| [#1127](https://github.com/matteobortolazzo/cenci/issues/1127) | watch: reap-orphans sweep-abort resilience and scan-row injection hardening (deferred from #1007) |
| [#1076](https://github.com/matteobortolazzo/cenci/issues/1076) | Add a freshness/edit-time binding to the planner's refinement-settled-posture suppression gate |
| [#910](https://github.com/matteobortolazzo/cenci/issues/910) | dispatch: scope resumed session to only the authorized escalation-answer comment |
| [#903](https://github.com/matteobortolazzo/cenci/issues/903) | dispatch: fail-closed state/ref resolution — DefaultStatePath must not collapse to CWD; pin autonomy probe to fetched OID (absorbs #908) |
| [#870](https://github.com/matteobortolazzo/cenci/issues/870) | babysit: automerge:ok grant is derived from unpinned, mutable PR metadata |
| [#868](https://github.com/matteobortolazzo/cenci/issues/868) | Deferred findings from #853: dispatch resume attempt-counter trust and abort-contract coverage gap |
| [#862](https://github.com/matteobortolazzo/cenci/issues/862) | babysit: authorize review-resolution and dismissal actors |
| [#847](https://github.com/matteobortolazzo/cenci/issues/847) | Add author-authorization check for autonomous planning-pickup dispatch |
| [#841](https://github.com/matteobortolazzo/cenci/issues/841) | Automerge policy engine: inert protectedPaths footgun + config-version drift (base-ref trust moved to #870) |
| [#838](https://github.com/matteobortolazzo/cenci/issues/838) | Lean planning: independent escalation-compliance check + cross-repo temp-file scoping (followup from #823) |
| [#758](https://github.com/matteobortolazzo/cenci/issues/758) | Verify Claude Code Bash-matcher semantics and close residual git-deny gaps |
| [#1210](https://github.com/matteobortolazzo/cenci/issues/1210) | incident: dedicated least-privilege Azure identity and guided setup command |
| [#1408](https://github.com/matteobortolazzo/cenci/issues/1408) | incident: record the live Azure staging run for the worker identity (P53, closes out #1210) |

### Stage 3

[Milestone 21](https://github.com/matteobortolazzo/cenci/milestone/21).

| Issue | Retained outcome |
|---|---|
| [#1516](https://github.com/matteobortolazzo/cenci/issues/1516) | sandbox: inherit user-approved host skills and instructions read-only |
| [#1517](https://github.com/matteobortolazzo/cenci/issues/1517) | sandbox: inherit host agent preferences with explicit sandbox overrides |
| [#1518](https://github.com/matteobortolazzo/cenci/issues/1518) | sandbox: optionally follow host Claude Code and Codex versions in shared CLI volumes |
| [#1519](https://github.com/matteobortolazzo/cenci/issues/1519) | sandbox: inherit approved host plugin selections without sharing writable caches |
| [#1520](https://github.com/matteobortolazzo/cenci/issues/1520) | sandbox: investigate a supported host-managed login lifecycle for Claude Code and Codex |
| [#939](https://github.com/matteobortolazzo/cenci/issues/939) | sandbox: per-repo in-container plugin provisioning (sandbox.plugins in .cenci/config.json) |
| [#1003](https://github.com/matteobortolazzo/cenci/issues/1003) | sandbox: consume CENCI_SANDBOX_PLUGINS in Claude/Codex entrypoint and settings provisioning (2/2) |
| [#938](https://github.com/matteobortolazzo/cenci/issues/938) | installer: select which plugins to install/update (--only <list> + wizard multi-select) |
| [#1004](https://github.com/matteobortolazzo/cenci/issues/1004) | Installer: --only <list> flag and exit-2 usage-error contract (1/3) |
| [#1005](https://github.com/matteobortolazzo/cenci/issues/1005) | Installer: update mode reconciles only installed plugins (2/3) |
| [#1006](https://github.com/matteobortolazzo/cenci/issues/1006) | Installer: interactive plugin wizard, gating-audit comments, and docs (3/3) |

## Dropped work

Each issue is closed with reason `not_planned`. Its body retains the original
scope below a dated decision note. This records a scope decision, not proof
that an old report was fixed, a benchmark target was met, or all criteria shipped.

| Issues | Reason |
|---|---|
| [#1317](https://github.com/matteobortolazzo/cenci/issues/1317) | Retire the Paseo-specific experiment. The next runtime evaluation will target Herdr after cenci's tmux baseline is finished; this does not assert that Herdr already meets the headless/lifecycle requirements. |
| [#1318](https://github.com/matteobortolazzo/cenci/issues/1318), [#723](https://github.com/matteobortolazzo/cenci/issues/723), [#1185](https://github.com/matteobortolazzo/cenci/issues/1185) | Retire the separate push-channel, multi-host widget and Claude-only panel proposals from the committed roadmap. Reassess the underlying attention/dashboard needs during the later Herdr pilot; mobile background notifications and custom-panel portability are not assumed solved. |
| [#1220](https://github.com/matteobortolazzo/cenci/issues/1220), [#1221](https://github.com/matteobortolazzo/cenci/issues/1221), [#1222](https://github.com/matteobortolazzo/cenci/issues/1222) | OpenCode remains disabled and outside the selected Claude/Codex scope. Archive the uncommitted re-enablement/provisioning/diagnostics work; retain the implementation and history for an explicit future decision. |
| [#1061](https://github.com/matteobortolazzo/cenci/issues/1061), [#1062](https://github.com/matteobortolazzo/cenci/issues/1062), [#1063](https://github.com/matteobortolazzo/cenci/issues/1063), [#1064](https://github.com/matteobortolazzo/cenci/issues/1064), [#1065](https://github.com/matteobortolazzo/cenci/issues/1065), [#1066](https://github.com/matteobortolazzo/cenci/issues/1066), [#1067](https://github.com/matteobortolazzo/cenci/issues/1067) | Stacked PR delivery is not needed to finish the current ticket-to-PR flow. Drop the whole dependent feature chain together. Existing serialized child delivery remains supported; no stacked-delivery claim is made. |
| [#1024](https://github.com/matteobortolazzo/cenci/issues/1024), [#1025](https://github.com/matteobortolazzo/cenci/issues/1025) | Defer the optional installer-output redesign and Go lifecycle extraction. Finish selective installation using its existing #938/#1004/#1005/#1006 owners; these refactors are not completion gates. |
| [#1021](https://github.com/matteobortolazzo/cenci/issues/1021), [#1022](https://github.com/matteobortolazzo/cenci/issues/1022), [#1023](https://github.com/matteobortolazzo/cenci/issues/1023) | Remove the unscheduled enterprise proxy/CA, Azure DevOps credential bridge and private-source provisioning expansion from the committed baseline. This does not remove existing capabilities or close the separate Azure incident identity evidence (#1210/#1408). Reopen only against a concrete adoption requirement; private sources remain unsupported by #1519. |
| [#952](https://github.com/matteobortolazzo/cenci/issues/952) | A separate GitHub posting identity is optional account/infrastructure work. Keep existing attribution banners and markers; a new bot identity is not required for the current delivery outcome. |
| [#724](https://github.com/matteobortolazzo/cenci/issues/724) | The plan-directory relocation is cosmetic and would add migration work without improving the selected delivery outcome. Keep the existing .plans convention. |
| [#1106](https://github.com/matteobortolazzo/cenci/issues/1106) | This is an unscoped collection of possible testing MCP integrations with no concrete failing workflow or acceptance target. Reopen a narrowly scoped integration only when a real project needs it. |
| [#1364](https://github.com/matteobortolazzo/cenci/issues/1364), [#1104](https://github.com/matteobortolazzo/cenci/issues/1104), [#1100](https://github.com/matteobortolazzo/cenci/issues/1100) | Retire the remaining conditional fresh-agent repair experiment and its measurement prerequisite. Keep the optimization work already merged and the existing measurement history. This is a roadmap decision, not a measured defer verdict, proof of the under-150k target for all workloads, or a claim that the unexecuted failing-workload experiment passed. |
| [#762](https://github.com/matteobortolazzo/cenci/issues/762), [#687](https://github.com/matteobortolazzo/cenci/issues/687) | Retire the remaining low-priority reporting/documentation polish and its umbrella. Preserve the already merged #759/#760/#761 work. Closing the umbrella as not planned records that its remaining polish was dropped, not that every original criterion shipped. |

The retired parent #687 retains merged #759/#760/#761 while its remaining
polish #762 is dropped. Parent #1100 retains delivered optimizations and existing
measurements; neither the unexecuted failing-workload experiment nor a universal
under-150k claim is certified by closing it.

## Delivered during this reset

[#1458](https://github.com/matteobortolazzo/cenci/issues/1458) — retained tmux
window cleanup — was delivered by merged
[PR #1515](https://github.com/matteobortolazzo/cenci/pull/1515).
Its historical milestone remains unchanged.

## Historical queues

Milestones 13–18 are closed as superseded queues, with an archive explanation.
Closed issues and merged PRs keep their historical membership. Retained open
issues move to milestones 19–21; #1524 starts milestone 22. The old milestones
are not labelled completed releases; archived OpenCode work remains opt-in.

## Verification

GitHub readback verified all 45 retained issues and 27 not-planned closures,
including unchanged retained bodies, labels, assignees and parent relations.
All previous native dependencies remain, with no cycles or retained dependency
on dropped work. Herdr has 29 top-level blockers; the five additional ordering
edges described above are present. The three Planned issues remain open.

The disposition sets cover all 73 initial issues exactly once, including merged
#1458/#1515. The saved plan files were not edited. Relative-file link validation
checked 67 links across the four changed documents, and git diff whitespace
validation passed. The historical queues were checked using their actual open
issue inventories: GitHub's aggregate milestone counters lagged the mutations.
This reset changes documentation and tracker metadata; application behavior
is unchanged.
