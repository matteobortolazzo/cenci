#!/usr/bin/env bash
#
# Reverses the Cenci SwiftBar wiring: removes the symlinked plugin script
# (only if it's still the cenci-owned symlink) from the resolved Plugin
# Folder and reloads SwiftBar so the change takes effect immediately.
#
# SwiftBar's `PluginDirectory` UserDefault is deliberately left untouched —
# other plugins may depend on it — only the cenci.5s.sh symlink is removed.
#
# Usage: ./plugin/macos/uninstall.sh [plugin-dir]
#   plugin-dir defaults to $SWIFTBAR_PLUGIN_DIR, then SwiftBar's existing
#   PluginDirectory default (if already set), then ~/SwiftBarPlugins.

set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "SwiftBar is macOS-only — skipping." >&2
  exit 0
fi

if [ ! -d /Applications/SwiftBar.app ]; then
  echo "SwiftBar not found in /Applications — nothing to uninstall." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$DIR/cenci.5s.sh"

EXISTING_DIR="$(defaults read com.ameba.SwiftBar PluginDirectory 2>/dev/null || true)"
PLUGIN_DIR="${1:-${SWIFTBAR_PLUGIN_DIR:-${EXISTING_DIR:-$HOME/SwiftBarPlugins}}}"
DEST="$PLUGIN_DIR/cenci.5s.sh"

# NOT_OURS tracks the "DEST exists but isn't cenci-owned" case so the caller
# (install.sh) can tell it apart from a real removal or a true no-op via a
# distinct exit code (2), instead of collapsing all three into one banner.
NOT_OURS=0
if [ -L "$DEST" ] && [ "$(readlink "$DEST")" = "$SCRIPT" ]; then
  rm -f "$DEST"
  echo "Removed symlink: $DEST"
elif [ -e "$DEST" ]; then
  echo "$DEST exists and is not a cenci-owned symlink — left untouched." >&2
  NOT_OURS=1
else
  echo "No cenci widget symlink found at $DEST — nothing to remove."
fi

# Reload so the removal takes effect now instead of waiting for the next
# SwiftBar launch. Same wait-for-gone idiom as install.sh (avoids a race
# between `open` and Launch Services noticing the app quit).
if pgrep -x SwiftBar >/dev/null 2>&1; then
  killall SwiftBar
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    pgrep -x SwiftBar >/dev/null 2>&1 || break
    sleep 0.2
  done
fi
open -a SwiftBar

echo "Done."

if [ "$NOT_OURS" -eq 1 ]; then
  exit 2
fi
