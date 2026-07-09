# agentwatch

An event-driven tmux watcher that monitors Claude Code and OpenAI Codex sessions via hooks and shows live status in the tmux status bar:

- **▶ blue** — running (generating, tool use, thinking)
- **✓ green** — done (finished, waiting for next prompt)
- **! red** — need input (permission dialog)
- **~ dim** — idle (fresh prompt, no task yet)

When the agent exits or agentwatch stops, the original window name is restored.

## Architecture

```
Claude/Codex hooks  →  agentwatch notify  →  event socket  →  daemon (session-keyed)
                                                                       |
                                                              [tmux frontend]
                                                              window rename/style
                                                                       |
                                                    broadcast socket → agentwatch status
                                                                    (waybar, noctalia, dms)
```

The core daemon keys state by agent session id, maps hook events to statuses, and owns the paneless TTL sweep. All window work is delegated to an injected frontend:

- **tmux frontend** (`internal/frontend/tmux/`): the one interactive frontend — window rename, style, pane-based stale sweep, renumber migration.
- **status JSON** (`internal/frontend/status/`): read-only broadcast in the [Waybar custom module protocol](https://github.com/Alexays/Waybar/wiki/Module:-Custom); consumed by `agentwatch status` and the waybar, noctalia, and dms display widgets.

No polling for normal state changes. Agent hooks push state changes to the daemon instantly via a Unix socket; the daemon sweeps periodically for stale/exited sessions.

## Install (Claude Code)

Install the plugin from the marketplace — the binary and daemon auto-bootstrap on
your first session, so this is all you need:

```bash
# Register the repo as a marketplace (works with private repos too)
claude plugin marketplace add matteobortolazzo/claude-tools

# Install the plugin (persists across sessions)
claude plugin install agentwatch
```

On the first `SessionStart` after install, the plugin downloads the `agentwatch`
binary matching the plugin version (with checksum verification) into the plugin's
`bin/` directory and starts the daemon. Bootstrap runs detached and never blocks
the agent, so the very first session may take a moment before status appears; the
daemon then persists for all later sessions.

To update later: `claude plugin update agentwatch` (the next session re-bootstraps
the matching binary).

## Setup (OpenAI Codex)

Codex reuses the same host daemon and binary provisioned by the Claude Code
plugin — the Codex hooks call `agentwatch` on `$PATH`. If you only use Codex,
install the binary manually (see [Advanced / development](#advanced--development))
and start the daemon once; both agents then share it.

Codex support uses the hook config in `plugin/codex/hooks.json`.

If you do not already have a Codex hooks file:

```bash
mkdir -p ~/.codex
cp /path/to/claude-tools/agentwatch/plugin/codex/hooks.json ~/.codex/hooks.json
```

If `~/.codex/hooks.json` already exists, merge the `hooks` entries from `plugin/codex/hooks.json` instead of replacing the file.

Codex will ask you to review/trust new hooks. Use `/hooks` in Codex if the hooks are listed as pending review.

This repository also includes a Codex plugin manifest at `plugin/codex/.codex-plugin/plugin.json`. Codex plugin-bundled hooks currently require this feature flag in `~/.codex/config.toml`:

```toml
[features]
plugin_hooks = true
```

Full self-contained Codex bootstrap is tracked in
[#33](https://github.com/matteobortolazzo/claude-tools/issues/33).

## Advanced / development

The marketplace install above provisions the binary and daemon automatically. You
only need this section to install the binary by hand (e.g. Codex-only setups),
hack on agentwatch, or run against a local plugin directory.

### Install the binary manually

```bash
go install github.com/matteobortolazzo/claude-tools/agentwatch@latest
```

Or build from source:

```bash
git clone https://github.com/matteobortolazzo/claude-tools.git
cd claude-tools/agentwatch
make build
```

### Run against a local plugin directory

`make plugin-bin` builds the current source into `plugin/bin/agentwatch` and stamps
the version marker, so `claude --plugin-dir ./plugin` uses your local build instead
of downloading a released artifact:

```bash
make plugin-bin
claude --plugin-dir /path/to/agentwatch/plugin
```

### Start the daemon manually

When you install the binary by hand, start the daemon once (the marketplace plugin
does this for you):

```bash
agentwatch        # run in background or a dedicated pane
agentwatch -v     # verbose logging
```

A second `agentwatch daemon` is a safe no-op — it detects the running daemon, logs
"daemon already running", and exits without disturbing it.

| Flag | Default | Description |
|------|---------|-------------|
| `-v` | `false` | Verbose logging |
| `-event-socket` | `$XDG_RUNTIME_DIR/agentwatch-events.sock` | Event socket for hook notifications |
| `-socket` | `$XDG_RUNTIME_DIR/agentwatch.sock` | Broadcast socket for waybar clients |
| `-sweep` | `30` | Stale session sweep interval in seconds |
| `-session-ttl` | `2h` | Idle TTL for paneless sessions (Go duration); sessions without a pane are expired after this duration if no `SessionEnd` fires |
| `-style-running` | `fg=blue,dim` | tmux style for running state (inactive windows) |
| `-style-done` | `fg=green,dim` | tmux style for done state (inactive windows) |
| `-style-input` | `fg=red,dim` | tmux style for need-input state (inactive windows) |
| `-style-idle` | `dim` | tmux style for idle state (inactive windows) |
| `-symbol-running` | `▶` | Symbol shown in status bar indicator |
| `-symbol-done` | `✓` | Symbol shown in status bar indicator |
| `-symbol-input` | `!` | Symbol shown in status bar indicator |
| `-symbol-idle` | `~` | Symbol shown in status bar indicator |

### Status / Waybar module

`agentwatch status` connects to the daemon's broadcast socket, reads the current state, prints a single line of JSON in the [Waybar custom module protocol](https://github.com/Alexays/Waybar/wiki/Module:-Custom), and exits. (`agentwatch waybar` is a backwards-compatible alias.)

```bash
agentwatch status
```

| Flag | Default | Description |
|------|---------|-------------|
| `-socket` | `$XDG_RUNTIME_DIR/agentwatch.sock` | Broadcast socket path |
| `-symbol-running` | `▶` | Symbol for running count |
| `-symbol-done` | `✓` | Symbol for done count |
| `-symbol-input` | `!` | Symbol for need-input count |

#### Waybar config

```jsonc
"custom/agentwatch": {
    "exec": "agentwatch status",
    "return-type": "json",
    "interval": 1
}
```

Then add `"custom/agentwatch"` to your bar's modules.

#### Waybar styling

The module sets a `class` based on the highest-priority status: `need-input` > `running` > `done` > `idle`.

```css
#custom-agentwatch {
    padding: 0 8px;
}

#custom-agentwatch.need-input {
    color: #f38ba8;
}

#custom-agentwatch.running {
    color: #89b4fa;
}

#custom-agentwatch.done {
    color: #a6e3a1;
}

#custom-agentwatch.idle {
    color: #6c7086;
}
```

## How it works

### Hook-to-status mapping

#### Claude Code

| Hook Event | Status | Notes |
|------------|--------|-------|
| `SessionStart` | Idle | Fresh session, no task yet |
| `UserPromptSubmit` | Running | User just submitted a prompt |
| `Notification` (permission_prompt) | NeedInput | Permission dialog shown |
| `PreToolUse` (when NeedInput) | Running | Permission was granted |
| `Stop` | Done | Claude finished responding |
| `SessionEnd` | Remove | Restore window, clean up |

#### OpenAI Codex

| Hook Event | Status | Notes |
|------------|--------|-------|
| `SessionStart` | Idle | Fresh session, no task yet |
| `UserPromptSubmit` | Running | User just submitted a prompt |
| `PermissionRequest` | NeedInput | Approval prompt shown |
| `PreToolUse` | Running | Codex is about to run a tool |
| `PostToolUse` | Running | Codex completed a tool call and is still working |
| `Stop` | Done | Codex finished responding |

Codex does not currently document a `SessionEnd` hook. agentwatch restores tracked Codex windows during the stale sweep once the pane returns to a non-Codex command after a completed/idle turn.

### Stale session sweep

The daemon has two sweep mechanisms:

**Pane-based sweep (tmux-backed sessions)**: Every 30s (configurable with `-sweep`), the tmux frontend checks if tracked pane IDs still exist in tmux. If a pane is gone (e.g. an agent crashed without firing a cleanup hook), the window is restored. For Codex, the sweep also restores the window after a completed session exits back to the user's shell.

**Paneless TTL sweep**: Sessions without a tmux pane (plain terminals, dev-sandbox without a pane) are tracked by session id only. They are removed on `SessionEnd`; if no `SessionEnd` fires (e.g. a crash or a Codex session), the daemon expires them after the idle TTL (default `2h`, configurable with `-session-ttl`).

### Paneless sessions

`agentwatch notify` accepts events even when `$TMUX_PANE` is unset. Sessions running in plain terminals or dev-sandbox without a tmux pane appear in `agentwatch status` output with empty `session` and `window_index` fields; their tooltip line reads `name (status)` rather than `sess:idx - name (status)`.

**Caveat**: for paneless sessions the task name comes only from the hook payload's `task_name` field — there is no pane title to read. Paneless Codex sessions may therefore show no task name.

### Custom status-format integration

agentwatch exposes two per-window user variables for custom `status-format` configs:

- `@agentwatch-symbol` — the status symbol (`~`, `▶`, `✓`, `!`)
- `@agentwatch-style` — the status style (e.g. `fg=blue,dim`)

Use them in your `status-format` to replace the default indicator and color:

```
# Replace ● with agentwatch symbol when active, keep ● otherwise
#{?#{@agentwatch-symbol},#{@agentwatch-symbol},●}

# Use agentwatch style when active, fall back to default color
#{?#{@agentwatch-style},#[#{@agentwatch-style}],#[fg=brightblack]}
```

For users with the default tmux status format, agentwatch automatically prepends `#{@agentwatch-symbol}` to `window-status-format` and `window-status-current-format` during tracking, and restores them on cleanup.

### Manual window names

agentwatch respects manually set window names:

- If a window has `automatic-rename` set to `off` (i.e. you renamed it with `Ctrl+b ,`), agentwatch will show status indicators but keep your window name.
- If you rename a window while an agent is running, agentwatch detects the change and stops overriding your name.
- When the agent exits, manually-named windows keep their name (indicators are removed).

### Daemon restart

If the daemon restarts while agent sessions are active, it re-discovers them on the next hook event — a `ListPanes` call maps the `$TMUX_PANE` to the correct window.

## Troubleshooting

**No status updates**: Ensure the hook/plugin is loaded (`claude plugin list`, `claude --plugin-dir ./plugin`, or Codex `/hooks`). Check that `agentwatch notify` can reach the event socket (`agentwatch -v` shows the socket path).

**Binary/daemon didn't bootstrap**: The SessionStart bootstrap fails silently so it
never blocks the agent. Check the bootstrap log at
`${TMPDIR:-/tmp}/agentwatch-bootstrap.log` — it records download, checksum, arch,
and network failures (e.g. no release published yet, or an unsupported OS/arch). If
bootstrap can't run, install the binary manually and start the daemon (see
[Advanced / development](#advanced--development)).

**Names not restoring**: agentwatch restores names on clean exit (Ctrl+C / SIGTERM) and via the stale sweep. If it was killed with SIGKILL, manually rename windows or restart tmux.

**Daemon not running**: `agentwatch notify` fails silently (exit 0) — the agent is never blocked.

### Verbose mode

When running with `-v`, agentwatch logs task names derived from pane titles to stderr. These titles may reflect file paths, command output, or other workspace context. Task names and window names are truncated to 50 characters in log output to limit exposure.

If verbose logs are persisted (e.g. by a process supervisor), direct output to a user-owned file with restricted permissions:

```bash
agentwatch -v 2>~/.local/state/agentwatch.log
```

## License

MIT
