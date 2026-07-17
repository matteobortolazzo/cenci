---
name: project-core
description: Resolve cenci's neutral project configuration and shared guidance consistently.
user-invocable: false
---

# Project core resolution

For every cenci workflow:

1. Read `.cenci/config.json` when it exists.
2. Otherwise read `.claude/config.json` as a legacy, read-only fallback.
3. If neither exists, stop and direct the user to `cenci:configure` using the active
   client's invocation syntax.
4. New configuration writes always target `.cenci/config.json`. Preserve unknown keys.
5. Read applicable `AGENTS.md` guidance first. A legacy substantive `CLAUDE.md` may be
   read as additional compatibility context, but shared guidance mutations target
   `AGENTS.md`.

`.claude/settings.json`, `.codex/agents/`, and generated
`CLAUDE.md` imports are client adapters, not sources of shared workflow truth.
