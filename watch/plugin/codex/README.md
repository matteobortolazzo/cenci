# AgentWatch for Codex

Forwards Codex lifecycle hooks to `agentwatch notify` so the agentwatch daemon can update tmux window indicators and Waybar-compatible counts.

## Requirements

- plugin-local `agentwatch` binary (provisioned automatically on first session)
- Codex hooks enabled (default in current Codex releases)

## Binary and daemon (self-bootstrapping)

These Codex hooks bootstrap themselves — no manual setup and no dependency on the
Claude Code plugin. On `SessionStart`, `bootstrap.sh` runs detached and:

- downloads the release binary matching the plugin version and SHA-256-verifies it
  against the release `checksums.txt`;
- installs it to the plugin's `bin/` and symlinks it onto `$PATH` (preferring the
  first existing writable `$PATH` entry, falling back to `~/.local/bin` with a
  one-line "add it to your PATH" hint in the log) for interactive use and bar widgets;
- autostarts the daemon.

It is idempotent: once the version-matched binary and daemon are provisioned, a
re-run is a no-op (including the common case where the Claude Code plugin already
bootstrapped the shared daemon). Everything is non-fatal — failures log one line to
`${TMPDIR:-/tmp}/agentwatch-bootstrap.log` and never block the session. If a machine
also runs the Claude plugin, both share the same host daemon.

After a login or restart, the daemon does not need a separate tmux startup entry:
the first lifecycle hook that finds the default event socket unavailable starts the
plugin-local daemon, waits briefly, and retries the same event. Explicit custom event
sockets remain under the caller's control.

**Manual / Codex-only install** (alternative): install the binary yourself
(`go install github.com/matteobortolazzo/agent-stack/agentwatch/v4@latest` or
`make build`) and start the daemon once (`agentwatch`). See the main
[README](../../README.md#advanced--development).

## Manual hook install

If you do not already have Codex hooks configured:

```bash
mkdir -p ~/.codex
cp /path/to/agent-stack/agentwatch/plugin/codex/hooks.json ~/.codex/hooks.json
```

If `~/.codex/hooks.json` already exists, merge the `hooks` entries from this directory's `hooks.json` instead of replacing the file.

Codex will ask you to review/trust new hooks. Use `/hooks` in Codex if the hooks are listed as pending review.

## Plugin install

The marketplace plugin root includes the Codex manifest at
`../.codex-plugin/plugin.json`; it points back to this directory's hooks. Plugins and
their bundled hooks are stable and enabled by default in current Codex releases — no
feature flag is required.

## Trust model

Codex hash-pins `hooks.json`: it records a hash of the file it trusted. Every
plugin update that changes `hooks.json` changes that hash, so Codex marks the
hooks as pending review and you must re-trust them via `/hooks` in Codex before
they run again.
