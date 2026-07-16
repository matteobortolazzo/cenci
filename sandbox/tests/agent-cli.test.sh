#!/usr/bin/env bash
# Host-runnable tests for the persistent, user-local agent CLI lifecycle.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

FAILURES=0
PASSES=0

fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }
pass() { PASSES=$((PASSES + 1)); }

make_npm() {
    local bin="$1"
    mkdir -p "${bin}"
    cat >"${bin}/npm" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"${NPM_LOG}"
case "$*" in
  *"@anthropic-ai/claude-code@latest"*) agent=claude; version='2.9.0 (Claude Code)' ;;
  *"@openai/codex@latest"*) agent=codex; version='codex-cli 9.8.7' ;;
  *) exit 64 ;;
esac
mkdir -p "${NPM_CONFIG_PREFIX}/bin"
if [ "${NPM_PARTIAL:-0}" -eq 1 ]; then
    printf '#!/bin/sh\nexit 99\n' >"${NPM_CONFIG_PREFIX}/bin/${agent}"
    chmod +x "${NPM_CONFIG_PREFIX}/bin/${agent}"
    exit "${NPM_EXIT:-23}"
fi
[ "${NPM_EXIT:-0}" -eq 0 ] || exit "${NPM_EXIT}"
cat >"${NPM_CONFIG_PREFIX}/bin/${agent}" <<SCRIPT
#!/bin/sh
echo '${version}'
SCRIPT
chmod +x "${NPM_CONFIG_PREFIX}/bin/${agent}"
EOF
    chmod +x "${bin}/npm"
    cat >"${bin}/flock" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"${FLOCK_LOG}"
exit 0
EOF
    chmod +x "${bin}/flock"
}

new_case() {
    CASE="$1"
    CASE_DIR="${WORK}/${CASE}"
    HOME="${CASE_DIR}/home"
    BIN="${CASE_DIR}/bin"
    NPM_LOG="${CASE_DIR}/npm.log"
    FLOCK_LOG="${CASE_DIR}/flock.log"
    mkdir -p "${HOME}" "${BIN}"
    : >"${NPM_LOG}"
    : >"${FLOCK_LOG}"
    export HOME NPM_LOG FLOCK_LOG
    PATH="${BIN}:/usr/bin:/bin"
    export PATH
    unset NPM_EXIT
    unset NPM_PARTIAL
    make_npm "${BIN}"
}

run_agent_cli() {
    local function_name="$1" agent="$2"
    # shellcheck disable=SC2016 # Positional parameters expand in the isolated bash.
    env -i HOME="${HOME}" CENCI_SANDBOX_AGENT_HOME="${HOME}" PATH="${PATH}" \
        NPM_LOG="${NPM_LOG}" FLOCK_LOG="${FLOCK_LOG}" \
        NPM_EXIT="${NPM_EXIT:-0}" NPM_PARTIAL="${NPM_PARTIAL:-0}" \
        bash -c 'source "$1"; "$2" "$3"' _ \
        "${ROOT}/sandbox/lib/agent-cli.sh" "${function_name}" "${agent}"
}

echo "agent-cli.test.sh"

echo "case: agent names map to their official npm packages"
new_case mapping
if [[ "$(run_agent_cli agent_cli_package claude)" == "@anthropic-ai/claude-code" ]] \
    && [[ "$(run_agent_cli agent_cli_package codex)" == "@openai/codex" ]]; then
    pass
else
    fail "agent/package mapping is incorrect"
fi

echo "case: first launch installs the selected agent into the writable prefix"
new_case first-install
if run_agent_cli ensure_agent_cli claude \
    && [[ -x "${HOME}/.local/bin/claude" ]] \
    && grep -Fqx -- "install --global @anthropic-ai/claude-code@latest" "${NPM_LOG}" \
    && grep -Fqx -- "-x 9" "${FLOCK_LOG}"; then
    pass
else
    fail "first install did not create the user-local Claude executable"
fi

echo "case: an existing user-local executable performs no recurring npm check"
NPM_EXIT=55
export NPM_EXIT
if run_agent_cli ensure_agent_cli claude && [[ "$(wc -l <"${NPM_LOG}")" -eq 1 ]]; then
    pass
else
    fail "reuse unexpectedly invoked npm"
fi

echo "case: an explicit update reinstalls @latest and prints the installed version"
unset NPM_EXIT
UPDATE_OUTPUT="$(run_agent_cli update_agent_cli claude)"
if [[ "$(wc -l <"${NPM_LOG}")" -eq 2 ]] \
    && [[ "${UPDATE_OUTPUT}" == *"2.9.0 (Claude Code)"* ]]; then
    pass
else
    fail "forced update did not reinstall or report the version"
fi

echo "case: a system binary does not suppress migration to the writable prefix"
new_case migration
cat >"${BIN}/codex" <<'EOF'
#!/bin/sh
echo system-codex
EOF
chmod +x "${BIN}/codex"
if run_agent_cli ensure_agent_cli codex \
    && [[ -x "${HOME}/.local/bin/codex" ]] \
    && grep -Fqx -- "install --global @openai/codex@latest" "${NPM_LOG}"; then
    pass
else
    fail "system Codex binary incorrectly prevented user-local migration"
fi

echo "case: npm failures propagate and explain that first launch needs network access"
new_case npm-failure
NPM_EXIT=23
export NPM_EXIT
set +e
FAILURE_OUTPUT="$(run_agent_cli ensure_agent_cli claude 2>&1)"
FAILURE_STATUS=$?
set -e
if [[ "${FAILURE_STATUS}" -ne 0 ]] \
    && [[ "${FAILURE_OUTPUT}" == *"failed to install Claude Code"* ]] \
    && [[ "${FAILURE_OUTPUT}" == *"network access"* ]]; then
    pass
else
    fail "npm failure was swallowed or unclear"
fi

echo "case: invalid agents are rejected before npm runs"
new_case invalid-agent
set +e
INVALID_OUTPUT="$(run_agent_cli ensure_agent_cli gemini 2>&1)"
INVALID_STATUS=$?
set -e
if [[ "${INVALID_STATUS}" -eq 2 ]] \
    && [[ "${INVALID_OUTPUT}" == *"unknown agent"* ]] \
    && [[ ! -s "${NPM_LOG}" ]]; then
    pass
else
    fail "invalid agent was not rejected cleanly"
fi

echo "case: a partial failed update leaves a repair marker instead of a false reusable executable"
new_case partial-update
run_agent_cli ensure_agent_cli codex >/dev/null
NPM_PARTIAL=1
NPM_EXIT=23
export NPM_PARTIAL NPM_EXIT
set +e
run_agent_cli update_agent_cli codex >/dev/null 2>&1
PARTIAL_STATUS=$?
set -e
unset NPM_PARTIAL NPM_EXIT
if [[ "${PARTIAL_STATUS}" -ne 0 ]] && [[ ! -x "${HOME}/.local/bin/codex" ]] \
    && run_agent_cli ensure_agent_cli codex >/dev/null \
    && [[ -x "${HOME}/.local/bin/codex" ]]; then
    pass
else
    fail "partial npm failure left a falsely reusable executable"
fi

echo
echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
