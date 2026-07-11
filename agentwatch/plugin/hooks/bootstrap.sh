#!/bin/sh
# agentwatch bootstrap — provisions the plugin-local binary, puts it on $PATH,
# and starts the daemon.
#
# Runs detached from the SessionStart hook, so it MUST never block the agent and
# MUST never exit non-zero: every failure path logs one line and exits 0. When
# the release artifact matching the plugin version is missing from bin/, it is
# downloaded (with sha256 verification) from the GitHub release. The binary is
# then symlinked onto $PATH so bare `agentwatch` invocations (tmux run-shell,
# shell-spawned bar widgets) resolve it even though the plugin cache is on no
# login PATH. The daemon is finally started if it isn't already (the daemon's
# own already-running guard makes a redundant start a harmless no-op).

set -u

LOG="${TMPDIR:-/tmp}/agentwatch-bootstrap.log"

log() {
	printf '%s agentwatch-bootstrap: %s\n' \
		"$(date '+%Y-%m-%dT%H:%M:%S' 2>/dev/null || echo '-')" "$1" >>"$LOG" 2>/dev/null || true
}

# Resolve the plugin root. CLAUDE_PLUGIN_ROOT is set by Claude Code; fall back to
# this script's parent directory for manual/dev invocation.
ROOT="${CLAUDE_PLUGIN_ROOT:-$(dirname "$0")/..}"
BIN="$ROOT/bin/agentwatch"
MARKER="$ROOT/bin/.agentwatch-version"
PLUGIN_JSON="$ROOT/.claude-plugin/plugin.json"

# start_daemon launches the daemon detached. The already-running guard in the
# binary makes this a no-op when a daemon already owns the socket.
start_daemon() {
	[ -x "$BIN" ] || return 0
	nohup "$BIN" daemon >/dev/null 2>&1 &
}

# download fetches $1 into $2 using curl, falling back to wget. Returns non-zero
# when the fetch fails or neither tool is available.
download() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		log "neither curl nor wget available"
		return 1
	fi
}

# install_binary downloads, verifies, and installs the release artifact. Returns
# non-zero (after logging) on any failure so the caller can still try to start an
# existing daemon.
install_binary() {
	VERSION=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$PLUGIN_JSON" 2>/dev/null | head -n1)
	if [ -z "$VERSION" ]; then
		log "could not read version from $PLUGIN_JSON"
		return 1
	fi

	# Already installed and up to date — nothing to download.
	if [ -x "$BIN" ] && [ -f "$MARKER" ] && [ "$(cat "$MARKER" 2>/dev/null)" = "$VERSION" ]; then
		return 0
	fi

	os=$(uname -s 2>/dev/null)
	arch=$(uname -m 2>/dev/null)
	case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*)
		log "unsupported OS '$os'; install the agentwatch binary manually"
		return 1
		;;
	esac
	case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*)
		log "unsupported arch '$arch'; install the agentwatch binary manually"
		return 1
		;;
	esac

	base="https://github.com/matteobortolazzo/agent-stack/releases/download/agentwatch/v${VERSION}"
	tarball="agentwatch_${VERSION}_${os}_${arch}.tar.gz"

	tmp=$(mktemp -d 2>/dev/null) || {
		log "mktemp failed"
		return 1
	}

	if ! download "$base/$tarball" "$tmp/$tarball"; then
		log "download failed (no release yet or no network): $base/$tarball"
		rm -rf "$tmp"
		return 1
	fi
	if ! download "$base/checksums.txt" "$tmp/checksums.txt"; then
		log "checksums download failed: $base/checksums.txt"
		rm -rf "$tmp"
		return 1
	fi

	expected=$(awk -v f="$tarball" '$2 == f {print $1}' "$tmp/checksums.txt" 2>/dev/null | head -n1)
	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$tmp/$tarball" 2>/dev/null | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "$tmp/$tarball" 2>/dev/null | awk '{print $1}')
	else
		log "no sha256 tool (sha256sum/shasum) available"
		rm -rf "$tmp"
		return 1
	fi
	if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
		log "checksum mismatch for $tarball (expected='$expected' actual='$actual')"
		rm -rf "$tmp"
		return 1
	fi

	if ! tar -xzf "$tmp/$tarball" -C "$tmp" 2>/dev/null; then
		log "extract failed for $tarball"
		rm -rf "$tmp"
		return 1
	fi
	if [ ! -f "$tmp/agentwatch" ]; then
		log "binary 'agentwatch' not found in archive $tarball"
		rm -rf "$tmp"
		return 1
	fi

	mkdir -p "$ROOT/bin" 2>/dev/null || true
	if ! mv "$tmp/agentwatch" "$BIN" 2>/dev/null && ! cp "$tmp/agentwatch" "$BIN" 2>/dev/null; then
		log "failed to install binary to $BIN"
		rm -rf "$tmp"
		return 1
	fi
	chmod +x "$BIN" 2>/dev/null || true
	printf '%s\n' "$VERSION" >"$MARKER" 2>/dev/null || true
	rm -rf "$tmp"
	log "installed agentwatch $VERSION ($os/$arch)"
	return 0
}

# install_on_path maintains one predictable launcher at ~/.local/bin/agentwatch.
# Symlinks are re-pointed on version bumps; regular files and directories are
# never overwritten. The operation is idempotent and non-fatal.
install_on_path() {
	[ -x "$BIN" ] || return 0
	dir="$HOME/.local/bin"
	dest="$dir/agentwatch"
	mkdir -p "$dir" 2>/dev/null || true
	if [ -e "$dest" ] && [ ! -L "$dest" ]; then
		log "$dest exists and is not a symlink; left untouched"
		return 0
	fi
	if ! ln -sf "$BIN" "$dest" 2>/dev/null; then
		log "could not link agentwatch at $dest"
		return 0
	fi
	case ":$PATH:" in
	*":$dir:"*) log "linked agentwatch at $dest" ;;
	*) log "linked agentwatch at $dest; add \"$dir\" to your PATH so bare agentwatch invocations resolve" ;;
	esac
	return 0
}

install_binary || true
install_on_path || true
start_daemon
exit 0
