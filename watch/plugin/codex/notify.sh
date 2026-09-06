#!/bin/sh
# cenci notify wrapper (Codex) — resolves a working cenci binary and
# forwards this hook event to it.
#
# Covers the SessionStart race: bootstrap.sh runs detached from the
# SessionStart hook, so the first hook events of a session can fire before
# it has finished (or even started) installing the plugin-local binary —
# on a genuinely fresh container, the plugin-local binary does not exist
# yet no matter what bootstrap.sh eventually does. Resolution order: the
# plugin-local binary first (the common case once bootstrap has run), then
# the shared resolver (../lib/resolve-bin.sh) for every other known
# location. Hooks stay silent by design; when nothing resolves, this exits
# 0 with no output.

set -u

# Codex sets PLUGIN_ROOT for native plugins. Keep the legacy variable as a
# compatibility fallback, then resolve one level above codex/ for manual runs.
ROOT="${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(dirname "$0")/..}}"
BIN="$ROOT/bin/cenci"

if [ ! -x "$BIN" ]; then
	# shellcheck source-path=SCRIPTDIR
	# shellcheck source=../lib/resolve-bin.sh
	. "$(dirname "$0")/../lib/resolve-bin.sh"
	BIN="$(resolve_cenci_bin 2>/dev/null)" || exit 0
	[ -n "$BIN" ] || exit 0
fi

exec "$BIN" notify -agent codex
