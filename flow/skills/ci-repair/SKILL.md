---
name: ci-repair
description: Diagnose and repair failing CI for an existing pull request without reopening the implementation pipeline.
argument-hint: <pr-number> <head-branch> <failing-checks>
user-invocable: true
disable-model-invocation: true
---

For Claude and Codex, apply the diagnosis trigger in the plugin's
`docs/model-escalation.md`: after two failed attempts at the same unexplained
defect, delegate a bounded second opinion to `domain-specialist` with logs,
source paths, and attempted fixes. Validate its evidence before applying advice.
This does not add repair attempts or defer the ambiguity stop below; if that stop
already applies, return the diagnosis through it without another fix attempt.

Read `project-core`, `testing`, `shell-rules`, and `subagent-safety`. Diagnose the named
failing jobs, work only in the PR branch worktree, reproduce the root cause locally, make
the smallest test-backed fix, verify the fix is green via the project's local gate
(`docs/health-gates.md`) before pushing, rather than relying on a CI round-trip, commit,
and push normally. Never force-push, change lifecycle labels, open another PR, or mark an
infrastructure/flaky/external failure as repaired. If the cause is ambiguous, checkpoint
evidence and return control to the babysit supervisor for human input.
