#!/usr/bin/env bash
#
# Installs the Cenci GNOME Shell extension without touching the Extensions
# GUI: copies this widget into the per-user extensions folder under its UUID,
# compiles its settings schema, and live-reloads the extension so the change
# takes effect immediately.
#
# We COPY (not symlink) the widget dir so the generated gschemas.compiled never
# dirties the marketplace git checkout. Re-running refreshes the copy and
# reloads — that is what makes `cenci update` show widget changes.
#
# Usage: ./plugin/gnome/install.sh

set -euo pipefail

UUID="cenci@matteobortolazzo.github.io"

# GNOME Shell must be present. `gnome-extensions` is the CLI we drive the
# enable/disable toggle with; a running gnome-shell implies it.
if ! command -v gnome-shell >/dev/null 2>&1 && ! command -v gnome-extensions >/dev/null 2>&1; then
  echo "GNOME Shell not detected (no gnome-shell/gnome-extensions) — skipping." >&2
  exit 0
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.local/share/gnome-shell/extensions/$UUID"

# Fresh copy every run so removed files don't linger and the compiled schema
# lands in the copy, not the checkout.
mkdir -p "$DEST"
rm -rf "${DEST:?}/"* 2>/dev/null || true
cp -R "$DIR/." "$DEST/"

if command -v glib-compile-schemas >/dev/null 2>&1 && [ -d "$DEST/schemas" ]; then
  glib-compile-schemas "$DEST/schemas"
fi

echo "Installed: $DEST"

# The disable→enable toggle live-reloads an already-loaded extension on both
# X11 and Wayland — this is what makes `update` show changes without a relogin.
# `enable` is the actual reload action — capture its real exit status instead
# of swallowing it with `|| true`, so a failed reload doesn't get an
# unconditional success echo. `disable` stays best-effort (unverified): it's
# just clearing state ahead of the `enable` that follows. RELOADED uses a
# third state (-1) to distinguish "gnome-extensions absent from PATH" from
# "enable ran and failed" so each gets its own actionable message.
FRESH=0
if command -v gnome-extensions >/dev/null 2>&1; then
  gnome-extensions list 2>/dev/null | grep -qx "$UUID" || FRESH=1
  gnome-extensions disable "$UUID" 2>/dev/null || true
  if gnome-extensions enable "$UUID" 2>/dev/null; then
    RELOADED=1
  else
    RELOADED=0
  fi
else
  RELOADED=-1
fi

if [ "$FRESH" -eq 1 ]; then
  echo "First install of a new extension dir — reload GNOME Shell so it scans it:"
  echo "  X11:     press Alt+F2, type 'r', Enter"
  echo "  Wayland: log out and back in (Shell can't hot-reload on Wayland)"
  echo "then: gnome-extensions enable \"$UUID\""
elif [ "$RELOADED" -eq 1 ]; then
  echo "Reloaded the Cenci extension."
elif [ "$RELOADED" -eq 0 ]; then
  echo "Could not reload the Cenci extension — reload it manually: gnome-extensions enable \"$UUID\"" >&2
else
  echo "Could not reload the Cenci extension — gnome-extensions not found on PATH; reload GNOME Shell manually to pick up changes." >&2
fi
echo "Done. Cenci appears in the top bar once a session is live."
echo "Settings: gnome-extensions prefs \"$UUID\""
