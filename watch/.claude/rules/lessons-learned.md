# Lessons Learned

This file captures mistakes made during implementation to prevent recurrence.
Claude reads this file automatically. Its rules are authoritative and override assumptions.

---

<!-- Entries will be added below this line by the lessons-collector agent -->

## `os.IsNotExist` never matches an `os/exec` error

**Context**: PR #77's `CodexTokenReader.TokensInWindow` ran `exec.Command("sqlite3", ...).Output()` and then checked `if os.IsNotExist(err)` to treat a missing DB as zero tokens. That branch is dead code: a missing file makes `sqlite3` exit non-zero, so `err` is an `*exec.ExitError` (or `*exec.Error`), never an `fs.ErrNotExist`. The intended "missing DB → 0" path never fired; instead `sqlite3` errored, `windowHeadroom` clamped to 0, and codex was marked permanently budget-exhausted. Worse, `sqlite3 <missing-path>` leaves a stray 0-byte DB file behind.

**Rule**: `os.IsNotExist` (and `errors.Is(err, fs.ErrNotExist)`) only classify filesystem-call errors (`os.Open`, `os.Stat`, `os.ReadDir`, …). They will NOT match an error returned by `exec.Command(...).Run/Output`. To handle a missing input file for an external command, `os.Stat` the path yourself first and branch on that — don't infer it from the command's exit error. Doing the explicit stat also avoids side effects from running the command against a nonexistent path.

## Status symbols belong in user variables, not window names

**Context**: PR #22 embedded status symbols (▶, ✓, !, ~) directly in window names (e.g., `▶ writing tests`). This broke custom `status-format` configs because:
1. The symbol appeared inside `#W` alongside the config's own indicator (e.g., `●`), doubling up
2. Hardcoded colors in `status-format` overrode `window-status-style` changes
3. Users couldn't reference or control the symbol from their format strings

**Rule**: Status symbols MUST be set via the `@cenci-symbol` user variable (per window), NOT prepended to window names. Window names should contain only the task name or original name. Use `@cenci-style` for the style value. For default-format users, prepend `#{@cenci-symbol}` to `window-status-format`/`window-status-current-format` during tracking and restore on cleanup.
