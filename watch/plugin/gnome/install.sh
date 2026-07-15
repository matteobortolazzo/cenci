#!/usr/bin/env bash
#
# Installs the AgentWatch GNOME Shell extension without touching the Extensions
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

UUID="agentwatch@matteobortolazzo.github.io"

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
FRESH=0
if command -v gnome-extensions >/dev/null 2>&1; then
  gnome-extensions list 2>/dev/null | grep -qx "$UUID" || FRESH=1
  gnome-extensions disable "$UUID" 2>/dev/null || true
  gnome-extensions enable "$UUID" 2>/dev/null || true
fi

if [ "$FRESH" -eq 1 ]; then
  echo "First install of a new extension dir — reload GNOME Shell so it scans it:"
  echo "  X11:     press Alt+F2, type 'r', Enter"
  echo "  Wayland: log out and back in (Shell can't hot-reload on Wayland)"
  echo "then: gnome-extensions enable \"$UUID\""
else
  echo "Reloaded the AgentWatch extension."
fi
echo "Done. AgentWatch appears in the top bar once a session is live."
echo "Settings: gnome-extensions prefs \"$UUID\""
