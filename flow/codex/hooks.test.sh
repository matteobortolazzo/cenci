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

# ── #795: a Bash PreToolUse matcher must wire both guard scripts ────
# Extends the existing Write|Edit matcher's write-guard coverage to Bash
# redirection/tee targets. Both check-sensitive-files.sh and
# guard-main-worktree.sh must be wired under a "Bash" matcher entry.
if jq -e '
  [.hooks.PreToolUse[] | select(.matcher == "Bash")] as $bash_matchers
  | ($bash_matchers | length) > 0
  and ([$bash_matchers[].hooks[].command] | any(contains("check-sensitive-files.sh")))
  and ([$bash_matchers[].hooks[].command] | any(contains("guard-main-worktree.sh")))
' "${CODEX_HOOKS}" >/dev/null; then
    pass
else
    fail "Codex hooks must wire a Bash PreToolUse matcher covering both check-sensitive-files.sh and guard-main-worktree.sh"
fi

CONTRACT_DIR="$(mktemp -d)"
trap 'rm -rf "${CONTRACT_DIR}"' EXIT
session="$(cd "${CONTRACT_DIR}" && PLUGIN_ROOT="${FLOW_DIR}" node "${FLOW_DIR}/codex/hook-output.mjs" session)"
compact="$(PLUGIN_ROOT="${FLOW_DIR}" node "${FLOW_DIR}/codex/hook-output.mjs" compact)"
stop="$(PLUGIN_ROOT="${FLOW_DIR}" node "${FLOW_DIR}/codex/hook-output.mjs" stop)"
jq -e '.hookSpecificOutput.hookEventName == "SessionStart" and (.hookSpecificOutput.additionalContext | type == "string")' <<<"${session}" >/dev/null && pass || fail "SessionStart contract"
jq -e '.hookSpecificOutput.hookEventName == "PreCompact" and (.hookSpecificOutput.additionalContext | type == "string")' <<<"${compact}" >/dev/null && pass || fail "PreCompact contract"
jq -e '.systemMessage | type == "string"' <<<"${stop}" >/dev/null && pass || fail "Stop contract"

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
