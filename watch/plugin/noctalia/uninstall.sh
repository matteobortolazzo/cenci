#!/usr/bin/env bash
#
# Uninstalls the Cenci noctalia-shell bar widget: removes the symlink
# installed by install.sh (only if it's still the cenci-owned symlink) and
# restarts the shell so the change takes effect immediately.
#
# Usage: ./plugin/noctalia/uninstall.sh

set -euo pipefail

if ! command -v noctalia-shell >/dev/null 2>&1 && [ ! -d "$HOME/.config/noctalia" ]; then
  echo "noctalia-shell not detected (no noctalia-shell, no ~/.config/noctalia) — skipping." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.config/noctalia/plugins/cenci"

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
  echo "No cenci plugin symlink found at $DEST — nothing to remove."
fi

# Reload so noctalia re-scans its plugins. Same idiom as install.sh: kill the
# running shell and respawn it detached so it outlives this script.
if pkill -f noctalia-shell >/dev/null 2>&1; then
  if command -v qs >/dev/null 2>&1; then
    nohup qs -c noctalia-shell >/dev/null 2>&1 &
    disown 2>/dev/null || true
    echo "Restarted noctalia-shell."
  else
    echo "Killed noctalia-shell — restart it yourself (qs not found): qs -c noctalia-shell &"
  fi
else
  echo "noctalia-shell was not running — nothing to reload."
fi

echo "Done."

if [ "$NOT_OURS" -eq 1 ]; then
  exit 2
fi
