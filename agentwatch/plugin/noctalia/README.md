# AgentWatch — noctalia-shell bar widget

Live counts of Claude Code and Codex tmux sessions in your noctalia bar. Polls `agentwatch waybar` and renders the snapshot.

## Requirements

- [noctalia-shell](https://noctalia.dev/) ≥ 4.4.1
- [agentwatch](https://github.com/matteobortolazzo/agent-stack/tree/main/agentwatch) daemon running on your tmux server
- `agentwatch` binary on `$PATH` (or set `agentwatchPath` in plugin settings)

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
- Right-click → widget settings.

## Settings

| Key | Default | Notes |
|---|---|---|
| `pollIntervalMs` | `2000` | How often to call `agentwatch waybar` |
| `agentwatchPath` | `agentwatch` | Path or command name for the agentwatch binary |
