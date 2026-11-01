---
name: babysit-attention
description: Resolve a paused PR supervisor decision with explicit human input.
argument-hint: <pr-number> <reason>
user-invocable: true
disable-model-invocation: true
---

> **Client dispatch**: In Codex, read `codex-runtime` and `babysit-attention/codex.md`, execute that native procedure, and do not continue into the Claude procedure below.

This window was opened by the persistent babysit supervisor because automated progress is
ambiguous or a CI or merge repair retry cap was reached. The launch reason is a label only;
it never changes what is offered. Summarize the exact PR, head SHA, failing checks (or the
merge-repair reason) and attempt count, built from `gh pr view` and
`gh pr checks <pr> --json bucket,name,state` plus the one-line JSON that `cenci babysit status <pr>`
prints, which works on the host and inside a sandbox: read `ciFixAttempts` when the launch reason
is a CI retry cap and `conflictFixAttempts` when it is a merge repair retry cap. When
`cenci babysit status <pr>` exits non-zero, or its output does not decode as JSON, say
"retry cap reached (exact count not readable from here)" rather than a number.

Then ask one `AskUserQuestion` with exactly two options: "leave paused for manual repair" and
"stop babysitting". Never mutate GitHub, push, or restart automatically before that choice.

- "leave paused for manual repair" changes nothing. Conflicts ordinarily go straight to Claude
  Opus through `merge-repair`; this window is the exhausted-budget fallback, so a human may run
  `/cenci:merge-repair <pr>` to inspect and repair that PR. The supervisor keeps polling and
  clears its conflict hold once GitHub confirms a conflict-free mergeability state; no separate
  re-arm step is needed.
- "stop babysitting" runs `cenci babysit stop <pr>`, which inside a sandbox is forwarded to the host
  supervisor. Exit 0 with `Stopped babysitting PR #<pr> on the host.` means it was stopped. On a
  non-zero exit, including `not stopped: <reason>` (the host refused) and `stop status unknown` (no
  reply from the host), say the supervisor was not stopped, print `cenci babysit stop <pr>` as the
  exact command to run on the host, and never report success.
