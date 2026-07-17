#!/usr/bin/env bash
#
# Uninstalls the Cenci DankMaterialShell (DMS) bar widget: removes the
# symlink installed by install.sh (only if it's still the cenci-owned
# symlink) and restarts DMS so the change takes effect immediately.
#
# Usage: ./plugin/dms/uninstall.sh

set -euo pipefail

if ! command -v dms >/dev/null 2>&1 && [ ! -d "$HOME/.config/DankMaterialShell" ]; then
  echo "DankMaterialShell not detected (no dms, no ~/.config/DankMaterialShell) — skipping." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.config/DankMaterialShell/plugins/cenci"

# NOT_OURS tracks the "DEST exists but isn't cenci-owned" case so the caller
# (install.sh) can tell it apart from a real removal or a true no-op via a
# distinct exit code (2), instead of collapsing all three into one banner.
NOT_OURS=0
if [ -L "$DEST" ] && [ "$(readlink "$DEST")" = "$DIR" ]; then
  rm -f "$DEST"
  echo "Removed symlink: $DEST"
elif [ -e "$DEST" ]; then
  echo "$DEST exists and is not a cenci-owned symlink — left untouched." >&2
  NOT_OURS=1
else
  echo "No cenci widget symlink found at $DEST — nothing to remove."
fi

# Reload so DMS re-scans its plugins. Same idiom as install.sh: the systemd
# user unit is the clean path, falling back to killing the quickshell process.
# Unlike install.sh, capture each reload command's real exit status instead of
# swallowing it with `|| true`, so a failed reload doesn't get an unconditional
# success echo.
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
    echo "DMS does not appear to be running — nothing to reload." >&2
  fi
fi

echo "Done."

if [ "$NOT_OURS" -eq 1 ]; then
  exit 2
fi
