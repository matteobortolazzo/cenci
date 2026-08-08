#!/usr/bin/env bash
# autonomous-loop-protectedpaths-trailing-slash.test.sh — doc-contract test
# for ticket #967: docs/autonomous-loop.md's `protectedPaths` bullet (§3,
# "Declare what a merge is allowed to touch") describes protectedPaths as
# globs where `*` matches any character including `/`, but omits the
# trailing-slash directory-prefix rule that `matchesProtected` actually
# implements (watch/internal/babysit/automerge.go: a pattern ending in "/"
# with no trailing "*" matches that directory and everything under it).
# Read literally, the doc makes this repo's own committed directory-prefix
# entries (flow/skills/, watch/internal/, ...) look like dead literal-match
# strings.
#
# This test asserts, by grepping the real committed doc content (not by
# re-implementing matchesProtected), that:
#   1. docs/autonomous-loop.md's protectedPaths bullet documents the
#      trailing-slash / directory-prefix rule.
#   2. flow/skills/configure/SKILL.md's automerge.protectedPaths field
#      description states or links that same rule.
#
# Follows the fixture-driven idiom of automerge-config-schema.test.sh: a
# `failures=` counter, small assert_* helpers, self-contained, auto-
# discovered by the flow gate's `*.test.sh` glob.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" || { echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failed to resolve script directory." >&2; exit 2; }
FLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)" || { echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failed to resolve flow directory." >&2; exit 2; }
REPO_ROOT="$(cd "${FLOW_DIR}/.." && pwd)" || { echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failed to resolve repository root." >&2; exit 2; }
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures+1)); }

# read_file_raw <absolute-path> — pure extraction, no fail() side effect
# here: it is deliberately safe to call inside a $(...) command substitution
# (flow/docs/shell-scripting-gotchas.md rule on fail()-in-command-substitution).
read_file_raw() {
  cat "$1" 2>/dev/null
}

# require_file <result-var> <absolute-path> — nameref wrapper that assigns
# the real committed file's content into <result-var>, or fails closed with
# a distinct "not found" message and assigns "" if not found. Must NOT be
# invoked via $(...).
require_file() {
  local -n _result="$1"
  local _path="$2"
  local _content
  if ! _content="$(read_file_raw "${_path}")"; then
    fail "${_path}: doc not found/unreadable"
    _result=""
    return 1
  fi
  _result="${_content}"
}

assert_contains_any() {
  # assert_contains_any <content> <label> <pattern...> — pass if any
  # <pattern> is a substring of <content>.
  local content="$1" label="$2"
  shift 2
  local pattern
  for pattern in "$@"; do
    [[ "${content}" == *"${pattern}"* ]] && return 0
  done
  fail "${label}: expected one of [$*] not found"
  return 1
}

# =====================================================================
# docs/autonomous-loop.md §3 — the protectedPaths bullet must document the
# trailing-slash directory-prefix rule, not just the `*` glob rule.
# =====================================================================
FILE="${REPO_ROOT}/docs/autonomous-loop.md"
if require_file CONTENT "${FILE}"; then
  BULLET_BLOCK="$(printf '%s\n' "${CONTENT}" | grep -A 4 -F '`protectedPaths` are globs')"
  if [[ -z "${BULLET_BLOCK}" ]]; then
    fail "docs/autonomous-loop.md: protectedPaths bullet not found"
  else
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet documents trailing-slash directory-prefix rule)" \
      "ending in \`/\`" "ending in \"/\"" "ends in \`/\`"
    assert_contains_any "${BULLET_BLOCK}" \
      "docs/autonomous-loop.md (protectedPaths bullet states directory-and-everything-under semantics)" \
      "everything under"
  fi
fi

# =====================================================================
# flow/skills/configure/SKILL.md — the automerge.protectedPaths field
# description must state or link the same trailing-slash rule.
# =====================================================================
FILE="${FLOW_DIR}/skills/configure/SKILL.md"
if require_file CONTENT "${FILE}"; then
  FIELD_BLOCK="$(printf '%s\n' "${CONTENT}" | grep -A 4 -F '`automerge.protectedPaths`')"
  if [[ -z "${FIELD_BLOCK}" ]]; then
    fail "flow/skills/configure/SKILL.md: automerge.protectedPaths field description not found"
  else
    assert_contains_any "${FIELD_BLOCK}" \
      "flow/skills/configure/SKILL.md (automerge.protectedPaths field description states or links the trailing-slash rule)" \
      "ending in \`/\`" "ending in \"/\"" "ends in \`/\`" "everything under" "docs/autonomous-loop.md"
  fi
fi

echo "autonomous-loop-protectedpaths-trailing-slash.test.sh: failures=${failures}"
[[ "${failures}" -eq 0 ]]
