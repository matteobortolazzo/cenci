#!/usr/bin/env bash
# refine-idempotent-creation.test.sh -- doc-contract suite for ticket #876
# (2/12 of #661): refine's child- and design-ticket creation must be
# recoverably idempotent across timeouts, retries, crashes, and resumed
# refinement sessions, via a deterministic ensure-issue.sh helper invoked
# identically by SKILL.md and codex.md.
#
# This suite pins the doc-level contract only -- the four things a future
# edit to SKILL.md/codex.md could silently drift on without the behavior
# suite (ensure-issue.test.sh) catching it, since that suite tests the
# script directly, never the prose that invokes it:
#   1. The exact creation-marker literal, `<!-- cenci-refine-create:<nonce> -->`.
#   2. The checkpoint path/schema: `.plans/.refine-<parent>.checkpoint.json`,
#      schemaVersion 1, and its field names.
#   3. Fail-closed language for a missing/corrupt checkpoint.
#   4. Claude<->Codex parity: both files invoke the script identically and
#      document the same init/ensure/link/clear command surface.
#
# RED-phase note: SKILL.md and codex.md do not carry any of this yet -- every
# assertion below is expected to fail until Phase 4 lands the script and the
# doc edits.
#
# Follows flow/tests/refine-skill-contract.test.sh's idiom: `failures`
# counter, a pure `read_doc_raw` extractor (no fail() side effect, safe
# inside $(...)) paired with a `require_doc` nameref wrapper (calls fail()
# in the parent shell) -- flow/docs/shell-scripting-gotchas.md's
# never-fail()-inside-$(...) rule, enforced by
# flow/tests/read-helper-purity-contract.test.sh. Auto-discovered by
# scripts/run-checks.sh's `*.test.sh` glob -- no registration needed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "refine-idempotent-creation.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "refine-idempotent-creation.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> -- pure extraction, no fail() side
  # effect here: it is deliberately safe to call inside a $(...) command
  # substitution.
  local _relpath="$1"
  cat "${FLOW_DIR}/${_relpath}" 2>/dev/null
}

require_doc() {
  # require_doc <result-var> <flow-relative-path> -- nameref wrapper that
  # assigns the real committed file's content into <result-var>, or fails
  # closed with a distinct "not found" message and assigns "" if not
  # found. Must NOT be invoked via $(...).
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

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ -n "${pattern}" ]] || { fail "${label}: empty required pattern (test bug)"; return; }
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: required text missing: [${pattern}]"
}

require_doc SKILL "skills/refine/SKILL.md" || true
require_doc CODEX "skills/refine/codex.md" || true
require_doc SCRIPT_SRC "skills/refine/scripts/ensure-issue.sh" || true

# =====================================================================
# 1. The creation-marker literal.
# =====================================================================
MARKER_LITERAL='<!-- cenci-refine-create:'

if [[ -n "${SKILL}" ]]; then
  assert_contains "${SKILL}" "${MARKER_LITERAL}" "skills/refine/SKILL.md must document the exact creation marker literal"
fi
if [[ -n "${CODEX}" ]]; then
  assert_contains "${CODEX}" "${MARKER_LITERAL}" "skills/refine/codex.md must document the exact creation marker literal"
fi
if [[ -n "${SCRIPT_SRC}" ]]; then
  assert_contains "${SCRIPT_SRC}" "${MARKER_LITERAL}" "skills/refine/scripts/ensure-issue.sh must embed the exact creation marker literal"
fi

# =====================================================================
# 2. Checkpoint path and schema.
# =====================================================================
CHECKPOINT_PATH_LITERAL='.plans/.refine-'
CHECKPOINT_SUFFIX_LITERAL='.checkpoint.json'
SCHEMA_FIELDS=(
  "schemaVersion"
  "creatorLogin"
  "titleHash"
  "bodyHash"
  "issueNumber"
  "verification"
  "\"pending\""
  "\"verified\""
  "\"mismatch\""
  "\"unlinked\""
  "\"linked\""
)

if [[ -n "${SKILL}" ]]; then
  assert_contains "${SKILL}" "${CHECKPOINT_PATH_LITERAL}" "skills/refine/SKILL.md must document the checkpoint path prefix .plans/.refine-<parent>"
  assert_contains "${SKILL}" "${CHECKPOINT_SUFFIX_LITERAL}" "skills/refine/SKILL.md must document the checkpoint path suffix .checkpoint.json"
  assert_contains "${SKILL}" "## Creation Checkpoint" "skills/refine/SKILL.md must add a ## Creation Checkpoint subsection"
fi
if [[ -n "${CODEX}" ]]; then
  assert_contains "${CODEX}" "${CHECKPOINT_PATH_LITERAL}" "skills/refine/codex.md must document the checkpoint path prefix .plans/.refine-<parent>"
  assert_contains "${CODEX}" "${CHECKPOINT_SUFFIX_LITERAL}" "skills/refine/codex.md must document the checkpoint path suffix .checkpoint.json"
fi
if [[ -n "${SCRIPT_SRC}" ]]; then
  for field in "${SCHEMA_FIELDS[@]}"; do
    assert_contains "${SCRIPT_SRC}" "${field}" "skills/refine/scripts/ensure-issue.sh must reference checkpoint schema field/value [${field}]"
  done
fi

# =====================================================================
# 3. Fail-closed language for a missing/corrupt checkpoint.
# =====================================================================
FAIL_CLOSED_LITERAL='fail closed'

if [[ -n "${SKILL}" ]]; then
  assert_contains "${SKILL}" "${FAIL_CLOSED_LITERAL}" "skills/refine/SKILL.md must document fail-closed handling for a missing/corrupt checkpoint"
fi
if [[ -n "${CODEX}" ]]; then
  assert_contains "${CODEX}" "${FAIL_CLOSED_LITERAL}" "skills/refine/codex.md must document fail-closed handling for a missing/corrupt checkpoint"
fi
if [[ -n "${SCRIPT_SRC}" ]]; then
  assert_contains "${SCRIPT_SRC}" "partial-state" "skills/refine/scripts/ensure-issue.sh must emit a partial-state report on a fail-closed exit"
fi

# =====================================================================
# 4. Claude<->Codex parity: identical script invocation, same command
# surface (init/ensure/link/clear).
# =====================================================================
SKILL_INVOKE='bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh"'
CODEX_INVOKE='"${PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh"'

if [[ -n "${SKILL}" ]]; then
  assert_contains "${SKILL}" "${SKILL_INVOKE}" "skills/refine/SKILL.md must invoke ensure-issue.sh via the standard Claude CLAUDE_PLUGIN_ROOT-anchored form"
fi
if [[ -n "${CODEX}" ]]; then
  assert_contains "${CODEX}" "${CODEX_INVOKE}" "skills/refine/codex.md must invoke ensure-issue.sh via the standard Codex PLUGIN_ROOT-anchored form"
fi

SUBCOMMANDS=(init ensure link clear)
if [[ -n "${SKILL}" ]]; then
  for cmd in "${SUBCOMMANDS[@]}"; do
    assert_contains "${SKILL}" "ensure-issue.sh ${cmd}" "skills/refine/SKILL.md must document the ${cmd} subcommand invocation"
  done
fi
if [[ -n "${CODEX}" ]]; then
  for cmd in "${SUBCOMMANDS[@]}"; do
    assert_contains "${CODEX}" "ensure-issue.sh ${cmd}" "skills/refine/codex.md must document the ${cmd} subcommand invocation"
  done
fi

# --- allowed-tools grant present (Claude only -- codex has no such gate) ---
GRANT_LITERAL='Bash(bash "${CLAUDE_PLUGIN_ROOT}/skills/refine/scripts/ensure-issue.sh":*)'
if [[ -n "${SKILL}" ]]; then
  assert_contains "${SKILL}" "${GRANT_LITERAL}" "skills/refine/SKILL.md allowed-tools must grant the ensure-issue.sh invocation"
fi

echo "refine-idempotent-creation.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
