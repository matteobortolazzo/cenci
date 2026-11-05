# shellcheck shell=sh
# Hook-health record writer (#1239) — the POSIX-sh half of the per-user store
# the Go package internal/hookhealth owns. Sourced lazily by the notify.sh
# wrappers (no binary to resolve) and by bootstrap-common.sh (install failed),
# i.e. exactly the failures with no cenci binary able to record them.
#
# Contract (shared with internal/hookhealth, keep in sync):
#   dir     <S>/cenci/hook-health, S = $XDG_STATE_HOME when absolute, else
#           $HOME/.local/state (HOME must be absolute); neither => do nothing.
#           The state root <S>/cenci and the leaf are created 0700 when
#           absent. When present each must not be a symlink, must be a
#           directory owned by the current uid, and must not be group/other
#           writable (mirrors hardenDir in watch/pkg/watch/socket.go).
#   record  one file per category holding exactly "<epoch> <client>\n"
#           (at most 32 bytes, mode 0600), written to a PID-unique temp file
#           and renamed into place.
#   rate    a valid record younger than 60 s (or at most 5 s in the future)
#           is not rewritten; invalid and older records are replaced.
#
# Hooks must stay silent and exit 0, so cenci_hook_health_record swallows every
# error and never prints. It runs in a subshell: the umask, the working
# variables and any early exit stay out of the caller. Internal variables are
# `_hh_`-prefixed (POSIX sh has no `local`), mirroring resolve-bin.sh.
#
# Closed sets: categories missing-binary, bootstrap-failure, malformed-input,
# socket-unavailable, delivery-failure; clients claude, codex. last-ok is
# written by the Go side only. OpenCode never records.

# _hh_safe_dir succeeds when $1 is a real (non-symlink) directory owned by the
# current uid that is neither group- nor other-writable. One find call tests
# owner and mode together and fails closed when find or id is unavailable.
_hh_safe_dir() {
	[ ! -L "$1" ] || return 1
	[ -d "$1" ] || return 1
	_hh_uid=$(id -u 2>/dev/null) || return 1
	[ -n "$_hh_uid" ] || return 1
	_hh_ok=$(find "$1" -prune -user "$_hh_uid" ! -perm -020 ! -perm -002 -print 2>/dev/null) || return 1
	[ -n "$_hh_ok" ]
}

# _hh_dir resolves the store directory into _hh_d, creating the state root and
# the leaf 0700 when absent, and returns non-zero when it is unusable.
_hh_dir() {
	case "${XDG_STATE_HOME:-}" in
	/*) _hh_s="$XDG_STATE_HOME" ;;
	*)
		case "${HOME:-}" in
		/*) _hh_s="$HOME/.local/state" ;;
		*) return 1 ;;
		esac
		;;
	esac
	if [ ! -e "$_hh_s" ] && [ ! -L "$_hh_s" ]; then
		mkdir -p "$_hh_s" 2>/dev/null || return 1
	fi
	for _hh_p in "$_hh_s/cenci" "$_hh_s/cenci/hook-health"; do
		if [ ! -e "$_hh_p" ] && [ ! -L "$_hh_p" ]; then
			mkdir -m 700 "$_hh_p" 2>/dev/null || true
		fi
		_hh_safe_dir "$_hh_p" || return 1
	done
	_hh_d="$_hh_s/cenci/hook-health"
}

# _hh_fresh succeeds when $1 is a valid record (a regular non-symlink file of
# at most 32 bytes holding exactly "<epoch> <claude|codex>\n") no later than
# $2 + 5 s and younger than 60 s: the rate limit says not to rewrite it.
_hh_fresh() {
	[ -f "$1" ] && [ ! -L "$1" ] || return 1
	_hh_sz=$(wc -c <"$1" 2>/dev/null | tr -d ' \t')
	case "$_hh_sz" in
	'' | *[!0-9]*) return 1 ;;
	esac
	[ "$_hh_sz" -le 32 ] || return 1
	IFS=' ' read -r _hh_ts _hh_cl <"$1" || return 1
	case "$_hh_ts" in
	'' | *[!0-9]* | 0?* | ?????????????*) return 1 ;;
	esac
	case "$_hh_cl" in
	claude | codex) ;;
	*) return 1 ;;
	esac
	[ $((${#_hh_ts} + 1 + ${#_hh_cl} + 1)) -eq "$_hh_sz" ] || return 1
	[ "$_hh_ts" -le $(($2 + 5)) ] || return 1
	[ $(($2 - _hh_ts)) -lt 60 ]
}

# _hh_record is the unguarded writer body; run it only via
# cenci_hook_health_record, which isolates and silences it.
_hh_record() {
	case "$1" in
	missing-binary | bootstrap-failure | malformed-input | socket-unavailable | delivery-failure) ;;
	*) return 1 ;;
	esac
	case "$2" in
	claude | codex) ;;
	*) return 1 ;;
	esac
	umask 077
	_hh_now=$(date +%s 2>/dev/null) || return 1
	case "$_hh_now" in
	'' | *[!0-9]*) return 1 ;;
	esac
	_hh_dir || return 1
	_hh_f="$_hh_d/$1"
	if _hh_fresh "$_hh_f" "$_hh_now"; then
		return 0
	fi
	[ ! -d "$_hh_f" ] || return 1
	_hh_t="$_hh_d/.$1.$$.tmp"
	if printf '%s %s\n' "$_hh_now" "$2" >"$_hh_t" && mv -f "$_hh_t" "$_hh_f"; then
		return 0
	fi
	rm -f "$_hh_t"
	return 1
}

# cenci_hook_health_record <category> <client> — best-effort, silent, always 0.
cenci_hook_health_record() {
	(_hh_record "${1:-}" "${2:-}") >/dev/null 2>&1
	return 0
}
