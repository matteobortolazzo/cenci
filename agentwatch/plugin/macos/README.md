# AgentWatch — macOS menu bar (SwiftBar)

Live counts of Claude Code and Codex sessions in your macOS menu bar. Polls
`agentwatch status` and renders the snapshot as a [SwiftBar](https://swiftbar.app)
plugin — the same Waybar JSON contract the noctalia and dms widgets consume, so
there are no daemon or Go changes.

## Requirements

- [SwiftBar](https://swiftbar.app) — `brew install swiftbar`
- [agentwatch](https://github.com/matteobortolazzo/agent-stack/tree/main/agentwatch) daemon running
- The `agentwatch` binary reachable from SwiftBar (see the PATH note below)

### The GUI-PATH gotcha

SwiftBar is a GUI app and runs plugins with a minimal `PATH` that does **not**
include `/opt/homebrew/bin`, `/usr/local/bin`, or the plugin `bin/` dir. The plugin
resolves the binary in this order:

1. `$AGENTWATCH_BIN` (env var, or a SwiftBar variable — set this if the item never appears)
2. `/opt/homebrew/bin/agentwatch`, `/usr/local/bin/agentwatch`
3. `~/.claude/plugins/cache/*/agentwatch/*/bin/agentwatch` (bootstrap install)
4. bare `agentwatch` on `PATH`

If none resolve, the plugin emits nothing (the menu bar item stays hidden).

## Install

First get the daemon + binary, then wire up SwiftBar.

### 1. agentwatch daemon + binary

If you installed the [Claude Code plugin](../../README.md#install-claude-code), the
macOS binary and daemon auto-bootstrap on your first session — nothing else to do:

```sh
claude plugin marketplace add matteobortolazzo/agent-stack
claude plugin install agentwatch   # or: claude plugin update agentwatch
```

Confirm it works (prints Waybar JSON when a session is live, nothing when idle):

```sh
agentwatch status
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
~/.claude/plugins/marketplaces/*/agentwatch/plugin/macos/install.sh
```

(From a **repo checkout**, run `./plugin/macos/install.sh` inside `agentwatch/` instead.)

This sets SwiftBar's **Plugin Folder** (a plain `defaults` key — `PluginDirectory`
under `com.ameba.SwiftBar` — no need to click through SwiftBar's own folder picker)
to `~/SwiftBarPlugins`, or wherever `PluginDirectory` already points, or
`$SWIFTBAR_PLUGIN_DIR` / `install.sh <dir>` if you want somewhere else — symlinks
`agentwatch.5s.sh` in, and restarts SwiftBar so it takes effect immediately.

Re-run it any time (e.g. after an `agentwatch` update) — it's idempotent, and never
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
ln -sf "$HOME"/.claude/plugins/marketplaces/*/agentwatch/plugin/macos/agentwatch.5s.sh \
  "$PLUGIN_DIR/agentwatch.5s.sh"
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

## Behavior

- Polls on SwiftBar's filename interval (`agentwatch status` is a cheap socket read).
- **Hides completely** when there are no live sessions (`class=none`) or the daemon
  is down — matching the noctalia/dms widgets.
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

## Settings

| Knob | Default | Notes |
|---|---|---|
| Filename interval (`.5s.`) | `5s` | Polling cadence; rename the segment |
| `AGENTWATCH_BIN` | (auto-resolved) | Env var / SwiftBar variable overriding the binary path |

## Troubleshooting

- **Item never appears**: confirm `agentwatch status` prints JSON in a shell. If it
  prints nothing / `"class":"none"`, there are no live sessions — start a Claude Code
  or Codex session and try again. If it works in a shell but not in SwiftBar, it's the
  GUI-PATH gotcha: set `AGENTWATCH_BIN` to the absolute path (`command -v agentwatch`).
- **Daemon not running**: check `pgrep -af agentwatch`. The daemon is started by the
  plugin bootstrap / your tmux config.
- **Verify the plugin directly**:
  ```sh
  AGENTWATCH_BIN="$(command -v agentwatch)" ./plugin/macos/agentwatch.5s.sh
  ```
- **Run the smoke test**: `./plugin/macos/test.sh` (or `make test-macos`).
