#!/bin/sh
# cenci status wrapper (Claude Code) — resolves a working cenci binary and
# runs `cenci status` for the opt-in /cenci-status mods command (#1183).
#
# Same resolution order as notify.sh: the plugin-local binary first, then the
# shared resolver (../lib/resolve-bin.sh) for every other known location. Unlike
# a hook, this is invoked by the user, so a missing binary is reported: the
# reason on stderr and exit 127 (the shell's "command not found"). It writes no
# hook-health record, because it is not a hook.
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

exec "$BIN" status
