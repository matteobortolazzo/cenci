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
```

On first launch SwiftBar asks for a **Plugin Folder**. It can be *any* directory —
SwiftBar's default suggestion lives under `~/Library`, which the macOS open panel
hides and won't let you navigate to normally. Two easy ways around it:

- Pick (or make) an ordinary visible folder, e.g. `mkdir -p ~/SwiftBarPlugins` and
  choose that — **recommended**, and what the commands below assume.
- Or, in the folder picker, press `⌘⇧G` and paste a path (e.g.
  `~/Library/Application Support/SwiftBar/Plugins`).

You can see/change it later in SwiftBar → **Preferences** → *Plugin Folder*.

### 3. Symlink the plugin

Symlink the script into whatever Plugin Folder you chose, then **Refresh All** (or
restart SwiftBar):

```sh
PLUGIN_DIR="$HOME/SwiftBarPlugins"   # the folder you picked in step 2
```

From a **marketplace install** — point at the marketplace checkout, which is a
stable path (unlike the versioned `cache/…/agentwatch/<version>/` copy, it survives
plugin updates without re-linking); the `*` resolves the marketplace name:

```sh
ln -sf "$HOME"/.claude/plugins/marketplaces/*/agentwatch/plugin/macos/agentwatch.5s.sh \
  "$PLUGIN_DIR/agentwatch.5s.sh"
```

From a **repo checkout** (run inside `agentwatch/`):

```sh
chmod +x plugin/macos/agentwatch.5s.sh
ln -sf "$PWD/plugin/macos/agentwatch.5s.sh" "$PLUGIN_DIR/agentwatch.5s.sh"
```

The `.5s.` in the filename is the refresh interval — rename the segment
(`.2s.`, `.10s.`) to change the cadence.

## Behavior

- Polls on SwiftBar's filename interval (`agentwatch status` is a cheap socket read).
- **Hides completely** when there are no live sessions (`class=none`) or the daemon
  is down — matching the noctalia/dms widgets.
- Menu bar line shows the counts (`text`), tinted by the highest-priority status.
- The dropdown lists one row per session (`session:index - name`, or `name` for
  paneless sessions), each colored by *its own* status — so a `need-input` row is
  red with an alert symbol even when other sessions are just running.
- Icon + color map (mirrors `iconForClass`/`colorForClass` in the QML widgets):

  | class | SF Symbol | color |
  |---|---|---|
  | `need-input` | `exclamationmark.triangle.fill` | red |
  | `running` | `gearshape.fill` | blue |
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
