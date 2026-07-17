#!/usr/bin/env bash
# End-to-end regression tests for the `install.sh uninstall` MODE (#457).
#
# TDD RED phase: install.sh has no `uninstall` MODE yet, so every case here
# currently fails — `./install.sh uninstall` hits the unrecognized-option
# path (`die "unknown option 'uninstall' ..."`) instead of doing anything.
# This suite pins the CLI surface uninstall must implement:
#   - per-client plugin uninstall (qualified-then-bare fallback, mirroring
#     plugin_cmd) + marketplace removal, with pinned exact call strings
#   - PATH link removal (~/.local/bin/{cenci,cn,cenci-installer}), refusing
#     to clobber a real (non-symlink) file — mirrors link_launcher in reverse
#   - daemon stop (`cenci socket-dir` then `cenci daemon stop`) + managed
#     state dir removal
#   - ~/.config/cenci/config.json removal + rmdir of the now-empty dir
#   - a single y/n confirmation gate: --yes proceeds non-interactively;
#     no --yes without a tty safely aborts and removes nothing
#   - lazyboards removal strictly opt-in via --lazyboards
#   - shell rc files are never edited, only a manual removal note is printed
#   - env -i scrub of the mocked subprocess environment (regression #353)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# ------------------------------------------------------------- mock tools ----

make_common_tools() {
    local bin="$1" tool
    mkdir -p "${bin}"
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep \
        pkill pgrep nohup chmod sed head tail cut tr rm mktemp rmdir basename id; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
}

# make_claude installs a claude stub that logs every invocation (space-joined
# argv) to CALL_LOG. Removal verbs' exit codes are controllable per test via
# CLAUDE_UNINSTALL_EXIT (qualified "<p>@cenci" form), CLAUDE_UNINSTALL_BARE_EXIT
# (bare fallback form), and CLAUDE_MARKETPLACE_REMOVE_EXIT — mirroring
# plugin_cmd's qualified-then-bare fallback shape (install.sh:153) in reverse.
make_claude() {
    local bin="$1"
    cat >"${bin}/claude" <<'EOF'
#!/bin/sh
printf 'claude %s\n' "$*" >>"${CALL_LOG}"
# Regression probe (#353): a leaked host secret in this subprocess's
# environment must surface in the captured call log so the sentinel-secret
# case can prove the harness's env -i scrub keeps host secrets out.
[ -n "${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "${OPENAI_API_KEY}" >>"${CALL_LOG}"
[ -n "${CONTEXT7_API_KEY:-}" ] && printf 'env-leak CONTEXT7_API_KEY=%s\n' "${CONTEXT7_API_KEY}" >>"${CALL_LOG}"
case "$*" in
  "plugin uninstall "*"@cenci")
    exit "${CLAUDE_UNINSTALL_EXIT:-0}"
    ;;
  "plugin uninstall "*)
    exit "${CLAUDE_UNINSTALL_BARE_EXIT:-0}"
    ;;
  "plugin marketplace remove cenci")
    exit "${CLAUDE_MARKETPLACE_REMOVE_EXIT:-0}"
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/claude"
}

# make_codex mirrors make_claude for the codex CLI's removal verbs
# ("plugin remove <p>@cenci" qualified-then-bare, "plugin marketplace remove
# cenci"), per the ticket Q&A's resolved verb decision.
make_codex() {
    local bin="$1"
    cat >"${bin}/codex" <<'EOF'
#!/bin/sh
printf 'codex %s\n' "$*" >>"${CALL_LOG}"
case "$*" in
  "plugin remove "*"@cenci")
    exit "${CODEX_REMOVE_EXIT:-0}"
    ;;
  "plugin remove "*)
    exit "${CODEX_REMOVE_BARE_EXIT:-0}"
    ;;
  "plugin marketplace remove cenci")
    exit "${CODEX_MARKETPLACE_REMOVE_EXIT:-0}"
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/codex"
}

# make_cenci_daemon writes a fake cenci binary (the version-pinned plugin
# cache binary, resolvable via ~/.local/bin/cenci) that logs every invocation
# to CALL_LOG, prints socket_dir on `socket-dir`, and exits 0 on
# `daemon stop` — the two lifecycle calls uninstall must make (in that
# order, "while the binary still exists") before removing anything else.
make_cenci_daemon() {
    local path="$1" socket_dir="$2"
    mkdir -p "$(dirname "${path}")"
    cat >"${path}" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>"\${CALL_LOG}"
[ -n "\${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "\${OPENAI_API_KEY}" >>"\${CALL_LOG}"
[ -n "\${CONTEXT7_API_KEY:-}" ] && printf 'env-leak CONTEXT7_API_KEY=%s\n' "\${CONTEXT7_API_KEY}" >>"\${CALL_LOG}"
if [ "\${1:-}" = socket-dir ]; then
    printf '%s\n' "${socket_dir}"
    exit 0
fi
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = stop ]; then
    exit 0
fi
exit 0
EOF
    chmod +x "${path}"
}

# ------------------------------------------------------------- fixtures ----

# prepare_full_layout <name> — a fake HOME that looks like a completed dual-
# client (claude + codex) install: marketplace checkouts, a version-pinned
# claude plugin cache with a live cenci binary, the three link_launcher
# symlinks (cenci, cn, cenci-installer), a populated ~/.config/cenci/, and a
# managed socket dir with marker files simulating live daemon state.
prepare_full_layout() {
    local name="$1"
    local home="${WORK}/${name}/home" bin="${WORK}/${name}/bin"
    local call_log="${WORK}/${name}/calls"
    local socket_dir="${WORK}/${name}/socket-dir"
    mkdir -p "${home}" "${bin}" "${socket_dir}"
    : >"${call_log}"
    touch "${socket_dir}/cenci.pid" "${socket_dir}/cenci.sock"

    make_common_tools "${bin}"
    make_claude "${bin}"
    make_codex "${bin}"

    mkdir -p "${home}/.claude/plugins/marketplaces/cenci"
    mkdir -p "${home}/.codex/plugins/marketplaces/cenci"
    touch "${home}/.claude/plugins/marketplaces/cenci/marker"
    touch "${home}/.codex/plugins/marketplaces/cenci/marker"

    local cache_root="${home}/.claude/plugins/cache/cenci/cenci-watch/1.0.0"
    local cache_bin="${cache_root}/bin/cenci"
    make_cenci_daemon "${cache_bin}" "${socket_dir}"
    mkdir -p "${cache_root}/.claude-plugin"
    printf '{"name":"cenci-watch","version":"1.0.0"}\n' >"${cache_root}/.claude-plugin/plugin.json"

    mkdir -p "${home}/.local/bin"
    ln -s "${cache_bin}" "${home}/.local/bin/cenci"
    ln -s "${home}/.local/bin/cenci" "${home}/.local/bin/cn"
    printf '#!/bin/sh\nexit 0\n' >"${home}/.claude/plugins/marketplaces/cenci/cenci"
    chmod +x "${home}/.claude/plugins/marketplaces/cenci/cenci"
    ln -s "${home}/.claude/plugins/marketplaces/cenci/cenci" "${home}/.local/bin/cenci-installer"

    mkdir -p "${home}/.config/cenci"
    printf '{}\n' >"${home}/.config/cenci/config.json"

    LAYOUT_HOME="${home}"
    LAYOUT_BIN="${bin}"
    LAYOUT_CALL_LOG="${call_log}"
    LAYOUT_SOCKET_DIR="${socket_dir}"
    LAYOUT_CACHE_ROOT="${cache_root}"
    LAYOUT_CLAUDE_CHECKOUT="${home}/.claude/plugins/marketplaces/cenci"
    LAYOUT_CODEX_CHECKOUT="${home}/.codex/plugins/marketplaces/cenci"
}

# add_lazyboards <home> — pre-creates a managed lazyboards binary + config,
# mirroring lazyboards_managed_binary's guard (a real, non-symlink executable
# at ~/.local/bin/lazyboards) and seed_lazyboards_config's target file.
add_lazyboards() {
    local home="$1"
    mkdir -p "${home}/.local/bin" "${home}/.config/lazyboards"
    printf '#!/bin/sh\nexit 0\n' >"${home}/.local/bin/lazyboards"
    chmod +x "${home}/.local/bin/lazyboards"
    printf 'columns: []\n' >"${home}/.config/lazyboards/config.yml"
}

# run_uninstall [ENV=val ...] -- [flag ...] — invokes `install.sh uninstall`
# against the layout set by the last prepare_full_layout call, scrubbing the
# subprocess environment via `env -i HOME=... PATH=... CALL_LOG=...` plus any
# extra ENV=val pairs before the `--` separator (regression #353: never let
# host secrets ride along implicitly).
run_uninstall() {
    local env_args=() flag_args=() seen_sep=0 a
    for a in "$@"; do
        if [[ "${seen_sep}" -eq 0 && "${a}" == "--" ]]; then
            seen_sep=1
            continue
        fi
        if [[ "${seen_sep}" -eq 0 ]]; then
            env_args+=("${a}")
        else
            flag_args+=("${a}")
        fi
    done
    set +e
    env -i HOME="${LAYOUT_HOME}" PATH="${LAYOUT_BIN}" CALL_LOG="${LAYOUT_CALL_LOG}" \
        ${env_args[@]+"${env_args[@]}"} \
        bash "${ROOT}/install.sh" uninstall ${flag_args[@]+"${flag_args[@]}"} \
        >"${WORK}/last-output" 2>&1
    UNINSTALL_EXIT=$?
    set -e
    UNINSTALL_OUTPUT="${WORK}/last-output"
}

assert_contains() {
    local file="$1" needle="$2"
    if ! grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: expected '${needle}' in ${file}" >&2
        sed -n '1,200p' "${file}" >&2
        exit 1
    fi
}

assert_not_contains() {
    local file="$1" needle="$2"
    if grep -Fq -- "${needle}" "${file}"; then
        echo "FAIL: did not expect '${needle}' in ${file}" >&2
        sed -n '1,200p' "${file}" >&2
        exit 1
    fi
}

echo "uninstall.test.sh"

# --- case 1: --yes full removal (dual client) -------------------------------
echo "case: --yes removes plugins + marketplace registration for every detected client, PATH links, daemon, socket dir, and config"
prepare_full_layout full-yes
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

assert_contains "${LAYOUT_CALL_LOG}" "claude plugin uninstall cenci@cenci"
assert_contains "${LAYOUT_CALL_LOG}" "claude plugin uninstall cenci-watch@cenci"
assert_contains "${LAYOUT_CALL_LOG}" "claude plugin uninstall cenci-sandbox@cenci"
assert_contains "${LAYOUT_CALL_LOG}" "claude plugin marketplace remove cenci"
assert_contains "${LAYOUT_CALL_LOG}" "codex plugin remove cenci@cenci"
assert_contains "${LAYOUT_CALL_LOG}" "codex plugin remove cenci-watch@cenci"
assert_contains "${LAYOUT_CALL_LOG}" "codex plugin remove cenci-sandbox@cenci"
assert_contains "${LAYOUT_CALL_LOG}" "codex plugin marketplace remove cenci"

[[ ! -e "${LAYOUT_HOME}/.local/bin/cenci" ]]
[[ ! -e "${LAYOUT_HOME}/.local/bin/cn" ]]
[[ ! -e "${LAYOUT_HOME}/.local/bin/cenci-installer" ]]

if ! grep -qx "daemon stop" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected 'daemon stop' invocation in ${LAYOUT_CALL_LOG}" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
[[ ! -d "${LAYOUT_SOCKET_DIR}" ]]
[[ ! -e "${LAYOUT_HOME}/.config/cenci/config.json" ]]
[[ ! -d "${LAYOUT_HOME}/.config/cenci" ]]

# --- case 2: CLI-absent/failure fallback -------------------------------------
echo "case: a failing claude removal verb and an absent codex CLI both fall back to rm -rf of the marketplace checkout, touching nothing else"
prepare_full_layout fallback
mkdir -p "${LAYOUT_HOME}/.claude/plugins/marketplaces/other-repo"
touch "${LAYOUT_HOME}/.claude/plugins/marketplaces/other-repo/marker"
rm -f "${LAYOUT_BIN}/codex"
run_uninstall CLAUDE_UNINSTALL_EXIT=1 CLAUDE_UNINSTALL_BARE_EXIT=1 CLAUDE_MARKETPLACE_REMOVE_EXIT=1 -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

[[ ! -d "${LAYOUT_CLAUDE_CHECKOUT}" ]]
[[ ! -d "${LAYOUT_CODEX_CHECKOUT}" ]]
[[ ! -d "${LAYOUT_CACHE_ROOT}" ]]
[[ -d "${LAYOUT_HOME}/.claude/plugins/marketplaces/other-repo" ]]
[[ -f "${LAYOUT_HOME}/.claude/plugins/marketplaces/other-repo/marker" ]]

# --- case 3: real-file guard --------------------------------------------------
echo "case: a real (non-symlink) file at ~/.local/bin/cn is left untouched and a warning is printed"
prepare_full_layout real-file-guard
rm -f "${LAYOUT_HOME}/.local/bin/cn"
printf 'user-owned script, not ours\n' >"${LAYOUT_HOME}/.local/bin/cn"
chmod +x "${LAYOUT_HOME}/.local/bin/cn"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
[[ ! -L "${LAYOUT_HOME}/.local/bin/cn" ]]
[[ -f "${LAYOUT_HOME}/.local/bin/cn" ]]
if [[ "$(cat "${LAYOUT_HOME}/.local/bin/cn")" != "user-owned script, not ours" ]]; then
    echo "FAIL: expected the real cn file's contents to survive uninstall untouched" >&2
    exit 1
fi
assert_contains "${UNINSTALL_OUTPUT}" ".local/bin/cn exists and is not a symlink — left untouched"

# --- case 4: non-interactive without --yes safely aborts ---------------------
echo "case: non-interactive (no tty) run without --yes aborts before removing anything"
prepare_full_layout no-confirm
run_uninstall --
[[ "${UNINSTALL_EXIT}" -ne 0 ]]
assert_contains "${UNINSTALL_OUTPUT}" "confirmation"
assert_contains "${UNINSTALL_OUTPUT}" "--yes"

if [[ -s "${LAYOUT_CALL_LOG}" ]]; then
    echo "FAIL: expected no plugin/marketplace/daemon calls when uninstall aborts for lack of confirmation" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
[[ -L "${LAYOUT_HOME}/.local/bin/cenci" ]]
[[ -L "${LAYOUT_HOME}/.local/bin/cn" ]]
[[ -L "${LAYOUT_HOME}/.local/bin/cenci-installer" ]]
[[ -f "${LAYOUT_HOME}/.config/cenci/config.json" ]]
[[ -d "${LAYOUT_SOCKET_DIR}" ]]

# --- case 5: lazyboards left untouched by default -----------------------------
echo "case: lazyboards is left untouched by default (no --lazyboards, no interactive opt-in)"
prepare_full_layout lazyboards-default
add_lazyboards "${LAYOUT_HOME}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
[[ ! -e "${LAYOUT_HOME}/.config/cenci/config.json" ]]
[[ -f "${LAYOUT_HOME}/.local/bin/lazyboards" ]]
[[ -f "${LAYOUT_HOME}/.config/lazyboards/config.yml" ]]

# --- case 6: --lazyboards removes both binary and config ----------------------
echo "case: --lazyboards removes both the managed binary and its config"
prepare_full_layout lazyboards-opt-in
add_lazyboards "${LAYOUT_HOME}"
run_uninstall -- --yes --lazyboards
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
[[ ! -e "${LAYOUT_HOME}/.local/bin/lazyboards" ]]
[[ ! -e "${LAYOUT_HOME}/.config/lazyboards/config.yml" ]]

# --- case 7: rc-file invariant -------------------------------------------------
echo "case: uninstall never edits shell rc files, and prints a manual removal note naming the file and line"
prepare_full_layout rc-invariant
bashrc="${LAYOUT_HOME}/.bashrc"
cat >"${bashrc}" <<'EOF'
# custom user prefs, unrelated to cenci
export PATH="$HOME/.local/bin:$PATH"
alias ll='ls -la'
EOF
bashrc_before="$(cat "${bashrc}")"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
bashrc_after="$(cat "${bashrc}")"
if [[ "${bashrc_before}" != "${bashrc_after}" ]]; then
    echo "FAIL: ~/.bashrc was modified by uninstall — rc files must never be edited" >&2
    diff <(printf '%s' "${bashrc_before}") <(printf '%s' "${bashrc_after}") >&2 || true
    exit 1
fi
assert_contains "${UNINSTALL_OUTPUT}" ".bashrc"
# shellcheck disable=SC2016 # literal line we expect printed verbatim, not expanded here
assert_contains "${UNINSTALL_OUTPUT}" 'export PATH="$HOME/.local/bin:$PATH"'

# --- case 8: sentinel-secret regression (#353) --------------------------------
echo "case: host secrets in the test runner's own environment never reach CALL_LOG or captured output"
prepare_full_layout sentinel-secrets
export OPENAI_API_KEY="sk-test-sentinel-should-not-leak"
export CONTEXT7_API_KEY="ctx7-test-sentinel-should-not-leak"
run_uninstall -- --yes
unset OPENAI_API_KEY CONTEXT7_API_KEY
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
if ! grep -qx "daemon stop" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected a real uninstall run ('daemon stop' in the call log) to prove the harness actually exercised the removal path" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
assert_not_contains "${LAYOUT_CALL_LOG}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${UNINSTALL_OUTPUT}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${LAYOUT_CALL_LOG}" "ctx7-test-sentinel-should-not-leak"
assert_not_contains "${UNINSTALL_OUTPUT}" "ctx7-test-sentinel-should-not-leak"

# --- case 9: no resolvable binary falls back to the pkill/pgrep daemon stop -
echo "case: with no resolvable cenci binary at all (no ~/.local/bin/cenci, no plugin-cache binary), uninstall falls back to the pkill/pgrep daemon-stop path and actually kills a live cenci-daemon-shaped process"
name="no-binary-fallback"
home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin"
call_log="${WORK}/${name}/calls"
mkdir -p "${home}" "${bin}"
: >"${call_log}"

make_common_tools "${bin}"
make_claude "${bin}"
make_codex "${bin}"

mkdir -p "${home}/.claude/plugins/marketplaces/cenci"
mkdir -p "${home}/.codex/plugins/marketplaces/cenci"
touch "${home}/.claude/plugins/marketplaces/cenci/marker"
touch "${home}/.codex/plugins/marketplaces/cenci/marker"
mkdir -p "${home}/.config/cenci"
printf '{}\n' >"${home}/.config/cenci/config.json"
# Deliberately no ~/.local/bin/cenci symlink and no plugins/cache/cenci
# binary, so resolve_uninstall_cenci_binary finds nothing and
# uninstall_stop_daemon must take the pkill/pgrep fallback path.

# A live process shaped like a hand-started `cenci daemon`, standing in for
# a daemon uninstall must confirm gone via the fallback's pgrep re-check —
# unrelated to any resolvable binary path, matched purely by cmdline shape.
fake_daemon_dir="${WORK}/${name}/fake-daemon"
mkdir -p "${fake_daemon_dir}"
cat >"${fake_daemon_dir}/cenci" <<'EOF'
#!/bin/sh
sleep 30
EOF
chmod +x "${fake_daemon_dir}/cenci"
"${fake_daemon_dir}/cenci" daemon &
FAKE_DAEMON_PID=$!
sleep 0.2

LAYOUT_HOME="${home}"
LAYOUT_BIN="${bin}"
LAYOUT_CALL_LOG="${call_log}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

if kill -0 "${FAKE_DAEMON_PID}" 2>/dev/null; then
    echo "FAIL: expected the pkill/pgrep fallback to have killed the fake cenci-daemon process" >&2
    kill -9 "${FAKE_DAEMON_PID}" 2>/dev/null || true
    cat "${UNINSTALL_OUTPUT}" >&2
    exit 1
fi
kill -9 "${FAKE_DAEMON_PID}" 2>/dev/null || true

# Still exercised the rest of the removal path without hanging or crashing.
[[ ! -e "${home}/.config/cenci/config.json" ]]

echo "passed: uninstall MODE removes plugins/marketplace registration, PATH links, daemon + state, and config behind a single confirmation gate; lazyboards stays opt-in; rc files are never edited; subprocess env stays scrubbed"
