#!/bin/sh
# cenci measurement wrapper (Claude Code) — resolves a working cenci binary and
# runs `cenci notify -agent claude -measurement` for the opt-in
# nativeMeasurements mods module (#1451). The module writes one JSON object to
# stdin ({"cwd": ..., "measurement": {...}}); exec passes it through untouched.
#
# Same resolution order as status.sh and notify.sh: the plugin-local binary
# first, then the shared resolver (../lib/resolve-bin.sh) for every other known
# location. A missing binary is reported on stderr with exit 127; the module
# ignores the result. It writes no hook-health record, because it is not a
# settings hook.
#
# The root comes from this script's own location only: it is not run by Claude
# Code's hook machinery, so CLAUDE_PLUGIN_ROOT is not guaranteed.

set -u

ROOT="$(dirname "$0")/.."
BIN="$ROOT/bin/cenci"

if [ ! -x "$BIN" ]; then
	# A missing resolve-bin.sh (partial plugin update) means nothing resolves
	# (dash exits on a failed `.`, which would show up as a misleading exit 2).
	# BIN and ROOT stay set while the resolver runs: its self-adoption guard
	# reads them.
	_resolver="$ROOT/lib/resolve-bin.sh"
	_found=""
	if [ -r "$_resolver" ]; then
		# shellcheck source-path=SCRIPTDIR
		# shellcheck source=../lib/resolve-bin.sh
		. "$_resolver"
		_found="$(resolve_cenci_bin 2>/dev/null)" || _found=""
	fi
	BIN="$_found"
	if [ -z "$BIN" ]; then
		echo "cenci binary not found" >&2
		exit 127
	fi
fi

exec "$BIN" notify -agent claude -measurement
