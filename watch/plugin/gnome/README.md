# Cenci — GNOME Shell extension

> A [cenci-watch](../../README.md) status surface for GNOME Shell.

Live counts of Claude Code and Codex agent sessions in the GNOME Shell top bar
(Ubuntu's default desktop). Polls `cenci widget-json` and renders the snapshot —
a read-only frontend over the same Waybar JSON contract as the waybar, noctalia,
dms, and macOS widgets. No daemon or Go changes.

## Requirements

- GNOME Shell **45 or newer** (Ubuntu 23.10+). Older shells are not supported.
- [cenci](https://github.com/matteobortolazzo/cenci/tree/main/watch)
  daemon running on your tmux server.
- The `cenci` binary reachable by the extension — either on the GNOME
  session `PATH`, or set its absolute path in the extension's preferences.

## Install

The [one-command installer](../../README.md#installation) auto-detects GNOME
Shell and wires this widget up for you (on both install and `cenci
update`). To do it directly — from the marketplace checkout or a repo checkout:

```sh
~/.claude/plugins/marketplaces/cenci/watch/plugin/gnome/install.sh
# from a repo checkout, inside watch/: ./plugin/gnome/install.sh
```

It **copies** this widget into `~/.local/share/gnome-shell/extensions/<UUID>` (a
copy, not a symlink, so the generated `gschemas.compiled` never dirties the git
checkout), compiles the schema, and live-reloads the extension via the
disable→enable toggle. It's idempotent — re-run after any `cenci` update.
A brand-new extension dir still needs one Shell reload (X11 <kbd>Alt</kbd>+<kbd>F2</kbd>,
`r`) or relogin (Wayland) the first time, which the script tells you about.

### Install (manual / local dev)

Symlink this directory into the per-user extensions folder under its UUID, then
compile the settings schema:

```sh
UUID="cenci@matteobortolazzo.github.io"
ln -s "$PWD/plugin/gnome" ~/.local/share/gnome-shell/extensions/"$UUID"
glib-compile-schemas ~/.local/share/gnome-shell/extensions/"$UUID"/schemas
```

Reload GNOME Shell so it sees the new extension:

- **X11**: <kbd>Alt</kbd>+<kbd>F2</kbd>, type `r`, <kbd>Enter</kbd>.
- **Wayland**: log out and back in (Shell can't hot-reload on Wayland).

Then enable it:

```sh
gnome-extensions enable "$UUID"
```

(or use the **Extensions** / **Extension Manager** app). Open its settings with
`gnome-extensions prefs "$UUID"`.

## Uninstall

`cenci-installer uninstall` removes this widget as part of removing the whole
attention layer. To do it directly — from the marketplace checkout or a repo
checkout:

```sh
~/.claude/plugins/marketplaces/cenci/watch/plugin/gnome/uninstall.sh
# from a repo checkout, inside watch/: ./plugin/gnome/uninstall.sh
```

It disables and uninstalls the extension via `gnome-extensions`, then removes
the copied `~/.local/share/gnome-shell/extensions/<UUID>` directory. Disabling
takes effect immediately — no Shell reload needed.

## Behavior

- Polls every `poll-interval-ms` (default 2000 ms).
- Hides the indicator when cenci reports `alt: "none"` (no sessions and the
  fleet dispatch loop is disabled/absent), or the daemon is down (non-zero exit).
- Panel shows a status icon + the count string (`▶ 2  ! 1`). The icon color
  reflects the highest-priority status:
  `failed`/`need-input` (red) > `running` (blue) > `done` (green) >
  `stopped` (orange) > `idle` (grey).
- Click the indicator to open a menu listing each session
  (`session:index - name`), with a per-session status icon colored independently.
- When the daemon's fleet dispatch loop is enabled, a compact `⟳` glyph appears
  in the panel text. The indicator no longer hides when there are zero live
  sessions but the loop is enabled (`alt: "dispatch-only"`) — only `alt: "none"`
  hides it.

## Settings

| Key | Default | Notes |
|---|---|---|
| `poll-interval-ms` | `2000` | How often to run `cenci widget-json` (250–60000). |
| `cenci-path` | `cenci` | Path or command name for the binary. Use an **absolute path** if it is not on the GNOME session PATH. |

## Troubleshooting

- **Indicator never appears**: run `cenci widget-json` in a terminal. If it
  prints JSON, the daemon is fine — set the absolute binary path in preferences,
  since the GNOME session PATH under Wayland is often minimal. If it prints
  nothing, no sessions are live (start a Claude Code / Codex tmux pane).
- **Enabled but nothing happens**: check `journalctl --user -f -o cat /usr/bin/gnome-shell`
  for extension errors (e.g. a schema that wasn't compiled).
- **Schema errors**: rerun `glib-compile-schemas .../schemas` after any change to
  the `.gschema.xml`.

## Test

`./test.sh` — a drift check that fails if `cenci widget-json` gains a status
class that `extension.js` doesn't map. It does not launch GNOME Shell.
