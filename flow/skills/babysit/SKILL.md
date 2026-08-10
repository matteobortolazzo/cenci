---
name: babysit
description: "Follow an open PR with the client-neutral cenci supervisor until it merges or closes."
compatibility: Portable — invokes the client-neutral `cenci babysit` CLI directly, no client-specific tools required.
argument-hint: <pr-number> [interval e.g. 15m]
user-invocable: true
disable-model-invocation: true
model: sonnet
allowed-tools: Read, Bash(cenci babysit:*), Bash(sh "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh":*)
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
this field. Run it under Claude as:

```bash
sh "${CLAUDE_PLUGIN_ROOT}/hooks/scripts/resolve-babysit-interval.sh"
```

(under Codex, `${PLUGIN_ROOT}` replaces `${CLAUDE_PLUGIN_ROOT}`); pass the affected
project's slug for a monorepo, no slug for a single-repo top-level lookup. Non-empty output →
use it as `<interval>`; empty output (field unset, or no config file) → omit `--interval`
entirely and let `cenci babysit` apply its built-in `15m` default. An explicit second argument
always wins over the config.

Detect the current client and run exactly one of the three (dropping `--interval <interval>`
when the interval was neither passed as an argument nor resolved from config):

```bash
cenci babysit <pr> --agent claude --interval <interval>
```

```bash
cenci babysit <pr> --agent codex --interval <interval>
```

```bash
cenci babysit <pr> --agent opencode --interval <interval>
```

Use `--once` only when the user explicitly requests one tick. To stop a supervisor:

```bash
cenci babysit stop <pr>
```

Report the command's result. Do not reproduce the polling pipeline in the agent session,
arm Claude `/loop`, create a Codex goal, or maintain `${TMPDIR:-/tmp}/cenci` state.

## Safety guarantees

The supervisor never force-pushes. It launches the selected client through `cenci run`
for CI repair or review handling, preserving those workflows' approval gates. Launched
repair agents confirm a fix against the project's local gate (`docs/health-gates.md`,
exit-0-is-healthy) before pushing, so a broken fix is caught locally instead of via a
CI round-trip. After three failed repair launches it pauses and opens a visible babysit
window for human direction.

On a merged PR, each closing issue's `In Review` label swaps to `Implemented` first,
exactly as before. The supervisor then reconciles split-parent completion at merge
time from the live native GitHub parent/sub-issue graph — never from a plan-time
`isLastChild` value. For each closed issue's native `parent`, once every native
`subIssues` node on that parent reads `CLOSED`, the parent is eligible to close, but
only if its comment thread carries no live (non-blockquoted) `parent-gap-report`
marker (`docs/comment-attribution.md`): no marker closes and relabels the parent
`Implemented`; a live marker leaves the parent untouched and reports a
distinguishable "held by a recorded acceptance-criteria gap report" outcome for
human triage instead. An unreadable, empty, or truncated sub-issue graph, or an
unreadable or truncated comment read, fails closed with no parent mutation. An
already-closed parent is an idempotent no-op. A stale gap report holds the parent
indefinitely until a human resolves it — by editing or deleting the comment, or by
closing the parent manually; babysit only detects a recorded gap report, it never
supersedes or re-audits one. A PR closed without merging leaves labels unchanged.
