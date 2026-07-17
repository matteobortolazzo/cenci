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
# Capture each reload command's real exit status instead of swallowing it with
# `|| true`, so a failed reload doesn't get an unconditional success echo.
if command -v systemctl >/dev/null 2>&1 && systemctl --user status dms >/dev/null 2>&1; then
  if systemctl --user restart dms >/dev/null 2>&1; then
    echo "Restarted the dms user service."
  else
    echo "Could not restart the dms user service — reload it manually: systemctl --user restart dms" >&2
  fi
else
  if pkill -f 'qs -c dms' >/dev/null 2>&1; then
    echo "Signalled DMS to reload (pkill 'qs -c dms')."
  else
    echo "DMS does not appear to be running — start it or check its status manually." >&2
  fi
fi

echo "Done. Open Settings (dms ipc call settings toggle) → Plugins → enable"
echo "Cenci → DankBar → add the widget to a section."
