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

Parse the first argument as a PR number, stripping a leading `#`. Parse the optional
second argument as the interval; default to `15m`. Reject missing or non-numeric PRs.

Read `project-core`, resolve neutral configuration, and use the resulting config.

Detect the current client and run exactly one of:

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
