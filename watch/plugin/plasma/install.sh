#!/usr/bin/env bash
#
# Installs the AgentWatch KDE Plasma widget without touching the panel GUI:
# symlinks this plasmoid into the per-user plasmoids folder under its plugin id
# and restarts plasmashell so the change takes effect immediately.
#
# The symlink-to-checkout keeps the install stable across `cenci update`
# (the checkout is refreshed in place) and needs no re-copy. You still add the
# widget to a panel once via the Plasma "Add Widgets…" GUI.
#
# Usage: ./plugin/plasma/install.sh

set -euo pipefail

ID="com.github.matteobortolazzo.agentwatch"

if ! command -v plasmashell >/dev/null 2>&1; then
  echo "KDE Plasma not detected (no plasmashell) — skipping." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.local/share/plasma/plasmoids/$ID"

mkdir -p "$(dirname "$DEST")"
if [ -e "$DEST" ] && [ ! -L "$DEST" ]; then
  echo "$DEST exists and is not a symlink — left untouched; remove it manually and re-run." >&2
  exit 1
fi
ln -sfn "$DIR" "$DEST"
echo "Symlinked: $DEST -> $DIR"

# Reload so the panel re-reads the plasmoid. kquitapp6 + kstart is the clean
# path; fall back to --replace when those Plasma 6 helpers are absent.
if command -v kquitapp6 >/dev/null 2>&1 && command -v kstart >/dev/null 2>&1; then
  kquitapp6 plasmashell >/dev/null 2>&1 || true
  kstart plasmashell >/dev/null 2>&1 &
else
  plasmashell --replace >/dev/null 2>&1 &
fi
echo "Reloaded plasmashell."

echo "Done. Right-click the panel → Add Widgets… → search 'AgentWatch' → add it."
echo "Right-click the widget → Configure AgentWatch… to set the binary path."
