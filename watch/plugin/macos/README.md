# Cenci — macOS menu bar (SwiftBar)

> A [cenci-watch](../../README.md) status surface for SwiftBar.

Live counts of Claude Code and Codex sessions in your macOS menu bar. Polls
`cenci widget-json` and renders the snapshot as a [SwiftBar](https://swiftbar.app)
plugin — the same Waybar JSON contract the noctalia and dms widgets consume, so
there are no daemon or Go changes.

## Requirements

- [SwiftBar](https://swiftbar.app) — `brew install swiftbar`
- [cenci](https://github.com/matteobortolazzo/cenci/tree/main/watch) daemon running
- The `cenci` binary reachable from SwiftBar (see the PATH note below)

### The GUI-PATH gotcha

SwiftBar is a GUI app and runs plugins with a minimal `PATH` that does **not**
include `/opt/homebrew/bin`, `/usr/local/bin`, or the plugin `bin/` dir. The plugin
resolves the binary in this order:

1. `$CENCI_BIN` (env var, or a SwiftBar variable — set this if the item never appears)
2. `/opt/homebrew/bin/cenci`, `/usr/local/bin/cenci`
3. `~/.claude/plugins/cache/*/cenci-watch/*/bin/cenci` (bootstrap install)
4. bare `cenci` on `PATH`

If none resolve, the plugin emits nothing (the menu bar item stays hidden).

## Install

First get the daemon + binary, then wire up SwiftBar.

### 1. cenci daemon + binary

If you installed the [cenci attention layer](../../README.md#installation), the
macOS binary and daemon auto-bootstrap on your first session — nothing else to do:

```sh
claude plugin marketplace add matteobortolazzo/cenci
claude plugin install cenci-watch   # or: claude plugin update cenci-watch
```

Confirm it works (prints Waybar JSON when a session is live, nothing when idle):

```sh
cenci widget-json
```

Codex-only or hacking on the source? Install the binary by hand and start the daemon
per the [main README](../../README.md#advanced--development).

### 2. SwiftBar

```sh
brew install swiftbar
open -a SwiftBar   # first launch, so its app bundle + defaults domain exist
```

### 3. Wire up the plugin

```sh
~/.claude/plugins/marketplaces/cenci/watch/plugin/macos/install.sh
```

(From a **repo checkout**, run `./plugin/macos/install.sh` inside `watch/` instead.)

This sets SwiftBar's **Plugin Folder** (a plain `defaults` key — `PluginDirectory`
under `com.ameba.SwiftBar` — no need to click through SwiftBar's own folder picker)
to `~/SwiftBarPlugins`, or wherever `PluginDirectory` already points, or
`$SWIFTBAR_PLUGIN_DIR` / `install.sh <dir>` if you want somewhere else — symlinks
`cenci.5s.sh` in, and restarts SwiftBar so it takes effect immediately.

Re-run it any time (e.g. after a `cenci` update) — it's idempotent, and never
overwrites a Plugin Folder you've already customized.

The `.5s.` in the filename is the refresh interval — rename the segment
(`.2s.`, `.10s.`) to change the cadence.

<details>
<summary>Doing it manually instead</summary>

On first launch SwiftBar asks for a **Plugin Folder**. It can be *any* directory —
SwiftBar's default suggestion lives under `~/Library`, which the macOS open panel
hides and won't let you navigate to normally. Two easy ways around it:

- Pick (or make) an ordinary visible folder, e.g. `mkdir -p ~/SwiftBarPlugins` and
  choose that.
- Or, in the folder picker, press `⌘⇧G` and paste a path (e.g.
  `~/Library/Application Support/SwiftBar/Plugins`).

You can see/change it later in SwiftBar → **Preferences** → *Plugin Folder*, or via
`defaults write com.ameba.SwiftBar PluginDirectory -string "/path"`.

Then symlink the script into whatever Plugin Folder you chose, and **Refresh All**
(or restart SwiftBar):

```sh
PLUGIN_DIR="$HOME/SwiftBarPlugins"   # the folder you picked above
ln -sf "$HOME"/.claude/plugins/marketplaces/*/watch/plugin/macos/cenci.5s.sh \
  "$PLUGIN_DIR/cenci.5s.sh"
```

</details>

### Menu bar icon not showing at all?

SwiftBar (Cocoa's `NSStatusItem`) remembers per-item visibility across launches. If
the icon was ever dragged off the menu bar, it stays hidden on relaunch even with a
live session. Check for a key like `NSStatusItem Visible<something> = 0`:

```sh
defaults read com.ameba.SwiftBar | grep 'NSStatusItem Visible'
defaults write com.ameba.SwiftBar "<the key from above>" -bool true
killall SwiftBar; open -a SwiftBar
```

## Uninstall

`cenci-installer uninstall` removes this widget as part of removing the whole
attention layer. To do it directly:

```sh
~/.claude/plugins/marketplaces/cenci/watch/plugin/macos/uninstall.sh
```

(From a **repo checkout**, run `./plugin/macos/uninstall.sh` inside `watch/`
instead.)

It removes the `cenci.5s.sh` symlink from the resolved Plugin Folder (if it's
still the one this plugin created) and restarts SwiftBar so the change takes
effect immediately. The `PluginDirectory` default itself is left untouched.

## Behavior

- Polls on SwiftBar's filename interval (`cenci widget-json` is a cheap socket read).
- **Hides completely** when `alt=none` (no live sessions and the fleet dispatch loop
  is disabled/absent) or the daemon is down — matching the noctalia/dms widgets. When
  there are zero live sessions but the fleet dispatch loop is enabled, `class` stays
  `none` but `alt` becomes `dispatch-only`, so the item stays visible.
- Menu bar line shows the counts (`text`), tinted by the highest-priority status.
- The dropdown lists one row per session (`session:index - name`, or `name` for
  paneless sessions), each colored by *its own* status — so a `need-input` row is
  red with an alert symbol even when other sessions are just running.
- Icon + color map (colors mirror `colorForClass` in the QML widgets; icons use SF
  Symbols, so they diverge from the QML widgets' icon sets where SF Symbols has no
  equivalent glyph — e.g. no literal robot icon, hence `brain.head.profile.fill` for
  `running` instead of noctalia's `robot` / dms's `smart_toy`):

  | class | SF Symbol | color |
  |---|---|---|
  | `need-input` | `exclamationmark.triangle.fill` | red |
  | `running` | `brain.head.profile.fill` | blue |
  | `done` | `checkmark.circle.fill` | green |
  | `stopped` | `pause.circle.fill` | orange |
  | `idle` | (no icon) | — |

  `need-input` is the loudest treatment — that's the whole point of the surface.
- When per-agent-type budget headroom is available, the dropdown also gets one row
  per agent (e.g. `claude 73%`), read from the numeric `headroom` field (not by
  re-parsing `text`/`tooltip`) and sorted by agent name for a stable order. Each row
  is colored by its own threshold band, independent of session status colors:

  | headroom | color |
  |---|---|
  | `>25%` | green |
  | `10–25%` (inclusive) | orange |
  | `<10%` | red |

  When no headroom data is available (budget loop disabled/unconfigured), no
  headroom rows are rendered — same "nothing shown" fallback as waybar.
- When the daemon's fleet dispatch loop is enabled, the menu bar text gets a
  compact `⟳` glyph and the dropdown gets a dedicated summary row, e.g.
  `dispatch: on (5m) — last run 12:04, 2 dispatched / 3 skipped`, or
  `dispatch: on (5m) — last run failed: <err>` after a failed pass. No SF
  Symbol/color is applied to this row. It is read from the structured
  `dispatch` field (not by re-parsing `tooltip`) and is omitted entirely when
  the loop is disabled/absent.

## Settings

| Knob | Default | Notes |
|---|---|---|
| Filename interval (`.5s.`) | `5s` | Polling cadence; rename the segment |
| `CENCI_BIN` | (auto-resolved) | Env var / SwiftBar variable overriding the binary path |

## Troubleshooting

- **Item never appears**: confirm `cenci widget-json` prints JSON in a shell. If it
  prints nothing (`"alt":"none"`), there are no live sessions and the fleet dispatch
  loop is off — start a Claude Code or Codex session and try again. If it works in a
  shell but not in SwiftBar, it's the GUI-PATH gotcha: set `CENCI_BIN` to the
  absolute path (`command -v cenci`).
- **Daemon not running**: check `pgrep -af cenci`. The daemon is started by the
  plugin bootstrap / your tmux config.
- **Verify the plugin directly**:
  ```sh
  CENCI_BIN="$(command -v cenci)" ./plugin/macos/cenci.5s.sh
  ```
- **Run the smoke test**: `./plugin/macos/test.sh` (or `make test-macos`).
