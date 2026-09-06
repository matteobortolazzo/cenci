# Cenci — noctalia-shell bar widget

> A [cenci-watch](../../README.md) status surface for noctalia-shell.

Live counts of Claude Code and Codex tmux sessions in your noctalia bar. Polls `cenci waybar` and renders the snapshot.

## Requirements

- [noctalia-shell](https://noctalia.dev/) ≥ 4.4.1
- [cenci](https://github.com/matteobortolazzo/cenci/tree/main/watch) daemon running on your tmux server
- `cenci` binary on `$PATH`. The plugin bootstrap auto-links it onto your
  writable PATH (`~/.local/bin`) on every session, and `install.sh` sets up
  visibility for GUI bars by offering a one-time `/usr/local/bin` link (see
  Troubleshooting). If your bar still can't find it, set `cenciPath` to the
  binary's full path in plugin settings.

## Install

The [one-command installer](../../README.md#installation) auto-detects noctalia
and wires this widget up for you (on both install and `cenci update`). To
do it directly — from the marketplace checkout or a repo checkout:

```sh
~/.claude/plugins/marketplaces/cenci/watch/plugin/noctalia/install.sh
# from a repo checkout, inside watch/: ./plugin/noctalia/install.sh
```

It symlinks this plugin into `~/.config/noctalia/plugins/cenci` and restarts
noctalia-shell so it picks the plugin up. It's idempotent — re-run after any
`cenci` update. You still add the widget to a bar section once (below).

### Install (manual / local dev)

Symlink this directory into noctalia's plugin folder:

```sh
ln -s "$PWD/plugin/noctalia" ~/.config/noctalia/plugins/cenci
```

Restart the shell:

```sh
pkill -f noctalia-shell && qs -c noctalia-shell &
```

Then open Settings (SUPER+R) → Bar, and add the **Cenci** widget to a section.

## Uninstall

`cenci-installer uninstall` removes this widget as part of removing the whole
attention layer. To do it directly — from the marketplace checkout or a repo
checkout:

```sh
~/.claude/plugins/marketplaces/cenci/watch/plugin/noctalia/uninstall.sh
# from a repo checkout, inside watch/: ./plugin/noctalia/uninstall.sh
```

It removes the `~/.config/noctalia/plugins/cenci` symlink (if it's still the
one this plugin created) and restarts noctalia-shell so the change takes
effect immediately.

## Behavior

- Polls every `pollIntervalMs` (default 2000ms).
- Hides when cenci reports no sessions (or daemon is down).
- Icon and color reflect the highest-priority status: `need-input` (red) > `escalated` (secondary) > `running` (primary) > `done` > `stopped` > `idle`.
- Hover tooltip lists each window: `session:index - name (status)`.
- Per-agent budget headroom (when reported) renders as a small percent badge next to the status text, colored by threshold: >25% normal (primary), 10-25% warning (tertiary), <10% critical (error). No badge is shown when headroom data is absent.
- When the daemon's fleet dispatch loop is enabled, a compact `⟳` glyph and a `dispatch: on (...)` tooltip line appear. The widget no longer hides when there are zero live sessions but the loop is enabled (`alt: "dispatch-only"`) — only a true `alt: "none"` (no sessions and dispatch disabled/absent) hides it.
- Right-click → widget settings.

## Settings

| Key | Default | Notes |
|---|---|---|
| `pollIntervalMs` | `2000` | How often to call `cenci waybar` |
| `cenciPath` | `cenci` | Path or command name for the cenci binary |

## Troubleshooting

- **Widget never appears**: first confirm the bar can *find* the binary. GUI/compositor bars inherit the **login** PATH, which typically lacks `~/.local/bin` — so a bare `cenci` the daemon set up for your shell may be invisible to noctalia. Reproduce the bar's environment with a minimal PATH:
  ```sh
  env -i HOME=$HOME XDG_STATE_HOME=$XDG_STATE_HOME CENCI_SOCKET_DIR=$CENCI_SOCKET_DIR PATH=/usr/local/bin:/usr/bin sh -c 'cenci waybar'
  ```
  Forward `XDG_STATE_HOME`/`CENCI_SOCKET_DIR` (not `XDG_RUNTIME_DIR`, which is
  inert for socket resolution — see `docs/cli-conventions.md`'s Sockets row) so
  this reproduction resolves the SAME socket dir as your actual daemon.
  If that says "command not found", link the binary onto the login PATH (re-run `install.sh` and accept the GUI-bar prompt, or `sudo ln -sf "$HOME/.local/bin/cenci" /usr/local/bin/cenci`) or set `cenciPath` to its full path. If it prints `"alt": "none"` there are no live sessions and the fleet dispatch loop is off — start a Claude Code or Codex tmux pane and try again (`"alt": "dispatch-only"` means the widget stays visible even with no sessions, because the dispatch loop is enabled).
