---
name: babysit-attention
description: Resolve a paused PR supervisor decision with explicit human input.
argument-hint: <pr-number> <reason>
user-invocable: true
disable-model-invocation: true
---

This window was opened by the persistent babysit supervisor because automated progress is
ambiguous or a CI/merge repair retry cap was reached. Read the
supervisor state, summarize the exact PR, SHA, failing checks, and attempt count, then ask
the user to choose whether to retry with a fresh budget, leave it paused for manual repair,
or stop babysitting. Use the active client's native input mechanism. Never mutate GitHub,
push, or restart automatically before that choice.

When the reason is the merge repair retry cap, summarize the PR, head SHA and merge
repair attempts, then offer "leave paused" or "stop babysitting". Conflicts ordinarily
go straight to Claude Opus through `merge-repair`; this window is the exhausted-budget
fallback. A human may run `/cenci:merge-repair <pr>` to inspect and repair that PR.
The supervisor keeps polling and clears its conflict hold once GitHub confirms a
conflict-free mergeability state; no separate re-arm step is needed.
