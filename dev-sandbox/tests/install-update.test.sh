#!/bin/bash
# End-to-end regression tests for host installer update behavior.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
PIDS_FILE="${WORK}/daemon-pids"
LOG="${WORK}/agentwatch.log"

cleanup() {
    if [[ -f "${PIDS_FILE}" ]]; then
        while IFS= read -r pid; do
            kill "${pid}" 2>/dev/null || true
        done < "${PIDS_FILE}"
    fi
    rm -rf "${WORK}"
}
trap cleanup EXIT

HOME="${WORK}/home"
MOCK_BIN="${WORK}/bin"
mkdir -p "${HOME}" "${MOCK_BIN}"

cat > "${MOCK_BIN}/claude" <<'EOF'
#!/bin/sh
if [ "${1:-}" = plugin ] && [ "${2:-}" = list ]; then
    cat <<'OUT'
Installed plugins:

  ❯ agentwatch@agent-stack
    Version: 2.0.0
    Scope: user
    Status: ✔ enabled
OUT
fi
exit 0
EOF
chmod +x "${MOCK_BIN}/claude"

cat > "${MOCK_BIN}/sudo" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "${MOCK_BIN}/sudo"

make_agentwatch() {
    local path="$1" label="$2"
    mkdir -p "$(dirname "${path}")"
    cat > "${path}" <<EOF
#!/bin/sh
case "\${1:-}" in
version)
    echo "agentwatch ${label}"
    exit 0
    ;;
daemon)
    echo "${label}:\$$" >> "\${AGENTWATCH_TEST_LOG}"
    echo "\$$" >> "\${AGENTWATCH_TEST_PIDS}"
    trap 'exit 0' TERM INT
    while :; do sleep 1; done
    ;;
esac
exit 1
EOF
    chmod +x "${path}"
}

OLD_BIN="${HOME}/.claude/plugins/cache/agent-stack/agentwatch/1.0.0/bin/agentwatch"
NEW_ROOT="${HOME}/.claude/plugins/cache/agent-stack/agentwatch/2.0.0"
NEW_BIN="${NEW_ROOT}/bin/agentwatch"
make_agentwatch "${OLD_BIN}" "1.0.0"
make_agentwatch "${NEW_BIN}" "2.0.0"
mkdir -p "${NEW_ROOT}/.claude-plugin"
printf '{"name":"agentwatch","version":"2.0.0"}\n' > "${NEW_ROOT}/.claude-plugin/plugin.json"

AGENTWATCH_TEST_LOG="${LOG}" AGENTWATCH_TEST_PIDS="${PIDS_FILE}" \
    "${OLD_BIN}" daemon &
OLD_PID=$!
sleep 0.1

echo "install-update.test.sh"
echo "case: update replaces a stale agentwatch daemon"
HOME="${HOME}" \
PATH="${MOCK_BIN}:/usr/bin:/bin" \
AGENTWATCH_TEST_LOG="${LOG}" \
AGENTWATCH_TEST_PIDS="${PIDS_FILE}" \
    bash "${ROOT}/install.sh" update --yes --no-build >/dev/null

sleep 0.2
if kill -0 "${OLD_PID}" 2>/dev/null; then
    echo "FAIL: stale agentwatch daemon survived the update" >&2
    exit 1
fi
if ! grep -q '^2\.0\.0:' "${LOG}"; then
    echo "FAIL: updated agentwatch daemon was not started" >&2
    exit 1
fi
if [[ ! -L "${HOME}/.local/bin/agentwatch" ]] ||
    [[ "$(readlink "${HOME}/.local/bin/agentwatch")" != "${NEW_BIN}" ]]; then
    echo "FAIL: launcher does not point to the updated agentwatch binary" >&2
    exit 1
fi

echo "passed: stale daemon replaced by updated binary"
