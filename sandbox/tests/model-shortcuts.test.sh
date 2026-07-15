#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source-path=SCRIPTDIR/../lib
# shellcheck source=../lib/repo-scope.sh
source "${SANDBOX_DIR}/lib/repo-scope.sh"
REPO_ROOT="$(git -C "${SANDBOX_DIR}" rev-parse --show-toplevel)"
REPO_SLUG="$(slugify "$(basename "${REPO_ROOT}")")"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "${TEST_ROOT}"' EXIT

BIN_DIR="${TEST_ROOT}/bin"
CALLS_FILE="${TEST_ROOT}/runtime-calls"
mkdir -p "${BIN_DIR}" "${TEST_ROOT}/home/.claude" "${TEST_ROOT}/home/.codex"
touch "${TEST_ROOT}/home/.claude/.credentials.json"
touch "${TEST_ROOT}/home/.codex/auth.json"

cat > "${BIN_DIR}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%q ' "$@" >> "${CALLS_FILE}"
printf '\n' >> "${CALLS_FILE}"

case "${1:-} ${2:-}" in
    "image inspect") exit 0 ;;
    "ps --format") ;;
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

cat > "${BIN_DIR}/cenci" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${BIN_DIR}/cenci"

export CALLS_FILE
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"

echo "case: 'sb ch' launches Claude with the haiku model"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" ch -p test

if ! grep -Eq '^run .* -e CENCI_SANDBOX_AGENT=claude ' "${CALLS_FILE}"; then
    echo "FAIL: 'ch' did not select the claude agent" >&2
    exit 1
fi
if ! grep -Eq "^exec -it -u dev .*claude-sand-${REPO_SLUG} claude --dangerously-skip-permissions --model haiku -p test " "${CALLS_FILE}"; then
    echo "FAIL: 'ch' did not launch claude with --model haiku" >&2
    exit 1
fi

echo "case: 'sb xt' launches Codex with the gpt-5.6-terra model"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" xt -p test

if ! grep -Eq '^run .* -e CENCI_SANDBOX_AGENT=codex ' "${CALLS_FILE}"; then
    echo "FAIL: 'xt' did not select the codex agent" >&2
    exit 1
fi
if ! grep -Eq "^exec -it -u dev .*codex-sand-${REPO_SLUG} codex --dangerously-bypass-approvals-and-sandbox --model gpt-5\\.6-terra -p test " "${CALLS_FILE}"; then
    echo "FAIL: 'xt' did not launch codex with --model gpt-5.6-terra" >&2
    exit 1
fi

echo "case: unadorned --agent codex still defaults to the gpt-5.6-terra model"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" --agent codex -p test

if ! grep -Eq "^exec -it -u dev .*codex-sand-${REPO_SLUG} codex --dangerously-bypass-approvals-and-sandbox --model gpt-5\\.6-terra -p test " "${CALLS_FILE}"; then
    echo "FAIL: default codex launch did not fall back to --model gpt-5.6-terra" >&2
    exit 1
fi

echo "case: a shortcut token later in the args is passed through untouched"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" -p ch

if ! grep -Eq "^exec -it -u dev .*claude-sand-${REPO_SLUG} claude --dangerously-skip-permissions --model sonnet -p ch " "${CALLS_FILE}"; then
    echo "FAIL: 'ch' in a non-first position was not forwarded as a literal argument" >&2
    exit 1
fi

echo "case: user-supplied --model is forwarded exactly once (no double injection)"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/cenci-sand" --model opus -p test

EXEC_LINE="$(grep -E "^exec -it -u dev .*claude-sand-${REPO_SLUG} claude " "${CALLS_FILE}" || true)"
if [[ -z "${EXEC_LINE}" ]]; then
    echo "FAIL: no exec call found for user-supplied --model" >&2
    exit 1
fi
MODEL_COUNT="$(grep -o -- '--model' <<< "${EXEC_LINE}" | wc -l)"
if [[ "${MODEL_COUNT}" -ne 1 ]]; then
    echo "FAIL: expected exactly one --model in the agent CLI invocation, found ${MODEL_COUNT}: ${EXEC_LINE}" >&2
    exit 1
fi
if ! grep -q -- '--model opus' <<< "${EXEC_LINE}"; then
    echo "FAIL: user-supplied --model value was not forwarded" >&2
    exit 1
fi

echo "case: a shortcut's agent conflicting with an explicit --agent is rejected"
printf '' > "${CALLS_FILE}"
set +e
DESYNC_STDERR="$("${SANDBOX_DIR}/cenci-sand" ch --agent codex -p test 2>&1 1>/dev/null)"
DESYNC_EXIT=$?
set -e
if [[ "${DESYNC_EXIT}" -eq 0 ]]; then
    echo "FAIL: 'ch --agent codex' should have been rejected (conflicting agent/model shortcut)" >&2
    exit 1
fi
if ! grep -qi 'error' <<< "${DESYNC_STDERR}"; then
    echo "FAIL: expected an error message for conflicting shortcut/--agent, got: ${DESYNC_STDERR}" >&2
    exit 1
fi
if [[ -s "${CALLS_FILE}" ]]; then
    echo "FAIL: conflicting shortcut/--agent should not have launched any container" >&2
    exit 1
fi

echo "passed: agent+model shortcuts (ch/cs/co/cf, xl/xt/xs) and their defaults"
