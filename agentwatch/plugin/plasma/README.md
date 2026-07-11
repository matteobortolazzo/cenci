# AgentWatch — KDE Plasma widget

Live counts of Claude Code and Codex agent sessions in the KDE Plasma panel
(Kubuntu's default desktop). Polls `agentwatch status` and renders the snapshot —
a read-only frontend over the same Waybar JSON contract as the waybar, noctalia,
dms, and macOS widgets. No daemon or Go changes.

## Requirements

- **KDE Plasma 6** (Qt 6). Plasma 5 is not supported.
- [agentwatch](https://github.com/matteobortolazzo/agent-stack/tree/main/agentwatch)
  daemon running on your tmux server.
- The `agentwatch` binary reachable by the widget — either on the Plasma session
  `PATH`, or set its absolute path in the widget's settings.

## Install (local dev)

Install the package with `kpackagetool6`:

```sh
kpackagetool6 --type Plasma/Applet --install plugin/plasma
# update in place after edits:
kpackagetool6 --type Plasma/Applet --upgrade plugin/plasma
```

Then right-click the panel → **Add Widgets…** → search **AgentWatch** → add it.
Right-click the widget → **Configure AgentWatch…** to set the binary path.

For a quick edit loop without reinstalling, symlink into the local plasmoid dir
and restart Plasma:

```sh
ln -s "$PWD/plugin/plasma" \
  ~/.local/share/plasma/plasmoids/com.github.matteobortolazzo.agentwatch
kquitapp6 plasmashell && kstart plasmashell
```

## Behavior

- Polls every `pollIntervalMs` (default 2000 ms).
- Hides from the panel when agentwatch reports no sessions (`class: none`) or the
  daemon is down (non-zero exit).
- Compact panel view shows a status icon + the count string (`▶ 2  ! 1`). The
  icon color reflects the highest-priority status:
  `failed`/`need-input` (negative) > `running` (highlight) > `done` (positive) >
  `stopped` (neutral) > `idle` (disabled).
- Click to expand a list of each session (`session:index - name`) with a
  per-session status icon and badge, colored independently.

## Settings

| Key | Default | Notes |
|---|---|---|
| `pollIntervalMs` | `2000` | How often to run `agentwatch status` (250–60000). |
| `agentwatchPath` | `agentwatch` | Path or command name for the binary. Use an **absolute path** if it is not on the Plasma session PATH. |

## Troubleshooting

- **Widget never appears / stays hidden**: run `agentwatch status` in a terminal.
  If it prints JSON, set the absolute binary path in the widget settings (the
  Plasma session PATH may not include it). If it prints nothing, no sessions are
  live — start a Claude Code / Codex tmux pane.
- **Errors**: run `plasmashell --replace` from a terminal and watch its output,
  or check `journalctl --user -f`.

## Test

`./test.sh` — a drift check that fails if `agentwatch status` gains a status
class that `main.qml` doesn't map. It does not launch Plasma.
