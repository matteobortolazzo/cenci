#!/usr/bin/env bash
#
# Wires SwiftBar up to the cenci-watch plugin without touching SwiftBar's GUI:
# sets its Plugin Folder (a plain UserDefaults key) and symlinks the plugin
# script in, then reloads SwiftBar so the change takes effect immediately.
#
# Usage: ./plugin/macos/install.sh [plugin-dir]
#   plugin-dir defaults to $SWIFTBAR_PLUGIN_DIR, then SwiftBar's existing
#   PluginDirectory default (if already set), then ~/SwiftBarPlugins.

set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "install.sh is macOS-only (SwiftBar)." >&2
  exit 1
fi

if [ ! -d /Applications/SwiftBar.app ]; then
  echo "SwiftBar not found in /Applications. Install it first:" >&2
  echo "  brew install swiftbar" >&2
  exit 1
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$DIR/agentwatch.5s.sh"

EXISTING_DIR="$(defaults read com.ameba.SwiftBar PluginDirectory 2>/dev/null || true)"
PLUGIN_DIR="${1:-${SWIFTBAR_PLUGIN_DIR:-${EXISTING_DIR:-$HOME/SwiftBarPlugins}}}"

mkdir -p "$PLUGIN_DIR"
chmod +x "$SCRIPT"
ln -sf "$SCRIPT" "$PLUGIN_DIR/agentwatch.5s.sh"

CHANGED_DEFAULT=0
if [ "$EXISTING_DIR" != "$PLUGIN_DIR" ]; then
  defaults write com.ameba.SwiftBar PluginDirectory -string "$PLUGIN_DIR"
  CHANGED_DEFAULT=1
fi

echo "Plugin Folder: $PLUGIN_DIR"
echo "Symlinked:     $PLUGIN_DIR/agentwatch.5s.sh -> $SCRIPT"

# Reload so the (possibly new) Plugin Folder and symlink take effect now,
# instead of waiting for the next SwiftBar launch. `open` right after `killall`
# can race Launch Services (it hasn't noticed the app quit yet) and silently
# fail to relaunch, so wait for the process to actually be gone first.
if pgrep -x SwiftBar >/dev/null 2>&1; then
  killall SwiftBar
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    pgrep -x SwiftBar >/dev/null 2>&1 || break
    sleep 0.2
  done
fi
open -a SwiftBar

if [ "$CHANGED_DEFAULT" -eq 1 ]; then
  echo "Set SwiftBar's Plugin Folder (previously $([ -n "$EXISTING_DIR" ] && echo "$EXISTING_DIR" || echo "unset"))."
fi
echo "Done. AgentWatch should appear in the menu bar once a session is live."
