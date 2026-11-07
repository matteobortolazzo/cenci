#!/usr/bin/env bash
# implement-model-switch-line.test.sh — contract test for ticket #1179 (AC 7): the
# implement pipeline's Phase 9 PR body reports Claude Code model switches, and
# both client surfaces document it.
#
# Why this is a contract: a flag-triggered fallback silently moves an unattended
# run onto an older model (docs/autonomous-loop.md, "Model fallback on flagged
# requests"). cenci-watch records each PostModelSwitch event; Phase 9 renders the
# record through `cenci daemon model-switches`. The PR body line is only honest if
# (a) it is read from the CLI each time, never from memory, (b) an unreadable
# record reads `Model switch status unknown` and never "no fallback happened", and
# (c) Codex/OpenCode runs, which never record a switch, emit neither a switch line
# nor the unknown line.
#
# Follows the idiom of implement-background-shell-reap.test.sh: a `failures=`
# counter, small assert_* helpers, self-contained, auto-discovered by the flow
# gate's `*.test.sh` glob, greps the real committed docs relative to FLOW_DIR.
#
# Marker choice, per docs/shell-scripting-gotchas.md rule 3 (assert the specific
# new text at the edit site, never a generic marker that may already exist): none
# of "Model switch", "model-switches" or "## Model switches" occurred in
# phase-9-pr.md or codex.md before this ticket (verified with a whole-file read
# while writing this test). Rule 5 (a sentence assertion proves the sentence, not
# the mechanism): the Phase 9 rendering rule is extracted as one block and the
# CLI call, the exit-code branches and the omit rule are asserted together inside
# that block, with a non-vacuity self-test of the extractor on a broken copy.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "implement-model-switch-line.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "implement-model-switch-line.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, safe inside $(...).
  local _relpath="$1"
  cat "${FLOW_DIR}/${_relpath}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper; fails closed
# on a missing or unreadable file. Must NOT be invoked via $(...).
require_doc() {
  local -n _result="$1"
  local _relpath="$2"
  local _content
  if ! _content="$(read_doc_raw "${_relpath}")"; then
    fail "${_relpath}: doc not found/unreadable: ${FLOW_DIR}/${_relpath}"
    _result=""
    return 1
  fi
  _result="${_content}"
}

# assert_contains_ws <content> <phrase> <label> — whitespace-insensitive presence
# check (the docs are Markdown-wrapped). An empty phrase would match vacuously,
# so it fails loudly instead.
assert_contains_ws() {
  local content="$1" phrase="$2" label="$3" nc np
  if [[ -z "${phrase}" ]]; then
    fail "${label}: empty required phrase (test bug)"
    return
  fi
  nc="$(printf '%s' "${content}" | tr -s '[:space:]' ' ')"
  np="$(printf '%s' "${phrase}" | tr -s '[:space:]' ' ')"
  [[ "${nc}" == *"${np}"* ]] || fail "${label}: expected text not found: [${phrase}]"
}

# first_line_of <flow-relative-path> <fixed-string> — first matching line number,
# or empty when absent.
first_line_of() {
  local _relpath="$1" _needle="$2"
  read_doc_raw "${_relpath}" | grep -n -m 1 -F -- "${_needle}" | cut -d: -f1
}

# rule_block <start-literal> <stop-literal> — reads a doc on stdin and prints from
# the first line that starts with <start-literal> up to, not including, the first
# later line equal to <stop-literal>. Exact literals, no phrase heuristics, so
# soft-wrapping cannot move the boundary (gotchas rule 29).
rule_block() {
  awk -v start="$1" -v stop="$2" '
    !on && index($0, start) == 1 { on = 1 }
    on && $0 == stop { exit }
    on { print }
  '
}

# The one form of each line, shared by both surfaces.
LINE_MAIN='Model switch: <from> → <to> (main session)'
LINE_SUB='Model switch: <from> → <to> (subagent <agent_type>)'
LINE_MORE='Model switch: +<n> more'
UNKNOWN='Model switch status unknown'
CLI='cenci daemon model-switches'
FALLBACK_RULE='only when the switch source is `auto`'

# =====================================================================
# skills/implement/phases/phase-9-pr.md (Claude Code) — the enforcement point.
# =====================================================================
FILE="skills/implement/phases/phase-9-pr.md"
RULE_START='`## Model switches` (Claude Code only)'
RULE_STOP='## Followup Ticket'
if require_doc CONTENT "${FILE}"; then
  assert_contains_ws "${CONTENT}" "${LINE_MAIN}" "${FILE} (main-session line form)"
  assert_contains_ws "${CONTENT}" "${LINE_SUB}" "${FILE} (subagent line form)"
  assert_contains_ws "${CONTENT}" "${LINE_MORE}" "${FILE} (overflow line form)"
  assert_contains_ws "${CONTENT}" "${FALLBACK_RULE}" "${FILE} (fallback label only for source auto)"
  assert_contains_ws "${CONTENT}" "${UNKNOWN}" "${FILE} (unknown wording)"
  assert_contains_ws "${CONTENT}" "${CLI}" "${FILE} (CLI read path)"
  assert_contains_ws "${CONTENT}" "Never write a model-switch line from memory" "${FILE} (never-from-memory rule)"
  assert_contains_ws "${CONTENT}" "on every re-entry into this phase" "${FILE} (re-run on re-entry)"
  assert_contains_ws "${CONTENT}" "resumed in a fresh session or after \`/clear\`" "${FILE} (cross-session residual gap)"

  # The body template carries the section, between Review and Checklist.
  assert_contains_ws "${CONTENT}" "## Model switches" "${FILE} (template heading)"
  REVIEW_LINE="$(first_line_of "${FILE}" "<Review line — see below>")"
  SWITCH_LINE="$(first_line_of "${FILE}" "<Model switch lines — see below")"
  CHECKLIST_LINE="$(first_line_of "${FILE}" "- [x] Tests pass")"
  if [[ -z "${REVIEW_LINE}" || -z "${SWITCH_LINE}" || -z "${CHECKLIST_LINE}" ]]; then
    fail "${FILE} (template placement): could not locate all three markers (review=[${REVIEW_LINE}] switches=[${SWITCH_LINE}] checklist=[${CHECKLIST_LINE}])"
  else
    [[ "${SWITCH_LINE}" -gt "${REVIEW_LINE}" ]] || fail "${FILE} (template placement): model-switch placeholder at line ${SWITCH_LINE} must come after the Review placeholder at line ${REVIEW_LINE}"
    [[ "${SWITCH_LINE}" -lt "${CHECKLIST_LINE}" ]] || fail "${FILE} (template placement): model-switch placeholder at line ${SWITCH_LINE} must come before the Checklist at line ${CHECKLIST_LINE}"
  fi

  # Wiring (rule 5): the rendering rule itself runs the CLI and maps each outcome.
  BLOCK="$(printf '%s\n' "${CONTENT}" | rule_block "${RULE_START}" "${RULE_STOP}")"
  if [[ -z "${BLOCK}" ]]; then
    fail "${FILE} (rendering rule): block starting [${RULE_START}] before [${RULE_STOP}] not found"
  else
    assert_contains_ws "${BLOCK}" "run \`${CLI}\`" "${FILE} (rendering rule runs the CLI)"
    assert_contains_ws "${BLOCK}" "Exit \`0\` with output" "${FILE} (rendering rule: exit 0 with lines)"
    assert_contains_ws "${BLOCK}" "Exit \`0\` with empty output" "${FILE} (rendering rule: exit 0 empty)"
    assert_contains_ws "${BLOCK}" "omit the whole section" "${FILE} (rendering rule: omit when no lines)"
    assert_contains_ws "${BLOCK}" "Exit \`1\`" "${FILE} (rendering rule: exit 1)"
    assert_contains_ws "${BLOCK}" "${UNKNOWN}" "${FILE} (rendering rule: unknown line)"
    assert_contains_ws "${BLOCK}" "never that no fallback happened" "${FILE} (rendering rule: unknown is not none)"
  fi
fi

# Non-vacuity of the extractor: a broken copy whose rule lacks the exit-1 branch
# must not satisfy the block assertions, and a copy without the heading must
# yield an empty block.
BROKEN_DOC="$(printf '%s\n' \
  '## Review' \
  '`## Model switches` (Claude Code only) runs the CLI.' \
  '- Exit `0` with output: copy the lines.' \
  '' \
  '## Followup Ticket' \
  'Exit `1` appears here, outside the rule block.')"
BROKEN_BLOCK="$(printf '%s\n' "${BROKEN_DOC}" | rule_block "${RULE_START}" "${RULE_STOP}")"
if [[ "${BROKEN_BLOCK}" == *'Exit `1`'* ]]; then
  fail "extractor self-test: rule_block leaked text past the stop heading"
fi
NO_HEADING_BLOCK="$(printf '%s\n' '## Review' 'nothing here' '## Followup Ticket' | rule_block "${RULE_START}" "${RULE_STOP}")"
if [[ -n "${NO_HEADING_BLOCK}" ]]; then
  fail "extractor self-test: rule_block returned text without the start marker"
fi

# =====================================================================
# skills/implement/codex.md (Codex adapter) — documents the same line forms and
# unknown wording, and states that Codex runs emit neither.
# =====================================================================
FILE="skills/implement/codex.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains_ws "${CONTENT}" "${LINE_MAIN}" "${FILE} (main-session line form)"
  assert_contains_ws "${CONTENT}" "${LINE_SUB}" "${FILE} (subagent line form)"
  assert_contains_ws "${CONTENT}" "${LINE_MORE}" "${FILE} (overflow line form)"
  assert_contains_ws "${CONTENT}" "${FALLBACK_RULE}" "${FILE} (fallback label only for source auto)"
  assert_contains_ws "${CONTENT}" "${UNKNOWN}" "${FILE} (unknown wording)"
  assert_contains_ws "${CONTENT}" "${CLI}" "${FILE} (CLI read path)"
  assert_contains_ws "${CONTENT}" "A Codex run emits no model-switch line and no \`${UNKNOWN}\` line" "${FILE} (Codex no-line rule)"
  assert_contains_ws "${CONTENT}" "omit the \`## Model switches\` section entirely" "${FILE} (Codex omits the section)"
  assert_contains_ws "${CONTENT}" "OpenCode is the same" "${FILE} (OpenCode stated)"
fi

echo "implement-model-switch-line.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
