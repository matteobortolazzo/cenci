#!/usr/bin/env bash
# End-to-end regression tests for host installer update behavior: the daemon
# restart path (restart_agentwatch_daemon in install.sh) must delegate to the
# just-installed binary's own `agentwatch daemon restart` lifecycle verb
# rather than install.sh reimplementing pkill/nohup restart logic itself. The
# old ad-hoc pkill/nohup restart survives only as a fallback for when the
# binary can't do it itself.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
PIDS_FILE="${WORK}/daemon-pids"

cleanup() {
    if [[ -f "${PIDS_FILE}" ]]; then
        while IFS= read -r pid; do kill "${pid}" 2>/dev/null || true; done <"${PIDS_FILE}"
    fi
    rm -rf "${WORK}"
}
trap cleanup EXIT

make_tools() {
    local bin="$1" tool
    mkdir -p "${bin}"
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep nohup chmod sed head rm; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${bin}/docker"
}

# make_logging_pkill_pgrep installs pkill/pgrep stubs (instead of the real
# system tools) that record every invocation to PKILL_LOG and behave like
# "nothing found" (pgrep's documented no-match exit code is 1), so a test can
# assert whether install.sh's own fallback path ever shelled out to them.
make_logging_pkill_pgrep() {
    local bin="$1"
    cat >"${bin}/pkill" <<'EOF'
#!/bin/sh
printf 'pkill %s\n' "$*" >>"${PKILL_LOG}"
exit 0
EOF
    cat >"${bin}/pgrep" <<'EOF'
#!/bin/sh
printf 'pgrep %s\n' "$*" >>"${PKILL_LOG}"
exit 1
EOF
    chmod +x "${bin}/pkill" "${bin}/pgrep"
}

make_client() {
    local bin="$1" client="$2"
    cat >"${bin}/${client}" <<EOF
#!/bin/sh
if [ "\${1:-}" = plugin ] && [ "\${2:-}" = list ]; then
    if [ "${client}" = claude ]; then
        echo 'agentwatch@agent-stack'
    else
        echo 'agentwatch@agent-stack installed'
    fi
fi
exit 0
EOF
    chmod +x "${bin}/${client}"
}

# make_agentwatch writes a fake agentwatch binary at path that logs every
# invocation's argv (space-joined) to CALL_LOG. `daemon restart` exits
# restart_exit (0 = the binary handled its own restart; nonzero = simulate a
# binary that can't self-restart, so install.sh's fallback should kick in). A
# bare `daemon` invocation (the fallback's nohup-spawned form) loops until
# signaled, recording its pid to PIDS_FILE for the harness to reap.
make_agentwatch() {
    local path="$1" restart_exit="${2:-0}"
    mkdir -p "$(dirname "${path}")"
    cat >"${path}" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>"\${CALL_LOG}"
if [ "\${1:-}" = daemon ] && [ "\${2:-}" = restart ]; then
    exit ${restart_exit}
fi
if [ "\${1:-}" = daemon ] && [ -z "\${2:-}" ]; then
    echo "\$\$" >>"\${PIDS_FILE}"
    trap 'exit 0' TERM INT
    while :; do sleep 1; done
fi
exit 0
EOF
    chmod +x "${path}"
}

# setup_layout provisions a fake HOME with a client plugin cache containing
# a single "updated" agentwatch binary (current_agentwatch_binary always finds
# a binary already in place, so step_agentwatch_setup's update path calls
# restart_agentwatch_daemon immediately, no bootstrap needed).
setup_layout() {
    local name="$1" client="$2" restart_exit="$3"
    local home="${WORK}/${name}/home" mock_bin="${WORK}/${name}/bin"
    local call_log="${WORK}/${name}/calls" pkill_log="${WORK}/${name}/pkill-calls"
    mkdir -p "${home}"
    : >"${call_log}"
    : >"${pkill_log}"
    make_tools "${mock_bin}"
    make_logging_pkill_pgrep "${mock_bin}"
    make_client "${mock_bin}" "${client}"

    local cache_dir manifest_dir new_root new_bin
    if [[ "${client}" == claude ]]; then
        cache_dir="${home}/.claude/plugins/cache/agent-stack/agentwatch"
        manifest_dir=.claude-plugin
    else
        cache_dir="${home}/.codex/plugins/cache/agent-stack/agentwatch"
        manifest_dir=.codex-plugin
    fi
    new_root="${cache_dir}/2.0.0"
    new_bin="${new_root}/bin/agentwatch"
    make_agentwatch "${new_bin}" "${restart_exit}"
    mkdir -p "${new_root}/${manifest_dir}"
    printf '{"name":"agentwatch","version":"2.0.0"}\n' >"${new_root}/${manifest_dir}/plugin.json"

    LAYOUT_HOME="${home}"
    LAYOUT_BIN="${mock_bin}"
    LAYOUT_CALL_LOG="${call_log}"
    LAYOUT_PKILL_LOG="${pkill_log}"
    LAYOUT_NEW_BIN="${new_bin}"
}

run_update() {
    set +e
    HOME="${LAYOUT_HOME}" PATH="${LAYOUT_BIN}" CALL_LOG="${LAYOUT_CALL_LOG}" \
        PIDS_FILE="${PIDS_FILE}" PKILL_LOG="${LAYOUT_PKILL_LOG}" \
        bash "${ROOT}/install.sh" update --yes --no-build >"${WORK}/last-output" 2>&1
    UPDATE_EXIT=$?
    set -e
}

echo "install-update.test.sh"

echo "case: update invokes 'daemon restart' on the updated Claude-cache binary, never pkill"
setup_layout happy-claude claude 0
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -qx "daemon restart" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected 'daemon restart' invocation in ${LAYOUT_CALL_LOG}" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if [[ -s "${LAYOUT_PKILL_LOG}" ]]; then
    echo "FAIL: expected pkill/pgrep to never be invoked when 'daemon restart' succeeds" >&2
    cat "${LAYOUT_PKILL_LOG}" >&2
    exit 1
fi
if [[ ! -L "${LAYOUT_HOME}/.local/bin/agentwatch" ]] ||
    [[ "$(readlink "${LAYOUT_HOME}/.local/bin/agentwatch")" != "${LAYOUT_NEW_BIN}" ]]; then
    echo "FAIL: Claude cache launcher does not point to the updated binary" >&2
    exit 1
fi

echo "case: update invokes 'daemon restart' on the updated Codex-cache binary, never pkill"
setup_layout happy-codex codex 0
run_update
[[ "${UPDATE_EXIT}" -eq 0 ]]
if ! grep -qx "daemon restart" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected 'daemon restart' invocation (codex cache) in ${LAYOUT_CALL_LOG}" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if [[ -s "${LAYOUT_PKILL_LOG}" ]]; then
    echo "FAIL: expected pkill/pgrep to never be invoked when 'daemon restart' succeeds (codex cache)" >&2
    cat "${LAYOUT_PKILL_LOG}" >&2
    exit 1
fi

echo "case: when 'daemon restart' fails, the installer falls back to a manual pkill/nohup restart"
setup_layout fallback claude 1
run_update
if ! grep -qx "daemon restart" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected the installer to still try 'daemon restart' first" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if ! grep -q '^pkill ' "${LAYOUT_PKILL_LOG}"; then
    echo "FAIL: expected the fallback path to invoke pkill after 'daemon restart' failed" >&2
    cat "${LAYOUT_PKILL_LOG}" >&2
    exit 1
fi
if ! grep -qx "daemon" "${LAYOUT_CALL_LOG}"; then
    echo "FAIL: expected the fallback path to nohup-spawn a bare 'daemon' invocation" >&2
    cat "${LAYOUT_CALL_LOG}" >&2
    exit 1
fi
if ! grep -q "restarted agentwatch with the updated binary" "${WORK}/last-output"; then
    echo "FAIL: expected the fallback restart to report success" >&2
    cat "${WORK}/last-output" >&2
    exit 1
fi

echo "passed: restart path delegates to 'agentwatch daemon restart', falling back to pkill/nohup only on failure"
