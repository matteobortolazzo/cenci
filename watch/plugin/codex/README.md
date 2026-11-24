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

## Shared app-server and stale tmux panes

Codex's shared app-server can keep `TMUX_PANE` from the terminal that first
started it. If that pane closes, hooks from a new terminal can still deliver
events with the old pane ID. Hook trust and delivery can both be healthy while
that session disappears during watch's pane cleanup.

When a successful tmux listing proves an untracked Codex pane absent, watch
retains the session without a pane association. It remains visible in status
output and widget counts, follows Stop/SessionEnd, and expires through the normal
session TTL. A later event with a valid pane attaches it normally. Watch cannot
infer which other terminal belongs to the thread, so this fallback does not
rename/style that terminal's tmux window. A previously tracked pane closing keeps
the normal cleanup behavior; a failed tmux listing never proves a pane missing.

Watch also checks the tmux server pid. `cenci notify` sends the second field of
`$TMUX` with a Codex event, and the daemon attaches the pane only when that pid
equals the live server's `#{pid}` from the same pane listing. A pane that is
listed under another server (an equal pane id on a different tmux server, or an id
reused after a restart) and an event with no pid (an older `notify` binary) are
refused the same way: the session is retained without a pane. Events with no tmux
socket at all (sandboxed sessions) keep the existing attach path.

The server pid does not prove the session runs in that pane, because the shared
app-server can keep a pane that is still alive. Automatic association therefore
also needs a process-ancestry proof. For a Codex main-agent event inside tmux,
`cenci notify` reads the hook's process chain with one bounded `ps` call (1s, and
the hook still delivers and exits 0 when the read fails) and sends only the pids
and one single-client flag, never command lines. Watch attaches the pane without
a bind only when all three hold: the chain includes the hook-supplied pane's
`pane_pid`, the server pid matches the live server, and the nearest Codex
ancestor is not the shared app-server. An event whose nearest Codex ancestor is
the shared app-server stays unproven even while the supplied pane is alive, which
keeps two simultaneous clients on one app-server from sharing that pane.

Codex hook input carries no client identity, so watch tells the shared
app-server from a client by a marker in the nearest Codex ancestor's command
line: `app-server`, recorded from a native run (see
[codex-tmux-association.md](../../docs/codex-tmux-association.md)). The marker
applies on **Linux only**, the one platform where it was read whole from `ps`.
On Linux a single-client Codex (`codex exec`, or a TUI that runs in-process
rather than on the shared app-server) in a tmux pane is attached automatically,
with no bind. A thread on the shared app-server, which is how a default TUI runs,
stays unproven and needs `cenci bind`; so does a client whose own command line
contains `app-server` (a prompt naming it). On macOS and every other platform
the marker table is empty, so every host Codex hook pane stays unproven and
`cenci bind` is the only way to attach it. Sandboxed sessions (no tmux socket)
and Claude sessions are not affected.

On Linux, `codex --no-daemon` starts the interactive client in the pane even
when a shared Codex server is running. To use that mode when typing bare
`codex` in tmux, you can add this function to your interactive zsh or bash config.

```sh
codex() {
    if [ "$#" -eq 0 ] && [ -n "${TMUX:-}" ] && [ -n "${TMUX_PANE:-}" ]; then
        set -- --no-daemon
    fi
    command codex "$@"
}
```

A session in that state is shown by `cenci status` with a hint line:

```text
hint: hooks were delivered but no tmux association is proven; run 'cenci bind <id>' in the session's tmux pane
```

Run `cenci bind <session-id>` in the tmux pane that shows the session (Codex
sessions only). The daemon verifies the pane and server pid, then styles that
window and keeps using it even when later events still carry the stale pane; a
binding always outranks an ancestry proof, and an event with `agent_id` set
never moves the parent's pane. Bindings live in memory and are dropped on daemon
restart, so bind again after a restart. Exit codes, refusal
reasons and limits are in [codex-tmux-association.md](../../docs/codex-tmux-association.md);
the CLI is in the [main README](../../README.md#pinning-a-codex-session-to-a-tmux-pane-cenci-bind).

## Trust model

Codex records approval against the current hook definition and plugin contents,
including bundled scripts. **Trust all and continue** approves those current
contents; changing them can require another review even if `hooks.json` is
unchanged. Runtime binaries and version markers therefore live outside the bundle.
See [Codex's hook trust documentation](https://learn.chatgpt.com/docs/hooks).

After updating, review the changed hooks with `/hooks`. If an older bootstrap
already produced `plugin contents changed; hook approval required`, restart
Codex and review the current hooks. cenci never changes persisted hook approval.
