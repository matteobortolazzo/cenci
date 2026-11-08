# shellcheck shell=sh
# Codex fingerprints the installed plugin tree when hooks are trusted. Keep
# downloaded binaries and their version markers in writable runtime storage.
# This read-only helper is shared by bootstrap, notify and the host installer.
# It prints the binary directory, or fails without printing an unsafe path.
cenci_codex_bin_dir() (
	_root=$(CDPATH='' cd -P -- "$1" 2>/dev/null && pwd -P) || return 1
	_version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$_root/.codex-plugin/plugin.json" 2>/dev/null | head -n1)
	case "$_version" in
	'' | *[!A-Za-z0-9._+-]* | . | .. | *..*) return 1 ;;
	esac
	if [ -n "${PLUGIN_DATA:-}" ]; then
		_data="$PLUGIN_DATA"
	elif [ -n "${CLAUDE_PLUGIN_DATA:-}" ]; then
		_data="$CLAUDE_PLUGIN_DATA"
	elif [ -n "${XDG_CACHE_HOME:-}" ]; then
		_data="$XDG_CACHE_HOME/cenci/codex"
	else
		[ -n "${HOME:-}" ] || return 1
		_data="$HOME/.cache/cenci/codex"
	fi
	case "$_data" in
	/*) ;;
	*) return 1 ;;
	esac
	case "/${_data#/}/" in
	*/../* | */./*) return 1 ;;
	esac
	_dir="${_data%/}/$_version/bin"

	# Resolve the nearest existing ancestor without creating it. This also
	# rejects a data-directory symlink that leads back into the plugin tree.
	_parent="$_dir"
	_suffix=""
	while [ ! -d "$_parent" ]; do
		[ ! -e "$_parent" ] && [ ! -L "$_parent" ] || return 1
		_suffix="/${_parent##*/}$_suffix"
		_parent="${_parent%/*}"
		[ -n "$_parent" ] || _parent=/
	done
	_parent=$(CDPATH='' cd -P -- "$_parent" 2>/dev/null && pwd -P) || return 1
	case "${_parent%/}$_suffix" in
	"$_root" | "$_root"/*) return 1 ;;
	esac
	printf '%s\n' "$_dir"
)
