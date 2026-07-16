#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CODEX_MANIFEST="${FLOW_DIR}/.codex-plugin/plugin.json"
CLAUDE_HOOKS="${FLOW_DIR}/hooks/hooks.json"

FAILURES=0
PASSES=0

fail() {
    echo "FAIL: $1" >&2
    FAILURES=$((FAILURES + 1))
}

pass() {
    PASSES=$((PASSES + 1))
}

echo "hooks.test.sh"

if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not found on PATH"
    exit 0
fi

HOOKS_PATH="$(jq -r '.hooks // empty' "${CODEX_MANIFEST}")"
if [[ -n "${HOOKS_PATH}" ]]; then
    pass
else
    fail "Codex manifest must declare an explicit hooks path"
fi

CODEX_HOOKS="${FLOW_DIR}/${HOOKS_PATH#./}"
if [[ -f "${CODEX_HOOKS}" ]]; then
    pass
else
    fail "Codex hooks file does not exist: ${CODEX_HOOKS}"
fi

if [[ -f "${CODEX_HOOKS}" ]] && jq -e '
  (([.hooks | keys[]] | sort) == ["PreCompact","PreToolUse","SessionStart","Stop"]) and
  ([.hooks | to_entries[] | .value[] | .hooks[] | .timeout] | all(. > 0 and . <= 30)) and
  ([.hooks | to_entries[] | .value[] | .hooks[] | .command] | all(contains("${PLUGIN_ROOT}")))
' "${CODEX_HOOKS}" >/dev/null; then
    pass
else
    fail "Codex hooks must cover guards/context/reminders with seconds-based timeouts"
fi

if jq -e '[.hooks | to_entries[] | .value[] | .hooks[] | keys[]] | all(. == "type" or . == "command" or . == "timeout")' "${CODEX_HOOKS}" >/dev/null; then
    pass
else
    fail "Codex hook handlers contain unsupported keys"
fi

for script in session-context.sh compact-context.sh stop-reminder.sh; do
    output="$(PLUGIN_ROOT="${FLOW_DIR}" "${FLOW_DIR}/codex/scripts/${script}")"
    if jq -e . >/dev/null 2>&1 <<<"${output}"; then pass; else fail "${script} must emit JSON"; fi
done

if jq -e '(.hooks.Stop | length) > 0 and (.hooks.PreToolUse | length) > 0 and (.hooks.SessionStart | length) > 0' "${CLAUDE_HOOKS}" >/dev/null; then
    pass
else
    fail "Claude hooks must retain their existing lifecycle handlers"
fi

SKILLS_PATH="$(jq -r '.skills // empty' "${CODEX_MANIFEST}")"
SKILL_COUNT="$(find "${FLOW_DIR}/${SKILLS_PATH#./}" -mindepth 2 -maxdepth 2 -name SKILL.md | wc -l)"
if [[ -n "${SKILLS_PATH}" && "${SKILL_COUNT}" -gt 0 ]]; then
    pass
else
    fail "Codex manifest must keep cenci skills discoverable"
fi

echo "passed: ${PASSES}, failed: ${FAILURES}"
[[ "${FAILURES}" -eq 0 ]]
