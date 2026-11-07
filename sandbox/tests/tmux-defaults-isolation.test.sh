#!/usr/bin/env bash
# Real-tmux regressions for the shipped default tmux config
# (watch/plugin/tmux/tmux.conf, #645). Everything runs against real tmux
# servers confined to a private TMUX_TMPDIR:
#
#   - isolation: a "user" server on its own -L socket (custom status-style, a
#     custom key binding, two sessions) is snapshotted five ways before and
#     after the cenci server starts from an installed-shaped copy of the asset;
#     the snapshots must be byte-identical (dedicated-socket design).
#   - options: the cenci server carries the documented defaults.
#   - rendering: @cenci-symbol / @cenci-style / @cenci-headroom-<agent> reach
#     #{E:window-status-format} and #{E:status-right} as the config promises.
#   - window-local: a fresh window has an empty window-local
#     window-status-format, so the watch frontend's window-local prepend
#     (watch/internal/frontend/tmux/window.go) cannot double the marker.
#
# Without tmux the suite skips (exit 0) locally and fails when CI=true, where
# sandbox-ci.yml installs tmux so the isolation criterion is actually proven.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ASSET="${ROOT}/watch/plugin/tmux/tmux.conf"

# An inherited TMUX/TMUX_PANE would make bare tmux calls resolve against the
# developer's real server (watch/docs/tmux.md); drop them before any tmux call.
unset TMUX TMUX_PANE

if ! command -v tmux >/dev/null 2>&1; then
    if [ "${CI:-}" = "true" ]; then
        echo "FAIL: tmux is not installed but CI=true — the isolation suite must run in CI" >&2
        exit 1
    fi
    echo "SKIP: tmux not installed; tmux-defaults-isolation suite not run"
    exit 0
fi

WORK="$(mktemp -d)" || {
    echo "FAIL: could not create a temp dir" >&2
    exit 1
}
if [ -z "${WORK}" ] || [ ! -d "${WORK}" ]; then
    echo "FAIL: temp dir '${WORK}' is not a directory" >&2
    exit 1
fi
export TMUX_TMPDIR="${WORK}"

CENCI_SOCK="cenci"
USER_SOCK="user-$$"

cleanup() {
    # Only sockets inside the private TMUX_TMPDIR are ever addressed.
    tmux -L "${CENCI_SOCK}" kill-server >/dev/null 2>&1 || true
    tmux -L "${USER_SOCK}" kill-server >/dev/null 2>&1 || true
    rm -rf "${WORK:?}"
}
trap cleanup EXIT

FAILED=0
pass() { echo "ok - $1"; }
fail() {
    echo "FAIL - $1" >&2
    FAILED=$((FAILED + 1))
}

assert_eq() { # name actual expected
    if [ "$2" = "$3" ]; then
        pass "$1"
    else
        fail "$1: expected '$3', got '$2'"
    fi
}

assert_contains() { # name haystack needle
    case "$2" in
    *"$3"*) pass "$1" ;;
    *) fail "$1: '$3' not found in '$2'" ;;
    esac
}

assert_not_contains() { # name haystack needle
    case "$2" in
    *"$3"*) fail "$1: unexpected '$3' in '$2'" ;;
    *) pass "$1" ;;
    esac
}

sha256_of() { # file -> hex digest on stdout
    local out
    if command -v sha256sum >/dev/null 2>&1; then
        out="$(sha256sum "$1")" || return 1
    elif command -v shasum >/dev/null 2>&1; then
        out="$(shasum -a 256 "$1")" || return 1
    else
        return 1
    fi
    printf '%s\n' "${out%% *}"
}

ut() { tmux -L "${USER_SOCK}" "$@"; }
ct() { tmux -L "${CENCI_SOCK}" "$@"; }

# assert_cenci_out <name> <expected> <tmux args...>: the cenci-server call must
# exit 0 AND print exactly <expected>. An empty expectation therefore cannot be
# satisfied by a tmux error that happens to print nothing to stdout.
assert_cenci_out() {
    local name="$1" expected="$2" out status=0
    shift 2
    out="$(ct "$@" 2>/dev/null)" || status=$?
    if [ "${status}" -ne 0 ]; then
        fail "${name}: 'tmux $*' exited ${status}"
    else
        assert_eq "${name}" "${out}" "${expected}"
    fi
}

# --- shipped asset ---------------------------------------------------------
BODY="${ASSET}"
if [ -f "${ASSET}" ]; then
    pass "shipped asset exists at watch/plugin/tmux/tmux.conf"
else
    fail "shipped asset watch/plugin/tmux/tmux.conf is missing"
    : >"${WORK}/empty-asset.conf"
    BODY="${WORK}/empty-asset.conf"
fi

# Installed-shaped copy: managed header line + asset, header hash = sha256 of
# every byte after the first line (exactly what install.sh will write).
INSTALLED="${WORK}/cenci-config/cenci/tmux.conf"
mkdir -p "${WORK}/cenci-config/cenci"
HEX="$(sha256_of "${BODY}")" || {
    echo "FAIL: neither sha256sum nor shasum is available" >&2
    exit 1
}
{
    printf '# cenci-managed tmux defaults — sha256=%s\n' "${HEX}"
    cat "${BODY}"
} >"${INSTALLED}"
TAIL_HEX="$(tail -n +2 "${INSTALLED}" >"${WORK}/body.check" && sha256_of "${WORK}/body.check")" || {
    echo "FAIL: could not hash the installed copy body" >&2
    exit 1
}
assert_eq "installed-shaped copy header hash equals sha256 of the body" "${TAIL_HEX}" "${HEX}"

# --- user server with state that must never change -------------------------
ut -f /dev/null new-session -d -s alpha
ut new-session -d -s beta
ut set-option -g status-style 'bg=red,fg=white'
ut bind-key F11 display-message 'user-binding'
ut set-option -t alpha @user-mark alpha-value
ut set-option -t beta @user-mark beta-value

snapshot() { # label
    local label="$1"
    ut show-options -s >"${WORK}/${label}.server" 2>&1
    ut show-options -g >"${WORK}/${label}.global" 2>&1
    ut show-options -gw >"${WORK}/${label}.globalwin" 2>&1
    ut list-keys >"${WORK}/${label}.keys" 2>&1
    ut show-options -t alpha >"${WORK}/${label}.alpha" 2>&1
    ut show-options -t beta >"${WORK}/${label}.beta" 2>&1
}

assert_contains "user server carries its custom status-style" \
    "$(ut show-options -gv status-style)" "bg=red"
snapshot before

# --- cenci server ----------------------------------------------------------
if ct -f "${INSTALLED}" new-session -d -s cenci; then
    pass "cenci server starts from the installed copy (exit 0)"
else
    fail "cenci server failed to start from the installed copy"
fi

# tmux starts and exits 0 even when lines of the -f file fail to parse, so also
# apply the installed copy through the server's own command parser and require a
# clean exit. This is a test-side check only: the shipped asset is never
# applied by anything but tmux's own -f startup path.
if ct source-file "${INSTALLED}"; then
    pass "every line of the installed copy parses on the cenci server (exit 0)"
else
    fail "the installed copy has lines the cenci server cannot parse"
fi

snapshot after
for part in server global globalwin keys alpha beta; do
    if cmp -s "${WORK}/before.${part}" "${WORK}/after.${part}"; then
        pass "user server snapshot '${part}' is byte-identical after the cenci server starts"
    else
        fail "user server snapshot '${part}' changed after the cenci server started"
    fi
done
assert_eq "user server still reports exactly its two sessions" \
    "$(ut list-sessions -F '#{session_name}' | sort | tr '\n' ' ')" "alpha beta "
assert_contains "user server status-style is untouched" "$(ut show-options -gv status-style)" "bg=red"

# --- cenci server options --------------------------------------------------
assert_eq "mouse is on" "$(ct show-options -gv mouse)" "on"
assert_eq "status-position is bottom" "$(ct show-options -gv status-position)" "bottom"
assert_eq "base-index is 1" "$(ct show-options -gv base-index)" "1"
assert_eq "renumber-windows is on" "$(ct show-options -gv renumber-windows)" "on"
assert_eq "focus-events is on" "$(ct show-options -sv focus-events)" "on"
assert_eq "default-terminal is tmux-256color" "$(ct show-options -sv default-terminal)" "tmux-256color"
assert_eq "prefix stays C-b" "$(ct show-options -gv prefix)" "C-b"
assert_cenci_out "status-justify is left" "left" show-options -gv status-justify
assert_cenci_out "status-left is empty" "" show-options -gv status-left
assert_cenci_out "status-right-length is 80" "80" show-options -gv status-right-length
assert_cenci_out "window-status-separator is a single space" " " show-options -gwv window-status-separator
ESC_TIME="$(ct show-options -sv escape-time)"
if [ "${ESC_TIME}" -le 10 ] 2>/dev/null; then
    pass "escape-time is at most 10 ms (${ESC_TIME})"
else
    fail "escape-time should be <= 10, got '${ESC_TIME}'"
fi
GLOBAL_FORMAT="$(ct show-options -gwv window-status-format)"
assert_contains "global window-status-format references @cenci-symbol" "${GLOBAL_FORMAT}" "@cenci-symbol"
assert_contains "global window-status-format references @cenci-style" "${GLOBAL_FORMAT}" "@cenci-style"
CURRENT_FORMAT="$(ct show-options -gwv window-status-current-format)"
assert_contains "global window-status-current-format references @cenci-symbol" "${CURRENT_FORMAT}" "@cenci-symbol"
assert_contains "global window-status-current-format references @cenci-style" "${CURRENT_FORMAT}" "@cenci-style"

# --- marker rendering ------------------------------------------------------
MARKED="$(ct new-window -d -t cenci -n marked -P -F '#{window_id}')"
PLAIN="$(ct new-window -d -t cenci -n plain -P -F '#{window_id}')"
ct set-option -w -t "${MARKED}" @cenci-symbol '▶'
ct set-option -w -t "${MARKED}" @cenci-style 'fg=blue'

MARKED_OUT="$(ct display-message -p -t "${MARKED}" '#{E:window-status-format}')"
assert_contains "marked window renders its @cenci-symbol" "${MARKED_OUT}" '▶'
assert_contains "marked window renders its @cenci-style as #[fg=blue]" "${MARKED_OUT}" '#[fg=blue]'
MARKED_CURRENT="$(ct display-message -p -t "${MARKED}" '#{E:window-status-current-format}')"
assert_contains "marked window renders its symbol in the current format" "${MARKED_CURRENT}" '▶'
assert_contains "marked window renders its style in the current format" "${MARKED_CURRENT}" '#[fg=blue]'

PLAIN_OUT="$(ct display-message -p -t "${PLAIN}" '#{E:window-status-format}')"
assert_contains "window without options renders the ● fallback" "${PLAIN_OUT}" '●'
assert_not_contains "window without options renders no marker symbol" "${PLAIN_OUT}" '▶'

# --- headroom segments -----------------------------------------------------
UNSET_RIGHT="$(ct display-message -p -t "${PLAIN}" '#{E:status-right}')"
assert_not_contains "status-right has no % while headroom is unset" "${UNSET_RIGHT}" '%'
assert_eq "status-right is just the session name while headroom is unset" "${UNSET_RIGHT}" 'cenci'

ct set-option -g @cenci-headroom-claude 73
CLAUDE_RIGHT="$(ct display-message -p -t "${PLAIN}" '#{E:status-right}')"
assert_contains "status-right shows claude headroom 73%" "${CLAUDE_RIGHT}" '73%'

ct set-option -g @cenci-headroom-codex 41
CODEX_RIGHT="$(ct display-message -p -t "${PLAIN}" '#{E:status-right}')"
assert_contains "status-right shows codex headroom 41%" "${CODEX_RIGHT}" '41%'
assert_contains "status-right keeps claude headroom alongside codex" "${CODEX_RIGHT}" '73%'

ct set-option -g @cenci-headroom-claude 0
ZERO_RIGHT="$(ct display-message -p -t "${PLAIN}" '#{E:status-right}')"
assert_contains "a headroom of 0 is still shown (0%)" "${ZERO_RIGHT}" '0%'

# --- window-local format stays empty ---------------------------------------
# Positive control: on a scratch window, a window-local format IS reported by
# the very same show-options call, so an empty answer below means "unset", not
# "the query failed or reads the wrong scope".
SCRATCH="$(ct new-window -d -t cenci -n scratch -P -F '#{window_id}')"
ct set-option -w -t "${SCRATCH}" window-status-format 'control-format'
assert_cenci_out "positive control: a window-local window-status-format is reported" \
    "control-format" show-options -wv -t "${SCRATCH}" window-status-format
ct set-option -w -t "${SCRATCH}" window-status-current-format 'control-current-format'
assert_cenci_out "positive control: a window-local window-status-current-format is reported" \
    "control-current-format" show-options -wv -t "${SCRATCH}" window-status-current-format

assert_cenci_out "fresh window has an empty window-local window-status-format" \
    "" show-options -wv -t "${PLAIN}" window-status-format
assert_cenci_out "fresh window has an empty window-local window-status-current-format" \
    "" show-options -wv -t "${PLAIN}" window-status-current-format

if [ "${FAILED}" -ne 0 ]; then
    echo "${FAILED} assertion(s) failed" >&2
    exit 1
fi
echo "all tmux-defaults isolation assertions passed"
