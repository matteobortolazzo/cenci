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
#              and fails schema validation (drift), or when validation cannot
#              run because Python 3.11+ with tomllib is missing (a separate
#              prerequisite advisory); silent (no output, exit 0) otherwise,
#              including for unverified-only notes. Never blocks.
#   --plain    machine-readable one-liner for scripted consumers (maintain's
#              check.sh agent-roles check, and this script's own tests); read
#              it as `read -r state rest`:
#                clean
#                clean <note>[; <note> ...]      (unverified/advisory notes)
#                drift <failing finding>[; <failing finding> ...]
#                absent
#                unavailable python3.11+-with-tomllib-required
#                unknown <reason>                (resolver/validator failure)
#              Always exits 0 — drift is advisory, never a gate.
#
# Validation is delegated to validate-agent-roles.sh --plain so the hook and
# the maintain check can never disagree with what that validator considers a
# schema violation. Validator exit 0 → clean (any output line must be a known
# note kind, else `unknown validator-output` — never `clean`); exit 1 → drift;
# exit 3 → unavailable; anything else → unknown. Messages carry no validator
# stderr, stack traces or secrets.
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

is_note_kind() {  # is_note_kind <finding line> — non-failing validator kinds
  case "${1%% *}" in
    unverified-key | unverified-value | name-convention) return 0 ;;
    *) return 1 ;;
  esac
}

# Split the validator's one-finding-per-line output into failing findings and
# notes, each joined with "; ". UNKNOWN_LINE=1 flags an empty-kind or
# unrecognized shape the caller must not read as clean.
FAILING_SUMMARY=""
NOTES_SUMMARY=""
UNKNOWN_LINE=0
if [[ -n "$FINDINGS" ]]; then
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    if is_note_kind "$line"; then
      NOTES_SUMMARY="${NOTES_SUMMARY:+${NOTES_SUMMARY}; }${line}"
    elif [[ "$STATUS" -eq 1 ]]; then
      FAILING_SUMMARY="${FAILING_SUMMARY:+${FAILING_SUMMARY}; }${line}"
    else
      UNKNOWN_LINE=1
    fi
  done <<< "$FINDINGS"
fi

emit_advisory() {  # emit_advisory <context text> — SessionStart JSON, never blocks
  command -v jq >/dev/null 2>&1 || finish_quiet "unknown no-jq"
  jq -n --arg ctx "$1" \
    '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
  exit 0
}

case "$STATUS" in
  0)
    [[ "$UNKNOWN_LINE" -eq 0 ]] || finish_quiet "unknown validator-output"
    if [[ -n "$NOTES_SUMMARY" ]]; then
      finish_quiet "clean ${NOTES_SUMMARY}"
    fi
    finish_quiet "clean"
    ;;
  1)
    [[ -n "$FAILING_SUMMARY" ]] || finish_quiet "unknown validator-output"
    if [[ "$MODE" == "plain" ]]; then
      printf 'drift %s\n' "$FAILING_SUMMARY"
      exit 0
    fi
    emit_advisory "This repo's .codex/agents/*.toml no longer match the current Codex agent-role schema (${FAILING_SUMMARY}). install-agents.sh never overwrites an existing agent file, so a repo configured before a template fix stays on the broken shape until repaired by hand. Surface this to the user at a natural moment; review a diff against templates/codex/agent-roles/ before updating any file — do not overwrite user edits unprompted."
    ;;
  3)
    if [[ "$MODE" == "plain" ]]; then
      printf 'unavailable python3.11+-with-tomllib-required\n'
      exit 0
    fi
    emit_advisory "cenci: Codex agent-role validation is unavailable — Python 3.11+ with tomllib was not found on PATH, so this repo's .codex/agents/*.toml were not checked. Surface this to the user at a natural moment: install Python 3.11 or newer so python3 provides tomllib; no agent-role file was changed."
    ;;
  *)
    finish_quiet "unknown validator-error"
    ;;
esac
