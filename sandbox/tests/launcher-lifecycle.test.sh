#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source-path=SCRIPTDIR/../lib
# shellcheck source=../lib/repo-scope.sh
source "${SANDBOX_DIR}/lib/repo-scope.sh"
REPO_ROOT="$(git -C "${SANDBOX_DIR}" rev-parse --show-toplevel)"
MOCK_CONTAINER_NAME="claude-sand-$(slugify "$(basename "${REPO_ROOT}")")"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

BIN_DIR="${TEST_ROOT}/bin"
CALLS_FILE="${TEST_ROOT}/runtime-calls"
mkdir -p "${BIN_DIR}" "${TEST_ROOT}/home/.claude"
touch "${TEST_ROOT}/home/.claude/.credentials.json"

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
    "inspect --format") printf '%s\n' detached ;;
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

cat > "${BIN_DIR}/agentwatch" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${BIN_DIR}/agentwatch"

export CALLS_FILE
export MOCK_CONTAINER_NAME
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"

export MOCK_RUNNING=false
"${SANDBOX_DIR}/agent-sand" -p test

if ! grep -Eq '^run .* -d .*claude-sand-' "${CALLS_FILE}"; then
    echo "FAIL: shared container was not started detached" >&2
    exit 1
fi

if ! grep -Eq '^run .*--label agent-sand\.lifecycle=detached ' "${CALLS_FILE}"; then
    echo "FAIL: detached container is missing its lifecycle label" >&2
    exit 1
fi

if ! grep -Eq '^run .* -e AGENT_SAND_AGENT=claude ' "${CALLS_FILE}"; then
    echo "FAIL: selected agent was not forwarded to the new container" >&2
    exit 1
fi

if ! grep -Eq '^exec -it -u dev .*claude-sand-.* claude --dangerously-skip-permissions --model sonnet -p test ' "${CALLS_FILE}"; then
    echo "FAIL: first agent was not launched through docker exec" >&2
    exit 1
fi

if grep -Eq '^run .* claude --dangerously-skip-permissions' "${CALLS_FILE}"; then
    echo "FAIL: agent process still owns the shared container lifecycle" >&2
    exit 1
fi

printf '' > "${CALLS_FILE}"
export MOCK_RUNNING=true
"${SANDBOX_DIR}/agent-sand" -p second

if grep -Eq '^run ' "${CALLS_FILE}"; then
    echo "FAIL: existing detached container was not reused" >&2
    exit 1
fi

READY_LINE="$(grep -n 'exec -u dev claude-sand-.* test -e /tmp/agent-sand-ready' "${CALLS_FILE}" | cut -d: -f1)"
AGENT_LINE="$(grep -n 'exec -it -u dev .* claude --dangerously-skip-permissions --model sonnet -p second' "${CALLS_FILE}" | cut -d: -f1)"
if [[ -z "${READY_LINE}" || -z "${AGENT_LINE}" || "${READY_LINE}" -ge "${AGENT_LINE}" ]]; then
    echo "FAIL: reused container did not wait for readiness before launching the agent" >&2
    exit 1
fi

printf '' > "${CALLS_FILE}"
export MOCK_RUNNING=false
MOCK_CONTAINER_NAME="codex-sand-$(slugify "$(basename "${REPO_ROOT}")")"
export MOCK_CONTAINER_NAME
"${SANDBOX_DIR}/agent-sand" --agent codex --update-plugins

if ! grep -Eq '^run --rm --entrypoint /bin/bash -e AGENT_SAND_AGENT=codex .*provision_codex_plugins' "${CALLS_FILE}"; then
    echo "FAIL: Codex forced update did not use the baked-in Codex plugin path" >&2
    exit 1
fi
if grep -q '/usr/local/bin/claude' "${CALLS_FILE}"; then
    echo "FAIL: Codex forced update still requires a host Claude binary" >&2
    exit 1
fi

echo "passed: detached shared container, independent and readiness-gated exec sessions"
