#!/bin/sh
# agentwatch bootstrap (Codex) — provisions the plugin-local binary, puts it on
# $PATH, and starts the daemon.
#
# Runs detached from the SessionStart hook, so it MUST never block the agent and
# MUST never exit non-zero: every failure path logs one line and exits 0. When
# the release artifact matching the plugin version is missing from bin/, it is
# downloaded (with sha256 verification) from the GitHub release. Because Codex
# hooks invoke a bare `agentwatch`, the binary is then symlinked onto $PATH. The
# daemon is finally started if it isn't already (the daemon's own already-running
# guard makes a redundant start a harmless no-op — the common case when the
# Claude plugin already bootstrapped it).

set -u

LOG="${TMPDIR:-/tmp}/agentwatch-bootstrap.log"

log() {
	printf '%s agentwatch-bootstrap: %s\n' \
		"$(date '+%Y-%m-%dT%H:%M:%S' 2>/dev/null || echo '-')" "$1" >>"$LOG" 2>/dev/null || true
}

# Codex sets PLUGIN_ROOT for native plugins. Keep the legacy variable as a
# compatibility fallback, then resolve one level above codex/ for manual runs.
ROOT="${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(dirname "$0")/..}}"
BIN="$ROOT/bin/agentwatch"
MARKER="$ROOT/bin/.agentwatch-version"
PLUGIN_JSON="$ROOT/.codex-plugin/plugin.json"

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

# install_on_path symlinks $BIN onto $PATH so Codex hooks can invoke a bare
# `agentwatch`. Idempotent and non-fatal. Prefers the first existing writable
# $PATH entry; otherwise falls back to ~/.local/bin and logs a PATH hint.
install_on_path() {
	[ -x "$BIN" ] || return 0

	# Already resolvable to our binary (directly or via an equivalent symlink)?
	existing=$(command -v agentwatch 2>/dev/null || true)
	if [ -n "$existing" ]; then
		if [ "$existing" = "$BIN" ]; then
			return 0
		fi
		if [ -L "$existing" ] && [ "$(readlink "$existing" 2>/dev/null)" = "$BIN" ]; then
			return 0
		fi
	fi

	# Pick the first existing, writable directory on $PATH. set -f disables glob
	# expansion so a PATH entry is never mistaken for a filename pattern.
	set -f
	IFS=:
	for dir in $PATH; do
		[ -n "$dir" ] || continue
		if [ -d "$dir" ] && [ -w "$dir" ] && ln -sf "$BIN" "$dir/agentwatch" 2>/dev/null; then
			unset IFS
			set +f
			log "linked agentwatch onto PATH at $dir/agentwatch"
			return 0
		fi
	done
	unset IFS
	set +f

	# Fallback: ~/.local/bin, which may not be on $PATH yet.
	fallback="$HOME/.local/bin"
	mkdir -p "$fallback" 2>/dev/null || true
	if ln -sf "$BIN" "$fallback/agentwatch" 2>/dev/null; then
		log "linked agentwatch at $fallback/agentwatch; add \"$fallback\" to your PATH so Codex hooks can find it"
		return 0
	fi
	log "could not place agentwatch on PATH; symlink $BIN into a directory on your PATH manually"
	return 0
}

install_binary || true
install_on_path || true
start_daemon
exit 0
