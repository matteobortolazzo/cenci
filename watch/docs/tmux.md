# tmux Behavior

Lessons on non-obvious tmux command behavior that differ from API expectations or vary by version.

## Rules

- **Unscoped tmux target resolution honors $TMUX_PANE environment variable.** Commands like `tmux display-message -p "#{session_name}"` (without `-t`) do not resolve to the server's most-recently-active session; tmux's cmd-find logic prioritizes any valid `$TMUX_PANE` from the client's environment ahead of most-recent-session, even ahead of a genuinely attached client on another session. This behavior is version-dependent (confirmed on tmux 3.4). When a test's red state depends on an external binary's resolution behavior, verify it empirically against the installed version before committing to the mechanics — do not assume unscoped forms ignore environment variables.

- **`tmux display-message -t <nonexistent-pane> -p "#{fmt}"` exits 0 with empty stdout instead of erroring.** Unlike most tmux subcommands (e.g., `rename-window`, `list-panes`), `display-message` with `-p` does not fail when the `-t` target is invalid; it succeeds silently with empty output. Production code must include an explicit empty-output check when a non-empty value is the contract — do not assume a tmux subcommand will error on an invalid target.

- **Status symbols belong in user variables, not window names.** Embedding a status symbol (▶, ✓, !, ~) directly in a window name breaks custom `status-format` configs: it doubles up with the config's own indicator inside `#W`, hardcoded colors override `window-status-style` changes, and users can't reference or control the symbol from their format strings. Set symbols via the `@cenci-symbol` user variable (per window) and style via `@cenci-style`; window names should contain only the task name or original name. For default-format users, prepend `#{@cenci-symbol}` to `window-status-format`/`window-status-current-format` during tracking and restore on cleanup.
