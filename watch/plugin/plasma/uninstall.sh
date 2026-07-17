#!/usr/bin/env bash
#
# Uninstalls the Cenci KDE Plasma widget: removes the plasmoid symlink
# installed by install.sh (only if it's still the cenci-owned symlink) and
# restarts plasmashell so the change takes effect immediately.
#
# Usage: ./plugin/plasma/uninstall.sh

set -euo pipefail

ID="com.github.matteobortolazzo.cenci"

if ! command -v plasmashell >/dev/null 2>&1; then
  echo "KDE Plasma not detected (no plasmashell) — skipping." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.local/share/plasma/plasmoids/$ID"

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
  echo "No cenci plasmoid symlink found at $DEST — nothing to remove."
fi

# Reload so the panel drops the plasmoid. Same idiom as install.sh: kquitapp6
# + kstart is the clean path, falling back to --replace when absent. kstart's
# whole job is to launch its target and return immediately (unlike
# plasmashell itself), so its own exit status is capturable synchronously
# without waiting on the panel process it starts — check that instead of
# echoing an unconditional "reloaded" claim. plasmashell --replace has no
# such wrapper and becomes the panel process itself, so it must stay
# backgrounded (awaiting it would hang this script for the rest of the
# session) — same fire-and-forget shape as install.sh's reload block.
if command -v kquitapp6 >/dev/null 2>&1 && command -v kstart >/dev/null 2>&1; then
  kquitapp6 plasmashell >/dev/null 2>&1 || true
  if kstart plasmashell >/dev/null 2>&1; then
    echo "Reloaded plasmashell."
  else
    echo "Could not reload plasmashell via kstart — restart it manually: kstart plasmashell" >&2
  fi
else
  plasmashell --replace >/dev/null 2>&1 &
  echo "Reloaded plasmashell."
fi

echo "Done."

if [ "$NOT_OURS" -eq 1 ]; then
  exit 2
fi
