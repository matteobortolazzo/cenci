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
`gh pr checks <pr> --json bucket,name,state` plus the supervisor state at
`$XDG_STATE_HOME/cenci/babysit/<12-hex>-<pr>.json` (fallback `~/.local/state/cenci/babysit/`),
read only when exactly one file matches this PR's `repo` and `pr`. When that state is not
readable, say "retry cap reached (exact count not readable from here)" rather than a number.

Then ask one `AskUserQuestion` with exactly two options: "leave paused for manual repair" and
"stop babysitting". Never mutate GitHub, push, or restart automatically before that choice.

- "leave paused for manual repair" changes nothing. Conflicts ordinarily go straight to Claude
  Opus through `merge-repair`; this window is the exhausted-budget fallback, so a human may run
  `/cenci:merge-repair <pr>` to inspect and repair that PR. The supervisor keeps polling and
  clears its conflict hold once GitHub confirms a conflict-free mergeability state; no separate
  re-arm step is needed.
- "stop babysitting" runs `cenci babysit stop <pr>`. On a non-zero exit, or a refusal because the
  sandbox cannot reach the host supervisor, say the supervisor was not stopped, print
  `cenci babysit stop <pr>` as the exact command to run on the host, and never report success.
