# Lessons Learned

This file captures mistakes made during implementation to prevent recurrence.
Claude reads this file automatically. Its rules are authoritative and override assumptions.

---

<!-- Entries will be added below this line by the lessons-collector agent -->

## Status symbols belong in user variables, not window names

**Context**: PR #22 embedded status symbols (▶, ✓, !, ~) directly in window names (e.g., `▶ writing tests`). This broke custom `status-format` configs because:
1. The symbol appeared inside `#W` alongside the config's own indicator (e.g., `●`), doubling up
2. Hardcoded colors in `status-format` overrode `window-status-style` changes
3. Users couldn't reference or control the symbol from their format strings

**Rule**: Status symbols MUST be set via the `@agentwatch-symbol` user variable (per window), NOT prepended to window names. Window names should contain only the task name or original name. Use `@agentwatch-style` for the style value. For default-format users, prepend `#{@agentwatch-symbol}` to `window-status-format`/`window-status-current-format` during tracking and restore on cleanup.
