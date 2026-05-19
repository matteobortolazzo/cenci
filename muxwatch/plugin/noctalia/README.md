# MuxWatch — noctalia-shell bar widget

Live counts of Claude Code and Codex tmux sessions in your noctalia bar. Polls `muxwatch waybar` and renders the snapshot.

## Requirements

- [noctalia-shell](https://noctalia.dev/) ≥ 4.4.1
- [muxwatch](https://github.com/matteobortolazzo/claude-tools/tree/main/muxwatch) daemon running on your tmux server
- `muxwatch` binary on `$PATH` (or set `muxwatchPath` in plugin settings)

## Install (local dev)

Symlink this directory into noctalia's plugin folder:

```sh
ln -s "$PWD/plugin/noctalia" ~/.config/noctalia/plugins/muxwatch
```

Restart the shell:

```sh
pkill -f noctalia-shell && qs -c noctalia-shell &
```

Then open Settings (SUPER+R) → Bar, and add the **MuxWatch** widget to a section.

## Behavior

- Polls every `pollIntervalMs` (default 2000ms).
- Hides when muxwatch reports no sessions (or daemon is down).
- Icon and color reflect the highest-priority status: `need-input` (red) > `running` (primary) > `done` > `stopped` > `idle`.
- Hover tooltip lists each window: `session:index - name (status)`.
- Right-click → widget settings.

## Settings

| Key | Default | Notes |
|---|---|---|
| `pollIntervalMs` | `2000` | How often to call `muxwatch waybar` |
| `muxwatchPath` | `muxwatch` | Path or command name for the muxwatch binary |
