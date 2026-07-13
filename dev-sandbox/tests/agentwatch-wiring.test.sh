#!/usr/bin/env bash
set -euo pipefail

# Regression test for #195: agent-sand must not silently create the shared
# container without agentwatch wiring just because the host daemon hasn't
# started yet (it starts lazily, on the first `agentwatch notify`). The
# launcher now starts the daemon itself when the socket is missing, warns
# when the wiring cannot be established, and warns on attach when a running
# container was created without the events-socket mount.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source-path=SCRIPTDIR/../lib
# shellcheck source=../lib/repo-scope.sh
source "${SANDBOX_DIR}/lib/repo-scope.sh"
REPO_ROOT="$(git -C "${SANDBOX_DIR}" rev-parse --show-toplevel)"
MOCK_CONTAINER_NAME="claude-sand-$(slugify "$(basename "${REPO_ROOT}")")"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 not found on PATH (needed to create Unix sockets)"
    exit 0
fi

BIN_DIR="${TEST_ROOT}/bin"
CALLS_FILE="${TEST_ROOT}/runtime-calls"
AGENTWATCH_CALLS="${TEST_ROOT}/agentwatch-calls"
STDERR_FILE="${TEST_ROOT}/stderr"
RUNTIME_DIR="${TEST_ROOT}/runtime"
EVENTS_SOCKET="${RUNTIME_DIR}/agentwatch-events.sock"
mkdir -p "${BIN_DIR}" "${RUNTIME_DIR}" "${TEST_ROOT}/home/.claude"
chmod 700 "${RUNTIME_DIR}"
touch "${TEST_ROOT}/home/.claude/.credentials.json"

make_socket() {
    python3 -c 'import socket, sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' "$1"
}

cat > "${BIN_DIR}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "${CALLS_FILE}"
printf '\n' >> "${CALLS_FILE}"

case "${1:-} ${2:-}" in
    "image inspect") exit 0 ;;
    "ps --format")
        if [[ "${MOCK_RUNNING:-false}" == true ]]; then
            printf '%s\n' "${MOCK_CONTAINER_NAME}"
        fi
        ;;
    "inspect --format")
        if [[ "${3:-}" == *Mounts* ]]; then
            printf '%b' "${MOCK_MOUNTS:-}"
        else
            printf '%s\n' detached
        fi
        ;;
    "rm "*) exit 0 ;;
    "run -d") printf '%s\n' sandbox-container-id ;;
    "exec "*) exit 0 ;;
    *) exit 0 ;;
esac
EOF
chmod +x "${BIN_DIR}/docker"
ln -s docker "${BIN_DIR}/podman"

cat > "${BIN_DIR}/claude" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${BIN_DIR}/claude"

# `daemon` creates the events socket unless MOCK_DAEMON_STARTS=false, matching
# the real daemon binding its socket shortly after being spawned.
cat > "${BIN_DIR}/agentwatch" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "${AGENTWATCH_CALLS}"
printf '\n' >> "${AGENTWATCH_CALLS}"
if [[ "${1:-}" == daemon && "${MOCK_DAEMON_STARTS:-true}" == true ]]; then
    python3 -c 'import socket, sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' \
        "${XDG_RUNTIME_DIR}/agentwatch-events.sock"
fi
exit 0
EOF
chmod +x "${BIN_DIR}/agentwatch"

export CALLS_FILE
export AGENTWATCH_CALLS
export MOCK_CONTAINER_NAME
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"
export XDG_RUNTIME_DIR="${RUNTIME_DIR}"

# ── 1. Socket missing at launch → daemon is started, wiring is mounted ──
export MOCK_RUNNING=false
export MOCK_DAEMON_STARTS=true
"${SANDBOX_DIR}/agent-sand" -p test 2> "${STDERR_FILE}"

if ! grep -Eq '^daemon' "${AGENTWATCH_CALLS}"; then
    echo "FAIL: launcher did not start the agentwatch daemon when the socket was missing" >&2
    exit 1
fi

if ! grep -Eq "^run .* -v ${EVENTS_SOCKET}:/run/user/1000/agentwatch-events.sock " "${CALLS_FILE}"; then
    echo "FAIL: container was created without the events-socket mount" >&2
    exit 1
fi

if ! grep -Eq '^run .* -e XDG_RUNTIME_DIR=/run/user/1000 ' "${CALLS_FILE}"; then
    echo "FAIL: container was created without XDG_RUNTIME_DIR pointing at the socket" >&2
    exit 1
fi

if grep -q 'Warning: agentwatch' "${STDERR_FILE}"; then
    echo "FAIL: launcher warned even though the wiring was established" >&2
    exit 1
fi

# ── 2. Daemon never binds the socket → loud warning, container still starts ──
rm -f "${EVENTS_SOCKET}"
printf '' > "${CALLS_FILE}"
printf '' > "${AGENTWATCH_CALLS}"
export MOCK_DAEMON_STARTS=false
"${SANDBOX_DIR}/agent-sand" -p test 2> "${STDERR_FILE}"

if ! grep -q 'Warning: agentwatch' "${STDERR_FILE}"; then
    echo "FAIL: launcher degraded silently when the events socket never appeared" >&2
    exit 1
fi

if grep -Eq '^run .*agentwatch-events\.sock' "${CALLS_FILE}"; then
    echo "FAIL: container was given a socket mount that does not exist on the host" >&2
    exit 1
fi

if ! grep -Eq '^run .* -d ' "${CALLS_FILE}"; then
    echo "FAIL: container was not started after the agentwatch warning" >&2
    exit 1
fi

# ── 3. Attach to a running container created without the wiring → warn ──
make_socket "${EVENTS_SOCKET}"
printf '' > "${CALLS_FILE}"
export MOCK_RUNNING=true
export MOCK_MOUNTS='/workspace\n/home/dev\n'
"${SANDBOX_DIR}/agent-sand" -p test 2> "${STDERR_FILE}"

if ! grep -q "without agentwatch wiring" "${STDERR_FILE}"; then
    echo "FAIL: attach to an unwired container did not warn" >&2
    exit 1
fi

if ! grep -Eq '^exec -it -u dev .* claude ' "${CALLS_FILE}"; then
    echo "FAIL: agent was not launched into the unwired container" >&2
    exit 1
fi

# ── 4. Attach to a properly wired container → no warning ──
printf '' > "${CALLS_FILE}"
export MOCK_MOUNTS='/workspace\n/run/user/1000/agentwatch-events.sock\n/home/dev\n'
"${SANDBOX_DIR}/agent-sand" -p test 2> "${STDERR_FILE}"

if grep -q 'Warning' "${STDERR_FILE}"; then
    echo "FAIL: attach to a wired container warned spuriously" >&2
    exit 1
fi

echo "passed: daemon ensured before the gate, loud degradation, unwired-container detection"
