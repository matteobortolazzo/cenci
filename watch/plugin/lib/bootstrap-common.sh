# shellcheck shell=sh
# Shared cenci bootstrap logic sourced by watch/plugin/codex/bootstrap.sh and
# watch/plugin/hooks/bootstrap.sh.
#
# Runs detached from the SessionStart hook, so it MUST never block the agent
# and MUST never exit non-zero: every failure path logs one line and exits 0.
# When the release artifact matching the plugin version is missing from
# bin/, it is downloaded (with sha256 verification) from the GitHub release.
# The binary is then symlinked onto $PATH. The daemon is finally started if
# it isn't already (the daemon's own already-running guard makes a redundant
# start a harmless no-op).
#
# Callers must set, before sourcing this file:
#   ROOT                  - resolved plugin root directory
#   PLUGIN_MANIFEST_REL   - path to plugin.json relative to ROOT
#   PATH_AUDIENCE         - tail clause of the "add to PATH" log hint (e.g.
#                            "Codex hooks can find it")

LOG="${TMPDIR:-/tmp}/cenci-bootstrap.log"
BIN="$ROOT/bin/cenci"
MARKER="$ROOT/bin/.cenci-version"
PLUGIN_JSON="$ROOT/$PLUGIN_MANIFEST_REL"

# shellcheck source=./resolve-bin.sh
. "$(dirname "$0")/../lib/resolve-bin.sh"

log() {
	printf '%s cenci-bootstrap: %s\n' \
		"$(date '+%Y-%m-%dT%H:%M:%S' 2>/dev/null || echo '-')" "$1" >>"$LOG" 2>/dev/null || true
}

# start_daemon launches the daemon detached. The already-running guard in the
# binary makes this a no-op when a daemon already owns the socket. Inside a
# cenci sandbox container (CENCI_SANDBOX=1), a container-local daemon controls
# nothing on the host and would only mask real wiring failures (#195, #202),
# so this is skipped regardless of mount status.
start_daemon() {
	[ -x "$BIN" ] || return 0
	if [ "${CENCI_SANDBOX:-}" = "1" ]; then
		log "CENCI_SANDBOX=1; not starting a local daemon"
		return 0
	fi
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

# download_binary downloads, verifies, and installs the release artifact.
# Returns non-zero (after logging) on any failure so install_binary can fall
# back to adopt_fallback_binary.
download_binary() {
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
		log "unsupported OS '$os'; install the cenci binary manually"
		return 1
		;;
	esac
	case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*)
		log "unsupported arch '$arch'; install the cenci binary manually"
		return 1
		;;
	esac

	base="https://github.com/matteobortolazzo/cenci/releases/download/watch/v${VERSION}"
	tarball="cenci_${VERSION}_${os}_${arch}.tar.gz"

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
	if [ ! -f "$tmp/cenci" ]; then
		log "binary 'cenci' not found in archive $tarball"
		rm -rf "$tmp"
		return 1
	fi

	mkdir -p "$ROOT/bin" 2>/dev/null || true
	if ! mv "$tmp/cenci" "$BIN" 2>/dev/null && ! cp "$tmp/cenci" "$BIN" 2>/dev/null; then
		log "failed to install binary to $BIN"
		rm -rf "$tmp"
		return 1
	fi
	chmod +x "$BIN" 2>/dev/null || true
	printf '%s\n' "$VERSION" >"$MARKER" 2>/dev/null || true
	rm -rf "$tmp"
	log "installed cenci $VERSION ($os/$arch)"
	return 0
}

# adopt_fallback_binary copies a working cenci binary found via
# resolve_cenci_bin() (see lib/resolve-bin.sh) into place when the real
# download failed, so a hook event on this session still has a usable
# binary. The adopted marker value ("fallback:<source-path>") can never
# equal a real $VERSION string, so download_binary()'s own
# already-installed short-circuit above cannot latch onto it — a correct
# release supersedes the fallback automatically on the next session.
# Returns non-zero (after logging) when no candidate resolves.
adopt_fallback_binary() {
	# A healthy fallback (or a real install that raced ahead of us) is
	# already in place — never re-copy or re-probe on later sessions.
	[ -x "$BIN" ] && return 0

	candidate=$(resolve_cenci_bin) || return 1
	[ -n "$candidate" ] || return 1

	mkdir -p "$ROOT/bin" 2>/dev/null || true
	if ! cp "$candidate" "$BIN" 2>/dev/null; then
		log "failed to adopt fallback cenci from $candidate"
		return 1
	fi
	chmod +x "$BIN" 2>/dev/null || true
	printf 'fallback:%s\n' "$candidate" >"$MARKER" 2>/dev/null || true
	log "adopted fallback cenci from $candidate (no release artifact for ${VERSION:-unknown})"
	return 0
}

# install_binary wraps download_binary with a fallback-adoption attempt: if
# the real download fails for any reason, try to adopt a working cenci
# binary found elsewhere (see resolve-bin.sh) before giving up. Adoption is
# only ever attempted AFTER download_binary's own attempt fails, never
# before — adopting first would pay a copy cost on every healthy plugin
# version bump. Never blocks, never exits non-zero.
install_binary() {
	if download_binary; then
		return 0
	fi
	if adopt_fallback_binary; then
		return 0
	fi
	log "no cenci binary available: download failed and no fallback found"
	return 1
}

# install_on_path maintains one predictable launcher at ~/.local/bin/cenci.
# Symlinks are re-pointed on version bumps; regular files and directories are
# never overwritten. The operation is idempotent and non-fatal. Uses
# $PATH_AUDIENCE (set by the caller) as the tail clause of the log hint when
# the resolved bin dir isn't already on $PATH.
install_on_path() {
	[ -x "$BIN" ] || return 0
	dir="$HOME/.local/bin"
	dest="$dir/cenci"
	mkdir -p "$dir" 2>/dev/null || true
	if [ -e "$dest" ] && [ ! -L "$dest" ]; then
		log "$dest exists and is not a symlink; left untouched"
		return 0
	fi
	if ! ln -sf "$BIN" "$dest" 2>/dev/null; then
		log "could not link cenci at $dest"
		return 0
	fi
	case ":$PATH:" in
	*":$dir:"*) log "linked cenci at $dest" ;;
	*) log "linked cenci at $dest; add \"$dir\" to your PATH so $PATH_AUDIENCE" ;;
	esac
	return 0
}

install_binary || true
install_on_path || true
start_daemon
exit 0
