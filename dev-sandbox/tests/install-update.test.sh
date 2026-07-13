#!/usr/bin/env bash
# End-to-end regression tests for host installer update behavior.
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
    for tool in bash cat touch uname grep git mkdir dirname ln readlink sleep pkill pgrep nohup chmod sed head rm; do
        ln -s "$(command -v "${tool}")" "${bin}/${tool}"
    done
    cat >"${bin}/docker" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod +x "${bin}/docker"
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

make_agentwatch() {
    local path="$1" label="$2"
    mkdir -p "$(dirname "${path}")"
    cat >"${path}" <<EOF
#!/bin/sh
case "\${1:-}" in
daemon)
    echo "${label}:\$$" >>"\${AGENTWATCH_TEST_LOG}"
    echo "\$$" >>"\${AGENTWATCH_TEST_PIDS}"
    trap 'exit 0' TERM INT
    while :; do sleep 1; done
    ;;
esac
exit 0
EOF
    chmod +x "${path}"
}

run_layout() {
    local client="$1" home="${WORK}/${1}/home"
    local mock_bin="${WORK}/${1}/bin" log="${WORK}/${1}/agentwatch.log"
    local cache_dir manifest_dir new_root new_bin old_bin
    mkdir -p "${home}"
    make_tools "${mock_bin}"
    make_client "${mock_bin}" "${client}"

    if [[ "${client}" == claude ]]; then
        cache_dir="${home}/.claude/plugins/cache/agent-stack/agentwatch"
        manifest_dir=.claude-plugin
    else
        cache_dir="${home}/.codex/plugins/cache/agent-stack/agentwatch"
        manifest_dir=.codex-plugin
    fi
    old_bin="${cache_dir}/1.0.0/bin/agentwatch"
    new_root="${cache_dir}/2.0.0"
    new_bin="${new_root}/bin/agentwatch"
    make_agentwatch "${old_bin}" 1.0.0
    make_agentwatch "${new_bin}" 2.0.0
    mkdir -p "${new_root}/${manifest_dir}"
    printf '{"name":"agentwatch","version":"2.0.0"}\n' >"${new_root}/${manifest_dir}/plugin.json"

    AGENTWATCH_TEST_LOG="${log}" AGENTWATCH_TEST_PIDS="${PIDS_FILE}" "${old_bin}" daemon &
    local old_pid=$!
    sleep 0.1

    HOME="${home}" PATH="${mock_bin}" AGENTWATCH_TEST_LOG="${log}" \
        AGENTWATCH_TEST_PIDS="${PIDS_FILE}" \
        bash "${ROOT}/install.sh" update --yes --no-build >/dev/null

    sleep 0.2
    if kill -0 "${old_pid}" 2>/dev/null; then
        echo "FAIL: stale ${client} cache daemon survived the update" >&2
        exit 1
    fi
    if ! grep -q '^2\.0\.0:' "${log}"; then
        echo "FAIL: ${client} cache updated daemon was not started" >&2
        exit 1
    fi
    if [[ ! -L "${home}/.local/bin/agentwatch" ]] ||
        [[ "$(readlink "${home}/.local/bin/agentwatch")" != "${new_bin}" ]]; then
        echo "FAIL: ${client} cache launcher does not point to the updated binary" >&2
        exit 1
    fi
}

echo "install-update.test.sh"
echo "case: update replaces a stale daemon from the Claude cache"
run_layout claude
echo "case: update replaces a stale daemon from the Codex cache"
run_layout codex
echo "passed: stale daemon replaced for both client cache layouts"
