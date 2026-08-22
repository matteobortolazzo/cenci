#!/usr/bin/env bash
# check-agent-role-drift.sh — SessionStart advisory: nudge when this repo's
# installed .codex/agents/*.toml files no longer satisfy the current agent
# role schema (#1040). install-agents.sh deliberately never overwrites an
# existing agent file (they're user-editable), so a repo configured before a
# template fix lands (#409 added `description`, #422 added `name`) stays
# silently stuck on the broken shape forever. This hook surfaces that drift;
# it never repairs it — repair stays a manual, reviewed
# install-agents.sh/configure step so user edits are never clobbered.
#
# Modes:
#   (default)  Claude Code SessionStart hook — emits hookSpecificOutput JSON
#              with an additionalContext advisory when .codex/agents/ exists
#              and fails schema validation; silent (no output, exit 0)
#              otherwise. Never blocks.
#   --plain    machine-readable one-liner for scripted consumers (maintain's
#              check.sh agent-roles check, and this script's own tests):
#                clean
#                drift <finding>[; <finding> ...]
#                absent
#                unknown <reason>
#              Always exits 0 — drift is advisory, never a gate.
#
# Validation is delegated to validate-agent-roles.sh --plain so the hook and
# the maintain check can never disagree with what that validator considers a
# schema violation.
set -uo pipefail

MODE="hook"
if [[ "${1:-}" == "--plain" ]]; then
  MODE="plain"
fi

finish_quiet() {
  if [[ "$MODE" == "plain" ]]; then
    printf '%s\n' "$*"
  fi
  exit 0
}

AGENTS_DIR=".codex/agents"
[[ -d "$AGENTS_DIR" ]] || finish_quiet "absent"

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || finish_quiet "unknown self-dir-unresolvable"
VALIDATOR="${SELF_DIR}/../../codex/validate-agent-roles.sh"
[[ -f "$VALIDATOR" ]] || finish_quiet "unknown no-validator"

FINDINGS="$(bash "$VALIDATOR" --plain "$AGENTS_DIR" 2>/dev/null)"
STATUS=$?

if [[ "$STATUS" -eq 0 ]]; then
  finish_quiet "clean"
fi

if [[ "$STATUS" -ne 1 ]]; then
  finish_quiet "unknown validator-error"
fi

SUMMARY="$(printf '%s' "$FINDINGS" | tr '\n' ';' | sed 's/;$//; s/;/; /g')"

if [[ "$MODE" == "plain" ]]; then
  printf 'drift %s\n' "$SUMMARY"
  exit 0
fi

command -v jq >/dev/null 2>&1 || finish_quiet "unknown no-jq"

CTX="This repo's .codex/agents/*.toml no longer match the current Codex agent-role schema (${SUMMARY}). install-agents.sh never overwrites an existing agent file, so a repo configured before a template fix stays on the broken shape until repaired by hand. Surface this to the user at a natural moment; review a diff against templates/codex/agent-roles/ before updating any file — do not overwrite user edits unprompted."

jq -n --arg ctx "$CTX" \
  '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
exit 0
