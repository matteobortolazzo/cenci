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
review feedback arrives, merge conflicts need repair, or a retry cap needs human input.

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

## Where the supervisor runs

The supervisor always runs on the host — never inside a `cenci sandbox` container. Running
`cenci babysit` from inside a sandboxed session (`CENCI_SANDBOX=1`) never starts a local
supervisor there; the CLI forwards the arm request to the host daemon over its event socket
and reports one of three outcomes: **armed** (the supervisor now runs on the host), **not
armed** (the host daemon rejected the request — its reason is relayed verbatim, never
re-derived or re-worded), or **arm status unknown** (the host daemon did not respond before
the deadline; verify or re-arm from a host tmux pane: `cenci babysit <pr> --agent <agent>`,
which safely no-ops if a supervisor is already running). `cenci babysit stop <pr>` is
forwarded the same way from inside a sandbox and reports one of three outcomes: **stopped**
(`Stopped babysitting PR #<pr> on the host.`, exit 0), **not stopped** (`not stopped: <reason>`,
the host daemon rejected the request and its reason is relayed verbatim, exit 1), or **stop
status unknown** (the host daemon did not respond before the deadline, exit 1). Both failures
name the host command to run instead: `cenci babysit stop <pr>` from a host tmux pane.

**Verify with `status`.** `cenci babysit status <pr>` works on the host and inside a sandbox
(where it is forwarded to the host daemon). It prints one JSON line with `pr`, `repo`, `status`,
`ciFixAttempts`, `conflictFixAttempts`, `fixCap` and `supervisorLive`, and exits 1 with
`no supervisor found for PR #<pr>` when no state exists. Inside a sandbox, a rejected or
unanswered status request exits 1 and names `cenci babysit status <pr>` as the host command.

**On the host**, the supervisor's own state directory holds the same data:

```text
$XDG_STATE_HOME/cenci/babysit   (fallback: ~/.local/state/cenci/babysit)
  <12-hex-repo-hash>-<pr>.json  — the supervisor's persisted state for that PR
  <12-hex-repo-hash>-<pr>.log   — the detached supervisor's stdout/stderr
```

A `<pr>.json`/`<pr>.log` pair present for the PR means a supervisor is (or was) running for
it; `supervisorLive` in the `status` output is the authoritative liveness check.

## Safety guarantees

The supervisor never pushes itself. It launches the selected client through `cenci run`
for CI repair or review handling. Merge conflicts automatically launch `merge-repair`
with `--agent claude --model opus`, regardless of the supervising client. The repair
fetches the actual remote PR base, resolves in the PR's feature worktree, verifies
locally, and updates the same branch with an explicit SHA-pinned `--force-with-lease`.
It asks for human input only when resolution requires an unresolved decision. CI
and review repair dispatch waits while the PR conflicts, avoiding competing edits.
The supervisor waits while the recorded repair window has a live worker, including
across head changes, and keeps polling. If the worker exits while conflicts remain,
it retries even at the same head SHA; three launches in a continuous conflict
episode exhaust the budget and open an attention window. Probe failures are reported
without launching a second worker. A confirmed conflict-free state resets that budget. Launched
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
closing the parent manually, or — the supported path — running `/cenci:close <parent>`,
whose item-by-item coverage review of the merged child PRs closes the parent on zero
gaps and otherwise turns each gap into a new child; babysit only detects a recorded
gap report, it never supersedes or re-audits one. A PR closed without merging leaves labels unchanged.
