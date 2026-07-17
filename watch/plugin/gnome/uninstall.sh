#!/usr/bin/env bash
#
# Uninstalls the Cenci GNOME Shell extension: disables it live, removes the
# copied extension dir installed by install.sh, and best-effort deregisters
# it from gnome-extensions.
#
# The extension dir is entirely ours (install.sh copies, not symlinks, into
# it, including the compiled schema), so removal is an unconditional rm -rf
# of the exact UUID dir — no symlink-ownership guard needed here.
#
# Usage: ./plugin/gnome/uninstall.sh

set -euo pipefail

UUID="cenci@matteobortolazzo.github.io"

# GNOME Shell must be present. `gnome-extensions` is the CLI we drive the
# disable/uninstall toggle with; a running gnome-shell implies it.
if ! command -v gnome-shell >/dev/null 2>&1 && ! command -v gnome-extensions >/dev/null 2>&1; then
  echo "GNOME Shell not detected (no gnome-shell/gnome-extensions) — skipping." >&2
  exit 0
fi

DEST="$HOME/.local/share/gnome-shell/extensions/$UUID"

if command -v gnome-extensions >/dev/null 2>&1; then
  gnome-extensions disable "$UUID" >/dev/null 2>&1 || true
  gnome-extensions uninstall "$UUID" >/dev/null 2>&1 || true
fi

if [ -d "$DEST" ]; then
  rm -rf "${DEST:?}"
  echo "Removed: $DEST"
else
  echo "No cenci extension dir found at $DEST — nothing to remove."
fi

echo "Done."
