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
#
# Cases 11+ (#458, TDD RED phase against the current uninstall_sandbox_cleanup
# no-op stub): machine-wide sandbox cleanup — every cenci-owned container
# (claude-cenci-*/codex-cenci-*/opencode-cenci-*, running or stopped —
# broader than `cenci sandbox prune`, which only targets exited/created),
# every cenci-owned image (the cenci-sandbox:latest monolith, every
# cenci-sandbox-<slug>:latest per-repo image, and every cenci-sandbox-base
# tag incl. its :latest alias, with an optional podman localhost/ prefix),
# and every cenci-owned volume (claude|codex|opencode-cenci-home-* and
# cenci-agent-cli-{claude,codex,opencode} — #528) — removed across every
# repo on the machine, before plugin-cache/link removal, with the
# confirmation-gate output showing counts/names, a clean no-op when no
# container runtime is installed, and the same env -i secret scrub as the
# rest of this suite.
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

# make_container_runtime installs a docker/podman stub (named by the
# "runtime" argument) that logs every invocation to CALL_LOG — space-joined
# argv, prefixed with the binary name, mirroring make_claude/make_codex's
# logging convention — and answers the three read-only enumeration calls
# uninstall's machine-wide sandbox cleanup must make by printing the
# matching fixture file's contents back:
#   ps -a --format {{.Names}}                -> containers fixture
#   images --format {{.Repository}}:{{.Tag}} -> images fixture
#   volume ls --format {{.Name}}             -> volumes fixture
# Every other invocation (the rm -f / rmi / volume rm removal calls) is just
# logged and exits 0, so tests assert on removal calls purely via CALL_LOG.
# Also probes the same #353 env-leak sentinels as the other mocks in this
# file, so the sandbox-cleanup path is covered by the same secret-scrub
# regression as plugin/daemon removal.
#
# An optional 6th "fail" argument ("ps", "images", or "volume") makes that
# one enumeration call print nothing and exit 1 instead — simulating a
# runtime enumeration failure (permission error, daemon hiccup, etc.) while
# the other two enumeration calls still behave normally, so tests can pin
# collect_sandbox_cleanup_targets's per-category warn-and-skip handling
# (install.sh's `if ! out="$(... 2>/dev/null)"; then warn ...; out=""; fi`
# guards) without any change to production code.
make_container_runtime() {
    local bin="$1" runtime="$2" containers="$3" images="$4" volumes="$5" fail="${6:-}"
    cat >"${bin}/${runtime}" <<EOF
#!/bin/sh
printf '${runtime} %s\n' "\$*" >>"\${CALL_LOG}"
[ -n "\${OPENAI_API_KEY:-}" ] && printf 'env-leak OPENAI_API_KEY=%s\n' "\${OPENAI_API_KEY}" >>"\${CALL_LOG}"
[ -n "\${CONTEXT7_API_KEY:-}" ] && printf 'env-leak CONTEXT7_API_KEY=%s\n' "\${CONTEXT7_API_KEY}" >>"\${CALL_LOG}"
case "\$*" in
  "ps -a --format {{.Names}}")
    if [ "${fail}" = "ps" ]; then
        exit 1
    fi
    cat "${containers}" 2>/dev/null
    ;;
  "images --format {{.Repository}}:{{.Tag}}")
    if [ "${fail}" = "images" ]; then
        exit 1
    fi
    cat "${images}" 2>/dev/null
    ;;
  "volume ls --format {{.Name}}")
    if [ "${fail}" = "volume" ]; then
        exit 1
    fi
    cat "${volumes}" 2>/dev/null
    ;;
esac
exit 0
EOF
    chmod +x "${bin}/${runtime}"
}

# write_fixture <path> [line ...] — writes newline-separated fixture entries
# consumed by make_container_runtime's ps/images/volume-ls stand-ins.
write_fixture() {
    local path="$1"
    shift
    : >"${path}"
    local line
    for line in "$@"; do
        printf '%s\n' "${line}" >>"${path}"
    done
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

# assert_before <file> <needle_a> <needle_b> — fails unless the first line
# matching needle_a appears strictly before the first line matching
# needle_b, proving call *order* rather than just presence (case 14, #458:
# the sandbox runtime sweep must run before plugin-cache/link removal).
assert_before() {
    local file="$1" a="$2" b="$3" line_a line_b
    line_a="$(grep -Fn -- "${a}" "${file}" | head -n1 | cut -d: -f1 || true)"
    line_b="$(grep -Fn -- "${b}" "${file}" | head -n1 | cut -d: -f1 || true)"
    if [[ -z "${line_a}" ]]; then
        echo "FAIL: expected '${a}' in ${file}" >&2
        cat "${file}" >&2
        exit 1
    fi
    if [[ -z "${line_b}" ]]; then
        echo "FAIL: expected '${b}' in ${file}" >&2
        cat "${file}" >&2
        exit 1
    fi
    if [[ "${line_a}" -ge "${line_b}" ]]; then
        echo "FAIL: expected '${a}' (line ${line_a}) before '${b}' (line ${line_b}) in ${file}" >&2
        cat "${file}" >&2
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

# --- case 10: XDG_RUNTIME_DIR default-socket-dir fallback ----------------------
echo "case: with no resolvable binary but XDG_RUNTIME_DIR set, uninstall removes the daemon's real managed-state dir (\$XDG_RUNTIME_DIR/cenci — not a doubled \$XDG_RUNTIME_DIR/cenci/cenci)"
name="xdg-default-socket"
home="${WORK}/${name}/home"
bin="${WORK}/${name}/bin"
call_log="${WORK}/${name}/calls"
xdg="${WORK}/${name}/xdg"
mkdir -p "${home}" "${bin}" "${xdg}"
: >"${call_log}"

make_common_tools "${bin}"
make_claude "${bin}"
make_codex "${bin}"

mkdir -p "${home}/.config/cenci"
printf '{}\n' >"${home}/.config/cenci/config.json"
# No ~/.local/bin/cenci and no plugins/cache/cenci binary, so
# resolve_uninstall_cenci_binary finds nothing and uninstall_stop_daemon
# cannot ask `socket-dir` — it must compute the default from XDG_RUNTIME_DIR.
# The daemon nests a "cenci" segment under its runtime base (SocketDir in
# watch/pkg/watch/socket.go), so the real managed-state dir is
# $XDG_RUNTIME_DIR/cenci — a doubled $XDG_RUNTIME_DIR/cenci/cenci would leak it.
real_socket_dir="${xdg}/cenci"
mkdir -p "${real_socket_dir}"
touch "${real_socket_dir}/cenci.pid" "${real_socket_dir}/cenci.sock"

LAYOUT_HOME="${home}"
LAYOUT_BIN="${bin}"
LAYOUT_CALL_LOG="${call_log}"
run_uninstall XDG_RUNTIME_DIR="${xdg}" -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

if [[ -d "${real_socket_dir}" ]]; then
    echo "FAIL: expected the daemon's real managed-state dir (${real_socket_dir}) to be removed via the XDG_RUNTIME_DIR default; a doubled cenci/cenci path would leave it behind" >&2
    cat "${UNINSTALL_OUTPUT}" >&2
    exit 1
fi
[[ ! -e "${home}/.config/cenci/config.json" ]]

# --- case 11: machine-wide sandbox sweep ---------------------------------------
echo "case: machine-wide sandbox sweep removes every cenci-owned container/image/volume across multiple repos (including OpenCode) and leaves non-cenci objects (and OpenCode-look-alikes) untouched"
prepare_full_layout sandbox-sweep
containers_fixture="${WORK}/sandbox-sweep/containers"
images_fixture="${WORK}/sandbox-sweep/images"
volumes_fixture="${WORK}/sandbox-sweep/volumes"
write_fixture "${containers_fixture}" \
    "claude-cenci-repo-a" "codex-cenci-repo-b" "claude-cenci-repo-c" \
    "opencode-cenci-repo-d" \
    "unrelated-container" "web-app"
write_fixture "${images_fixture}" \
    "cenci-sandbox:latest" "cenci-sandbox-repo-a:latest" \
    "cenci-sandbox-base:abc123def456" "cenci-sandbox-base:latest" \
    "nginx:latest" "myapp:1.0"
write_fixture "${volumes_fixture}" \
    "claude-cenci-home-repo-a" "codex-cenci-home-repo-b" "opencode-cenci-home-repo-d" \
    "cenci-agent-cli-claude" "cenci-agent-cli-codex" "cenci-agent-cli-opencode" \
    "random-volume" "pgdata" "opencode-elsewhere" "random-opencode-volume"
make_container_runtime "${LAYOUT_BIN}" docker "${containers_fixture}" "${images_fixture}" "${volumes_fixture}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

for name in claude-cenci-repo-a codex-cenci-repo-b claude-cenci-repo-c opencode-cenci-repo-d; do
    assert_contains "${LAYOUT_CALL_LOG}" "docker rm -f ${name}"
done
for tag in cenci-sandbox:latest cenci-sandbox-repo-a:latest \
    cenci-sandbox-base:abc123def456 cenci-sandbox-base:latest; do
    assert_contains "${LAYOUT_CALL_LOG}" "docker rmi ${tag}"
done
for vol in claude-cenci-home-repo-a codex-cenci-home-repo-b opencode-cenci-home-repo-d \
    cenci-agent-cli-claude cenci-agent-cli-codex cenci-agent-cli-opencode; do
    assert_contains "${LAYOUT_CALL_LOG}" "docker volume rm ${vol}"
done
for untouched in "docker rm -f unrelated-container" "docker rm -f web-app" \
    "docker rmi nginx:latest" "docker rmi myapp:1.0" \
    "docker volume rm random-volume" "docker volume rm pgdata" \
    "docker volume rm opencode-elsewhere" "docker volume rm random-opencode-volume"; do
    assert_not_contains "${LAYOUT_CALL_LOG}" "${untouched}"
done

# --- case 12: running container force-removal -----------------------------------
echo "case: a running *-cenci-* container is force-removed (broader than 'cenci sandbox prune', which only targets exited/created containers)"
prepare_full_layout sandbox-running
containers_fixture="${WORK}/sandbox-running/containers"
images_fixture="${WORK}/sandbox-running/images"
volumes_fixture="${WORK}/sandbox-running/volumes"
write_fixture "${containers_fixture}" "claude-cenci-live-repo"
write_fixture "${images_fixture}"
write_fixture "${volumes_fixture}"
make_container_runtime "${LAYOUT_BIN}" docker "${containers_fixture}" "${images_fixture}" "${volumes_fixture}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${LAYOUT_CALL_LOG}" "docker ps -a --format {{.Names}}"
assert_not_contains "${LAYOUT_CALL_LOG}" "--filter status=exited"
assert_not_contains "${LAYOUT_CALL_LOG}" "--filter status=created"
assert_contains "${LAYOUT_CALL_LOG}" "docker rm -f claude-cenci-live-repo"

# --- case 13: confirmation list shows counts/names -------------------------------
echo "case: the collect step's output (before the confirmation gate) shows counts and names of sandbox containers/images/volumes that will be removed, including OpenCode-owned ones"
prepare_full_layout sandbox-collect
containers_fixture="${WORK}/sandbox-collect/containers"
images_fixture="${WORK}/sandbox-collect/images"
volumes_fixture="${WORK}/sandbox-collect/volumes"
write_fixture "${containers_fixture}" "claude-cenci-repo-a" "opencode-cenci-repo-a"
write_fixture "${images_fixture}" "cenci-sandbox:latest"
write_fixture "${volumes_fixture}" "claude-cenci-home-repo-a" "cenci-agent-cli-claude" "opencode-cenci-home-repo-a" "cenci-agent-cli-opencode"
make_container_runtime "${LAYOUT_BIN}" docker "${containers_fixture}" "${images_fixture}" "${volumes_fixture}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${UNINSTALL_OUTPUT}" "2 cenci sandbox container(s) across every repo on this machine: claude-cenci-repo-a, opencode-cenci-repo-a"
assert_contains "${UNINSTALL_OUTPUT}" "1 cenci sandbox image(s) across every repo on this machine: cenci-sandbox:latest"
assert_contains "${UNINSTALL_OUTPUT}" "4 cenci sandbox volume(s) across every repo on this machine: claude-cenci-home-repo-a, cenci-agent-cli-claude, opencode-cenci-home-repo-a, cenci-agent-cli-opencode"

# --- case 14: ordering — runtime sweep before plugin-cache/link removal ---------
echo "case: the sandbox container/image/volume sweep runs before plugin-cache and PATH-link removal"
prepare_full_layout sandbox-ordering
containers_fixture="${WORK}/sandbox-ordering/containers"
images_fixture="${WORK}/sandbox-ordering/images"
volumes_fixture="${WORK}/sandbox-ordering/volumes"
write_fixture "${containers_fixture}" "claude-cenci-repo-a"
write_fixture "${images_fixture}" "cenci-sandbox:latest"
write_fixture "${volumes_fixture}" "claude-cenci-home-repo-a"
make_container_runtime "${LAYOUT_BIN}" docker "${containers_fixture}" "${images_fixture}" "${volumes_fixture}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_before "${LAYOUT_CALL_LOG}" "docker rm -f claude-cenci-repo-a" "claude plugin uninstall cenci@cenci"
assert_before "${LAYOUT_CALL_LOG}" "docker rmi cenci-sandbox:latest" "claude plugin uninstall cenci@cenci"
assert_before "${LAYOUT_CALL_LOG}" "docker volume rm claude-cenci-home-repo-a" "claude plugin uninstall cenci@cenci"

# --- case 15: no container runtime installed is a clean no-op -------------------
echo "case: with no docker/podman on PATH, sandbox cleanup is a clean no-op — no error, empty removal list, the rest of uninstall still proceeds"
prepare_full_layout sandbox-none
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_not_contains "${UNINSTALL_OUTPUT}" "cenci sandbox container(s)"
assert_not_contains "${UNINSTALL_OUTPUT}" "cenci sandbox image(s)"
assert_not_contains "${UNINSTALL_OUTPUT}" "cenci sandbox volume(s)"
assert_not_contains "${LAYOUT_CALL_LOG}" "docker "
assert_not_contains "${LAYOUT_CALL_LOG}" "podman "
if ! grep -qx "daemon stop" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected the rest of uninstall (daemon stop) to still run when no container runtime is present" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi

# --- case 16: podman localhost/-prefixed images ----------------------------------
echo "case: podman's localhost/-prefixed image tags are matched and removed the same as unprefixed tags"
prepare_full_layout sandbox-podman
containers_fixture="${WORK}/sandbox-podman/containers"
images_fixture="${WORK}/sandbox-podman/images"
volumes_fixture="${WORK}/sandbox-podman/volumes"
write_fixture "${containers_fixture}"
write_fixture "${images_fixture}" \
    "localhost/cenci-sandbox:latest" \
    "localhost/cenci-sandbox-base:abc123def456" \
    "localhost/cenci-sandbox-base:latest" \
    "localhost/cenci-sandbox-repo-x:latest" \
    "docker.io/library/nginx:latest"
write_fixture "${volumes_fixture}"
make_container_runtime "${LAYOUT_BIN}" podman "${containers_fixture}" "${images_fixture}" "${volumes_fixture}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
for tag in localhost/cenci-sandbox:latest localhost/cenci-sandbox-base:abc123def456 \
    localhost/cenci-sandbox-base:latest localhost/cenci-sandbox-repo-x:latest; do
    assert_contains "${LAYOUT_CALL_LOG}" "podman rmi ${tag}"
done
assert_not_contains "${LAYOUT_CALL_LOG}" "podman rmi docker.io/library/nginx:latest"

# --- case 17: sentinel-secret regression through the runtime mock (#353) --------
echo "case: host secrets never leak into the container-runtime call log or captured output during sandbox cleanup"
prepare_full_layout sandbox-sentinel
containers_fixture="${WORK}/sandbox-sentinel/containers"
images_fixture="${WORK}/sandbox-sentinel/images"
volumes_fixture="${WORK}/sandbox-sentinel/volumes"
write_fixture "${containers_fixture}" "claude-cenci-repo-a"
write_fixture "${images_fixture}" "cenci-sandbox:latest"
write_fixture "${volumes_fixture}" "claude-cenci-home-repo-a"
make_container_runtime "${LAYOUT_BIN}" docker "${containers_fixture}" "${images_fixture}" "${volumes_fixture}"
export OPENAI_API_KEY="sk-test-sentinel-should-not-leak"
export CONTEXT7_API_KEY="ctx7-test-sentinel-should-not-leak"
run_uninstall -- --yes
unset OPENAI_API_KEY CONTEXT7_API_KEY
[[ "${UNINSTALL_EXIT}" -eq 0 ]]
assert_contains "${LAYOUT_CALL_LOG}" "docker rm -f claude-cenci-repo-a"
assert_not_contains "${LAYOUT_CALL_LOG}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${UNINSTALL_OUTPUT}" "sk-test-sentinel-should-not-leak"
assert_not_contains "${LAYOUT_CALL_LOG}" "ctx7-test-sentinel-should-not-leak"
assert_not_contains "${UNINSTALL_OUTPUT}" "ctx7-test-sentinel-should-not-leak"

# --- case 18: container enumeration failure warns and skips only that category --
echo "case: a failing 'ps -a' container enumeration call warns and skips container cleanup, without aborting image/volume cleanup or the rest of uninstall"
prepare_full_layout sandbox-ps-failure
containers_fixture="${WORK}/sandbox-ps-failure/containers"
images_fixture="${WORK}/sandbox-ps-failure/images"
volumes_fixture="${WORK}/sandbox-ps-failure/volumes"
write_fixture "${containers_fixture}" "claude-cenci-repo-a"
write_fixture "${images_fixture}" "cenci-sandbox:latest"
write_fixture "${volumes_fixture}" "claude-cenci-home-repo-a"
make_container_runtime "${LAYOUT_BIN}" docker "${containers_fixture}" "${images_fixture}" "${volumes_fixture}" ps
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

assert_contains "${UNINSTALL_OUTPUT}" "could not list docker containers — skipping sandbox container cleanup"
assert_not_contains "${LAYOUT_CALL_LOG}" "docker rm -f claude-cenci-repo-a"
assert_not_contains "${UNINSTALL_OUTPUT}" "cenci sandbox container(s)"

assert_contains "${LAYOUT_CALL_LOG}" "docker rmi cenci-sandbox:latest"
assert_contains "${LAYOUT_CALL_LOG}" "docker volume rm claude-cenci-home-repo-a"

if ! grep -qx "daemon stop" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected the rest of uninstall (daemon stop) to still run when container enumeration fails" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
[[ ! -e "${LAYOUT_HOME}/.config/cenci/config.json" ]]

# --- case 19: per-agent *-cenci-dind-* volumes are inventoried and removed,
#     with dind look-alikes excluded (#630) --------------------------------
echo "case: per-agent *-cenci-dind-* volumes are inventoried and removed alongside home/agent-CLI volumes, with dind look-alikes excluded"
prepare_full_layout sandbox-dind-volumes
containers_fixture="${WORK}/sandbox-dind-volumes/containers"
images_fixture="${WORK}/sandbox-dind-volumes/images"
volumes_fixture="${WORK}/sandbox-dind-volumes/volumes"
write_fixture "${containers_fixture}"
write_fixture "${images_fixture}"
write_fixture "${volumes_fixture}" \
    "claude-cenci-dind-repo-a" "codex-cenci-dind-repo-b" "opencode-cenci-dind-repo-c" \
    "claude-cenci-dindrepo-lookalike" "notclaude-cenci-dind-repo-x" "random-dind-volume"
make_container_runtime "${LAYOUT_BIN}" docker "${containers_fixture}" "${images_fixture}" "${volumes_fixture}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

for vol in claude-cenci-dind-repo-a codex-cenci-dind-repo-b opencode-cenci-dind-repo-c; do
    assert_contains "${LAYOUT_CALL_LOG}" "docker volume rm ${vol}"
    assert_contains "${UNINSTALL_OUTPUT}" "${vol}"
done
for lookalike in claude-cenci-dindrepo-lookalike notclaude-cenci-dind-repo-x random-dind-volume; do
    assert_not_contains "${LAYOUT_CALL_LOG}" "docker volume rm ${lookalike}"
    assert_not_contains "${UNINSTALL_OUTPUT}" "${lookalike}"
done

# --- case 20: both docker and podman installed — each runtime's own
#     cenci-owned containers/images/volumes (including dind volumes) are
#     inventoried and removed independently, tagged by runtime (#630) ------
echo "case: with both docker and podman installed, sandbox cleanup inventories and removes each runtime's own cenci-owned containers/images/volumes (including *-cenci-dind-* volumes) independently, without cross-runtime contamination, and shows every removed name in the confirmation output"
prepare_full_layout sandbox-multi-runtime
docker_containers="${WORK}/sandbox-multi-runtime/docker-containers"
docker_images="${WORK}/sandbox-multi-runtime/docker-images"
docker_volumes="${WORK}/sandbox-multi-runtime/docker-volumes"
podman_containers="${WORK}/sandbox-multi-runtime/podman-containers"
podman_images="${WORK}/sandbox-multi-runtime/podman-images"
podman_volumes="${WORK}/sandbox-multi-runtime/podman-volumes"
write_fixture "${docker_containers}" "claude-cenci-docker-repo"
write_fixture "${docker_images}" "cenci-sandbox:latest"
write_fixture "${docker_volumes}" "claude-cenci-home-docker-repo" "claude-cenci-dind-docker-repo"
write_fixture "${podman_containers}" "codex-cenci-podman-repo"
write_fixture "${podman_images}" "localhost/cenci-sandbox:latest"
write_fixture "${podman_volumes}" "codex-cenci-home-podman-repo" "codex-cenci-dind-podman-repo"
make_container_runtime "${LAYOUT_BIN}" docker "${docker_containers}" "${docker_images}" "${docker_volumes}"
make_container_runtime "${LAYOUT_BIN}" podman "${podman_containers}" "${podman_images}" "${podman_volumes}"
run_uninstall -- --yes
[[ "${UNINSTALL_EXIT}" -eq 0 ]]

assert_contains "${LAYOUT_CALL_LOG}" "docker rm -f claude-cenci-docker-repo"
assert_contains "${LAYOUT_CALL_LOG}" "docker rmi cenci-sandbox:latest"
assert_contains "${LAYOUT_CALL_LOG}" "docker volume rm claude-cenci-home-docker-repo"
assert_contains "${LAYOUT_CALL_LOG}" "docker volume rm claude-cenci-dind-docker-repo"

assert_contains "${LAYOUT_CALL_LOG}" "podman rm -f codex-cenci-podman-repo"
assert_contains "${LAYOUT_CALL_LOG}" "podman rmi localhost/cenci-sandbox:latest"
assert_contains "${LAYOUT_CALL_LOG}" "podman volume rm codex-cenci-home-podman-repo"
assert_contains "${LAYOUT_CALL_LOG}" "podman volume rm codex-cenci-dind-podman-repo"

# Runtime tagging must not cross-contaminate: docker's targets are never
# removed via podman, and vice versa.
assert_not_contains "${LAYOUT_CALL_LOG}" "podman rm -f claude-cenci-docker-repo"
assert_not_contains "${LAYOUT_CALL_LOG}" "docker rm -f codex-cenci-podman-repo"
assert_not_contains "${LAYOUT_CALL_LOG}" "podman volume rm claude-cenci-dind-docker-repo"
assert_not_contains "${LAYOUT_CALL_LOG}" "docker volume rm codex-cenci-dind-podman-repo"

# shown==removed parity across both runtimes: every removed name is also
# shown in the confirmation-list output.
for name in claude-cenci-docker-repo cenci-sandbox:latest claude-cenci-home-docker-repo claude-cenci-dind-docker-repo \
    codex-cenci-podman-repo codex-cenci-home-podman-repo codex-cenci-dind-podman-repo; do
    assert_contains "${UNINSTALL_OUTPUT}" "${name}"
done

echo "passed: uninstall MODE removes plugins/marketplace registration, PATH links, daemon + state, and config behind a single confirmation gate; lazyboards stays opt-in; rc files are never edited; subprocess env stays scrubbed; machine-wide sandbox container/image/volume cleanup (#458) runs before plugin-cache removal, shows counts/names in the confirmation list, force-removes running containers, matches podman's localhost/ prefix, is a clean no-op without a container runtime, warns-and-skips (without aborting) when one enumeration category fails, inventories/removes per-agent *-cenci-dind-* volumes with look-alike exclusion, and inventories/removes both Docker's and Podman's cenci-owned resources independently when both runtimes are installed (#630)"
