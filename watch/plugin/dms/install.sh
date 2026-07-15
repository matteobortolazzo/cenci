#!/usr/bin/env bash
#
# Installs the Cenci DankMaterialShell (DMS) bar widget without touching the
# GUI: symlinks this plugin into DMS's plugin folder and restarts DMS so the
# change takes effect immediately.
#
# The symlink-to-checkout keeps the install stable across `cenci update`.
# You still add the widget to a bar section once via DMS Settings → Plugins.
#
# Usage: ./plugin/dms/install.sh

set -euo pipefail

if ! command -v dms >/dev/null 2>&1 && [ ! -d "$HOME/.config/DankMaterialShell" ]; then
  echo "DankMaterialShell not detected (no dms, no ~/.config/DankMaterialShell) — skipping." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.config/DankMaterialShell/plugins/cenci"

mkdir -p "$(dirname "$DEST")"
if [ -e "$DEST" ] && [ ! -L "$DEST" ]; then
  echo "$DEST exists and is not a symlink — left untouched; remove it manually and re-run." >&2
  exit 1
fi
ln -sfn "$DIR" "$DEST"
echo "Symlinked: $DEST -> $DIR"

# Reload so DMS re-scans its plugins. The systemd user unit is the clean path;
# fall back to killing the quickshell process (a niri Wants=dms re-spawns it).
if command -v systemctl >/dev/null 2>&1 && systemctl --user status dms >/dev/null 2>&1; then
  systemctl --user restart dms >/dev/null 2>&1 || true
  echo "Restarted the dms user service."
else
  pkill -f 'qs -c dms' >/dev/null 2>&1 || true
  echo "Signalled DMS to reload (pkill 'qs -c dms')."
fi

echo "Done. Open Settings (dms ipc call settings toggle) → Plugins → enable"
echo "Cenci → DankBar → add the widget to a section."
