---
name: babysit-attention
description: Resolve a paused PR supervisor decision with explicit human input.
argument-hint: <pr-number> <reason>
user-invocable: false
---

This window was opened by the persistent babysit supervisor because automated progress is
ambiguous or its retry cap was reached. Read the supervisor state, summarize the exact PR,
SHA, failing checks, and attempt count, then ask the user to choose whether to retry with a
fresh budget, leave it paused for manual repair, or stop babysitting. Use the active
client's native input mechanism. Never mutate GitHub, push, or restart automatically before
that choice.
