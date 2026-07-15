#!/usr/bin/env bash
set -euo pipefail

# Regression test for #195: cenci-sand must not silently create the shared
# container without cenci wiring just because the host daemon hasn't
# started yet (it starts lazily, on the first `cenci notify`). The
# launcher now starts the daemon itself when the socket is missing, warns
# when the wiring cannot be established, and warns on attach when a running
# container was created without the events-socket mount.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source-path=SCRIPTDIR/../lib
# shellcheck source=../lib/repo-scope.sh
source "${SANDBOX_DIR}/lib/repo-scope.sh"
REPO_ROOT="$(git -C "${SANDBOX_DIR}" rev-parse --show-toplevel)"
MOCK_CONTAINER_NAME="claude-cenci-$(slugify "$(basename "${REPO_ROOT}")")"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 not found on PATH (needed to create Unix sockets)"
    exit 0
fi

BIN_DIR="${TEST_ROOT}/bin"
CALLS_FILE="${TEST_ROOT}/runtime-calls"
CENCI_CALLS="${TEST_ROOT}/cenci-calls"
STDERR_FILE="${TEST_ROOT}/stderr"
RUNTIME_DIR="${TEST_ROOT}/runtime"
SOCKET_DIR="${RUNTIME_DIR}/cenci"
EVENTS_SOCKET="${SOCKET_DIR}/cenci-events.sock"
# Container-side mount point; must match cenci-sand's CENCI_SOCKET_MOUNT_DEST.
SOCKET_MOUNT_DEST="/run/user/1000/cenci"
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

# `socket-dir` mkdir -p's and prints the socket directory, matching the real
# `cenci socket-dir` (#217), unless MOCK_SOCKET_DIR_FAILS=true, in which
# case it exits non-zero with a diagnostic on stderr (simulating a genuine CLI
# failure, distinct from cenci simply not being installed). `daemon`
# creates the events socket inside that directory unless MOCK_DAEMON_STARTS=false,
# matching the real daemon binding its socket shortly after being spawned.
cat > "${BIN_DIR}/cenci" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "${CENCI_CALLS}"
printf '\n' >> "${CENCI_CALLS}"
if [[ "${1:-}" == socket-dir ]]; then
    if [[ "${MOCK_SOCKET_DIR_FAILS:-false}" == true ]]; then
        echo "mock cenci: socket-dir: permission denied" >&2
        exit 1
    fi
    mkdir -p "${XDG_RUNTIME_DIR}/cenci"
    printf '%s\n' "${XDG_RUNTIME_DIR}/cenci"
fi
if [[ "${1:-}" == daemon && "${MOCK_DAEMON_STARTS:-true}" == true ]]; then
    mkdir -p "${XDG_RUNTIME_DIR}/cenci"
    python3 -c 'import socket, sys; socket.socket(socket.AF_UNIX).bind(sys.argv[1])' \
        "${XDG_RUNTIME_DIR}/cenci/cenci-events.sock"
fi
exit 0
EOF
chmod +x "${BIN_DIR}/cenci"

export CALLS_FILE
export CENCI_CALLS
export MOCK_CONTAINER_NAME
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"
export XDG_RUNTIME_DIR="${RUNTIME_DIR}"

# ── 1. Socket missing at launch → daemon is started, wiring is mounted ──
export MOCK_RUNNING=false
export MOCK_DAEMON_STARTS=true
"${SANDBOX_DIR}/cenci-sand" -p test 2> "${STDERR_FILE}"

if ! grep -Eq '^daemon' "${CENCI_CALLS}"; then
    echo "FAIL: launcher did not start the cenci daemon when the socket was missing" >&2
    exit 1
fi

if ! grep -Eq "^run .* -v ${SOCKET_DIR}:${SOCKET_MOUNT_DEST}:ro " "${CALLS_FILE}"; then
    echo "FAIL: container was created without the read-only cenci socket-directory mount" >&2
    exit 1
fi

if ! grep -Eq '^run .* -e XDG_RUNTIME_DIR=/run/user/1000 ' "${CALLS_FILE}"; then
    echo "FAIL: container was created without XDG_RUNTIME_DIR pointing at the socket" >&2
    exit 1
fi

if grep -q 'Warning: cenci' "${STDERR_FILE}"; then
    echo "FAIL: launcher warned even though the wiring was established" >&2
    exit 1
fi

# ── 2. `cenci socket-dir` itself fails → loud warning, no hang, no bad mount ──
rm -f "${EVENTS_SOCKET}"
printf '' > "${CALLS_FILE}"
printf '' > "${CENCI_CALLS}"
export MOCK_SOCKET_DIR_FAILS=true
timeout 30 "${SANDBOX_DIR}/cenci-sand" -p test 2> "${STDERR_FILE}"
unset MOCK_SOCKET_DIR_FAILS

if ! grep -q "socket-dir' failed" "${STDERR_FILE}"; then
    echo "FAIL: launcher did not warn loudly when 'cenci socket-dir' failed" >&2
    exit 1
fi

if ! grep -q "permission denied" "${STDERR_FILE}"; then
    echo "FAIL: launcher warning dropped the captured stderr diagnostic from 'cenci socket-dir'" >&2
    exit 1
fi

if grep -q 'Warning: cenci is installed but its events socket is unavailable' "${STDERR_FILE}"; then
    echo "FAIL: launcher duplicated the generic unavailable-socket warning alongside the specific socket-dir failure warning" >&2
    exit 1
fi

if grep -Eq '^run .* -v :' "${CALLS_FILE}"; then
    echo "FAIL: container was given a malformed/empty-source cenci mount after the socket-dir failure" >&2
    exit 1
fi

if grep -Eq -- "${SOCKET_MOUNT_DEST}" "${CALLS_FILE}"; then
    echo "FAIL: container was given cenci wiring despite the socket-dir CLI failing" >&2
    exit 1
fi

if ! grep -Eq '^run .* -d ' "${CALLS_FILE}"; then
    echo "FAIL: container was not started after the socket-dir CLI failure" >&2
    exit 1
fi

# ── 3. Daemon never binds the socket → loud warning, container still starts ──
rm -f "${EVENTS_SOCKET}"
printf '' > "${CALLS_FILE}"
printf '' > "${CENCI_CALLS}"
export MOCK_DAEMON_STARTS=false
"${SANDBOX_DIR}/cenci-sand" -p test 2> "${STDERR_FILE}"

if ! grep -q 'Warning: cenci' "${STDERR_FILE}"; then
    echo "FAIL: launcher degraded silently when the events socket never appeared" >&2
    exit 1
fi

if grep -Eq '^run .*cenci-events\.sock' "${CALLS_FILE}"; then
    echo "FAIL: container was given a socket mount that does not exist on the host" >&2
    exit 1
fi

if ! grep -Eq '^run .* -d ' "${CALLS_FILE}"; then
    echo "FAIL: container was not started after the cenci warning" >&2
    exit 1
fi

# ── 4. Attach to a running container created without the wiring → warn ──
make_socket "${EVENTS_SOCKET}"
printf '' > "${CALLS_FILE}"
export MOCK_RUNNING=true
export MOCK_MOUNTS='/workspace\n/home/dev\n'
"${SANDBOX_DIR}/cenci-sand" -p test 2> "${STDERR_FILE}"

if ! grep -q "without cenci wiring" "${STDERR_FILE}"; then
    echo "FAIL: attach to an unwired container did not warn" >&2
    exit 1
fi

if ! grep -Eq '^exec -it -u dev .* claude ' "${CALLS_FILE}"; then
    echo "FAIL: agent was not launched into the unwired container" >&2
    exit 1
fi

# ── 5. Attach to a properly wired container → no warning ──
printf '' > "${CALLS_FILE}"
export MOCK_MOUNTS="/workspace\n${SOCKET_MOUNT_DEST}\n/home/dev\n"
"${SANDBOX_DIR}/cenci-sand" -p test 2> "${STDERR_FILE}"

if grep -q 'Warning' "${STDERR_FILE}"; then
    echo "FAIL: attach to a wired container warned spuriously" >&2
    exit 1
fi

# ── 6. Daemon restart (stale inode) → mounting the directory survives it ──
# Regression for #218: a bind-mounted socket FILE pins the inode that existed
# at container-creation time. When the host daemon restarts it unlinks the old
# socket and binds a fresh one (new inode) at the same path, so every already
# running sandbox keeps pointing at the dead inode. Mounting the directory
# instead means lookups always resolve through the live path, immune to the
# daemon rebinding underneath. This is a mocked-docker path-reachability +
# mount-shape proxy (not a real `mount --bind`, which needs privileges we
# don't have here).
listen_once() {
    # Binds, listens, and accepts a single connection then exits — enough for
    # a client's connect() to succeed without racing an explicit accept().
    python3 -c '
import socket, sys
path = sys.argv[1]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(path)
s.listen(1)
conn, _ = s.accept()
conn.close()
s.close()
' "$1"
}

client_connects() {
    python3 -c '
import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(sys.argv[1])
s.close()
' "$1"
}

wait_for_socket() {
    for _ in {1..30}; do
        [[ -S "$1" ]] && return 0
        sleep 0.1
    done
    return 1
}

RESTART_DIR="${TEST_ROOT}/restart-sim"
mkdir -p "${RESTART_DIR}"
RESTART_SOCKET="${RESTART_DIR}/cenci-events.sock"

# The "old" daemon binds the socket (first inode).
listen_once "${RESTART_SOCKET}" &
OLD_SERVER_PID=$!
if ! wait_for_socket "${RESTART_SOCKET}"; then
    echo "FAIL: setup - original daemon socket never appeared" >&2
    kill "${OLD_SERVER_PID}" 2>/dev/null || true
    exit 1
fi

# Simulate the host daemon restarting: unlink the stale socket, rebind a
# fresh one (new inode) at the identical path.
kill "${OLD_SERVER_PID}" 2>/dev/null || true
wait "${OLD_SERVER_PID}" 2>/dev/null || true
rm -f "${RESTART_SOCKET}"

listen_once "${RESTART_SOCKET}" &
NEW_SERVER_PID=$!
if ! wait_for_socket "${RESTART_SOCKET}"; then
    echo "FAIL: setup - restarted daemon socket never appeared" >&2
    kill "${NEW_SERVER_PID}" 2>/dev/null || true
    exit 1
fi

if ! client_connects "${RESTART_SOCKET}"; then
    echo "FAIL: a fresh client could not connect through the path after a simulated daemon restart (stale inode)" >&2
    kill "${NEW_SERVER_PID}" 2>/dev/null || true
    exit 1
fi
kill "${NEW_SERVER_PID}" 2>/dev/null || true
wait "${NEW_SERVER_PID}" 2>/dev/null || true

# Mount shape: the launcher must mount the socket DIRECTORY, never the socket
# file itself — a file mount is exactly the stale-inode bug reproduced above.
rm -f "${EVENTS_SOCKET}"
printf '' > "${CALLS_FILE}"
printf '' > "${CENCI_CALLS}"
export MOCK_RUNNING=false
export MOCK_DAEMON_STARTS=true
"${SANDBOX_DIR}/cenci-sand" -p test 2> "${STDERR_FILE}"

if ! grep -Eq "^run .* -v ${SOCKET_DIR}:${SOCKET_MOUNT_DEST}:ro " "${CALLS_FILE}"; then
    echo "FAIL: launcher did not mount the cenci socket directory read-only" >&2
    exit 1
fi

if grep -Eq '^run .*cenci-events\.sock' "${CALLS_FILE}"; then
    echo "FAIL: launcher mounted the socket file directly; a bind-mounted file pins the inode and breaks across daemon restarts (#218)" >&2
    exit 1
fi

echo "passed: daemon ensured before the gate, loud degradation on daemon and socket-dir CLI failures, unwired-container detection, socket-directory mount survives daemon restart"
