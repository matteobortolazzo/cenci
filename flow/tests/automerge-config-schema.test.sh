#!/usr/bin/env bash
# automerge-config-schema.test.sh — doc-contract test for ticket #824: the
# automerge policy engine's `.cenci/config.json` schema (`automerge.*`, plus
# the per-`projects[]` equivalents) must be BOTH documented in
# skills/configure/SKILL.md AND registered in maintain/scripts/check.sh's
# CONFIG_SCHEMA -- a config field that is documented but not registered would
# make check_config_examples (check.sh's Q3 doc/config-drift scan) reject any
# fenced ```json example that legitimately uses it; a field registered but
# undocumented would leave configure's own escape-hatch schema silently
# incomplete. Both directions matter, so this test asserts both.
#
# Follows the fixture-driven idiom of implement-babysit-launch.test.sh: a
# `failures=` counter, small assert_* helpers, self-contained, auto-discovered
# by the flow gate's `*.test.sh` glob. Greps the real committed files directly
# (relative to FLOW_DIR) rather than re-deriving the schema.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "automerge-config-schema.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "automerge-config-schema.test.sh: failed to resolve flow directory." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

read_doc_raw() {
  # read_doc_raw <flow-relative-path> — pure extraction, no fail() side
  # effect here: it is deliberately safe to call inside a $(...) command
  # substitution (flow/docs/shell-scripting-gotchas.md rule on fail()-in-
  # command-substitution).
  local _relpath="$1"
  local _path="${FLOW_DIR}/${_relpath}"
  cat "${_path}" 2>/dev/null
}

# require_doc <result-var> <flow-relative-path> — nameref wrapper that
# assigns the real committed file's content into <result-var>, or fails
# closed with a distinct "not found" message and assigns "" if not found.
# Must NOT be invoked via $(...).
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

assert_contains() {
  local content="$1" pattern="$2" label="$3"
  [[ "${content}" == *"${pattern}"* ]] || fail "${label}: expected text not found: [${pattern}]"
}

# =====================================================================
# skills/configure/SKILL.md — the authoritative config emitter must document
# every automerge.* field: the fleet-wide enabled kill switch and each field
# of the per-repo policy block.
# =====================================================================
FILE="skills/configure/SKILL.md"
if require_doc CONTENT "${FILE}"; then
  assert_contains "${CONTENT}" "automerge.enabled" "${FILE} (fleet kill switch documented)"
  assert_contains "${CONTENT}" "automerge.protectedPaths" "${FILE} (protectedPaths documented)"
  assert_contains "${CONTENT}" "automerge.maxChangedFiles" "${FILE} (maxChangedFiles documented)"
  assert_contains "${CONTENT}" "automerge.maxDiffLines" "${FILE} (maxDiffLines documented)"
  assert_contains "${CONTENT}" "automerge.mergeMethod" "${FILE} (mergeMethod documented)"
  assert_contains "${CONTENT}" "automerge:ok" "${FILE} (per-ticket grant cross-referenced)"
  assert_contains "${CONTENT}" "no repo-level default" "${FILE} (no-repo-level-default statement cross-referenced)"
fi

# =====================================================================
# skills/maintain/scripts/check.sh — CONFIG_SCHEMA (the config-examples doc-
# drift scanner's field allowlist) must carry every automerge.* path, both
# top-level and per-projects[].
# =====================================================================
FILE="skills/maintain/scripts/check.sh"
if require_doc CONTENT "${FILE}"; then
  for path in \
    "'automerge|object'" \
    "'automerge.protectedPaths|array'" \
    "'automerge.protectedPaths[]|string'" \
    "'automerge.maxChangedFiles|number'" \
    "'automerge.maxDiffLines|number'" \
    "'automerge.mergeMethod|string'" \
    "'projects[].automerge|object'" \
    "'projects[].automerge.protectedPaths|array'" \
    "'projects[].automerge.protectedPaths[]|string'" \
    "'projects[].automerge.maxChangedFiles|number'" \
    "'projects[].automerge.maxDiffLines|number'" \
    "'projects[].automerge.mergeMethod|string'" \
  ; do
    assert_contains "${CONTENT}" "${path}" "${FILE} (CONFIG_SCHEMA entry)"
  done
fi

echo "automerge-config-schema.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
