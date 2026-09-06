# shellcheck shell=sh
# Shared cenci-binary resolver — walks known install locations for a working
# cenci when the plugin-local download hasn't happened (yet, or ever).
# Sourced by bootstrap-common.sh (adoption fallback) and by the hooks/codex
# notify.sh wrappers (the SessionStart-race fallback). Modeled directly on
# plugin/macos/cenci.5s.sh's resolve_bin(): one `for` loop, unquoted glob
# segments (a non-matching glob stays literal and fails the -x guard), no
# arrays, no bashisms — safe under any POSIX /bin/sh, including dash.
#
# Candidate order:
#   1. $CENCI_BIN, when -x (the established override, watch/README.md)
#   2. /usr/local/bin/cenci — host binary bind-mounted read-only in a
#      sandbox, and install.sh's optional sudo link on a host
#   3. /opt/homebrew/bin/cenci — macOS parity with the widget resolver
#   4. Sibling plugin caches (Claude Code cache, Codex cache, marketplace
#      checkout)
#   5. `command -v cenci` last
#
# Two guards the widget resolver does not need:
#
#   - Self-adoption guard (_resolve_is_self): reject a candidate that
#     resolves — directly, or through a live symlink chain — back to $BIN
#     (the path being installed) or anywhere under $ROOT/ (the plugin root).
#     install_on_path() symlinks ~/.local/bin/cenci *into* the plugin cache,
#     so a naive `command -v cenci` can point back at the very path being
#     installed. A dangling symlink is already rejected by the -x guard
#     below for free; the guard here exists for a LIVE symlink chain that
#     loops back to $ROOT/bin/cenci (e.g. a documented
#     `sudo ln -sf ~/.local/bin/cenci /usr/local/bin/cenci`, or a dev
#     container whose PATH ends in the plugin's own bin/), which -x alone
#     does not catch.
#   - Liveness probe (`"$c" --version`): -x alone passes an
#     arch-mismatched or truncated binary; one cheap exec rejects it. Not
#     gated on the version STRING matching — internal/ipc's HookEvent
#     degrades gracefully across a version mismatch, so any
#     protocol-acceptable binary that actually runs is fine.
#
# Callers may set (both optional, read verbatim, never exported by this
# file):
#   BIN   - the plugin-local binary path being installed/resolved for.
#   ROOT  - the plugin root directory.
#
# Test-only overrides (never set by production callers) point the fixed
# install-prefix candidates away from the real host paths, mirroring
# flow/skills/configure/scripts/detect-project.sh's
# CENCI_DETECT_DOCKERENV_PATH precedent — without them, a real
# /usr/local/bin/cenci in the dev/CI container would make a "no candidate
# available" test vacuously pass.
CENCI_RESOLVE_USR_LOCAL_BIN="${CENCI_RESOLVE_USR_LOCAL_BIN:-/usr/local/bin/cenci}"
CENCI_RESOLVE_HOMEBREW_BIN="${CENCI_RESOLVE_HOMEBREW_BIN:-/opt/homebrew/bin/cenci}"

# _resolve_canonical follows up to 20 symlink hops from $1 (POSIX
# `readlink`, one hop at a time — no `-f`, which is a GNU/util-linux
# extension not available on every platform this resolver runs on) and
# prints the final resolved path. A relative symlink target is resolved
# against its own symlink's directory. Bounded so a symlink loop cannot
# hang the resolver. Only the leaf component is checked (`-L "$_path"`, not
# every path segment), matching plain `readlink`'s own limitation: a path
# reached through a symlinked INTERMEDIATE directory component (rather than
# a symlinked leaf) is not further canonicalized here — accepted, since
# this resolver has no portable `readlink -f`/`realpath` to fall back on.
#
# All internal variables are prefixed with `_` and are local to this
# function's own (sub)shell invocation: POSIX sh has no `local`, so a bare
# name here would otherwise risk clobbering a same-named variable in a
# caller several frames up (see _resolve_is_self below, which is called
# synchronously — not via `$(...)` — from callers that themselves iterate
# over a `c` variable).
_resolve_canonical() {
	_path="$1"
	_hops=0
	while [ -L "$_path" ] && [ "$_hops" -lt 20 ]; do
		_hop_target=$(readlink "$_path" 2>/dev/null) || break
		case "$_hop_target" in
		/*) _path="$_hop_target" ;;
		*) _path="$(dirname "$_path")/$_hop_target" ;;
		esac
		_hops=$((_hops + 1))
	done
	printf '%s\n' "$_path"
}

# _resolve_is_self returns success when $1 resolves — directly, or through
# a symlink chain — back to $BIN exactly, or anywhere under $ROOT/. Guards
# by path, not by dangling-ness: see the file header. $ROOT itself is run
# through the same leaf-only canonicalization as the candidate, for parity
# (both sides of the comparison get the same treatment) — this does not
# close the intermediate-symlinked-directory-component gap noted above,
# which would need a portable `realpath` to close fully.
#
# Called SYNCHRONOUSLY (not via command substitution) from callers that
# hold a candidate in a variable named `c` — every local here uses an
# `_self_`-prefixed name so it cannot collide with (and clobber) that
# caller-scoped `c`. Do not reintroduce a bare `c`/`p`/`target`-style name
# in this function.
_resolve_is_self() {
	_self_target="$(_resolve_canonical "$1")"
	if [ -n "${BIN:-}" ] && [ "$_self_target" = "$BIN" ]; then
		return 0
	fi
	if [ -n "${ROOT:-}" ]; then
		_self_root="$(_resolve_canonical "$ROOT")"
		case "$_self_target" in
		"$_self_root"/*) return 0 ;;
		esac
	fi
	return 1
}

# _resolve_usable checks the three guards a candidate must clear: -x, not
# self (per _resolve_is_self), and a passing liveness probe. Takes its own
# candidate in `_usable_candidate` (never a bare `c`) for the same
# collision-avoidance reason as _resolve_is_self above.
_resolve_usable() {
	_usable_candidate="$1"
	[ -x "$_usable_candidate" ] || return 1
	_resolve_is_self "$_usable_candidate" && return 1
	"$_usable_candidate" --version >/dev/null 2>&1
}

# resolve_cenci_bin prints the first usable candidate cenci binary and
# returns non-zero when none is found.
resolve_cenci_bin() {
	if [ -n "${CENCI_BIN:-}" ] && _resolve_usable "${CENCI_BIN}"; then
		printf '%s\n' "${CENCI_BIN}"
		return 0
	fi

	# POSIX sh has no arrays, so walk the fixed-prefix and glob candidates
	# in one loop. Printing this loop's own `c` (rather than anything a
	# helper touched) is what keeps the printed path the literal configured
	# candidate rather than a canonicalized realpath — see the collision
	# note on _resolve_is_self/_resolve_usable above.
	for c in \
		"$CENCI_RESOLVE_USR_LOCAL_BIN" \
		"$CENCI_RESOLVE_HOMEBREW_BIN" \
		"$HOME"/.claude/plugins/cache/*/cenci-watch/*/bin/cenci \
		"$HOME"/.codex/plugins/cache/*/cenci-watch/*/bin/cenci \
		"$HOME"/.claude/plugins/marketplaces/*/watch/plugin/bin/cenci; do
		if _resolve_usable "$c"; then
			printf '%s\n' "$c"
			return 0
		fi
	done

	if command -v cenci >/dev/null 2>&1; then
		c="$(command -v cenci)"
		if _resolve_usable "$c"; then
			printf '%s\n' "$c"
			return 0
		fi
	fi

	return 1
}
