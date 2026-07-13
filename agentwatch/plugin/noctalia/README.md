# AgentWatch — noctalia-shell bar widget

Live counts of Claude Code and Codex tmux sessions in your noctalia bar. Polls `agentwatch waybar` and renders the snapshot.

## Requirements

- [noctalia-shell](https://noctalia.dev/) ≥ 4.4.1
- [agentwatch](https://github.com/matteobortolazzo/agent-stack/tree/main/agentwatch) daemon running on your tmux server
- `agentwatch` binary on `$PATH`. The plugin bootstrap auto-links it onto your
  writable PATH (`~/.local/bin`) on every session, and `install.sh` sets up
  visibility for GUI bars by offering a one-time `/usr/local/bin` link (see
  Troubleshooting). If your bar still can't find it, set `agentwatchPath` to the
  binary's full path in plugin settings.

## Install (local dev)

Symlink this directory into noctalia's plugin folder:

```sh
ln -s "$PWD/plugin/noctalia" ~/.config/noctalia/plugins/agentwatch
```

Restart the shell:

```sh
pkill -f noctalia-shell && qs -c noctalia-shell &
```

Then open Settings (SUPER+R) → Bar, and add the **AgentWatch** widget to a section.

## Behavior

- Polls every `pollIntervalMs` (default 2000ms).
- Hides when agentwatch reports no sessions (or daemon is down).
- Icon and color reflect the highest-priority status: `need-input` (red) > `running` (primary) > `done` > `stopped` > `idle`.
- Hover tooltip lists each window: `session:index - name (status)`.
- Per-agent budget headroom (when reported) renders as a small percent badge next to the status text, colored by threshold: >25% normal (primary), 10-25% warning (tertiary), <10% critical (error). No badge is shown when headroom data is absent.
- Right-click → widget settings.

## Settings

| Key | Default | Notes |
|---|---|---|
| `pollIntervalMs` | `2000` | How often to call `agentwatch waybar` |
| `agentwatchPath` | `agentwatch` | Path or command name for the agentwatch binary |

## Troubleshooting

- **Widget never appears**: first confirm the bar can *find* the binary. GUI/compositor bars inherit the **login** PATH, which typically lacks `~/.local/bin` — so a bare `agentwatch` the daemon set up for your shell may be invisible to noctalia. Reproduce the bar's environment with a minimal PATH:
  ```sh
  env -i HOME=$HOME XDG_RUNTIME_DIR=/run/user/$(id -u) PATH=/usr/local/bin:/usr/bin sh -c 'agentwatch waybar'
  ```
  If that says "command not found", link the binary onto the login PATH (re-run `install.sh` and accept the GUI-bar prompt, or `sudo ln -sf "$HOME/.local/bin/agentwatch" /usr/local/bin/agentwatch`) or set `agentwatchPath` to its full path. If it prints `"class": "none"` there are no live sessions — start a Claude Code or Codex tmux pane and try again.
