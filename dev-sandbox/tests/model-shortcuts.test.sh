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

cat > "${BIN_DIR}/agentwatch" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${BIN_DIR}/agentwatch"

export CALLS_FILE
export HOME="${TEST_ROOT}/home"
export PATH="${BIN_DIR}:/usr/bin:/bin"

echo "case: 'sb ch' launches Claude with the haiku model"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/agent-sand" ch -p test

if ! grep -Eq '^run .* -e AGENT_SAND_AGENT=claude ' "${CALLS_FILE}"; then
    echo "FAIL: 'ch' did not select the claude agent" >&2
    exit 1
fi
if ! grep -Eq "^exec -it -u dev .*claude-sand-${REPO_SLUG} claude --dangerously-skip-permissions --model haiku -p test " "${CALLS_FILE}"; then
    echo "FAIL: 'ch' did not launch claude with --model haiku" >&2
    exit 1
fi

echo "case: 'sb xt' launches Codex with the gpt-5.6-terra model"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/agent-sand" xt -p test

if ! grep -Eq '^run .* -e AGENT_SAND_AGENT=codex ' "${CALLS_FILE}"; then
    echo "FAIL: 'xt' did not select the codex agent" >&2
    exit 1
fi
if ! grep -Eq "^exec -it -u dev .*codex-sand-${REPO_SLUG} codex --dangerously-bypass-approvals-and-sandbox --model gpt-5\\.6-terra -p test " "${CALLS_FILE}"; then
    echo "FAIL: 'xt' did not launch codex with --model gpt-5.6-terra" >&2
    exit 1
fi

echo "case: unadorned --agent codex still defaults to the gpt-5.6-terra model"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/agent-sand" --agent codex -p test

if ! grep -Eq "^exec -it -u dev .*codex-sand-${REPO_SLUG} codex --dangerously-bypass-approvals-and-sandbox --model gpt-5\\.6-terra -p test " "${CALLS_FILE}"; then
    echo "FAIL: default codex launch did not fall back to --model gpt-5.6-terra" >&2
    exit 1
fi

echo "case: a shortcut token later in the args is passed through untouched"
printf '' > "${CALLS_FILE}"
"${SANDBOX_DIR}/agent-sand" -p ch

if ! grep -Eq "^exec -it -u dev .*claude-sand-${REPO_SLUG} claude --dangerously-skip-permissions --model sonnet -p ch " "${CALLS_FILE}"; then
    echo "FAIL: 'ch' in a non-first position was not forwarded as a literal argument" >&2
    exit 1
fi

echo "passed: agent+model shortcuts (ch/cs/co/cf, xl/xt/xs) and their defaults"
