# AgentWatch — DankMaterialShell bar widget

Live counts of Claude Code and Codex tmux sessions in your DankMaterialShell (DMS) bar. Polls `agentwatch waybar` and renders the snapshot.

## Requirements

- [DankMaterialShell](https://danklinux.com/docs/dankmaterialshell/) (recent build with the plugin system)
- [agentwatch](https://github.com/matteobortolazzo/agent-stack/tree/main/agentwatch) daemon running on your tmux server
- `agentwatch` binary on `$PATH`. The plugin bootstrap auto-links it onto your
  writable PATH (`~/.local/bin`) on every session, and `install.sh` sets up
  visibility for GUI bars by offering a one-time `/usr/local/bin` link (see
  below). If your bar still can't find it, set `agentwatchPath` to the binary's
  full path in plugin settings.

## Install (local dev)

Symlink this directory into DMS's plugin folder:

```sh
ln -s "$PWD/plugin/dms" ~/.config/DankMaterialShell/plugins/agentwatch
```

Restart DMS so it picks the new plugin up:

```sh
systemctl --user restart dms
# or: pkill -f 'qs -c dms'  (niri.service Wants=dms will re-spawn it)
```

Then open Settings (`dms ipc call settings toggle`) → **Plugins** → enable **AgentWatch** → **DankBar** → add the widget to a section.

## Behavior

- Polls every `pollIntervalMs` (default 2000 ms).
- Hides when agentwatch reports no sessions (or the daemon is down).
- Icon + color reflect the highest-priority status:
  `need-input` (error) > `running` (primary) > `done` (tertiary) > `stopped` (secondary) > `idle` (muted).
- Click the pill to open a popout listing each window: `session:index - name` with the per-session status badge.
- Right-click is unused (open plugin settings via Settings → Plugins → AgentWatch).

## Settings

| Key | Default | Notes |
|---|---|---|
| `pollIntervalMs` | `2000` | How often to call `agentwatch waybar` |
| `agentwatchPath` | `agentwatch` | Path or command name for the agentwatch binary |

## Troubleshooting

- **Pill never appears**: first confirm the bar can *find* the binary. GUI/compositor bars inherit the **login** PATH, which typically lacks `~/.local/bin` — so a bare `agentwatch` the daemon set up for your shell may be invisible to DMS. Reproduce the bar's environment with a minimal PATH:
  ```sh
  env -i HOME=$HOME XDG_RUNTIME_DIR=/run/user/$(id -u) PATH=/usr/local/bin:/usr/bin sh -c 'agentwatch waybar'
  ```
  If that says "command not found", link the binary onto the login PATH (re-run `install.sh` and accept the GUI-bar prompt, or `sudo ln -sf "$HOME/.local/bin/agentwatch" /usr/local/bin/agentwatch`) or set `agentwatchPath` to its full path. If it prints `"class": "none"` there are no live sessions — start a Claude Code or Codex tmux pane and try again.
- **Pill is stuck**: check the agentwatch daemon (`pgrep -a agentwatch`); the daemon is started by tmux (`run-shell -b "agentwatch"` in `~/.config/tmux/tmux.conf`).
- **Logs**: `journalctl --user -u dms -f` or wherever your DMS unit writes.
