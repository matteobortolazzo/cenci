# Cenci for Codex

> A [cenci-watch](../../README.md) integration for native Codex hooks.

Forwards Codex lifecycle hooks to `cenci notify` so the cenci daemon can update tmux window indicators and Waybar-compatible counts.

## Requirements

- `cenci` binary (provisioned automatically on first session)
- Codex hooks enabled (default in current Codex releases)
- Codex 0.166.0 or newer to receive `Interrupt` and `SessionEnd` (older clients skip
  those manifest entries and still recover through the stale and TTL sweeps)

## Binary and daemon (self-bootstrapping)

These Codex hooks bootstrap themselves — no manual setup and no dependency on the
Claude Code plugin. On `SessionStart`, `bootstrap.sh` runs detached and:

- downloads the release binary matching the plugin version and SHA-256-verifies it
  against the release `checksums.txt`;
- installs it outside the plugin bundle, in `$PLUGIN_DATA/<version>/bin/`, and
  symlinks it at `~/.local/bin/cenci` for interactive use and bar widgets;
- autostarts the daemon.

It is idempotent: once the version-matched binary and daemon are provisioned, a
re-run is a no-op (including the common case where the Claude Code plugin already
bootstrapped the shared daemon). Everything is non-fatal — failures log one line to
`${TMPDIR:-/tmp}/cenci-bootstrap.log` and never block the session. If a machine
also runs the Claude plugin, both share the same host daemon.

`CLAUDE_PLUGIN_DATA` is accepted when `PLUGIN_DATA` is absent. Manual bootstrap
and installer runs use `${XDG_CACHE_HOME:-$HOME/.cache}/cenci/codex/<version>/bin/`
when neither data variable is set. The installer clears both variables so an
unrelated plugin cannot redirect its binary selection. Each version has its own
binary and marker; startup never adds or rewrites files inside the Codex plugin
bundle. Legacy bundle-local binaries remain readable during migration.

After a login or restart, the daemon does not need a separate tmux startup entry:
the first lifecycle hook that finds the default event socket unavailable starts the
daemon, waits briefly, and retries the same event. Explicit custom event
sockets remain under the caller's control.

**Manual / Codex-only install** (alternative): install the binary yourself
(`go install github.com/matteobortolazzo/cenci/watch/v2@latest` or
`make build`) and start the daemon once (`cenci`). See the main
[README](../../README.md#advanced--development).

## Manual hook install

If you do not already have Codex hooks configured:

```bash
mkdir -p ~/.codex
cp /path/to/cenci/watch/plugin/codex/hooks.json ~/.codex/hooks.json
```

If `~/.codex/hooks.json` already exists, merge the `hooks` entries from this directory's `hooks.json` instead of replacing the file.

Codex will ask you to review/trust new hooks. Use `/hooks` in Codex if the hooks are listed as pending review.

## Plugin install

The marketplace plugin root includes the Codex manifest at
`../.codex-plugin/plugin.json`; it points back to this directory's hooks. Plugins and
their bundled hooks are stable and enabled by default in current Codex releases — no
feature flag is required.

## Trust model

Codex records approval against the current hook definition and plugin contents,
including bundled scripts. **Trust all and continue** approves those current
contents; changing them can require another review even if `hooks.json` is
unchanged. Runtime binaries and version markers therefore live outside the bundle.
See [Codex's hook trust documentation](https://learn.chatgpt.com/docs/hooks).

After updating, review the changed hooks with `/hooks`. If an older bootstrap
already produced `plugin contents changed; hook approval required`, restart
Codex and review the current hooks. cenci never changes persisted hook approval.
