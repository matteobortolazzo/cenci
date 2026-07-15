#!/usr/bin/env bash
#
# Installs the AgentWatch noctalia-shell bar widget without touching the GUI:
# symlinks this plugin into noctalia's plugin folder and restarts the shell so
# the change takes effect immediately.
#
# The symlink-to-checkout keeps the install stable across `cenci update`.
# You still add the widget to a bar section once via noctalia Settings → Bar.
#
# Usage: ./plugin/noctalia/install.sh

set -euo pipefail

if ! command -v noctalia-shell >/dev/null 2>&1 && [ ! -d "$HOME/.config/noctalia" ]; then
  echo "noctalia-shell not detected (no noctalia-shell, no ~/.config/noctalia) — skipping." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.config/noctalia/plugins/agentwatch"

mkdir -p "$(dirname "$DEST")"
if [ -e "$DEST" ] && [ ! -L "$DEST" ]; then
  echo "$DEST exists and is not a symlink — left untouched; remove it manually and re-run." >&2
  exit 1
fi
ln -sfn "$DIR" "$DEST"
echo "Symlinked: $DEST -> $DIR"

# Reload so noctalia re-scans its plugins. noctalia has no service unit, so kill
# the running shell and respawn it detached (nohup + disown) so it outlives this
# script.
if pkill -f noctalia-shell >/dev/null 2>&1; then
  if command -v qs >/dev/null 2>&1; then
    nohup qs -c noctalia-shell >/dev/null 2>&1 &
    disown 2>/dev/null || true
    echo "Restarted noctalia-shell."
  else
    echo "Killed noctalia-shell — restart it yourself (qs not found): qs -c noctalia-shell &"
  fi
else
  echo "noctalia-shell was not running — start it to load the widget: qs -c noctalia-shell &"
fi

echo "Done. Open Settings (SUPER+R) → Bar and add the AgentWatch widget to a section."
