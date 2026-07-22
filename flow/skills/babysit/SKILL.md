---
name: babysit
description: "Follow an open PR with the client-neutral cenci supervisor until it merges or closes."
argument-hint: <pr-number> [interval e.g. 15m]
user-invocable: true
allowed-tools: Bash
---

# Babysit a pull request

This skill is a thin client adapter over the persistent `cenci babysit` supervisor. The
supervisor owns polling and retry state outside the repository, so it keeps running after
the invoking agent session exits. It launches an agent only when CI fails, actionable
review feedback arrives, or a retry cap needs human input.

Read the shared `shell-rules` skill before invoking the CLI.

## Invocation

Parse the first argument as a PR number, stripping a leading `#`. Reject missing or
non-numeric PRs. Parse the optional second argument as the interval.

Read `project-core`, resolve neutral configuration, and use the resulting config.

**Resolve the interval consistently with implement's auto-launched hand-off** (implement
Phase 9 arms `cenci babysit` the same way), so a manual and an auto-armed supervisor agree on
cadence: **explicit interval arg → the `babysitInterval` config field → `15m` default.** When
no second argument is given, resolve `.cenci/config.json`'s optional `babysitInterval` via the
shared `hooks/scripts/resolve-babysit-interval.sh` resolver — the single source of truth for
this field (`${CLAUDE_PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh` under Claude,
`${PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh` under Codex; pass the affected
project's slug for a monorepo, no slug for a single-repo top-level lookup). Non-empty output →
use it as `<interval>`; empty output (field unset, or no config file) → omit `--interval`
entirely and let `cenci babysit` apply its built-in `15m` default. An explicit second argument
always wins over the config.

Detect the current client and run exactly one of (dropping `--interval <interval>` when the
interval was neither passed as an argument nor resolved from config):

```bash
cenci babysit <pr> --agent claude --interval <interval>
```

```bash
cenci babysit <pr> --agent codex --interval <interval>
```

Use `--once` only when the user explicitly requests one tick. To stop a supervisor:

```bash
cenci babysit stop <pr>
```

Report the command's result. Do not reproduce the polling pipeline in the agent session,
arm Claude `/loop`, create a Codex goal, or maintain `/tmp/claude` state.

## Safety guarantees

The supervisor never force-pushes. It launches the selected client through `cenci run`
for CI repair or review handling, preserving those workflows' approval gates. Launched
repair agents confirm a fix against the project's local gate (`docs/health-gates.md`,
exit-0-is-healthy) before pushing, so a broken fix is caught locally instead of via a
CI round-trip. After three failed repair launches it pauses and opens a visible babysit
window for human direction. A merged PR moves its closing issues from `In Review` to
`Implemented`; a PR closed without merging leaves labels unchanged.
